package daemon

// 讨论的局域网协作（docs/features/discussions.md「局域网多人」）：房主一侧。
// 房主的常驻进程在用户打开共享后监听协作端口（POSSE_COLLAB_PORT，默认 6791，开发包 6792），
// 拿到 possed.lock 之后才监听，交接开始时停下（失败再恢复），两代进程不会同时接受写入。
// 每个请求都按 TLS 证明的指纹和房间凭据复核成员资格和代际；协作服务只有下面这些窄方法，
// 不转发本机 RPC、终端、手机能力。

import (
	"context"
	"crypto/sha256"
	"crypto/tls"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"posse/internal/agent"
	"posse/internal/collaboration"
	"posse/internal/rpc"
	"posse/internal/store"
)

const (
	collabDefaultPort = 6791
	collabDevPort     = 6792
	// 每位成员 10 秒内最多发这么多条消息，超了拒绝（草稿留在对方窗口里）
	collabPostBurst = 20
	// 每位成员同时在传的附件
	collabUploadConcurrency = 2
)

// collabState 是协作在内存里的部分：服务、加入的房间的连接循环和连接状态。记录都在 SQLite。
type collabState struct {
	mu        sync.Mutex
	srv       *collaboration.Server
	listenErr string
	paused    bool // 交接开始了：不监听、不连房主
	started   bool // 拿到锁之后才开始
	ctx       context.Context
	rooms     map[string]*roomConn // 加入的房间 → 连接循环
	posts     map[string][]time.Time
	pushed    map[string]int64 // 房间 → 推送过的版本
	kick      chan struct{}
	cache     tls.ClientSessionCache
	uploads   sync.Map // 上传 ID → *sync.Mutex（同一份上传的分块串行写）
	// sessUploads 是连接开的、还没传完的上传：*collaboration.Session → *[]string
	sessUploads sync.Map
	// now 是测试换掉的时钟（心跳超时的显示）
	now func() time.Time
	// manualFetch 是测试关掉后台附件循环（由测试自己调 fetchRound）
	manualFetch bool
	// listen 测试里换成别的地址（端口 0）
	listenAddr string
}

func (d *Daemon) collabPort() int {
	port := collabDefaultPort
	if runningDevBundle() {
		port = collabDevPort
	}
	if v, err := strconv.Atoi(os.Getenv("POSSE_COLLAB_PORT")); err == nil {
		port = v
	}
	return port
}

// identity 读本机安装身份（没有就生成：首次建讨论、首次加入时），并告诉 store 自己是谁。
func (d *Daemon) identity() (collaboration.Identity, error) {
	id, err := collaboration.LoadOrCreate(d.cfg.Home)
	if err != nil {
		return id, rpc.Errorf(rpc.CodeInternal, "installation identity: %v", err)
	}
	if d.store.Self() != id.ID {
		d.store.SetSelf(id.ID)
	}
	return id, nil
}

// loadSelf 在启动时读已有的身份（没有不生成）。
func (d *Daemon) loadSelf() {
	if id, err := collaboration.Load(d.cfg.Home); err == nil {
		d.store.SetSelf(id.ID)
	}
}

// collabStart 在拿到 possed.lock 之后调用：按需监听，开始连加入的房间。
func (d *Daemon) collabStart(ctx context.Context) {
	c := &d.collab
	c.mu.Lock()
	c.ctx, c.started = ctx, true
	if c.kick == nil {
		c.kick = make(chan struct{}, 1)
	}
	c.mu.Unlock()
	d.collabEnsure()
	go d.collabPushLoop(ctx)
}

// collabEnsure 让服务和连接循环和库里一致：有共享的讨论就监听，没有就停；每个加入中的房间有一个连接循环。
func (d *Daemon) collabEnsure() {
	c := &d.collab
	c.mu.Lock()
	defer c.mu.Unlock()
	if !c.started || c.paused || d.store == nil {
		return
	}
	shared, _ := d.store.SharedRooms()
	switch {
	case len(shared) > 0 && c.srv == nil:
		id, err := d.identity()
		if err != nil {
			c.listenErr = err.Error()
			break
		}
		srv := collaboration.NewServer(id, collabHost{d})
		srv.Now = c.now
		addr := c.listenAddr
		if addr == "" {
			addr = ":" + strconv.Itoa(d.collabPort())
		}
		if err := srv.Listen(addr); err != nil {
			c.listenErr = err.Error()
			log.Printf("collaboration: %v", err)
			break
		}
		c.srv, c.listenErr = srv, ""
		log.Printf("collaboration listening on %s", srv.Addr())
	case len(shared) == 0 && c.srv != nil:
		c.srv.Close()
		c.srv = nil
	}
	if c.rooms == nil {
		c.rooms = map[string]*roomConn{}
	}
	joined, _ := d.store.JoinedRooms()
	for _, j := range joined {
		if j.State != store.RequestPending && j.State != store.JoinedActive && j.State != joinedLeaving {
			continue
		}
		if rc := c.rooms[j.Room]; rc != nil && !rc.stopped() {
			continue
		}
		rc := newRoomConn(d, j.Room)
		c.rooms[j.Room] = rc
		go rc.run(c.ctx)
	}
}

// collabPause 在交接开始时停下服务和连接（新进程拿到锁之后接上）；resume 为真时是交接失败了，恢复。
func (d *Daemon) collabPause(resume bool) {
	c := &d.collab
	c.mu.Lock()
	c.paused = !resume
	var rooms []*roomConn
	if !resume {
		if c.srv != nil {
			c.srv.Close()
			c.srv = nil
		}
		for _, rc := range c.rooms {
			rooms = append(rooms, rc)
		}
		c.rooms = nil
	}
	c.mu.Unlock()
	for _, rc := range rooms {
		rc.stop()
	}
	if resume {
		d.collabEnsure()
	}
}

// collabStop 在常驻进程退出时调用。
func (d *Daemon) collabStop() { d.collabPause(false) }

func (d *Daemon) collabServer() *collaboration.Server {
	d.collab.mu.Lock()
	defer d.collab.mu.Unlock()
	return d.collab.srv
}

// collabNotify 在讨论、任务有变化时调用：推给房间里的成员，叫醒加入的房间去补发事件。
func (d *Daemon) collabNotify() {
	c := &d.collab
	c.mu.Lock()
	k := c.kick
	rooms := make([]*roomConn, 0, len(c.rooms))
	for _, rc := range c.rooms {
		rooms = append(rooms, rc)
	}
	c.mu.Unlock()
	if k != nil {
		select {
		case k <- struct{}{}:
		default:
		}
	}
	for _, rc := range rooms {
		rc.poke()
	}
}

// collabPushLoop 看共享讨论的版本有没有变，变了推给连着的成员（他们再来取增量）。
func (d *Daemon) collabPushLoop(ctx context.Context) {
	t := time.NewTicker(2 * time.Second)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		case <-d.collab.kick:
		}
		srv := d.collabServer()
		if srv == nil {
			continue
		}
		rooms, err := d.store.SharedRooms()
		if err != nil {
			continue
		}
		d.collab.mu.Lock()
		if d.collab.pushed == nil {
			d.collab.pushed = map[string]int64{}
		}
		var push []store.Discussion
		for _, r := range rooms {
			if d.collab.pushed[r.Key] != r.Rev {
				d.collab.pushed[r.Key] = r.Rev
				push = append(push, r)
			}
		}
		d.collab.mu.Unlock()
		for _, r := range push {
			srv.Push(r.Key, r.Rev)
		}
	}
}

// --- 协作服务的方法（房主）

type collabHost struct{ d *Daemon }

// Closed：连接断了，它开的还没传完的上传作废（临时文件、记录、配额预留一起放掉）。
func (h collabHost) Closed(s *collaboration.Session) {
	d := h.d
	v, ok := d.collab.sessUploads.LoadAndDelete(s)
	if !ok {
		return
	}
	for _, id := range *v.(*[]string) {
		d.abortUpload(id)
	}
}

// Known：这个身份是不是某个共享房间里的有效成员（只决定连接名额，进房间仍要凭据）。
func (h collabHost) Known(fp string) bool {
	return h.d.store.KnownMember(fp)
}

// abortUpload 删掉没传完的上传的临时文件和记录。
func (d *Daemon) abortUpload(id string) {
	u, err := d.store.Upload(id)
	if err != nil || u.State != "uploading" {
		return
	}
	if disc, err := d.store.Discussion(u.Room); err == nil {
		if root, err := d.roomRoot(disc); err == nil {
			os.Remove(filepath.Join(root, store.AttachmentDir(disc.LocalID), ".upload-"+id))
		}
	}
	d.store.AbortUpload(id)
	d.collab.uploads.Delete(id)
}

// cleanupUploads 清掉半小时还没传完的上传（连接异常断开、房主重启时 Closed 没机会跑）。
func (d *Daemon) cleanupUploads() {
	us, err := d.store.StaleUploads(time.Now().Add(-30 * time.Minute))
	if err != nil {
		return
	}
	for _, u := range us {
		d.abortUpload(u.ID)
	}
}

// cerr 把 store 的错误转成线上的错误代号；撤销、归属不符、冲突是终态，对方不会重试。
func cerr(err error) error {
	var ce *collaboration.Error
	var re *rpc.Error
	switch {
	case err == nil:
		return nil
	case errors.As(err, &ce):
		return err
	case errors.As(err, &re):
		code := collaboration.CodeConflict
		switch re.Code {
		case rpc.CodeBadRequest:
			code = collaboration.CodeBadRequest
		case rpc.CodeNotFound:
			code = collaboration.CodeNotFound
		}
		return collaboration.Errorf(code, "%s", re.Message)
	case errors.Is(err, store.ErrRevoked):
		return collaboration.Errorf(collaboration.CodeRevoked, "your access to this discussion was revoked")
	case errors.Is(err, store.ErrNotOwner):
		return collaboration.Errorf(collaboration.CodeUnauthorized, "%v", err)
	case errors.Is(err, store.ErrNotFound):
		return collaboration.Errorf(collaboration.CodeNotFound, "%v", err)
	case errors.Is(err, store.ErrArchived):
		return collaboration.Errorf(collaboration.CodeConflict, "discussion is archived")
	case errors.Is(err, store.ErrQuota):
		return collaboration.Errorf(collaboration.CodeQuota, "the host's shared attachment quota is full; your draft is kept")
	case errors.Is(err, store.ErrUploadBusy):
		return collaboration.Errorf(collaboration.CodeRateLimited, "too many uploads in progress; try again when they finish")
	case errors.Is(err, store.ErrInviteExpired):
		return collaboration.Errorf(collaboration.CodeConflict, "the invitation has expired; ask the host for a new one")
	case errors.Is(err, store.ErrInviteUsed):
		return collaboration.Errorf(collaboration.CodeConflict, "the invitation was already used; ask the host for a new one")
	case errors.Is(err, store.ErrInviteInvalid):
		return collaboration.Errorf(collaboration.CodeConflict, "the invitation is not valid for this host")
	case errors.Is(err, store.ErrBusyMember):
		return collaboration.Errorf(collaboration.CodeConflict, "the agent has active or queued requests; finish, stop or cancel them first")
	case errors.Is(err, store.ErrMemberGone):
		return collaboration.Errorf(collaboration.CodeConflict, "the agent was removed from the discussion")
	case errors.Is(err, store.ErrRetried):
		return collaboration.Errorf(collaboration.CodeConflict, "this request was already retried")
	case errors.Is(err, store.ErrAddGone), errors.Is(err, store.ErrAddChanged):
		return collaboration.Errorf(collaboration.CodeConflict, "%v", err)
	case errors.Is(err, store.ErrConflict), errors.Is(err, store.ErrStaleRound):
		return collaboration.Errorf(collaboration.CodeConflict, "%v", err)
	}
	return err
}

type collabJoinParams struct {
	Room    string `json:"room"`
	Secret  string `json:"secret"`
	Request string `json:"request"`
	Name    string `json:"name"`
	Version int    `json:"version"`
}

type collabJoinStatus struct {
	State      string `json:"state"`
	Request    string `json:"request"`
	Generation int64  `json:"generation,omitempty"`
	Credential string `json:"credential,omitempty"`
	HostName   string `json:"hostName,omitempty"`
	Title      string `json:"title,omitempty"`
}

type collabHello struct {
	Room       string `json:"room"`
	Credential string `json:"credential"`
	Version    int    `json:"version"`
}

func (h collabHost) Serve(ctx context.Context, s *collaboration.Session, method string, raw json.RawMessage) (any, error) {
	d := h.d
	switch method {
	case "join":
		var p collabJoinParams
		if err := json.Unmarshal(raw, &p); err != nil {
			return nil, collaboration.Errorf(collaboration.CodeBadRequest, "bad request")
		}
		if p.Version != collaboration.ProtocolVersion {
			return nil, collaboration.Errorf(collaboration.CodeUpgrade, "the host runs a different collaboration protocol; update Posse on both computers")
		}
		if srv := d.collabServer(); srv != nil && !srv.AllowJoin(s.Addr) {
			return nil, collaboration.Errorf(collaboration.CodeRateLimited, "too many join attempts; wait a minute")
		}
		if len(p.Request) < 16 || len(p.Request) > 64 || p.Secret == "" {
			return nil, collaboration.Errorf(collaboration.CodeBadRequest, "bad request")
		}
		name := strings.TrimSpace(p.Name)
		if name == "" || utf8.RuneCountInString(name) > 60 {
			name = "guest-" + s.FP[:8]
		}
		r, err := d.store.SubmitJoin(p.Room, collaboration.HashSecret(p.Secret), p.Request, s.FP, name, s.Addr, time.Now())
		if err != nil {
			return nil, cerr(err)
		}
		d.taskNotify()
		return h.joinStatus(r)
	case "join.status":
		var p collabJoinParams
		json.Unmarshal(raw, &p)
		r, err := d.store.JoinRequest(p.Request)
		if err != nil || r.FP != s.FP || r.Room != p.Room {
			return nil, collaboration.Errorf(collaboration.CodeNotFound, "join request not found")
		}
		return h.joinStatus(r)
	case "hello":
		var p collabHello
		json.Unmarshal(raw, &p)
		if p.Version != collaboration.ProtocolVersion {
			return nil, collaboration.Errorf(collaboration.CodeUpgrade, "the host runs a different collaboration protocol; update Posse on both computers")
		}
		disc, err := d.store.Discussion(p.Room)
		if err != nil || disc.Mirror {
			return nil, collaboration.Errorf(collaboration.CodeUnauthorized, "not a member of this discussion")
		}
		person, err := d.store.Person(p.Room, s.FP)
		if err != nil {
			return nil, collaboration.Errorf(collaboration.CodeUnauthorized, "not a member of this discussion")
		}
		if person.State != store.PersonActive {
			return nil, collaboration.Errorf(collaboration.CodeRevoked, "your access to this discussion was revoked")
		}
		id, err := d.identity()
		if err != nil {
			return nil, err
		}
		if !collaboration.CheckCredential(id, p.Room, s.FP, person.Generation, p.Credential) {
			// 旧代的凭据（重新加入之前那份）也在这里被拒
			return nil, collaboration.Errorf(collaboration.CodeRevoked, "this credential is no longer valid; ask the host for a new invitation")
		}
		if !disc.Shared {
			return nil, collaboration.Errorf(collaboration.CodeOffline, "the host turned off sharing for this discussion")
		}
		s.Bind(p.Room, person.Generation)
		s.Touch()
		return map[string]any{"generation": person.Generation, "rev": disc.Rev, "hostName": id.Name}, nil
	}
	room, gen, err := h.auth(s)
	if err != nil {
		return nil, err
	}
	switch method {
	case "heartbeat":
		s.Touch()
		// 顺带告诉对方谁在线（房主的时钟，只用于显示）
		var online []string
		if srv := d.collabServer(); srv != nil {
			for fp := range srv.Online(room, collaboration.HeartbeatLost) {
				online = append(online, fp)
			}
		}
		return map[string]any{"online": online}, nil
	case "sync":
		var p struct {
			Since int64 `json:"since"`
			Limit int   `json:"limit"`
		}
		json.Unmarshal(raw, &p)
		if p.Limit <= 0 || p.Limit > 500 {
			p.Limit = 200
		}
		b, err := d.store.SyncSince(room, p.Since, p.Limit)
		return b, cerr(err)
	case "post":
		return h.post(s, room, gen, raw)
	case "key":
		var p struct {
			Message string `json:"message"`
			On      bool   `json:"on"`
		}
		json.Unmarshal(raw, &p)
		m, err := d.store.DiscMessage(p.Message)
		if err != nil || m.Discussion != room {
			return nil, collaboration.Errorf(collaboration.CodeNotFound, "message not found")
		}
		if err := d.store.SetKeyMessageBy(p.Message, p.On, s.FP, gen, time.Now()); err != nil {
			return nil, cerr(err)
		}
		d.taskNotify()
		return map[string]any{}, nil
	case "member.add", "member.update":
		var p struct {
			Member string `json:"member"`
			Name   string `json:"name"`
			Role   string `json:"role"`
			Agent  string `json:"agent"`
			Model  string `json:"model"`
			Effort string `json:"effort"`
			// Client 是这次添加的请求 ID：应答丢了重发时不再加一个（AddMemberOnce）
			Client string `json:"client"`
		}
		json.Unmarshal(raw, &p)
		p.Name, p.Role = strings.TrimSpace(p.Name), strings.TrimSpace(p.Role)
		if p.Name == "" || len(p.Name) > 200 || len(p.Role) > 4000 || len(p.Model) > 200 || len(p.Effort) > 50 {
			return nil, collaboration.Errorf(collaboration.CodeBadRequest, "name required; name, role, model or effort too long")
		}
		if !slices.Contains(agent.Known, p.Agent) {
			return nil, collaboration.Errorf(collaboration.CodeBadRequest, "unknown agent %s", p.Agent)
		}
		m := store.Member{ID: p.Member, Discussion: room, Name: p.Name, Role: p.Role, Agent: p.Agent, Model: p.Model, Effort: p.Effort,
			Owner: s.FP, Generation: gen, CreatedAt: time.Now()}
		if method == "member.add" {
			m.ID = store.NewID("dmb_") + store.NewID("")
			id, dup, err := d.store.AddMemberOnce(m, p.Client)
			if err != nil {
				return nil, cerr(err)
			}
			if dup {
				return map[string]any{"member": id, "duplicate": true}, nil
			}
		} else if _, err := d.store.OwnerMemberUpdate(room, s.FP, gen, m); err != nil {
			return nil, cerr(err)
		}
		d.taskNotify()
		return map[string]string{"member": m.ID}, nil
	case "member.remove":
		var p struct {
			Member string `json:"member"`
		}
		json.Unmarshal(raw, &p)
		if err := d.store.OwnerMemberRemove(room, s.FP, gen, p.Member, time.Now()); err != nil {
			return nil, cerr(err)
		}
		d.taskNotify()
		return map[string]any{}, nil
	case "claim", "cancel", "retry":
		var p struct {
			Execution string `json:"execution"`
			Abandon   bool   `json:"abandon"`
		}
		json.Unmarshal(raw, &p)
		var x store.Execution
		var err error
		switch method {
		case "claim":
			x, err = d.store.HostClaim(room, s.FP, gen, p.Execution)
		case "cancel":
			x, err = d.store.OwnerCancel(room, s.FP, gen, p.Execution)
		default:
			x, err = d.store.OwnerRetry(room, s.FP, gen, p.Execution, store.NewID("x_")+store.NewID(""), p.Abandon, time.Now())
		}
		if err != nil {
			return nil, cerr(err)
		}
		d.taskNotify()
		return map[string]any{"execution": x.ID, "state": x.State}, nil
	case "event":
		var p struct {
			Execution string           `json:"execution"`
			Seq       int64            `json:"seq"`
			Digest    string           `json:"digest"`
			Event     store.OwnerEvent `json:"event"`
		}
		if err := json.Unmarshal(raw, &p); err != nil || p.Seq <= 0 {
			return nil, collaboration.Errorf(collaboration.CodeBadRequest, "bad event")
		}
		if len(p.Event.Body) > collaboration.MaxBody {
			return nil, collaboration.Errorf(collaboration.CodeBadRequest, "event body is longer than %d KiB", collaboration.MaxBody>>10)
		}
		err := d.store.ApplyOwnerEvent(store.HostEvent{Room: room, FP: s.FP, Gen: gen, Exec: p.Execution, Seq: p.Seq, Digest: p.Digest, Event: p.Event})
		if err != nil {
			return nil, cerr(err)
		}
		d.taskNotify()
		return map[string]any{}, nil
	case "upload.begin", "upload.chunk", "upload.end":
		return h.upload(s, room, method, raw)
	case "download":
		return h.download(room, raw)
	case "leave":
		if _, err := d.store.RevokePerson(room, s.FP, store.PersonLeft, time.Now()); err != nil {
			return nil, cerr(err)
		}
		d.taskNotify()
		go func() {
			time.Sleep(200 * time.Millisecond)
			if srv := d.collabServer(); srv != nil {
				srv.Kick(room, s.FP, "left the discussion")
			}
		}()
		return map[string]any{}, nil
	}
	return nil, collaboration.Errorf(collaboration.CodeBadRequest, "unknown method %s", method)
}

// auth 是除加入申请之外每个请求都做的复核：连接绑定过房间，成员资格仍有效、代际没变、房间还开着共享。
func (h collabHost) auth(s *collaboration.Session) (string, int64, error) {
	room, gen := s.Room()
	if room == "" {
		return "", 0, collaboration.Errorf(collaboration.CodeUnauthorized, "say hello with a room credential first")
	}
	p, err := h.d.store.Person(room, s.FP)
	if err != nil || p.State != store.PersonActive || p.Generation != gen {
		go s.Close("access revoked")
		return "", 0, collaboration.Errorf(collaboration.CodeRevoked, "your access to this discussion was revoked")
	}
	disc, err := h.d.store.Discussion(room)
	if err != nil || !disc.Shared {
		go s.Close("sharing turned off")
		return "", 0, collaboration.Errorf(collaboration.CodeOffline, "the host turned off sharing for this discussion")
	}
	return room, gen, nil
}

func (h collabHost) joinStatus(r store.JoinRequest) (collabJoinStatus, error) {
	st := collabJoinStatus{State: r.State, Request: r.ID}
	disc, err := h.d.store.Discussion(r.Room)
	if err == nil {
		st.Title = disc.Title
	}
	id, err := h.d.identity()
	if err != nil {
		return st, err
	}
	st.HostName = id.Name
	if r.State == store.RequestApproved {
		// 凭据由房主私钥派生：同一申请再来取拿到同一份，不再消费邀请；成员资格之后变了代，这份就作废
		if p, err := h.d.store.Person(r.Room, r.FP); err == nil && p.State == store.PersonActive && p.Generation == r.Generation {
			st.Generation = r.Generation
			st.Credential = collaboration.Credential(id, r.Room, r.FP, r.Generation)
		} else {
			st.State = store.PersonRemoved
		}
	}
	return st, nil
}

type collabPostParams struct {
	ClientID string   `json:"clientId"`
	Body     string   `json:"body"`
	Quote    string   `json:"quote"`
	Target   string   `json:"target"`
	Mention  string   `json:"mention"`
	Uploads  []string `json:"uploads"`
}

func (h collabHost) post(s *collaboration.Session, room string, gen int64, raw json.RawMessage) (any, error) {
	d := h.d
	var p collabPostParams
	if err := json.Unmarshal(raw, &p); err != nil {
		return nil, collaboration.Errorf(collaboration.CodeBadRequest, "bad request")
	}
	p.Body = strings.TrimSpace(p.Body)
	switch {
	case p.Body == "" && len(p.Uploads) == 0:
		return nil, collaboration.Errorf(collaboration.CodeBadRequest, "message text required")
	case len(p.Body) > collaboration.MaxBody:
		return nil, collaboration.Errorf(collaboration.CodeBadRequest, "message is longer than %d KiB; attach it as a file", collaboration.MaxBody>>10)
	case !utf8.ValidString(p.Body):
		return nil, collaboration.Errorf(collaboration.CodeBadRequest, "message is not valid UTF-8")
	case len(p.Uploads) > discMaxAttachments:
		return nil, collaboration.Errorf(collaboration.CodeBadRequest, "at most %d attachments per message", discMaxAttachments)
	case p.ClientID == "" || len(p.ClientID) > 100:
		return nil, collaboration.Errorf(collaboration.CodeBadRequest, "client message id required")
	}
	if !d.collabAllowPost(s.FP) {
		return nil, collaboration.Errorf(collaboration.CodeRateLimited, "you are sending too fast; your draft is kept, try again in a few seconds")
	}
	person, err := d.store.Person(room, s.FP)
	if err != nil {
		return nil, cerr(store.ErrRevoked)
	}
	m, x, dup, err := d.store.PostMessage(store.PostInput{Discussion: room, ClientID: p.ClientID, Body: p.Body, Quote: p.Quote, Target: p.Target,
		Mention: p.Mention, Uploads: p.Uploads, At: time.Now(), Author: s.FP, AuthorName: person.Name, Generation: gen})
	if err != nil {
		if errors.Is(err, store.ErrNotOwner) {
			return nil, collaboration.Errorf(collaboration.CodeUnauthorized, "you can only send requests to your own agents; @ the owner to ask them")
		}
		return nil, cerr(err)
	}
	if dup && len(p.Uploads) > 0 {
		// 同一条消息重发：这次又传的附件没用上，放掉（文件和配额）
		disc, _ := d.store.Discussion(room)
		root, _ := d.roomRoot(disc)
		for _, u := range d.store.UnusedUploads(room, s.FP, p.Uploads) {
			if root != "" {
				os.RemoveAll(filepath.Join(root, filepath.Dir(u.Path)))
			}
			d.store.DropUpload(u.ID)
		}
	}
	d.taskNotify()
	res := map[string]any{"message": m.ID, "seq": m.Seq, "duplicate": dup}
	if x != nil {
		res["execution"] = x.ID
	}
	return res, nil
}

func (d *Daemon) collabAllowPost(fp string) bool {
	c := &d.collab
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.posts == nil {
		c.posts = map[string][]time.Time{}
	}
	now := time.Now()
	var keep []time.Time
	for _, t := range c.posts[fp] {
		if now.Sub(t) < 10*time.Second {
			keep = append(keep, t)
		}
	}
	if len(keep) >= collabPostBurst {
		c.posts[fp] = keep
		return false
	}
	c.posts[fp] = append(keep, now)
	return true
}

// roomRoot 是房主讨论的项目目录（附件放在它的 .posse/discussions/<讨论>/attachments 下）。
func (d *Daemon) roomRoot(disc store.Discussion) (string, error) {
	p, err := d.store.FindProject(disc.ProjectID)
	if err != nil {
		return "", collaboration.Errorf(collaboration.CodeConflict, "the discussion's project was removed on the host; attachments are unavailable")
	}
	if p.Kind == "git" {
		if _, done := notesExcluded.LoadOrStore(p.ID, true); !done {
			if err := excludeNotes(p.Path); err != nil {
				notesExcluded.Delete(p.ID)
			}
		}
	}
	return p.Path, nil
}

// upload 是成员分块上传附件：边收边写临时文件，长度和 SHA-256 对上才原子地改名发布。
// 文件名、目录由房主生成，不拼对方给的路径；配额和同时在传的数量在登记时查。
func (h collabHost) upload(s *collaboration.Session, room, method string, raw json.RawMessage) (any, error) {
	d := h.d
	var p struct {
		Upload string `json:"upload"`
		Name   string `json:"name"`
		Size   int64  `json:"size"`
		SHA    string `json:"sha"`
		Offset int64  `json:"offset"`
		Data   []byte `json:"data"`
	}
	if err := json.Unmarshal(raw, &p); err != nil {
		return nil, collaboration.Errorf(collaboration.CodeBadRequest, "bad request")
	}
	disc, err := d.store.Discussion(room)
	if err != nil {
		return nil, cerr(err)
	}
	root, err := d.roomRoot(disc)
	if err != nil {
		return nil, err
	}
	dir := filepath.Join(root, store.AttachmentDir(disc.LocalID))
	switch method {
	case "upload.begin":
		name := safeFileName(p.Name)
		if name == "" {
			name = "attachment"
		}
		if p.Size < 0 || p.Size > discMaxAttachSize {
			return nil, collaboration.Errorf(collaboration.CodeBadRequest, "attachment %s is larger than %d MiB", name, discMaxAttachSize>>20)
		}
		if b, err := hex.DecodeString(p.SHA); err != nil || len(b) != sha256.Size {
			return nil, collaboration.Errorf(collaboration.CodeBadRequest, "bad attachment digest")
		}
		d.cleanupUploads()
		id := store.NewID("a_") + store.NewID("")
		rel := filepath.Join(store.AttachmentDir(disc.LocalID), id, name)
		u := store.Upload{ID: id, Room: room, FP: s.FP, Name: name, Size: p.Size, SHA: p.SHA, Path: rel}
		if err := d.store.BeginUpload(u, collabUploadConcurrency, time.Now()); err != nil {
			return nil, cerr(err)
		}
		// 同样内容已经发布过：硬链接复用内容，附件记录照样独立。先按旧文件实际的长度和摘要核对，不信对方声称的
		if old, err := d.store.PublishedBySHA(room, p.SHA); err == nil {
			oldPath := filepath.Join(root, old.Path)
			if sum, n, err := fileSHA(oldPath); err == nil && sum == p.SHA && n == p.Size &&
				os.MkdirAll(filepath.Join(dir, id), 0o755) == nil && os.Link(oldPath, filepath.Join(root, rel)) == nil {
				d.store.FinishUpload(id)
				return map[string]any{"upload": id, "done": true}, nil
			}
		}
		if err := os.MkdirAll(dir, 0o755); err != nil {
			d.store.AbortUpload(id)
			return nil, err
		}
		f, err := os.OpenFile(filepath.Join(dir, ".upload-"+id), os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o644)
		if err != nil {
			d.store.AbortUpload(id)
			return nil, err
		}
		f.Close()
		v, _ := d.collab.sessUploads.LoadOrStore(s, &[]string{})
		lst := v.(*[]string)
		d.collab.mu.Lock()
		*lst = append(*lst, id)
		d.collab.mu.Unlock()
		return map[string]any{"upload": id, "done": false}, nil
	}
	u, err := d.store.Upload(p.Upload)
	if err != nil || u.Room != room || u.FP != s.FP {
		return nil, collaboration.Errorf(collaboration.CodeNotFound, "upload not found")
	}
	if u.State != "uploading" {
		return nil, collaboration.Errorf(collaboration.CodeConflict, "upload already finished")
	}
	lk, _ := d.collab.uploads.LoadOrStore(u.ID, &sync.Mutex{})
	lk.(*sync.Mutex).Lock()
	defer lk.(*sync.Mutex).Unlock()
	tmp := filepath.Join(dir, ".upload-"+u.ID)
	st, err := os.Stat(tmp)
	if err != nil {
		return nil, collaboration.Errorf(collaboration.CodeNotFound, "upload not found")
	}
	if method == "upload.chunk" {
		if len(p.Data) > collaboration.ChunkSize || p.Offset != st.Size() || st.Size()+int64(len(p.Data)) > u.Size {
			return nil, collaboration.Errorf(collaboration.CodeBadRequest, "chunk out of order or too large (have %d bytes)", st.Size())
		}
		f, err := os.OpenFile(tmp, os.O_WRONLY|os.O_APPEND, 0)
		if err != nil {
			return nil, err
		}
		_, err = f.Write(p.Data)
		if cerr := f.Close(); err == nil {
			err = cerr
		}
		if err != nil {
			return nil, err
		}
		return map[string]any{"received": st.Size() + int64(len(p.Data))}, nil
	}
	// upload.end
	sum, n, err := fileSHA(tmp)
	if err != nil || n != u.Size || sum != u.SHA {
		os.Remove(tmp)
		d.store.AbortUpload(u.ID)
		d.collab.uploads.Delete(u.ID)
		return nil, collaboration.Errorf(collaboration.CodeConflict, "attachment %s arrived incomplete or changed; send it again", u.Name)
	}
	dst := filepath.Join(root, u.Path)
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		return nil, err
	}
	if err := os.Rename(tmp, dst); err != nil {
		return nil, err
	}
	d.store.FinishUpload(u.ID)
	d.collab.uploads.Delete(u.ID)
	return map[string]any{"upload": u.ID, "done": true}, nil
}

func fileSHA(path string) (string, int64, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", 0, err
	}
	defer f.Close()
	h := sha256.New()
	n, err := io.Copy(h, f)
	return hex.EncodeToString(h.Sum(nil)), n, err
}

// download 按附件 ID 给一块内容（不接受路径）；文件在房主那边不在了报 not_found。
func (h collabHost) download(room string, raw json.RawMessage) (any, error) {
	d := h.d
	var p struct {
		Attachment string `json:"attachment"`
		Offset     int64  `json:"offset"`
	}
	json.Unmarshal(raw, &p)
	a, err := d.store.RoomAttachment(room, p.Attachment)
	if err != nil {
		return nil, collaboration.Errorf(collaboration.CodeNotFound, "attachment not found")
	}
	disc, err := d.store.Discussion(room)
	if err != nil {
		return nil, cerr(err)
	}
	root, err := d.roomRoot(disc)
	if err != nil {
		return nil, err
	}
	f, err := os.Open(filepath.Join(root, a.Path))
	if err != nil {
		return nil, collaboration.Errorf(collaboration.CodeNotFound, "attachment %s is missing on the host", a.Name)
	}
	defer f.Close()
	st, err := f.Stat()
	if err != nil || !st.Mode().IsRegular() {
		return nil, collaboration.Errorf(collaboration.CodeNotFound, "attachment %s is missing on the host", a.Name)
	}
	buf := make([]byte, collaboration.ChunkSize)
	n, err := f.ReadAt(buf, p.Offset)
	if err != nil && err != io.EOF {
		return nil, err
	}
	sha := a.SHA
	if sha == "" && p.Offset == 0 {
		sha, _, _ = fileSHA(filepath.Join(root, a.Path))
	}
	return map[string]any{"data": buf[:n], "size": st.Size(), "eof": p.Offset+int64(n) >= st.Size(), "sha": sha}, nil
}

// --- 房主的本机 RPC

func (d *Daemon) collabShare(ctx context.Context, raw json.RawMessage) (any, error) {
	var p struct {
		Discussion string `json:"discussion"`
		On         bool   `json:"on"`
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
	if disc.Mirror {
		return nil, rpc.Errorf(rpc.CodeConflict, "only the host can share this discussion")
	}
	id, err := d.identity()
	if err != nil {
		return nil, err
	}
	if err := d.store.SetShared(disc.Key, p.On, id.Name, time.Now()); err != nil {
		return nil, discError(err)
	}
	d.collabEnsure()
	if p.On {
		d.collab.mu.Lock()
		lerr, srv := d.collab.listenErr, d.collab.srv
		d.collab.mu.Unlock()
		if srv == nil {
			d.store.SetShared(disc.Key, false, id.Name, time.Now())
			d.collabEnsure()
			if lerr == "" {
				lerr = "collaboration service is not available while the daemon is starting or handing off"
			}
			return nil, rpc.Errorf(rpc.CodeConflict, "%s", lerr)
		}
	} else if srv := d.collabServer(); srv != nil {
		srv.Kick(disc.Key, "", "the host turned off sharing")
	}
	d.taskNotify()
	return d.store.Discussion(disc.Key)
}

func (d *Daemon) collabInvite(ctx context.Context, raw json.RawMessage) (any, error) {
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
	srv := d.collabServer()
	if !disc.Shared || srv == nil {
		return nil, rpc.Errorf(rpc.CodeConflict, "turn on LAN sharing first")
	}
	id, err := d.identity()
	if err != nil {
		return nil, err
	}
	port := d.collabPort()
	if a, ok := srv.Addr().(interface{ String() string }); ok {
		if _, ps, err := splitHostPort(a.String()); err == nil {
			port = ps
		}
	}
	addrs := collaboration.LocalAddrs(port)
	if len(addrs) == 0 {
		return nil, rpc.Errorf(rpc.CodeConflict, "this Mac has no LAN address; connect to a network first")
	}
	secret := collaboration.NewSecret()
	now := time.Now()
	exp := now.Add(collaboration.InviteTTL)
	if err := d.store.CreateInvite(disc.Key, store.NewID("inv_"), collaboration.HashSecret(secret), exp, now); err != nil {
		return nil, discError(err)
	}
	inv := collaboration.Invite{V: collaboration.ProtocolVersion, Host: id.ID, HostName: id.Name, Room: disc.Key, Title: disc.Title, Addrs: addrs, Secret: secret, Expires: exp.UnixMilli()}
	return map[string]any{"invite": inv.Encode(), "expires": exp, "addrs": addrs, "fingerprint": id.ID}, nil
}

func splitHostPort(addr string) (string, int, error) {
	i := strings.LastIndex(addr, ":")
	if i < 0 {
		return "", 0, fmt.Errorf("no port in %s", addr)
	}
	port, err := strconv.Atoi(addr[i+1:])
	return addr[:i], port, err
}

func (d *Daemon) collabDecide(ctx context.Context, raw json.RawMessage) (any, error) {
	var p struct {
		Request string `json:"request"`
		Approve bool   `json:"approve"`
	}
	if err := rpc.Decode(raw, &p); err != nil {
		return nil, err
	}
	if err := d.userOnly(raw); err != nil {
		return nil, err
	}
	var r store.JoinRequest
	var err error
	if p.Approve {
		r, err = d.store.ApproveRequest(p.Request, time.Now())
	} else {
		r, err = d.store.RejectRequest(p.Request, time.Now())
	}
	if err != nil {
		return nil, discError(err)
	}
	d.taskNotify()
	return r, nil
}

func (d *Daemon) collabRemovePerson(ctx context.Context, raw json.RawMessage) (any, error) {
	var p struct {
		Discussion string `json:"discussion"`
		Person     string `json:"person"`
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
	if disc.Mirror {
		return nil, rpc.Errorf(rpc.CodeConflict, "only the host can remove people")
	}
	fp, err := d.personSel(disc.Key, p.Person)
	if err != nil {
		return nil, err
	}
	person, err := d.store.RevokePerson(disc.Key, fp, store.PersonRemoved, time.Now())
	if err != nil {
		return nil, discError(err)
	}
	if srv := d.collabServer(); srv != nil {
		srv.Kick(disc.Key, fp, "removed by the host")
	}
	d.taskNotify()
	return person, nil
}

// personSel 按指纹（或它的开头）找房间里的人。
func (d *Daemon) personSel(room, sel string) (string, error) {
	if sel == "" {
		return "", rpc.Errorf(rpc.CodeBadRequest, "person required")
	}
	ps, err := d.store.People(room)
	if err != nil {
		return "", err
	}
	var hit []string
	for _, p := range ps {
		if p.FP == sel || len(sel) >= 6 && strings.HasPrefix(p.FP, sel) || p.Name == sel {
			hit = append(hit, p.FP)
		}
	}
	switch len(hit) {
	case 1:
		return hit[0], nil
	case 0:
		return "", rpc.Errorf(rpc.CodeNotFound, "person %s not found", sel)
	}
	return "", rpc.Errorf(rpc.CodeBadRequest, "%s matches several people; use the fingerprint", sel)
}

func (d *Daemon) collabQuota(ctx context.Context, raw json.RawMessage) (any, error) {
	var p struct {
		Bytes int64 `json:"bytes"`
	}
	if err := rpc.Decode(raw, &p); err != nil {
		return nil, err
	}
	if p.Bytes > 0 {
		if err := d.store.SetCollabQuota(p.Bytes); err != nil {
			return nil, err
		}
	}
	return map[string]int64{"quota": d.store.CollabQuota(), "used": d.store.CollabUsage()}, nil
}

func (d *Daemon) collabRename(ctx context.Context, raw json.RawMessage) (any, error) {
	var p struct {
		Name string `json:"name"`
	}
	if err := rpc.Decode(raw, &p); err != nil {
		return nil, err
	}
	if err := d.userOnly(raw); err != nil {
		return nil, err
	}
	name := strings.TrimSpace(p.Name)
	if name == "" || utf8.RuneCountInString(name) > 60 {
		return nil, rpc.Errorf(rpc.CodeBadRequest, "name must be 1-60 characters")
	}
	if _, err := d.identity(); err != nil {
		return nil, err
	}
	id, err := collaboration.SetName(d.cfg.Home, name)
	if err != nil {
		return nil, rpc.Errorf(rpc.CodeInternal, "rename: %v", err)
	}
	if err := d.store.SetHostName(id.ID, name); err != nil {
		return nil, err
	}
	d.taskNotify()
	return map[string]string{"id": id.ID, "name": id.Name}, nil
}

// collabStatusInfo 是协作的整体状态（设置、CLI）。
func (d *Daemon) collabStatus(ctx context.Context, raw json.RawMessage) (any, error) {
	c := &d.collab
	c.mu.Lock()
	res := map[string]any{"port": d.collabPort(), "listening": c.srv != nil, "error": c.listenErr, "started": c.started, "paused": c.paused}
	if c.srv != nil {
		res["addr"] = c.srv.Addr().String()
	}
	c.mu.Unlock()
	if id, err := collaboration.Load(d.cfg.Home); err == nil {
		res["id"], res["name"] = id.ID, id.Name
	}
	res["quota"], res["used"] = d.store.CollabQuota(), d.store.CollabUsage()
	joined, _ := d.store.JoinedRooms()
	var rooms []map[string]any
	for _, j := range joined {
		st := d.roomStatus(j.Room)
		rooms = append(rooms, map[string]any{"room": j.Room, "state": j.State, "host": j.HostName, "connected": st.Connected, "error": cmpOr(st.Error, j.Error)})
	}
	res["joined"] = rooms
	return res, nil
}

func tlsSessionCache() tls.ClientSessionCache { return tls.NewLRUClientSessionCache(32) }
