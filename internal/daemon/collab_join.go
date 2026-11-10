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

// joinedLeaving 是离线时点了退出：本机已停用（不领取、不开始新轮次、不自动恢复），连上房主后交完已有结果再撤销
const joinedLeaving = "leaving"

// collabLeavingReason 是退出中自己 Agent 的暂停原因
const collabLeavingReason = "leaving the discussion; results already on this computer are sent when the host is reachable"

// collabUnreachable 是连不上房主时的提示：系统不一定能区分没给本地网络权限和网络不通，合在一起说。
const collabUnreachable = "Could not reach the host. Check that both Macs are on the same network, Posse is running on the host with sharing turned on, and Local Network access is allowed for Posse on both Macs (System Settings › Privacy & Security › Local Network)."

var errStopRoom = errors.New("stop")

// 加入者等房主应答的上限。房主不应答但 TCP 没断（合盖、卡住）时，请求会一直等到系统判定连接中断，
// 期间占着连接锁，发言、退出都要等；超时就当断线，关掉这条连接走重连。测试里调小。
var (
	// collabCallTimeout：后台的一次请求（同步一页最多 200 条、领取一轮、补发一条事件、交个人资料、取头像）。
	// 都是小请求，局域网上远在 1 秒内；给到 60 秒，房主忙（大库同步）也不会误判
	collabCallTimeout = 60 * time.Second
	// collabBeatTimeout：心跳。两个心跳间隔（15 秒），房主那边 60 秒没收到才显示离线，30 秒内重连上不会闪成离线
	collabBeatTimeout = 30 * time.Second
	// collabUserCallTimeout：用户操作经房主的请求（发言、加 Agent、改设置……），用户在界面上等着，沿用原来的 30 秒
	collabUserCallTimeout = 30 * time.Second
)

// errHostStalled 是房主在限定时间内没应答：这条连接已经关掉，按断线重连
var errHostStalled = errors.New("the host stopped answering; reconnecting")

type roomStatus struct {
	Connected bool      `json:"connected"`
	Addr      string    `json:"addr,omitempty"`
	Error     string    `json:"error,omitempty"`
	Since     time.Time `json:"since"`
}

type roomConn struct {
	d    *Daemon
	room string
	// mu 是连接锁：一次只有一个调用在用连接（请求、同步、补发）。用带一格缓冲的 channel 做锁，
	// 用户操作等锁也能带时限（lockCtx），后台的照常一直等（lock）
	mu     chan struct{}
	cli    *collaboration.Client
	pokeCh chan struct{}
	// ctx、cancel 建的时候就定下，之后不变：stop 不用拿 mu（mu 可能被等房主应答的调用占着）
	ctx    context.Context
	cancel context.CancelFunc
	done   chan struct{}
	// lmu 护着 started：run 开始了 stop 才等它结束，没开始过的直接返回
	lmu     sync.Mutex
	started bool

	fetchCh chan struct{}
	bmu     sync.Mutex // 附件连接
	bulk    *collaboration.Client

	stmu     sync.Mutex
	status   roomStatus
	presence map[string]time.Time // 房主告诉的在线的人（收到的时间）
	missing  map[string]bool      // 房主说没有这个文件的附件（download 返回 not_found）：显示不可用，不再试
	retryAt  map[string]time.Time // 暂时下不了的附件下次再试的时间（退避，不会因此变成永久缺失）
	backoff  map[string]time.Duration
	// 个人资料：交给房主的结果（空是交上了或还没交、unsupported 是房主版本旧），取不到的头像
	profileErr   string
	profileTried int64
	avatarFailed map[string]bool
}

func newRoomConn(d *Daemon, parent context.Context, room string) *roomConn {
	ctx, cancel := context.WithCancel(parent)
	return &roomConn{d: d, room: room, ctx: ctx, cancel: cancel, mu: make(chan struct{}, 1), pokeCh: make(chan struct{}, 1), fetchCh: make(chan struct{}, 1), done: make(chan struct{}),
		missing: map[string]bool{}, retryAt: map[string]time.Time{}, backoff: map[string]time.Duration{}}
}

func (rc *roomConn) stopped() bool {
	select {
	case <-rc.done:
		return true
	default:
		return false
	}
}

// stop 取消连接循环并等它结束。取消后等着房主应答的请求马上返回，不用等它们放开 mu。
func (rc *roomConn) stop() {
	rc.cancel()
	rc.lmu.Lock()
	started := rc.started
	rc.lmu.Unlock()
	if started {
		<-rc.done
	}
}

func (rc *roomConn) lock()   { rc.mu <- struct{}{} }
func (rc *roomConn) unlock() { <-rc.mu }

// lockCtx 等连接锁直到 ctx 结束，拿到了返回 true。
func (rc *roomConn) lockCtx(ctx context.Context) bool {
	select {
	case rc.mu <- struct{}{}:
		return true
	case <-ctx.Done():
		return false
	}
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

func (rc *roomConn) run() {
	ctx := rc.ctx
	rc.lmu.Lock()
	if ctx.Err() != nil {
		// 还没开始就停了
		rc.lmu.Unlock()
		close(rc.done)
		return
	}
	rc.started = true
	rc.lmu.Unlock()
	defer close(rc.done)
	defer rc.cancel()
	go rc.fetchLoop(ctx)
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
		case store.JoinedActive, joinedLeaving:
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
			// setStatus 自己会叫醒这个循环（经 taskNotify），清掉，按退避等
			select {
			case <-rc.pokeCh:
			default:
			}
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

// session 用凭据连上房主，一直同步到断开。第一次补齐增量之后才算连上（才开始派发），
// 交接、退出时断开不暂停已领取的轮次（新进程接上就接着做）。
func (rc *roomConn) session(ctx context.Context, j store.Joined) error {
	cred, err := rc.d.loadCredential(j.Room)
	if err != nil {
		if cred, err = rc.refetchCredential(ctx, j); err != nil {
			return err
		}
	}
	cli, err := rc.dial(ctx, j)
	if err != nil {
		return err
	}
	var hello struct {
		Features []string `json:"features"`
	}
	if err := cli.Call(ctx, "hello", collabHello{Room: j.Room, Credential: cred.Credential, Version: collaboration.ProtocolVersion}, &hello); err != nil {
		closeClient(ctx, cli)
		return rc.callFailed(err)
	}
	// 每次连上都重记：房主升级、降级后按它现在的能力来
	if err := rc.d.store.SetHostFeatures(j.Room, hello.Features); err != nil {
		cli.Close()
		return err
	}
	rc.lock()
	rc.cli = cli
	rc.unlock()
	defer func() {
		rc.lock()
		rc.cli = nil
		rc.unlock()
		closeClient(ctx, cli)
		rc.closeBulk(ctx.Err() != nil)
		// 和房主断开（不是交接、退出）：已领取、还没开始的轮次等拥有者确认再开始；已经开始的照常做完，结果留在 outbox
		if ctx.Err() == nil {
			if n, _ := rc.d.store.PauseOwnQueues(j.Room, collabLostReason); n > 0 {
				rc.d.taskNotify()
			}
		}
	}()
	beat := time.NewTicker(collaboration.HeartbeatEvery)
	defer beat.Stop()
	if err := rc.heartbeat(ctx); err != nil {
		return err
	}
	// 新连接：资料和取不到的头像重新试（要在第一轮之前，第一轮记下的结果要留到下次重连）
	rc.stmu.Lock()
	clear(rc.avatarFailed)
	rc.profileErr, rc.profileTried = "", 0
	rc.stmu.Unlock()
	first := true
	for {
		if err := rc.step(ctx); err != nil {
			return err
		}
		if first {
			first = false
			// 连接回来了：暂时下不了的附件不用等退避，马上再试
			rc.stmu.Lock()
			clear(rc.retryAt)
			clear(rc.backoff)
			rc.stmu.Unlock()
			rc.setStatus(true, cli.Addr, "")
			rc.d.discKick()
		}
		if cur, err := rc.d.store.Joined(j.Room); err == nil && cur.State == joinedLeaving {
			return rc.finishLeave(ctx)
		}
		rc.kickFetch()
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

// refetchCredential：本机的凭据文件丢了。凭据由房主私钥派生，按本机记着的申请 ID 向房主重取
// （房主核对 TLS 指纹就是申请人、成员资格仍是那一代）；取不回来才停在缓存上。
func (rc *roomConn) refetchCredential(ctx context.Context, j store.Joined) (credentialFile, error) {
	fail := func() (credentialFile, error) {
		rc.d.revokeLocal(j.Room, store.PersonRemoved, "the room credential on this computer is missing; ask the host for a new invitation")
		return credentialFile{}, errStopRoom
	}
	if j.Request == "" {
		return fail()
	}
	cli, err := rc.dial(ctx, j)
	if err != nil {
		return credentialFile{}, err
	}
	defer cli.Close()
	var st collabJoinStatus
	if err := cli.Call(ctx, "join.status", collabJoinParams{Room: j.Room, Request: j.Request}, &st); err != nil {
		var ce *collaboration.Error
		if errors.As(err, &ce) {
			return fail()
		}
		return credentialFile{}, err
	}
	if st.State != store.RequestApproved || st.Credential == "" {
		return fail()
	}
	if err := rc.d.saveCredential(j.Room, j.Host, st.Generation, st.Credential); err != nil {
		return credentialFile{}, err
	}
	log.Printf("collaboration %s: room credential fetched again", short(j.Room))
	return rc.d.loadCredential(j.Room)
}

// finishLeave 是离线时点了退出、现在连上了：先把本机已有的结果交上去（step 里补发过），再请房主撤销，
// 然后在本机停用。
func (rc *roomConn) finishLeave(ctx context.Context) error {
	rc.lock()
	err := rc.callLocked(ctx, collabCallTimeout, "leave", map[string]any{}, nil)
	rc.unlock()
	if err != nil && !collaboration.IsCode(err, collaboration.CodeRevoked) {
		return err
	}
	rc.d.revokeLocal(rc.room, store.PersonLeft, "you left the discussion")
	return errStopRoom
}

// heartbeat 每 15 秒告诉房主还连着（房主据此显示在线、执行有没有失联），顺带拿回谁在线。
func (rc *roomConn) heartbeat(ctx context.Context) error {
	var res struct {
		Online []string `json:"online"`
	}
	rc.lock()
	err := rc.callLocked(ctx, collabBeatTimeout, "heartbeat", map[string]any{}, &res)
	rc.unlock()
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

// callLocked 在持有 rc.mu 时发一个请求，最多等 timeout。房主没在时限内应答（连接没断）时关掉这条连接：
// 和断线一样，session 看到连接关了就退出、按退避重连，不留半开的连接给下一次请求用。没连着报 ErrClosed。
func (rc *roomConn) callLocked(ctx context.Context, timeout time.Duration, method string, params, out any) error {
	cli := rc.cli
	if cli == nil {
		return collaboration.ErrClosed
	}
	cctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	err := cli.Call(cctx, method, params, out)
	if err != nil && ctx.Err() == nil && errors.Is(cctx.Err(), context.DeadlineExceeded) {
		log.Printf("collaboration %s: no answer to %s within %s; reconnecting", short(rc.room), method, timeout)
		cli.Abort()
		return errHostStalled
	}
	return err
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
	rc.lock()
	defer rc.unlock()
	if rc.cli == nil {
		return collaboration.ErrClosed
	}
	if err := rc.syncLocked(ctx); err != nil {
		return err
	}
	if err := rc.claimLocked(ctx); err != nil {
		return err
	}
	if err := rc.flushLocked(ctx); err != nil {
		return err
	}
	if err := rc.profileLocked(ctx); err != nil {
		return err
	}
	rc.avatarsLocked(ctx)
	return nil
}

func (rc *roomConn) syncLocked(ctx context.Context) error {
	changed := false
	for range 1000 {
		j, err := rc.d.store.Joined(rc.room)
		if err != nil {
			return err
		}
		var b store.SyncBatch
		if err := rc.callLocked(ctx, collabCallTimeout, "sync", map[string]any{"since": j.Cursor, "limit": 200}, &b); err != nil {
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
	if j.State == joinedLeaving {
		return nil
	}
	for _, x := range xs {
		if reason := d.intentMismatch(x); reason != "" {
			d.store.Accept(rc.room, x.ID, j.Generation, store.AcceptRefused, reason, time.Now())
			d.taskNotify()
			continue
		}
		var res map[string]any
		if err := rc.callLocked(ctx, collabCallTimeout, "claim", map[string]string{"execution": x.ID}, &res); err != nil {
			if collaboration.IsCode(err, collaboration.CodeConflict) || collaboration.IsCode(err, collaboration.CodeNotFound) {
				continue // 已经取消或不在了：下一次同步会带来新状态
			}
			return rc.callFailed(err)
		}
		// 先记进账本（fetching），后台下完请求和引用里的附件才转 accepted 进本机队列（上下文快照给本机路径）
		if err := d.store.Accept(rc.room, x.ID, j.Generation, store.AcceptFetching, "", time.Now()); err != nil {
			return err
		}
		d.taskNotify()
		rc.kickFetch()
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
		if m, err = d.store.DiscMessage(x.Message); err != nil {
			return "this request was not sent from this computer"
		}
		if m.AuthorID != "" && m.AuthorID != d.store.Self() {
			return d.accessMismatch(x, m)
		}
		if m.ClientID == "" {
			return "this request was not sent from this computer"
		}
		key = m.ClientID
	}
	in, err := d.store.Intent(x.Discussion, key)
	if err != nil {
		return "this request was not sent from this computer"
	}
	if mem, err := d.store.Member(x.Member); err != nil || mem.RemovedAt != nil {
		return "the agent was removed"
	}
	if in.Member != x.Member {
		return "the request targets a different agent than the one you asked"
	}
	if x.RetryOf == "" && in.Digest != store.IntentDigest(m.Body, m.Quote, m.Target, m.Attachments) {
		return "the request content differs from what you sent"
	}
	// 配置按内容核对，不只看房主给的版本号：房主那份执行快照、本机这份成员配置都要和发出时确认的一致
	mem, err := d.store.Member(x.Member)
	if err != nil || in.ConfigVersion != x.ConfigVersion || in.Config == "" ||
		store.OwnConfigDigest(store.Member{Role: x.Role, Agent: x.Agent, Model: x.Model, Effort: x.Effort}) != in.Config ||
		store.OwnConfigDigest(mem) != in.Config {
		return "the agent's configuration changed after you sent the request"
	}
	return ""
}

// accessMismatch 核对别人 @ 本机 Agent 的一轮：按本机确认过的访问设置（不信房主那份）这个人能 @ 它，
// 请求确实是发给它的，房主那份执行快照的配置和本机确认的一致。对得上为空。
// 发言人是房主记的：房主能冒充一个被放开的人发请求，访问设置防不了这一点。
func (d *Daemon) accessMismatch(x store.Execution, m store.DiscMessage) string {
	mem, err := d.store.Member(x.Member)
	if err != nil || m.Target != x.Member {
		return "the request targets a different agent"
	}
	if self := d.store.Self(); x.Owner != self || mem.Owner != self {
		return "this agent is not yours"
	}
	disc, err := d.store.Discussion(x.Discussion)
	if err != nil {
		return "the discussion is gone"
	}
	if !d.store.OwnConfigHolds(mem.ID) || store.ConfigUntrusted(mem.Paused) {
		return "the agent's settings on this computer need your review"
	}
	if !mem.Allows(m.AuthorID, disc.Host) {
		return "you have not allowed this person to use this agent"
	}
	if store.OwnConfigDigest(store.Member{Role: x.Role, Agent: x.Agent, Model: x.Model, Effort: x.Effort}) != store.OwnConfigDigest(mem) {
		return "the request's agent settings differ from yours"
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
		err := rc.callLocked(ctx, collabCallTimeout, "event", map[string]any{"execution": it.Exec, "seq": it.Seq, "digest": it.Digest, "event": it.Event}, nil)
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

// kickFetch 叫醒后台的附件下载（不占用同步用的连接）。
func (rc *roomConn) kickFetch() {
	select {
	case rc.fetchCh <- struct{}{}:
	default:
	}
}

// fetchLoop 在后台下载附件：先把已领取的请求要用的附件下完（下完才进本机队列），再按游标增量下载新消息的附件。
// 走单独的连接，大文件不挡心跳、同步和领取。
func (rc *roomConn) fetchLoop(ctx context.Context) {
	for {
		select {
		case <-ctx.Done():
			return
		case <-rc.fetchCh:
		}
		if !rc.d.collab.manualFetch {
			rc.fetchRound(ctx)
		}
	}
}

// fetchRound 是一遍附件处理：领取了等附件的执行，然后按游标的新消息附件。
func (rc *roomConn) fetchRound(ctx context.Context) {
	for _, exec := range rc.d.store.Fetching(rc.room) {
		x, err := rc.d.store.Execution(exec)
		if err != nil {
			continue
		}
		// 暂时下不了（断线、房主临时出错）：留在 fetching，不进本机队列，下次再试；
		// 房主确实没有的附件按不可用交给 Agent（上下文里标 missing）
		if !rc.fetchMessage(ctx, x.Message) || ctx.Err() != nil {
			continue
		}
		if ok, _ := rc.d.store.MarkAccepted(exec); ok {
			rc.d.taskNotify()
			rc.d.discKick()
		}
	}
	for ctx.Err() == nil {
		cur := rc.d.store.AttachmentCursor(rc.room)
		msgs, err := rc.d.store.AttachmentsAfter(rc.room, cur, 20)
		if err != nil || len(msgs) == 0 {
			break
		}
		stuck := false
		for _, m := range msgs {
			if !rc.fetchAll(ctx, m.Attachments) {
				stuck = true
				break
			}
			rc.d.store.SetAttachmentCursor(rc.room, m.Seq)
		}
		if stuck {
			break
		}
	}
}

// fetchMessage 下载一条消息和它引用的消息上的附件（领取时：上下文快照要给本机路径）。
// 返回是否都到位了（房主确实没有的算到位：按不可用处理）。
func (rc *roomConn) fetchMessage(ctx context.Context, msgID string) bool {
	m, err := rc.d.store.DiscMessage(msgID)
	if err != nil {
		return false
	}
	atts := m.Attachments
	if m.Quote != "" {
		if q, err := rc.d.store.DiscMessage(m.Quote); err == nil {
			atts = append(atts, q.Attachments...)
		}
	}
	return rc.fetchAll(ctx, atts)
}

// fetchAll 下载还没在缓存里的附件，返回是不是都到位了。只有 download 明确返回 not_found（房主没有这个文件）
// 才记成不可用、不再试；拨号、hello、断线、房主内部错误、内容对不上都是暂时的：按退避重试，不推进游标，
// 不会因为试的次数多就变成永久缺失。
func (rc *roomConn) fetchAll(ctx context.Context, atts []store.Attachment) bool {
	ok := true
	for _, a := range atts {
		if a.Path == "" {
			continue
		}
		dst := filepath.Join(rc.d.cfg.Home, a.Path)
		if _, err := os.Stat(dst); err == nil {
			continue
		}
		rc.stmu.Lock()
		missing, wait := rc.missing[a.ID], rc.retryAt[a.ID]
		rc.stmu.Unlock()
		if missing {
			continue
		}
		if time.Now().Before(wait) {
			ok = false
			continue
		}
		gone, err := rc.downloadTo(ctx, a, dst)
		rc.stmu.Lock()
		switch {
		case err == nil:
			delete(rc.retryAt, a.ID)
			delete(rc.backoff, a.ID)
		case gone:
			rc.missing[a.ID] = true
		default:
			b := min(max(2*rc.backoff[a.ID], 5*time.Second), 10*time.Minute)
			rc.backoff[a.ID], rc.retryAt[a.ID] = b, time.Now().Add(b)
			ok = false
		}
		rc.stmu.Unlock()
		if err != nil {
			log.Printf("collaboration %s: attachment %s: %v", short(rc.room), a.ID, err)
			continue
		}
		rc.d.taskNotify()
	}
	return ok
}

// bulkClient 是传附件用的单独连接（按需连上、hello），和同步用的连接分开。
func (rc *roomConn) bulkClient(ctx context.Context) (*collaboration.Client, error) {
	rc.bmu.Lock()
	defer rc.bmu.Unlock()
	if rc.bulk != nil {
		select {
		case <-rc.bulk.Done():
			rc.bulk = nil
		default:
			return rc.bulk, nil
		}
	}
	j, err := rc.d.store.Joined(rc.room)
	if err != nil {
		return nil, err
	}
	cred, err := rc.d.loadCredential(rc.room)
	if err != nil {
		return nil, err
	}
	cli, err := rc.dial(ctx, j)
	if err != nil {
		return nil, err
	}
	// 拿着 bmu 等 hello：限时，出错时停下来了或者房主没应答就直接断开，不等关闭握手（会话收尾的 closeBulk 也在等 bmu）
	hctx, hcancel := context.WithTimeout(ctx, collabCallTimeout)
	err = cli.Call(hctx, "hello", collabHello{Room: rc.room, Credential: cred.Credential, Version: collaboration.ProtocolVersion}, nil)
	stalled := hctx.Err() != nil
	hcancel()
	if err != nil {
		if stalled {
			cli.Abort()
		} else {
			cli.Close()
		}
		return nil, err
	}
	rc.bulk = cli
	return cli, nil
}

// closeBulk 关掉附件连接；abort 时不等关闭握手（停下来的时候房主可能不应答）。
func (rc *roomConn) closeBulk(abort bool) {
	rc.bmu.Lock()
	defer rc.bmu.Unlock()
	if rc.bulk != nil {
		if abort {
			rc.bulk.Abort()
		} else {
			rc.bulk.Close()
		}
		rc.bulk = nil
	}
}

// closeClient 断开到房主的连接：要停下了（ctx 已取消）就不等关闭握手，房主不应答时 Close 要等 5 秒，交接、退出跟着等。
func closeClient(ctx context.Context, cli *collaboration.Client) {
	if ctx.Err() != nil {
		cli.Abort()
		return
	}
	cli.Close()
}

// downloadTo 按附件 ID 分块下载到缓存：先写同目录的临时文件，长度和摘要对上再改名。
// gone 为真只表示 download 本身返回了 not_found（房主没有这个文件）；其余错误都是暂时的。
func (rc *roomConn) downloadTo(ctx context.Context, a store.Attachment, dst string) (gone bool, err error) {
	if err := os.MkdirAll(filepath.Dir(dst), 0o700); err != nil {
		return false, err
	}
	tmp := dst + ".part"
	f, err := os.OpenFile(tmp, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o600)
	if err != nil {
		return false, err
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
		cli, err := rc.bulkClient(ctx)
		if err != nil {
			f.Close()
			return false, fmt.Errorf("attachment connection: %v", err)
		}
		if err := cli.Call(ctx, "download", map[string]any{"attachment": a.ID, "offset": off}, &r); err != nil {
			f.Close()
			return collaboration.IsCode(err, collaboration.CodeNotFound), err
		}
		if sha == "" {
			sha = r.SHA
		}
		if r.Size > discMaxAttachSize || off+int64(len(r.Data)) > r.Size {
			f.Close()
			return false, fmt.Errorf("attachment is larger than announced")
		}
		f.Write(r.Data)
		h.Write(r.Data)
		off += int64(len(r.Data))
		if r.EOF || len(r.Data) == 0 {
			break
		}
	}
	if err := f.Close(); err != nil {
		return false, err
	}
	if sha != "" && hex.EncodeToString(h.Sum(nil)) != sha {
		return false, fmt.Errorf("attachment content does not match its digest")
	}
	return false, os.Rename(tmp, dst)
}

// call 在连着的时候发一个请求给房主，然后补齐增量（让本机缓存马上有结果）。没连着报房主离线。
// online 报告现在连着房主（call 能发出去）。
// 后台同步正占着连接、房主又不应答时，等 collabUserCallTimeout 还拿不到锁就当不在线。
func (rc *roomConn) online() bool {
	ctx, cancel := context.WithTimeout(rc.ctx, collabUserCallTimeout)
	defer cancel()
	if !rc.lockCtx(ctx) {
		return false
	}
	defer rc.unlock()
	return rc.cli != nil
}

// call 从调用起最多 collabUserCallTimeout 返回：等连接锁（后台同步可能正占着）、请求、请求后的补齐增量都算在里面。
// 等锁就到点了说明什么都没发出去，报房主不在线；请求超时照后台一样断开重连，报结果未知。
func (rc *roomConn) call(method string, params, out any) error {
	deadline := time.Now().Add(collabUserCallTimeout)
	ctx, cancel := context.WithDeadline(rc.ctx, deadline)
	defer cancel()
	if !rc.lockCtx(ctx) {
		return hostOffline(rc)
	}
	defer rc.unlock()
	if rc.cli == nil {
		return hostOffline(rc)
	}
	// 用 rc.ctx 加剩下的时间：到点时 callLocked 认得出是这次请求超时，断开这条连接
	if err := rc.callLocked(rc.ctx, time.Until(deadline), method, params, out); err != nil {
		var ce *collaboration.Error
		if errors.As(err, &ce) {
			if ce.Code == collaboration.CodeRevoked {
				go rc.d.revokeLocal(rc.room, store.PersonRemoved, ce.Message)
			}
			return rpc.Errorf(rpc.CodeConflict, "%s", ce.Message)
		}
		// 请求可能已经到了房主、只是应答没回来：说不准有没有做，Data 告诉调用方结果未知
		e := rpc.Errorf(rpc.CodeConflict, "the host did not answer (%v); it may or may not have done this", err)
		e.Data = json.RawMessage(`{"unknown":true}`)
		return e
	}
	// 补齐增量也在时限里：到点了就不等，下一轮后台同步补上
	if err := rc.syncLocked(ctx); err != nil && !errors.Is(err, errStopRoom) {
		log.Printf("collaboration %s: sync after %s: %v", short(rc.room), method, err)
	}
	return nil
}

// flushNow 把还没交出去的事件补发一遍（退出前）。
func (rc *roomConn) flushNow() {
	ctx, cancel := context.WithTimeout(rc.ctx, 15*time.Second)
	defer cancel()
	// 等锁也算在 15 秒里：后台正占着连接时不一直等
	if !rc.lockCtx(ctx) {
		return
	}
	defer rc.unlock()
	if rc.cli == nil {
		return
	}
	rc.flushLocked(ctx)
}

// syncNow 补齐一次增量（发消息前对账用）；没连着就算了。
func (rc *roomConn) syncNow() {
	ctx, cancel := context.WithTimeout(rc.ctx, 15*time.Second)
	defer cancel()
	if !rc.lockCtx(ctx) {
		return
	}
	defer rc.unlock()
	if rc.cli == nil {
		return
	}
	rc.syncLocked(ctx)
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
	return rpc.Errorf(rpc.CodeConflict, "not connected to the host: %s Nothing was sent.", strings.TrimSuffix(st, ".")+".")
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
	if !rc.d.roomStatus(rc.room).Connected {
		return "", hostOffline(rc)
	}
	// 走附件连接：大文件不挡心跳、同步和领取
	ctx, cancel := context.WithTimeout(rc.ctx, 10*time.Minute)
	defer cancel()
	cli, err := rc.bulkClient(ctx)
	if err != nil {
		return "", wrapHostErr(err)
	}
	var begin struct {
		Upload string `json:"upload"`
		Done   bool   `json:"done"`
	}
	if err := cli.Call(ctx, "upload.begin", map[string]any{"name": name, "size": st.Size(), "sha": sum}, &begin); err != nil {
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
			if err := cli.Call(ctx, "upload.chunk", map[string]any{"upload": begin.Upload, "offset": off, "data": buf[:n]}, nil); err != nil {
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
	if err := cli.Call(ctx, "upload.end", map[string]any{"upload": begin.Upload}, nil); err != nil {
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
		if rc == nil || !d.roomStatus(disc.Key).Connected {
			// 房主不在线：本机先停用（不领取、不开始新的一轮，不会自动恢复），已有的结果留着，
			// 连上房主后先交上去再请它撤销
			if err := d.store.SetJoined(disc.Key, joinedLeaving, j.Generation, nil, ""); err != nil {
				return nil, err
			}
			d.store.PauseOwnMembers(disc.Key, collabLeavingReason)
			d.taskNotify()
			return map[string]any{"left": disc.Key, "pending": true}, nil
		}
		rc.flushNow()
		if err := rc.call("leave", map[string]any{}, nil); err != nil {
			return nil, err
		}
	case joinedLeaving:
		return map[string]any{"left": disc.Key, "pending": true}, nil
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
// guestIn 报告本机在这个加入的讨论里是不是只读访客（按同步来的人员表；房主还会再核对）。
func (d *Daemon) guestIn(room string) bool {
	p, err := d.store.Person(room, d.store.Self())
	return err == nil && p.Role == store.RoleGuest
}

// guestErr 是只读访客做写操作时本机直接给的报错，和房主拒绝时的是同一句。
func guestErr() error { return rpc.Errorf(rpc.CodeConflict, "%v", store.ErrGuest) }

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
	if j.State == joinedLeaving {
		return nil, rpc.Errorf(rpc.CodeConflict, "you are leaving this discussion; it finishes when the host is reachable")
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
