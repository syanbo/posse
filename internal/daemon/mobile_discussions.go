package daemon

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"image"
	"image/jpeg"
	"io"
	"mime"
	"os"
	"path/filepath"
	"strings"

	"posse/internal/media"
	"posse/internal/rpc"
	"posse/internal/store"
)

// Discussions 只接手机用户的操作，worker 的上下文领取和汇报不经过这里。
func (h mobileHost) Discussions(ctx context.Context, device, method string, raw json.RawMessage) (any, error) {
	d := h.d
	var p map[string]json.RawMessage
	if len(raw) == 0 {
		raw = json.RawMessage(`{}`)
	}
	if err := json.Unmarshal(raw, &p); err != nil || p == nil {
		return nil, rpc.Errorf(rpc.CodeBadRequest, "invalid discussion parameters")
	}
	delete(p, "caller")
	delete(p, "addrs")
	delete(p, "path") // 手机给的是图片字节，不能让它读这台电脑上的文件
	if v, ok := p["workspace"]; ok {
		var sel string
		if json.Unmarshal(v, &sel) != nil {
			return nil, rpc.Errorf(rpc.CodeBadRequest, "invalid discussion workspace")
		}
		if sel != "" {
			proj, id, ok := strings.Cut(sel, "::")
			w, err := d.store.Workspace(id)
			if !ok || err != nil || w.ProjectID != proj {
				return nil, rpc.Errorf(rpc.CodeBadRequest, "invalid discussion workspace")
			}
			p["workspace"], _ = json.Marshal(w.ID)
		}
	}
	marshal := func() json.RawMessage { b, _ := json.Marshal(p); return b }
	if strings.HasPrefix(method, "discussion.upload.") {
		return d.mobileDiscUpload(device, method, marshal())
	}
	if method == "discussion.report.read" {
		return d.mobileDiscReport(marshal())
	}
	if method == "discussion.attachment.read" {
		return d.mobileDiscAttachment(marshal())
	}
	if method == "discussion.attachment.thumb" {
		return d.mobileDiscThumb(marshal())
	}
	if method == "discussion.answer" {
		var answer discAnswerParams
		if err := json.Unmarshal(marshal(), &answer); err != nil {
			return nil, err
		}
		if q, e := d.store.QuestionByTaskMessage(answer.Question); e == nil {
			if _, e = d.ownExecSel(q.Execution); e != nil {
				return nil, e
			}
			if answer.ClientID != "" {
				if m, _, e := d.store.MessageByClientID(q.Discussion, answer.ClientID); e == nil && m.Question == answer.Question && m.Kind == store.DiscAnswer && m.Body == strings.TrimSpace(answer.Body) {
					return map[string]any{"answered": true}, nil
				}
			}
		}
	}
	if method == "discussion.post" {
		return d.mobileDiscPost(ctx, device, marshal())
	}
	reset := false
	if method == "discussion.show" {
		var q discShowParams
		if err := json.Unmarshal(marshal(), &q); err != nil {
			return nil, err
		}
		if q.Limit <= 0 {
			q.Limit = 50
		}
		q.Limit = min(q.Limit, 100)
		disc, err := d.discussion(q.Discussion)
		if err != nil {
			return nil, err
		}
		if q.Since > disc.Rev || q.Since > 0 && disc.Rev-q.Since > 100 {
			q.Since = 0
			reset = true
		}
		p["limit"], _ = json.Marshal(q.Limit)
		p["since"], _ = json.Marshal(q.Since)
	}
	handlers := map[string]func(context.Context, json.RawMessage) (any, error){
		"discussion.list": d.discList, "discussion.show": d.discShow, "discussion.search": d.discSearch,
		"discussion.create": d.discCreate, "discussion.update": d.discUpdate, "discussion.archive": d.discArchive,
		"discussion.answer": d.discAnswer, "discussion.key": d.discKey, "discussion.cancel": d.discCancel,
		"discussion.stop": d.discStop, "discussion.retry": d.discRetry, "discussion.confirm": d.discConfirm,
		"discussion.isolate": d.discIsolate, "discussion.member.add": d.discMemberAdd,
		"discussion.member.update": d.discMemberUpdate, "discussion.member.remove": d.discMemberRemove,
		"discussion.member.restart": d.discMemberRestart, "discussion.member.resume": d.collabResume,
		"discussion.share": d.collabShare, "discussion.invite": d.collabInvite, "discussion.decide": d.collabDecide,
		"discussion.person.remove": d.collabRemovePerson, "discussion.join": d.collabJoin, "discussion.leave": d.collabLeave,
		"discussion.profile.get": d.profileGet, "discussion.profile.set": d.profileSet, "discussion.profile.avatar": d.profileAvatar,
	}
	fn := handlers[method]
	if fn == nil {
		return nil, rpc.Errorf("method_not_found", "unknown method %s", method)
	}
	res, err := fn(ctx, marshal())
	if err != nil {
		return nil, err
	}
	if method == "discussion.list" {
		m := res.(map[string]any)
		if items, ok := m["items"].([]store.DiscussionSummary); ok {
			for i := range items {
				if last := items[i].Last; last != nil {
					last.Body = string([]rune(last.Body)[:min(200, len([]rune(last.Body)))])
					last.Attachments = nil
				}
			}
		}
	}
	// 只转换 ID 字段，不把本机路径或附件磁盘布局带给手机。
	b, err := json.Marshal(res)
	if err != nil {
		return nil, err
	}
	var out any
	if err = json.Unmarshal(b, &out); err != nil {
		return nil, err
	}
	d.mobileDiscOutput(out)
	if reset {
		out.(map[string]any)["reset"] = true
	}
	return out, nil
}

func (d *Daemon) mobileDiscOutput(v any) {
	switch x := v.(type) {
	case []any:
		for _, child := range x {
			d.mobileDiscOutput(child)
		}
	case map[string]any:
		delete(x, "projectPath")
		delete(x, "path")
		if report, ok := x["report"].(string); ok && report != "" {
			x["hasReport"] = true
		}
		delete(x, "report")
		if id, ok := x["workspaceId"].(string); ok && id != "" {
			if w, err := d.store.Workspace(id); err == nil {
				x["workspaceId"] = w.ProjectID + "::" + w.ID
			} else {
				x["workspaceId"] = ""
			}
		}
		for _, child := range x {
			d.mobileDiscOutput(child)
		}
	}
}

func (d *Daemon) mobileDiscAttachment(raw json.RawMessage) (any, error) {
	var p struct {
		Discussion, Message, Attachment string
		Offset                          int64
	}
	if err := json.Unmarshal(raw, &p); err != nil {
		return nil, err
	}
	f, a, err := d.mobileDiscAttachmentFile(p.Discussion, p.Message, p.Attachment)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	st, err := f.Stat()
	if err != nil {
		return nil, err
	}
	if p.Offset < 0 || p.Offset > st.Size() {
		return nil, rpc.Errorf(rpc.CodeBadRequest, "invalid discussion attachment range")
	}
	buf := make([]byte, min(int64(1<<20), st.Size()-p.Offset))
	n, err := f.ReadAt(buf, p.Offset)
	if err != nil && !errors.Is(err, io.EOF) {
		return nil, err
	}
	return map[string]any{"content": buf[:n], "size": st.Size(), "name": a.Name, "type": a.Type}, nil
}

// discThumbWidth 是手机消息里图片缩略图的宽：消息宽约 300pt，3 倍屏下够用。
const discThumbWidth = 720

// discThumbs 一次只解一张图：解码后的大图一张几十 MB，手机翻得快时常驻进程不该因此涨上去。
var discThumbs = make(chan struct{}, 1)

// mobileDiscThumb 给手机消息里的图片附件一张 JPEG 缩略图；先只读尺寸，像素过多的图不解。
func (d *Daemon) mobileDiscThumb(raw json.RawMessage) (any, error) {
	var p struct{ Discussion, Message, Attachment string }
	if err := json.Unmarshal(raw, &p); err != nil {
		return nil, err
	}
	f, _, err := d.mobileDiscAttachmentFile(p.Discussion, p.Message, p.Attachment)
	if err != nil {
		return nil, err
	}
	b, err := io.ReadAll(f)
	f.Close()
	if err != nil {
		return nil, err
	}
	cfg, _, err := image.DecodeConfig(bytes.NewReader(b))
	if err != nil {
		return nil, rpc.Errorf(rpc.CodeBadRequest, "not an image")
	}
	if cfg.Width*cfg.Height > 24<<20 {
		return nil, rpc.Errorf(rpc.CodeBadRequest, "image is too large to preview")
	}
	discThumbs <- struct{}{}
	defer func() { <-discThumbs }()
	img, size, err := media.ScaledImageBytes(b, discThumbWidth)
	if err != nil {
		return nil, rpc.Errorf(rpc.CodeBadRequest, "not an image")
	}
	var out bytes.Buffer
	if err := jpeg.Encode(&out, img, &jpeg.Options{Quality: 82}); err != nil {
		return nil, err
	}
	return map[string]any{"content": out.Bytes(), "width": size.X, "height": size.Y}, nil
}

// mobileDiscAttachmentFile 打开讨论附件的文件：只认附件目录里的普通文件，软链不能把读取导向目录外。
func (d *Daemon) mobileDiscAttachmentFile(discussion, message, attachment string) (*os.File, store.Attachment, error) {
	disc, err := d.discussion(discussion)
	if err != nil {
		return nil, store.Attachment{}, err
	}
	msg, err := d.store.DiscMessage(message)
	if err != nil || msg.Discussion != disc.Key {
		return nil, store.Attachment{}, rpc.Errorf(rpc.CodeNotFound, "discussion attachment not found")
	}
	for _, a := range msg.Attachments {
		if a.ID != attachment {
			continue
		}
		root := disc.ProjectPath
		if proj, e := d.store.FindProject(disc.ProjectID); e == nil {
			root = proj.Path
		}
		rel := filepath.Join(store.AttachmentDir(disc.LocalID), a.ID, safeFileName(a.Name))
		if disc.Mirror {
			root = d.cfg.Home
			rel = store.MirrorAttachmentPath(disc.Key, a.ID, a.Name)
		}
		// 库里的路径不能越过附件目录，软链也不能把读取导向目录外。
		if filepath.Clean(a.Path) != rel {
			return nil, a, rpc.Errorf(rpc.CodeBadRequest, "invalid discussion attachment path")
		}
		dir := filepath.Join(root, filepath.Dir(rel))
		realDir, e := filepath.EvalSymlinks(dir)
		name, e2 := filepath.EvalSymlinks(filepath.Join(root, rel))
		realRoot, e3 := filepath.EvalSymlinks(root)
		contained := func(base, path string) bool {
			r, e := filepath.Rel(base, path)
			return e == nil && r != ".." && !strings.HasPrefix(r, ".."+string(filepath.Separator))
		}
		if e == nil && e2 == nil && e3 == nil && (realDir != filepath.Join(realRoot, filepath.Dir(rel)) || !contained(realDir, name)) {
			return nil, a, rpc.Errorf(rpc.CodeBadRequest, "invalid discussion attachment path")
		}
		if e != nil || e2 != nil || e3 != nil {
			if disc.Mirror {
				missing := false
				if rc := d.roomConnFor(disc.Key); rc != nil {
					rc.stmu.Lock()
					missing = rc.missing[a.ID]
					rc.stmu.Unlock()
				}
				if !missing {
					return nil, a, rpc.Errorf("attachment_pending", "discussion attachment is not downloaded yet")
				}
			}
			return nil, a, rpc.Errorf(rpc.CodeNotFound, "discussion attachment not found")
		}
		f, err := os.Open(name)
		if err != nil {
			return nil, a, err
		}
		st, err := f.Stat()
		if err != nil || !st.Mode().IsRegular() || st.Size() > discMaxAttachSize {
			f.Close()
			return nil, a, rpc.Errorf(rpc.CodeBadRequest, "invalid discussion attachment range")
		}
		return f, a, nil
	}
	return nil, store.Attachment{}, rpc.Errorf(rpc.CodeNotFound, "discussion attachment not found")
}

func (d *Daemon) mobileDiscReport(raw json.RawMessage) (any, error) {
	var p struct {
		Discussion, Message string
		Offset              int64
	}
	if err := json.Unmarshal(raw, &p); err != nil {
		return nil, err
	}
	disc, err := d.discussion(p.Discussion)
	if err != nil {
		return nil, err
	}
	m, err := d.store.DiscMessage(p.Message)
	if err != nil || m.Discussion != disc.Key || m.Report == "" || !filepath.IsAbs(m.Report) {
		return nil, rpc.Errorf(rpc.CodeNotFound, "discussion report not found")
	}
	f, err := os.Open(m.Report)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	st, err := f.Stat()
	if err != nil {
		return nil, err
	}
	if !st.Mode().IsRegular() || st.Size() > discMaxAttachSize || p.Offset < 0 || p.Offset > st.Size() {
		return nil, rpc.Errorf(rpc.CodeBadRequest, "invalid discussion report range")
	}
	buf := make([]byte, min(int64(1<<20), st.Size()-p.Offset))
	n, err := f.ReadAt(buf, p.Offset)
	if err != nil && !errors.Is(err, io.EOF) {
		return nil, err
	}
	return map[string]any{"content": buf[:n], "size": st.Size(), "name": filepath.Base(m.Report), "type": mime.TypeByExtension(filepath.Ext(m.Report))}, nil
}
