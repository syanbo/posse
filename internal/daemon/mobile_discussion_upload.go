package daemon

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"posse/internal/rpc"
)

var mobileStageLocks sync.Map
var mobileStagePins sync.Map

// 字段由对应设备的暂存锁保护。发送期间只保留文件引用，不持设备锁等待网络。
type mobileStagePin struct {
	refs   int
	remove bool
}

func (d *Daemon) mobileStage(device string) (string, *sync.Mutex) {
	sum := sha256.Sum256([]byte(device))
	root := filepath.Join(d.cfg.Home, "mobile-staging", hex.EncodeToString(sum[:]))
	lock, _ := mobileStageLocks.LoadOrStore(root, &sync.Mutex{})
	return root, lock.(*sync.Mutex)
}

type mobileStageMeta struct {
	Name    string
	Size    int64
	Ready   bool
	Created time.Time
}

func cleanMobileStages(root string) {
	entries, _ := os.ReadDir(root)
	for _, e := range entries {
		if _, busy := mobileStagePins.Load(filepath.Join(root, e.Name())); busy {
			continue
		}
		st, err := e.Info()
		if err == nil && time.Since(st.ModTime()) > time.Hour {
			_ = os.RemoveAll(filepath.Join(root, e.Name()))
		}
	}
	_ = os.Remove(root) // 只有空目录才会删掉，尚未过期的暂存不受影响
}

func stagePath(root, id string) (string, error) {
	if len(id) != 34 || !strings.HasPrefix(id, "u_") {
		return "", rpc.Errorf("upload_expired", "discussion upload expired; upload the file again")
	}
	if _, err := hex.DecodeString(id[2:]); err != nil {
		return "", rpc.Errorf("upload_expired", "discussion upload expired; upload the file again")
	}
	return filepath.Join(root, id), nil
}

func readStage(dir string) (mobileStageMeta, error) {
	var m mobileStageMeta
	b, err := os.ReadFile(filepath.Join(dir, "meta.json"))
	if err != nil || json.Unmarshal(b, &m) != nil || time.Since(m.Created) > time.Hour {
		return m, rpc.Errorf("upload_expired", "discussion upload expired; upload the file again")
	}
	return m, nil
}

func (d *Daemon) mobileDiscUpload(device, method string, raw json.RawMessage) (any, error) {
	var p struct {
		Staged, Name string
		Size, Offset int64
		Content      []byte
	}
	if err := json.Unmarshal(raw, &p); err != nil {
		return nil, rpc.Errorf(rpc.CodeBadRequest, "invalid discussion upload")
	}
	root, mu := d.mobileStage(device)
	mu.Lock()
	defer mu.Unlock()
	cleanMobileStages(root)
	if method == "discussion.upload.begin" {
		if p.Size < 0 || p.Size > discMaxAttachSize || safeFileName(p.Name) == "" {
			return nil, rpc.Errorf(rpc.CodeBadRequest, "invalid discussion upload")
		}
		entries, _ := os.ReadDir(root)
		if len(entries) >= 20 {
			return nil, rpc.Errorf(rpc.CodeConflict, "too many discussion uploads; remove unused attachments first")
		}
		id := "u_" + randomStageID()
		dir := filepath.Join(root, id)
		if err := os.MkdirAll(dir, 0700); err != nil {
			return nil, err
		}
		m := mobileStageMeta{Name: safeFileName(p.Name), Size: p.Size, Created: time.Now()}
		b, _ := json.Marshal(m)
		if err := os.WriteFile(filepath.Join(dir, "meta.json"), b, 0600); err != nil {
			os.RemoveAll(dir)
			return nil, err
		}
		if err := os.WriteFile(filepath.Join(dir, "data"), nil, 0600); err != nil {
			os.RemoveAll(dir)
			return nil, err
		}
		return map[string]any{"staged": id}, nil
	}
	dir, err := stagePath(root, p.Staged)
	if err != nil {
		return nil, err
	}
	if method == "discussion.upload.abort" {
		if _, busy := mobileStagePins.Load(dir); busy {
			return nil, rpc.Errorf(rpc.CodeConflict, "discussion attachment is being sent")
		}
		return map[string]any{"aborted": true}, os.RemoveAll(dir)
	}
	m, err := readStage(dir)
	if err != nil {
		return nil, err
	}
	switch method {
	case "discussion.upload.chunk":
		if m.Ready || len(p.Content) > 256<<10 || p.Offset < 0 || p.Offset+int64(len(p.Content)) > m.Size {
			return nil, rpc.Errorf(rpc.CodeBadRequest, "invalid discussion upload")
		}
		f, err := os.OpenFile(filepath.Join(dir, "data"), os.O_RDWR, 0600)
		if err != nil {
			return nil, err
		}
		defer f.Close()
		st, err := f.Stat()
		if err != nil {
			return nil, err
		}
		if p.Offset+int64(len(p.Content)) <= st.Size() {
			old := make([]byte, len(p.Content))
			n, e := f.ReadAt(old, p.Offset)
			if e == nil && n == len(old) && bytes.Equal(old, p.Content) {
				return map[string]any{"received": p.Offset + int64(len(p.Content))}, nil
			}
		}
		if st.Size() != p.Offset {
			return nil, rpc.Errorf(rpc.CodeConflict, "discussion upload offset does not match")
		}
		_, err = f.WriteAt(p.Content, p.Offset)
		return map[string]any{"received": p.Offset + int64(len(p.Content))}, err
	case "discussion.upload.commit":
		st, err := os.Stat(filepath.Join(dir, "data"))
		if err != nil {
			return nil, err
		}
		if st.Size() != m.Size {
			return nil, rpc.Errorf(rpc.CodeBadRequest, "discussion upload is incomplete")
		}
		m.Ready = true
		b, _ := json.Marshal(m)
		if err = os.WriteFile(filepath.Join(dir, "meta.json"), b, 0600); err != nil {
			return nil, err
		}
		return map[string]any{"staged": p.Staged, "name": m.Name, "size": m.Size}, nil
	}
	return nil, rpc.Errorf("method_not_found", "unknown method %s", method)
}

func (d *Daemon) mobileDiscPost(ctx context.Context, device string, raw json.RawMessage) (any, error) {
	var p discPostParams
	var input struct {
		Attachments []struct {
			Staged string  `json:"staged"`
			Name   string  `json:"name"`
			Path   *string `json:"path"`
		} `json:"attachments"`
	}
	if json.Unmarshal(raw, &p) != nil || json.Unmarshal(raw, &input) != nil || p.ClientID == "" || len(input.Attachments) > discMaxAttachments {
		return nil, rpc.Errorf(rpc.CodeBadRequest, "invalid discussion post")
	}
	for _, a := range input.Attachments {
		if a.Path != nil {
			return nil, rpc.Errorf(rpc.CodeBadRequest, "discussion attachments require an upload token")
		}
	}
	disc, err := d.discussion(p.Discussion)
	if err != nil {
		return nil, err
	}
	if disc.Mirror && len(input.Attachments) > 0 {
		if rc := d.roomConnFor(disc.Key); rc != nil {
			rc.syncNow()
		}
	}
	finish := func(res any) (any, error) {
		b, e := json.Marshal(res)
		if e != nil {
			return nil, e
		}
		var out any
		if e = json.Unmarshal(b, &out); e != nil {
			return nil, e
		}
		d.mobileDiscOutput(out)
		return out, nil
	}
	ids := make([]string, 0, len(input.Attachments))
	for _, a := range input.Attachments {
		ids = append(ids, a.Staged)
	}
	if m, x, e := d.store.MessageByClientID(disc.Key, p.ClientID); e == nil {
		d.releaseMobileStages(device, ids, true, false)
		return finish(discPostResult{Message: m, Execution: x, Duplicate: true})
	}
	res, err := d.withMobileStages(device, ids, func(atts []discAttachIn) (any, error) {
		p.Attachments = atts
		b, _ := json.Marshal(p)
		return d.discPost(ctx, b)
	})
	if err != nil {
		return nil, err
	}
	return finish(res)
}

func (d *Daemon) withMobileStages(device string, ids []string, run func([]discAttachIn) (any, error)) (any, error) {
	root, mu := d.mobileStage(device)
	mu.Lock()
	cleanMobileStages(root)
	var atts []discAttachIn
	for _, id := range ids {
		dir, err := stagePath(root, id)
		if err != nil {
			mu.Unlock()
			return nil, err
		}
		m, err := readStage(dir)
		if err != nil {
			mu.Unlock()
			return nil, err
		}
		if !m.Ready {
			mu.Unlock()
			return nil, rpc.Errorf(rpc.CodeBadRequest, "discussion upload is incomplete")
		}
		atts = append(atts, discAttachIn{Path: filepath.Join(dir, "data"), Name: m.Name})
	}
	for _, id := range ids {
		dir, _ := stagePath(root, id)
		pin, _ := mobileStagePins.LoadOrStore(dir, &mobileStagePin{})
		pin.(*mobileStagePin).refs++
	}
	mu.Unlock()
	success := false
	defer func() { d.releaseMobileStages(device, ids, success, true) }()
	res, err := run(atts)
	success = err == nil
	return res, err
}

func (d *Daemon) releaseMobileStages(device string, ids []string, success, pinned bool) {
	root, mu := d.mobileStage(device)
	mu.Lock()
	defer mu.Unlock()
	for _, id := range ids {
		dir, err := stagePath(root, id)
		if err != nil {
			continue
		}
		if value, ok := mobileStagePins.Load(dir); ok {
			pin := value.(*mobileStagePin)
			if pinned {
				pin.refs--
			}
			pin.remove = pin.remove || success
			if pin.refs > 0 {
				continue
			}
			mobileStagePins.Delete(dir)
			if pin.remove {
				_ = os.RemoveAll(dir)
			}
		} else if success {
			_ = os.RemoveAll(dir)
		}
	}
}

func randomStageID() string { b := make([]byte, 16); _, _ = rand.Read(b); return hex.EncodeToString(b) }

// 交接拿到锁后才清理旧暂存；过期清理不等待正在上传或向房主发送的设备。
func (d *Daemon) mobileStageLoop(ctx context.Context) {
	tick := time.NewTicker(time.Minute)
	defer tick.Stop()
	clean := func() {
		if !d.canDispatch() {
			return
		}
		root := filepath.Join(d.cfg.Home, "mobile-staging")
		dirs, _ := os.ReadDir(root)
		for _, dir := range dirs {
			path := filepath.Join(root, dir.Name())
			lock, _ := mobileStageLocks.LoadOrStore(path, &sync.Mutex{})
			mu := lock.(*sync.Mutex)
			if mu.TryLock() {
				cleanMobileStages(path)
				mu.Unlock()
			}
		}
	}
	clean()
	for {
		select {
		case <-ctx.Done():
			return
		case <-tick.C:
			clean()
		}
	}
}
