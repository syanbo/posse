package daemon

// 讨论的局域网协作：加入者一侧。每个加入的房间一个连接循环（roomConn）：申请没结果时轮询，
// 批准后用房间凭据连上房主，按游标补齐增量、领取归自己的请求（先核对本机授权意图）、补发 outbox、下载附件。
// 断开后按退避重连；已领取没开始的轮次暂停，等拥有者确认再开始。成员不监听端口。

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"posse/internal/collaboration"
	"posse/internal/rpc"
	"posse/internal/store"
)

// 断开时暂停已领取、还没开始的轮次的原因（界面认这个串，给「继续」）
const collabLostReason = "connection to the host was lost; confirm to continue"

// collabUnreachable 是连不上房主时的提示：系统不一定能区分没给本地网络权限和网络不通，合在一起说。
const collabUnreachable = "Could not reach the host. Check that both Macs are on the same network, Posse is running on the host with sharing turned on, and Local Network access is allowed for Posse on both Macs (System Settings › Privacy & Security › Local Network)."

var errStopRoom = errors.New("stop")

type roomStatus struct {
	Connected bool      `json:"connected"`
	Addr      string    `json:"addr,omitempty"`
	Error     string    `json:"error,omitempty"`
	Since     time.Time `json:"since"`
}

type roomConn struct {
	d      *Daemon
	room   string
	mu     sync.Mutex // 一次只有一个调用在用连接（请求、同步、补发）
	cli    *collaboration.Client
	pokeCh chan struct{}
	ctx    context.Context
	cancel context.CancelFunc
	done   chan struct{}

	stmu     sync.Mutex
	status   roomStatus
	presence map[string]time.Time // 房主告诉的在线的人（收到的时间）
	failed   map[string]int       // 附件下载失败次数
}

func newRoomConn(d *Daemon, room string) *roomConn {
	return &roomConn{d: d, room: room, pokeCh: make(chan struct{}, 1), done: make(chan struct{}), failed: map[string]int{}}
}

func (rc *roomConn) stopped() bool {
	select {
	case <-rc.done:
		return true
	default:
		return false
	}
}

func (rc *roomConn) stop() {
	rc.mu.Lock()
	cancel := rc.cancel
	rc.mu.Unlock()
	if cancel != nil {
		cancel()
	}
	<-rc.done
}

func (rc *roomConn) poke() {
	select {
	case rc.pokeCh <- struct{}{}:
	default:
	}
}

func (rc *roomConn) setStatus(connected bool, addr, errText string) {
	rc.stmu.Lock()
	changed := rc.status.Connected != connected || rc.status.Error != errText
	if rc.status.Connected != connected {
		rc.status.Since = time.Now()
	}
	rc.status.Connected, rc.status.Addr, rc.status.Error = connected, addr, errText
	rc.stmu.Unlock()
	if changed {
		// 连接状态不推进讨论版本：界面每次取详情时随 collab 带上
		rc.d.taskNotify()
	}
}

func (d *Daemon) roomStatus(room string) roomStatus {
	d.collab.mu.Lock()
	rc := d.collab.rooms[room]
	d.collab.mu.Unlock()
	if rc == nil {
		return roomStatus{}
	}
	rc.stmu.Lock()
	defer rc.stmu.Unlock()
	return rc.status
}

func (d *Daemon) roomConnFor(room string) *roomConn {
	d.collab.mu.Lock()
	defer d.collab.mu.Unlock()
	return d.collab.rooms[room]
}

func (rc *roomConn) run(parent context.Context) {
	defer close(rc.done)
	ctx, cancel := context.WithCancel(parent)
	rc.mu.Lock()
	rc.ctx, rc.cancel = ctx, cancel
	rc.mu.Unlock()
	defer cancel()
	backoff := time.Second
	for ctx.Err() == nil {
		j, err := rc.d.store.Joined(rc.room)
		if err != nil {
			return
		}
		start := time.Now()
		switch j.State {
		case store.RequestPending:
			err = rc.pollJoin(ctx, j)
		case store.JoinedActive:
			err = rc.session(ctx, j)
		default:
			return
		}
		if errors.Is(err, errStopRoom) || ctx.Err() != nil {
			rc.setStatus(false, "", "")
			return
		}
		if err != nil {
			msg := err.Error()
			var de *collaboration.DialError
			if errors.As(err, &de) {
				log.Printf("collaboration %s: %v", short(rc.room), err)
				msg = collabUnreachable
			}
			rc.setStatus(false, "", msg)
		}
		if time.Since(start) > time.Minute {
			backoff = time.Second
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(backoff):
		case <-rc.pokeCh:
		}
		backoff = min(backoff*2, 30*time.Second)
	}
}

func short(s string) string {
	if len(s) > 12 {
		return s[:12]
	}
	return s
}

func (rc *roomConn) dial(ctx context.Context, j store.Joined) (*collaboration.Client, error) {
	id, err := rc.d.identity()
	if err != nil {
		return nil, err
	}
	rc.d.collab.mu.Lock()
	if rc.d.collab.cache == nil {
		rc.d.collab.cache = tlsSessionCache()
	}
	cache := rc.d.collab.cache
	rc.d.collab.mu.Unlock()
	return collaboration.Dial(ctx, id, j.Host, j.Addrs, cache)
}

// pollJoin 提交（或重交同一份）加入申请，等房主批准：批准后存凭据，转成 active。
func (rc *roomConn) pollJoin(ctx context.Context, j store.Joined) error {
	cli, err := rc.dial(ctx, j)
	if err != nil {
		return err
	}
	defer cli.Close()
	rc.setStatus(true, cli.Addr, "")
	id, _ := rc.d.identity()
	for {
		var st collabJoinStatus
		var err error
		if j.Secret != "" {
			err = cli.Call(ctx, "join", collabJoinParams{Room: j.Room, Secret: j.Secret, Request: j.Request, Name: id.Name, Version: collaboration.ProtocolVersion}, &st)
		} else {
			err = cli.Call(ctx, "join.status", collabJoinParams{Room: j.Room, Request: j.Request}, &st)
		}
		if err != nil {
			var ce *collaboration.Error
			if errors.As(err, &ce) && ce.Code != collaboration.CodeRateLimited && ce.Code != collaboration.CodeInternal {
				rc.d.store.SetJoined(j.Room, store.RequestRejected, 0, nil, ce.Message)
				rc.d.taskNotify()
				return errStopRoom
			}
			return err
		}
		switch st.State {
		case store.RequestApproved:
			if err := rc.d.saveCredential(j.Room, j.Host, st.Generation, st.Credential); err != nil {
				return err
			}
			rc.d.store.SetJoined(j.Room, store.JoinedActive, st.Generation, nil, "")
			rc.d.taskNotify()
			return nil
		case store.RequestRejected, store.PersonRemoved:
			rc.d.store.SetJoined(j.Room, store.RequestRejected, 0, nil, "the host declined the request")
			rc.d.taskNotify()
			return errStopRoom
		}
		// 交过了：之后只查结果（凭据不再随请求发出去）
		if j.Secret != "" {
			rc.d.store.SetJoined(j.Room, store.RequestPending, 0, nil, "")
			j.Secret = ""
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-cli.Done():
			return collaboration.ErrClosed
		case <-time.After(3 * time.Second):
		case <-rc.pokeCh:
		}
	}
}

type credentialFile struct {
	Room       string `json:"room"`
	Host       string `json:"host"`
	Generation int64  `json:"generation"`
	Credential string `json:"credential"`
}

func (d *Daemon) credentialPath(room string) string {
	return filepath.Join(d.cfg.Home, "collab", store.RoomDir(room), "credential.json")
}

// saveCredential 把房间凭据存成 0600 的私有文件（原子写入）；凭据不进数据库、不写日志。
func (d *Daemon) saveCredential(room, host string, gen int64, cred string) error {
	if cred == "" {
		return fmt.Errorf("host sent no credential")
	}
	b, _ := json.MarshalIndent(credentialFile{Room: room, Host: host, Generation: gen, Credential: cred}, "", "  ")
	return collaboration.WriteFile(d.credentialPath(room), b)
}

func (d *Daemon) loadCredential(room string) (credentialFile, error) {
	var c credentialFile
	b, err := os.ReadFile(d.credentialPath(room))
	if err != nil {
		return c, err
	}
	err = json.Unmarshal(b, &c)
	return c, err
}

// session 用凭据连上房主，一直同步到断开。
func (rc *roomConn) session(ctx context.Context, j store.Joined) error {
	cred, err := rc.d.loadCredential(j.Room)
	if err != nil {
		rc.d.store.LocalRevoke(j.Room, store.PersonRemoved, "the room credential on this computer is missing; ask the host for a new invitation", time.Now())
		rc.d.taskNotify()
		return errStopRoom
	}
	cli, err := rc.dial(ctx, j)
	if err != nil {
		return err
	}
	var hello struct {
		Generation int64 `json:"generation"`
	}
	if err := cli.Call(ctx, "hello", collabHello{Room: j.Room, Credential: cred.Credential, Version: collaboration.ProtocolVersion}, &hello); err != nil {
		cli.Close()
		return rc.callFailed(err)
	}
	rc.mu.Lock()
	rc.cli = cli
	rc.mu.Unlock()
	rc.setStatus(true, cli.Addr, "")
	defer func() {
		rc.mu.Lock()
		rc.cli = nil
		rc.mu.Unlock()
		cli.Close()
		// 已领取、还没开始的轮次等拥有者确认再开始；已经开始的照常做完，结果留在 outbox
		if n, _ := rc.d.store.PauseOwnQueues(j.Room, collabLostReason); n > 0 {
			rc.d.taskNotify()
		}
	}()
	beat := time.NewTicker(collaboration.HeartbeatEvery)
	defer beat.Stop()
	if err := rc.heartbeat(ctx); err != nil {
		return err
	}
	for {
		if err := rc.step(ctx); err != nil {
			return err
		}
		rc.fetchAttachments(ctx)
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-cli.Done():
			return collaboration.ErrClosed
		case <-cli.Pushes():
		case <-rc.pokeCh:
		case <-beat.C:
			if err := rc.heartbeat(ctx); err != nil {
				return err
			}
		}
	}
}

// heartbeat 每 15 秒告诉房主还连着（房主据此显示在线、执行有没有失联），顺带拿回谁在线。
func (rc *roomConn) heartbeat(ctx context.Context) error {
	var res struct {
		Online []string `json:"online"`
	}
	rc.mu.Lock()
	cli := rc.cli
	var err error
	if cli != nil {
		err = cli.Call(ctx, "heartbeat", map[string]any{}, &res)
	}
	rc.mu.Unlock()
	if err != nil {
		return rc.callFailed(err)
	}
	now := time.Now()
	rc.stmu.Lock()
	rc.presence = map[string]time.Time{}
	for _, fp := range res.Online {
		rc.presence[fp] = now
	}
	rc.stmu.Unlock()
	return nil
}

// callFailed 看房主的错误是不是终态：撤销了就停在本机缓存上，房主关了共享就慢慢重试。
func (rc *roomConn) callFailed(err error) error {
	switch {
	case collaboration.IsCode(err, collaboration.CodeRevoked), collaboration.IsCode(err, collaboration.CodeUnauthorized):
		var ce *collaboration.Error
		errors.As(err, &ce)
		rc.d.revokeLocal(rc.room, store.PersonRemoved, ce.Message)
		return errStopRoom
	case collaboration.IsCode(err, collaboration.CodeOffline):
		return errors.New("the host turned off sharing for this discussion; you can read what was already synced")
	case collaboration.IsCode(err, collaboration.CodeUpgrade):
		var ce *collaboration.Error
		errors.As(err, &ce)
		return errors.New(ce.Message)
	}
	return err
}

// revokeLocal：被移除或退出之后，凭据删掉，缓存和本机结果留着。
func (d *Daemon) revokeLocal(room, state, reason string) {
	d.store.LocalRevoke(room, state, reason, time.Now())
	os.Remove(d.credentialPath(room))
	d.taskNotify()
}

// step 是一轮：补齐增量、领取归自己的请求、补发 outbox。
func (rc *roomConn) step(ctx context.Context) error {
	rc.mu.Lock()
	defer rc.mu.Unlock()
	if rc.cli == nil {
		return collaboration.ErrClosed
	}
	if err := rc.syncLocked(ctx); err != nil {
		return err
	}
	if err := rc.claimLocked(ctx); err != nil {
		return err
	}
	return rc.flushLocked(ctx)
}

func (rc *roomConn) syncLocked(ctx context.Context) error {
	changed := false
	for range 1000 {
		j, err := rc.d.store.Joined(rc.room)
		if err != nil {
			return err
		}
		var b store.SyncBatch
		if err := rc.cli.Call(ctx, "sync", map[string]any{"since": j.Cursor, "limit": 200}, &b); err != nil {
			return rc.callFailed(err)
		}
		ch, err := rc.d.store.ApplySync(rc.room, rc.d.store.Self(), b)
		if err != nil {
			return err
		}
		changed = changed || ch
		if !b.More {
			break
		}
	}
	if changed {
		rc.d.taskNotify()
		// 自己被移除（同步里看到资格失效）：停在缓存上
		if p, err := rc.d.store.Person(rc.room, rc.d.store.Self()); err == nil && p.State != store.PersonActive {
			rc.d.revokeLocal(rc.room, p.State, "your access to this discussion was revoked")
			return errStopRoom
		}
	}
	return nil
}

// claimLocked 领取房主那边归自己、还在排队的请求：先核对本机的授权意图（这台电脑上发出的 @、
// 目标 Agent、配置版本、请求摘要），对不上的不领取（记成拒绝执行），房主伪造不了一条让本机执行的请求。
func (rc *roomConn) claimLocked(ctx context.Context) error {
	d := rc.d
	self := d.store.Self()
	xs, err := d.store.ClaimCandidates(rc.room, self)
	if err != nil {
		return err
	}
	j, _ := d.store.Joined(rc.room)
	for _, x := range xs {
		if reason := d.intentMismatch(x); reason != "" {
			d.store.Accept(rc.room, x.ID, j.Generation, store.AcceptRefused, reason, time.Now())
			d.taskNotify()
			continue
		}
		var res map[string]any
		if err := rc.cli.Call(ctx, "claim", map[string]string{"execution": x.ID}, &res); err != nil {
			if collaboration.IsCode(err, collaboration.CodeConflict) || collaboration.IsCode(err, collaboration.CodeNotFound) {
				continue // 已经取消或不在了：下一次同步会带来新状态
			}
			return rc.callFailed(err)
		}
		// 先下载请求和引用里的附件，再进本机队列（上下文快照给本机缓存路径）
		d.fetchMessageAttachments(ctx, rc, x.Message)
		if err := d.store.Accept(rc.room, x.ID, j.Generation, store.AcceptAccepted, "", time.Now()); err != nil {
			return err
		}
		d.taskNotify()
		d.discKick()
	}
	return nil
}

// intentMismatch 是这轮执行和本机授权意图对不上的原因，对得上为空。
func (d *Daemon) intentMismatch(x store.Execution) string {
	key := ""
	var m store.DiscMessage
	if x.RetryOf != "" {
		key = "retry:" + x.RetryOf
	} else {
		var err error
		if m, err = d.store.DiscMessage(x.Message); err != nil || m.ClientID == "" {
			return "this request was not sent from this computer"
		}
		key = m.ClientID
	}
	in, err := d.store.Intent(x.Discussion, key)
	if err != nil {
		return "this request was not sent from this computer"
	}
	if in.Member != x.Member {
		return "the request targets a different agent than the one you asked"
	}
	if x.RetryOf == "" {
		if in.Digest != store.IntentDigest(m.Body, m.Quote, m.Target, m.Attachments) {
			return "the request content differs from what you sent"
		}
		if in.ConfigVersion != x.ConfigVersion {
			return "the agent's configuration changed after you sent the request"
		}
	}
	return ""
}

// flushLocked 按顺序补发 outbox；房主拒收（撤销、归属不符、冲突）是终态，不再重试。
func (rc *roomConn) flushLocked(ctx context.Context) error {
	items, err := rc.d.store.PendingOutbox(rc.room)
	if err != nil || len(items) == 0 {
		return err
	}
	for _, it := range items {
		err := rc.cli.Call(ctx, "event", map[string]any{"execution": it.Exec, "seq": it.Seq, "digest": it.Digest, "event": it.Event}, nil)
		var ce *collaboration.Error
		switch {
		case err == nil:
			rc.d.store.MarkOutbox(it.Exec, it.Seq, store.OutboxSent, "")
		case errors.As(err, &ce) && ce.Code == collaboration.CodeRevoked:
			return rc.callFailed(err)
		case errors.As(err, &ce) && ce.Code != collaboration.CodeInternal && ce.Code != collaboration.CodeRateLimited:
			rc.d.store.MarkOutbox(it.Exec, it.Seq, store.OutboxRejected, ce.Message)
		default:
			return err
		}
	}
	rc.d.taskNotify()
	return nil
}

// fetchAttachments 把缓存里还没下载的附件下下来（失败的最多试 3 次）。
func (rc *roomConn) fetchAttachments(ctx context.Context) {
	atts, err := rc.d.store.MirrorAttachments(rc.room)
	if err != nil {
		return
	}
	for _, a := range atts {
		if ctx.Err() != nil {
			return
		}
		rc.fetch(ctx, a)
	}
}

func (d *Daemon) fetchMessageAttachments(ctx context.Context, rc *roomConn, msgID string) {
	m, err := d.store.DiscMessage(msgID)
	if err != nil {
		return
	}
	atts := m.Attachments
	if m.Quote != "" {
		if q, err := d.store.DiscMessage(m.Quote); err == nil {
			atts = append(atts, q.Attachments...)
		}
	}
	for _, a := range atts {
		rc.fetchLocked(ctx, a)
	}
}

func (rc *roomConn) fetch(ctx context.Context, a store.Attachment) {
	rc.mu.Lock()
	defer rc.mu.Unlock()
	rc.fetchLocked(ctx, a)
}

// fetchLocked 按附件 ID 分块下载到缓存：先写同目录的临时文件，长度和摘要对上再改名。
func (rc *roomConn) fetchLocked(ctx context.Context, a store.Attachment) {
	if rc.cli == nil || rc.failed[a.ID] >= 3 || a.Path == "" {
		return
	}
	dst := filepath.Join(rc.d.cfg.Home, a.Path)
	if _, err := os.Stat(dst); err == nil {
		return
	}
	if err := rc.downloadTo(ctx, a, dst); err != nil {
		rc.failed[a.ID]++
		log.Printf("collaboration %s: attachment %s: %v", short(rc.room), a.ID, err)
		return
	}
	rc.d.taskNotify()
}

func (rc *roomConn) downloadTo(ctx context.Context, a store.Attachment, dst string) error {
	if err := os.MkdirAll(filepath.Dir(dst), 0o700); err != nil {
		return err
	}
	tmp := dst + ".part"
	f, err := os.OpenFile(tmp, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	defer os.Remove(tmp)
	h := sha256.New()
	var off int64
	sha := a.SHA
	for {
		var r struct {
			Data []byte `json:"data"`
			Size int64  `json:"size"`
			EOF  bool   `json:"eof"`
			SHA  string `json:"sha"`
		}
		if err := rc.cli.Call(ctx, "download", map[string]any{"attachment": a.ID, "offset": off}, &r); err != nil {
			f.Close()
			return err
		}
		if sha == "" {
			sha = r.SHA
		}
		if r.Size > discMaxAttachSize || off+int64(len(r.Data)) > r.Size {
			f.Close()
			return fmt.Errorf("attachment is larger than announced")
		}
		f.Write(r.Data)
		h.Write(r.Data)
		off += int64(len(r.Data))
		if r.EOF || len(r.Data) == 0 {
			break
		}
	}
	if err := f.Close(); err != nil {
		return err
	}
	if sha != "" && hex.EncodeToString(h.Sum(nil)) != sha {
		return fmt.Errorf("attachment content does not match its digest")
	}
	return os.Rename(tmp, dst)
}

// call 在连着的时候发一个请求给房主，然后补齐增量（让本机缓存马上有结果）。没连着报房主离线。
func (rc *roomConn) call(method string, params, out any) error {
	rc.mu.Lock()
	defer rc.mu.Unlock()
	if rc.cli == nil {
		return hostOffline(rc)
	}
	ctx, cancel := context.WithTimeout(rc.ctx, 30*time.Second)
	defer cancel()
	if err := rc.cli.Call(ctx, method, params, out); err != nil {
		var ce *collaboration.Error
		if errors.As(err, &ce) {
			if ce.Code == collaboration.CodeRevoked {
				go rc.d.revokeLocal(rc.room, store.PersonRemoved, ce.Message)
			}
			return rpc.Errorf(rpc.CodeConflict, "%s", ce.Message)
		}
		return rpc.Errorf(rpc.CodeConflict, "the host did not answer (%v); your draft is kept, try again", err)
	}
	if err := rc.syncLocked(ctx); err != nil && !errors.Is(err, errStopRoom) {
		log.Printf("collaboration %s: sync after %s: %v", short(rc.room), method, err)
	}
	return nil
}

func hostOffline(rc *roomConn) error {
	st := ""
	if rc != nil {
		rc.stmu.Lock()
		st = rc.status.Error
		rc.stmu.Unlock()
	}
	if st == "" {
		st = "the host is offline"
	}
	return rpc.Errorf(rpc.CodeConflict, "not connected to the host: %s Your draft is kept.", strings.TrimSuffix(st, ".")+".")
}

// upload 把本机文件分块传给房主，返回上传 ID（发消息时引用）。同样内容房主已有时不用传。
func (rc *roomConn) upload(path, name string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", rpc.Errorf(rpc.CodeBadRequest, "attachment %s: %v", filepath.Base(path), err)
	}
	defer f.Close()
	st, err := f.Stat()
	if err != nil || !st.Mode().IsRegular() {
		return "", rpc.Errorf(rpc.CodeBadRequest, "attachment %s is not a regular file", filepath.Base(path))
	}
	if st.Size() > discMaxAttachSize {
		return "", rpc.Errorf(rpc.CodeBadRequest, "attachment %s is larger than %d MiB", filepath.Base(path), discMaxAttachSize>>20)
	}
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", err
	}
	sum := hex.EncodeToString(h.Sum(nil))
	if name == "" {
		name = filepath.Base(path)
	}
	rc.mu.Lock()
	defer rc.mu.Unlock()
	if rc.cli == nil {
		return "", hostOffline(rc)
	}
	ctx, cancel := context.WithTimeout(rc.ctx, 10*time.Minute)
	defer cancel()
	var begin struct {
		Upload string `json:"upload"`
		Done   bool   `json:"done"`
	}
	if err := rc.cli.Call(ctx, "upload.begin", map[string]any{"name": name, "size": st.Size(), "sha": sum}, &begin); err != nil {
		return "", wrapHostErr(err)
	}
	if begin.Done {
		return begin.Upload, nil
	}
	buf := make([]byte, collaboration.ChunkSize)
	var off int64
	for {
		n, err := f.ReadAt(buf, off)
		if n > 0 {
			if err := rc.cli.Call(ctx, "upload.chunk", map[string]any{"upload": begin.Upload, "offset": off, "data": buf[:n]}, nil); err != nil {
				return "", wrapHostErr(err)
			}
			off += int64(n)
		}
		if err == io.EOF || n == 0 {
			break
		}
		if err != nil {
			return "", err
		}
	}
	if err := rc.cli.Call(ctx, "upload.end", map[string]any{"upload": begin.Upload}, nil); err != nil {
		return "", wrapHostErr(err)
	}
	return begin.Upload, nil
}

func wrapHostErr(err error) error {
	var ce *collaboration.Error
	if errors.As(err, &ce) {
		return rpc.Errorf(rpc.CodeConflict, "%s", ce.Message)
	}
	return rpc.Errorf(rpc.CodeConflict, "the host did not answer (%v); your draft is kept, try again", err)
}

// --- 加入、退出（本机 RPC）

type collabJoinLocal struct {
	Invite string   `json:"invite"`
	Addrs  []string `json:"addrs"` // 手动改过的地址（组播被挡、换了网段时）
	Caller string   `json:"caller"`
}

// collabJoin 用粘贴的邀请提交加入申请。先把申请（含凭据）落在本机，再交给房主：响应丢了重交同一份，
// 不会用掉第二次邀请。邀请无效、过期、被用过时报错，不留下空的讨论。
func (d *Daemon) collabJoin(ctx context.Context, raw json.RawMessage) (any, error) {
	var p collabJoinLocal
	if err := rpc.Decode(raw, &p); err != nil {
		return nil, err
	}
	if err := d.userOnly(raw); err != nil {
		return nil, err
	}
	inv, err := collaboration.ParseInvite(p.Invite)
	if err != nil {
		return nil, rpc.Errorf(rpc.CodeBadRequest, "%v", err)
	}
	if len(p.Addrs) > 0 {
		inv.Addrs = p.Addrs
	}
	id, err := d.identity()
	if err != nil {
		return nil, err
	}
	if inv.Host == id.ID {
		return nil, rpc.Errorf(rpc.CodeConflict, "this invitation is for a discussion hosted on this computer")
	}
	if old, err := d.store.Joined(inv.Room); err == nil && (old.State == store.JoinedActive || old.State == store.RequestPending) {
		return nil, rpc.Errorf(rpc.CodeConflict, "you already joined or asked to join this discussion")
	}
	_, local, _ := collaboration.SplitRoomKey(inv.Room)
	now := time.Now()
	disc := store.Discussion{Key: inv.Room, Host: inv.Host, LocalID: local, Title: cmpOr(inv.Title, local), ProjectName: inv.HostName, ProjectPath: d.cfg.Home, CreatedAt: now}
	j := store.Joined{Room: inv.Room, Host: inv.Host, HostName: inv.HostName, Addrs: inv.Addrs, Request: collaboration.NewRequestID(), State: store.RequestPending,
		Secret: inv.Secret, JoinedAt: now}
	if err := d.store.CreateMirror(disc, j); err != nil {
		return nil, discError(err)
	}
	// 先当面试一次：邀请不对马上告诉用户
	dctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	cli, derr := collaboration.Dial(dctx, id, inv.Host, inv.Addrs, nil)
	res := map[string]any{"discussion": inv.Room, "state": store.RequestPending, "host": inv.HostName, "fingerprint": inv.Host}
	if derr != nil {
		log.Printf("collaboration join %s: %v", short(inv.Room), derr)
		res["error"] = collabUnreachable
	} else {
		var st collabJoinStatus
		err := cli.Call(dctx, "join", collabJoinParams{Room: inv.Room, Secret: inv.Secret, Request: j.Request, Name: id.Name, Version: collaboration.ProtocolVersion}, &st)
		cli.Close()
		var ce *collaboration.Error
		if errors.As(err, &ce) && ce.Code != collaboration.CodeRateLimited && ce.Code != collaboration.CodeInternal {
			d.store.SetJoined(inv.Room, store.RequestRejected, 0, nil, ce.Message)
			d.store.DropMirror(inv.Room)
			d.taskNotify()
			return nil, rpc.Errorf(rpc.CodeConflict, "%s", ce.Message)
		}
		if err == nil {
			d.store.SetJoined(inv.Room, store.RequestPending, 0, nil, "")
			if st.Title != "" {
				res["title"] = st.Title
			}
		}
	}
	d.collabEnsure()
	d.taskNotify()
	return res, nil
}

// collabLeave 退出加入的讨论：房主撤销成员资格（同撤销），本机删凭据、留缓存和本机结果。
func (d *Daemon) collabLeave(ctx context.Context, raw json.RawMessage) (any, error) {
	var p struct {
		Discussion string `json:"discussion"`
	}
	if err := rpc.Decode(raw, &p); err != nil {
		return nil, err
	}
	if err := d.userOnly(raw); err != nil {
		return nil, err
	}
	disc, err := d.discussion(p.Discussion)
	if err != nil {
		return nil, err
	}
	if !disc.Mirror {
		return nil, rpc.Errorf(rpc.CodeConflict, "you host this discussion; archive it instead")
	}
	j, err := d.store.Joined(disc.Key)
	if err != nil {
		return nil, err
	}
	rc := d.roomConnFor(disc.Key)
	switch j.State {
	case store.JoinedActive:
		if rc == nil {
			return nil, hostOffline(nil)
		}
		if err := rc.call("leave", map[string]any{}, nil); err != nil {
			return nil, err
		}
	case store.RequestPending:
	default:
		return nil, rpc.Errorf(rpc.CodeConflict, "you are no longer in this discussion")
	}
	if rc != nil {
		go rc.stop()
	}
	d.revokeLocal(disc.Key, store.PersonLeft, "you left the discussion")
	if j.State == store.RequestPending {
		d.store.DropMirror(disc.Key)
	}
	return map[string]string{"left": disc.Key}, nil
}

// collabResume 是断开重连之后拥有者确认：已领取的轮次接着开始。
func (d *Daemon) collabResume(ctx context.Context, raw json.RawMessage) (any, error) {
	var p discMemberParams
	if err := rpc.Decode(raw, &p); err != nil {
		return nil, err
	}
	if err := d.userOnly(raw); err != nil {
		return nil, err
	}
	m, err := d.memberSel(p.Member)
	if err != nil {
		return nil, err
	}
	if !d.mine(m.Owner) {
		return nil, notYours(m.Name)
	}
	if m.Paused != collabLostReason && m.Paused != "daemon restarted" {
		return nil, rpc.Errorf(rpc.CodeConflict, "the agent is not paused for a lost connection (%s)", dash(m.Paused))
	}
	if err := d.store.SetMemberPaused(m.ID, ""); err != nil {
		return nil, discError(err)
	}
	d.taskNotify()
	d.discKick()
	return d.store.Member(m.ID)
}

// mine 判断一个 Agent 或执行是不是本机的（拥有者为空是本机房主的旧记录）。
func (d *Daemon) mine(owner string) bool {
	return owner == "" || owner == d.store.Self()
}

func notYours(name string) error {
	return rpc.Errorf(rpc.CodeConflict, "%s belongs to another person; only its owner can do this. @ the owner to ask them", name)
}

// mirrorConn 是加入的讨论到房主的连接；不在加入中（被拒、移除、退出）时报错。
func (d *Daemon) mirrorConn(disc store.Discussion) (*roomConn, error) {
	j, err := d.store.Joined(disc.Key)
	if err != nil {
		return nil, err
	}
	if j.State != store.JoinedActive {
		return nil, rpc.Errorf(rpc.CodeConflict, "you are no longer a member of this discussion; the history stays readable here")
	}
	rc := d.roomConnFor(disc.Key)
	if rc == nil {
		return nil, hostOffline(nil)
	}
	return rc, nil
}
