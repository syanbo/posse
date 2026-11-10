package daemon

import (
	"context"
	"net"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"posse/internal/agent"
	"posse/internal/collaboration"
	"posse/internal/store"
	"posse/internal/term"
)

// 两个常驻进程在同一台机器上冒充房主和成员：各自的 POSSE_HOME、各自的库，经回环地址上的协作端口相连。
// 只证明协议和授权逻辑；局域网可达、系统权限要两台真实的 Mac。

// collabClockOffset 把房主的时钟往后拨（心跳超时测试）
var collabClockOffset atomic.Int64

func fakeLaunch(d *Daemon) {
	var mu sync.Mutex
	d.disc.launch = func(w store.Workspace, name string, m agent.Model, prompt string, env ...string) (*session, error) {
		mu.Lock()
		defer mu.Unlock()
		s, err := d.launchTerminalLocked(w, []string{"/bin/cat"}, name, 100, 20, store.Terminal{}, env...)
		if err == nil {
			d.setAgent(s, term.AgentState{Status: agent.StatusWorking})
		}
		return s, err
	}
}

type collabPair struct {
	host, member *Daemon
	hw, mw       store.Workspace
	disc         store.Discussion
	cancel       context.CancelFunc
}

func startCollab(t *testing.T, d *Daemon) context.CancelFunc {
	ctx, cancel := context.WithCancel(context.Background())
	// 固定一个空闲端口：关掉共享再打开时地址不变（和真实的固定端口一样）
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	d.collab.listenAddr = l.Addr().String()
	l.Close()
	d.collab.now = func() time.Time { return time.Now().Add(time.Duration(collabClockOffset.Load())) }
	d.collabStart(ctx)
	t.Cleanup(func() {
		cancel()
		d.collabStop()
	})
	return cancel
}

func waitFor(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(30 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s", what)
}

func invite(t *testing.T, d *Daemon, disc store.Discussion) string {
	t.Helper()
	res := call(t, d.collabInvite, map[string]any{"discussion": disc.Key}).(map[string]any)
	return res["invite"].(string)
}

// newCollabPair：房主建讨论、打开共享、发邀请；成员粘贴邀请、房主批准、成员连上。
func newCollabPair(t *testing.T) *collabPair {
	t.Helper()
	t.Setenv("POSSE_COLLAB_ADDRS", "127.0.0.1")
	host, hw, disc := newDiscDaemon(t)
	member, mw := newTestDaemon(t)
	fakeLaunch(member)
	startCollab(t, host)
	startCollab(t, member)
	call(t, host.collabShare, map[string]any{"discussion": disc.Key, "on": true})
	inv := invite(t, host, disc)
	res := call(t, member.collabJoin, map[string]any{"invite": inv}).(map[string]any)
	if res["state"] != store.RequestPending {
		t.Fatalf("join %+v", res)
	}
	var reqs []store.JoinRequest
	waitFor(t, "join request", func() bool {
		reqs, _ = host.store.JoinRequests(disc.Key, true)
		return len(reqs) == 1
	})
	call(t, host.collabDecide, map[string]any{"request": reqs[0].ID, "approve": true})
	waitFor(t, "member connected", func() bool {
		j, _ := member.store.Joined(disc.Key)
		return j.State == store.JoinedActive && member.roomStatus(disc.Key).Connected
	})
	return &collabPair{host: host, member: member, hw: hw, mw: mw, disc: disc}
}

func (p *collabPair) memberDisc(t *testing.T) store.Discussion {
	d, err := p.member.store.Discussion(p.disc.Key)
	if err != nil {
		t.Fatal(err)
	}
	return d
}

func TestCollabJoinPostAndRemoteExecution(t *testing.T) {
	p := newCollabPair(t)
	h, m := p.host, p.member
	if !p.memberDisc(t).Mirror {
		t.Fatal("member copy should be a mirror")
	}
	alice := addMember(t, h, p.disc, "Alice", "claude")

	// 成员加自己的 Agent：公开配置在房主，工作区只在成员电脑上
	bob := call(t, m.discMemberAdd, map[string]any{"discussion": p.disc.Key, "name": "Bob", "agent": "codex", "workspace": p.mw.ID}).(store.Member)
	if bob.Owner != m.store.Self() || bob.WorkspaceID != p.mw.ID {
		t.Fatalf("member's copy %+v", bob)
	}
	hb, err := h.store.Member(bob.ID)
	if err != nil || hb.Owner != m.store.Self() || hb.WorkspaceID != "" {
		t.Fatalf("host copy %+v %v", hb, err)
	}

	// 只能 @ 自己的 Agent：房主 @ Bob、成员 @ Alice 都拒绝
	if _, err := callErr(h.discPost, map[string]any{"discussion": p.disc.Key, "body": "hi", "target": bob.ID}); err == nil || !strings.Contains(err.Error(), "owner") {
		t.Fatalf("host invoked the member's agent: %v", err)
	}
	if _, err := callErr(m.discPost, map[string]any{"discussion": p.disc.Key, "body": "hi", "target": alice.ID, "clientId": "c-x"}); err == nil {
		t.Fatal("member invoked the host's agent")
	}
	// 房主不能替成员停止、重试、配置他的 Agent
	if _, err := callErr(h.discMemberUpdate, map[string]any{"member": bob.ID, "role": "evil"}); err == nil {
		t.Fatal("host edited the member's agent")
	}

	// 普通发言 + 重复发送只落一条
	r0 := call(t, m.discPost, map[string]any{"discussion": p.disc.Key, "body": "hello from member", "clientId": "c-0"}).(discPostResult)
	again := call(t, m.discPost, map[string]any{"discussion": p.disc.Key, "body": "hello from member", "clientId": "c-0"}).(discPostResult)
	if !again.Duplicate || again.Message.ID != r0.Message.ID {
		t.Fatalf("duplicate: %+v", again)
	}
	hm, _ := h.store.DiscMessage(r0.Message.ID)
	if hm.AuthorID != m.store.Self() || hm.AuthorName == "" {
		t.Fatalf("host message author %+v", hm)
	}

	// @ 自己的 Agent：房主持久化请求，成员领取、在本机执行、结果回群
	r := call(t, m.discPost, map[string]any{"discussion": p.disc.Key, "body": "please review", "target": bob.ID, "clientId": "c-1"}).(discPostResult)
	if r.Execution == nil {
		t.Fatal("no execution")
	}
	var x store.Execution
	waitFor(t, "member runs the round", func() bool {
		m.dispatchAll()
		x, _ = m.store.Execution(r.Execution.ID)
		return x.State == store.ExecRunning
	})
	waitFor(t, "host sees it running", func() bool {
		hx, _ := h.store.Execution(x.ID)
		return hx.State == store.ExecRunning
	})
	// 房主不派发别人的 Agent
	if hx, _ := h.store.Execution(x.ID); hx.TaskID != "" {
		t.Fatalf("host started a worker for the member's agent: %+v", hx)
	}
	ctxText := readAll(t, m, x)
	if !strings.Contains(ctxText, "please review") || !strings.Contains(ctxText, "hello from member") {
		t.Fatalf("context:\n%s", ctxText)
	}
	call(t, m.taskDone, map[string]any{"task": x.TaskID, "caller": x.Worker, "execution": x.ID, "body": "## LGTM\nfrom Bob's computer"})
	// 重跑同一汇报：成功、不多一条
	call(t, m.taskDone, map[string]any{"task": x.TaskID, "caller": x.Worker, "execution": x.ID, "body": "## LGTM\nfrom Bob's computer"})
	if _, err := callErr(m.taskDone, map[string]any{"task": x.TaskID, "caller": x.Worker, "execution": x.ID, "body": "different"}); err == nil {
		t.Fatal("different reply accepted")
	}
	waitFor(t, "reply on host", func() bool {
		hx, _ := h.store.Execution(x.ID)
		return hx.State == store.ExecDone && hx.Reply != ""
	})
	waitFor(t, "reply synced back", func() bool {
		msgs, _ := m.store.DiscMessages(p.disc.Key, 0, 100)
		n := 0
		for _, mm := range msgs {
			if mm.Kind == store.DiscReply && strings.Contains(mm.Body, "from Bob's computer") {
				n++
			}
		}
		return n == 1
	})
	hmsgs, _ := h.store.DiscMessages(p.disc.Key, 0, 100)
	replies := 0
	for _, mm := range hmsgs {
		if mm.Kind == store.DiscReply {
			replies++
		}
	}
	if replies != 1 {
		t.Fatalf("%d replies on host", replies)
	}

	// 房主那边的关键标记、成员这边的关键标记都同步
	call(t, m.discKey, map[string]any{"message": r0.Message.ID, "on": true})
	waitFor(t, "key on host", func() bool {
		hm, _ := h.store.DiscMessage(r0.Message.ID)
		return hm.KeyAt != nil && hm.KeyBy == m.store.Self()
	})
}

// 房主伪造一条归属成员的请求（成员没有发过）：成员不领取、不执行。
func TestCollabForgedRequestNotExecuted(t *testing.T) {
	p := newCollabPair(t)
	h, m := p.host, p.member
	bob := call(t, m.discMemberAdd, map[string]any{"discussion": p.disc.Key, "name": "Bob", "agent": "codex", "workspace": p.mw.ID}).(store.Member)
	person, _ := h.store.Person(p.disc.Key, m.store.Self())
	// 直接写房主的库：冒充成员发了一条 @Bob
	_, fx, _, err := h.store.PostMessage(store.PostInput{Discussion: p.disc.Key, Body: "rm -rf everything", Target: bob.ID, ClientID: "forged",
		Author: m.store.Self(), AuthorName: person.Name, Generation: person.Generation, At: time.Now()})
	if err != nil {
		t.Fatal(err)
	}
	h.taskNotify()
	waitFor(t, "member refuses", func() bool {
		return m.store.AcceptState(fx.ID) == store.AcceptRefused
	})
	m.dispatchAll()
	if x, _ := m.store.Execution(fx.ID); x.State != store.ExecQueued || x.TaskID != "" {
		t.Fatalf("forged request ran: %+v", x)
	}
	if hx, _ := h.store.Execution(fx.ID); hx.State != store.ExecQueued {
		t.Fatalf("forged request was claimed: %s", hx.State)
	}
}

// 移除成员：连接断开、成员停在缓存上、之后的请求和旧凭据都被拒；重新加入是新的一代。
func TestCollabRevokeAndRejoin(t *testing.T) {
	p := newCollabPair(t)
	h, m := p.host, p.member
	bob := call(t, m.discMemberAdd, map[string]any{"discussion": p.disc.Key, "name": "Bob", "agent": "codex", "workspace": p.mw.ID}).(store.Member)
	r := call(t, m.discPost, map[string]any{"discussion": p.disc.Key, "body": "long job", "target": bob.ID, "clientId": "c-1"}).(discPostResult)
	var x store.Execution
	waitFor(t, "running", func() bool {
		m.dispatchAll()
		x, _ = m.store.Execution(r.Execution.ID)
		return x.State == store.ExecRunning
	})
	oldCred, err := m.loadCredential(p.disc.Key)
	if err != nil {
		t.Fatal(err)
	}
	call(t, h.collabRemovePerson, map[string]any{"discussion": p.disc.Key, "person": m.store.Self()})
	if hx, _ := h.store.Execution(x.ID); hx.State != store.ExecRevoked {
		t.Fatalf("host exec %s", hx.State)
	}
	waitFor(t, "member sees removal", func() bool {
		j, _ := m.store.Joined(p.disc.Key)
		return j.State == store.PersonRemoved
	})
	if _, err := os.Stat(m.credentialPath(p.disc.Key)); !os.IsNotExist(err) {
		t.Fatal("credential kept after removal")
	}
	// 本机已开始的一轮照常收尾，结果留在本机，不再交给房主
	readAll(t, m, x)
	call(t, m.taskDone, map[string]any{"task": x.TaskID, "caller": x.Worker, "execution": x.ID, "body": "late result"})
	if pend, _ := m.store.OutboxCount(p.disc.Key); pend != 0 {
		t.Fatalf("%d events still pending after revoke", pend)
	}
	hmsgs, _ := h.store.DiscMessages(p.disc.Key, 0, 100)
	for _, mm := range hmsgs {
		if strings.Contains(mm.Body, "late result") {
			t.Fatal("revoked member's result reached the host")
		}
	}
	if _, err := callErr(m.discPost, map[string]any{"discussion": p.disc.Key, "body": "still here?", "clientId": "c-2"}); err == nil {
		t.Fatal("removed member could post")
	}
	// 旧凭据直接连：被拒
	id, _ := collaboration.Load(m.cfg.Home)
	hostAddr := h.collabServer().Addr().String()
	cli, err := collaboration.Dial(context.Background(), id, h.store.Self(), []string{hostAddr}, nil)
	if err != nil {
		t.Fatal(err)
	}
	err = cli.Call(context.Background(), "hello", collabHello{Room: p.disc.Key, Credential: oldCred.Credential, Version: collaboration.ProtocolVersion}, nil)
	if !collaboration.IsCode(err, collaboration.CodeRevoked) {
		t.Fatalf("old credential: %v", err)
	}
	if err := cli.Call(context.Background(), "sync", map[string]any{"since": 0}, nil); !collaboration.IsCode(err, collaboration.CodeUnauthorized) {
		t.Fatalf("sync without hello: %v", err)
	}
	cli.Close()

	// 新邀请重新加入：新的一代，旧 Agent 不复活
	inv := invite(t, h, p.disc)
	call(t, m.collabJoin, map[string]any{"invite": inv})
	var reqs []store.JoinRequest
	waitFor(t, "rejoin request", func() bool {
		reqs, _ = h.store.JoinRequests(p.disc.Key, true)
		return len(reqs) == 1
	})
	call(t, h.collabDecide, map[string]any{"request": reqs[0].ID, "approve": true})
	waitFor(t, "reconnected", func() bool {
		j, _ := m.store.Joined(p.disc.Key)
		return j.State == store.JoinedActive && j.Generation == 2 && m.roomStatus(p.disc.Key).Connected
	})
	if hb, _ := h.store.Member(bob.ID); hb.RemovedAt == nil {
		t.Fatal("old agent came back")
	}
	if _, err := callErr(m.discPost, map[string]any{"discussion": p.disc.Key, "body": "@bob again", "target": bob.ID, "clientId": "c-3"}); err == nil {
		t.Fatal("old agent accepted a request after rejoin")
	}
	// 旧一代的执行事件不能借新资格补进来
	person, _ := h.store.Person(p.disc.Key, m.store.Self())
	err = h.store.ApplyOwnerEvent(store.HostEvent{Room: p.disc.Key, FP: m.store.Self(), Gen: person.Generation, Exec: x.ID, Seq: 99,
		Event: store.OwnerEvent{Kind: store.EventReply, Body: "replay"}, Digest: store.EventDigest(store.OwnerEvent{Kind: store.EventReply, Body: "replay"})})
	if err == nil {
		t.Fatal("old-generation event accepted")
	}
}

// 邀请：只能用一次、过期拒绝、陌生身份只能提交申请、不能读别的房间。
func TestCollabInviteRules(t *testing.T) {
	t.Setenv("POSSE_COLLAB_ADDRS", "127.0.0.1")
	h, _, disc := newDiscDaemon(t)
	startCollab(t, h)
	call(t, h.collabShare, map[string]any{"discussion": disc.Key, "on": true})
	other := call(t, h.discCreate, map[string]any{"title": "private", "workspace": func() string { ws, _ := h.store.Workspaces(""); return ws[0].ID }()}).(store.Discussion)
	inv := invite(t, h, disc)

	a, _ := newTestDaemon(t)
	b, _ := newTestDaemon(t)
	startCollab(t, a)
	startCollab(t, b)
	call(t, a.collabJoin, map[string]any{"invite": inv})
	if _, err := callErr(b.collabJoin, map[string]any{"invite": inv}); err == nil || !strings.Contains(err.Error(), "already used") {
		t.Fatalf("invite reused: %v", err)
	}
	if _, err := b.store.Discussion(disc.Key); err == nil {
		t.Fatal("failed join left a discussion behind")
	}
	// 过期的邀请
	secret := collaboration.NewSecret()
	if err := h.store.CreateInvite(disc.Key, "inv_old", collaboration.HashSecret(secret), "", time.Now().Add(-time.Minute), time.Now().Add(-11*time.Minute)); err != nil {
		t.Fatal(err)
	}
	parsed, _ := collaboration.ParseInvite(inv)
	parsed.Secret = secret
	if _, err := callErr(b.collabJoin, map[string]any{"invite": parsed.Encode()}); err == nil || !strings.Contains(err.Error(), "expired") {
		t.Fatalf("expired invite: %v", err)
	}
	// 还没批准的人不能读：hello 被拒
	id, _ := collaboration.LoadOrCreate(a.cfg.Home)
	cli, err := collaboration.Dial(context.Background(), id, h.store.Self(), []string{h.collabServer().Addr().String()}, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer cli.Close()
	if err := cli.Call(context.Background(), "hello", collabHello{Room: disc.Key, Credential: "x", Version: 1}, nil); err == nil {
		t.Fatal("pending applicant said hello")
	}
	// 批准后的成员拿 A 房间的凭据进不了 B 房间
	reqs, _ := h.store.JoinRequests(disc.Key, true)
	call(t, h.collabDecide, map[string]any{"request": reqs[0].ID, "approve": true})
	waitFor(t, "approved", func() bool { j, _ := a.store.Joined(disc.Key); return j.State == store.JoinedActive })
	cred, _ := a.loadCredential(disc.Key)
	if err := cli.Call(context.Background(), "hello", collabHello{Room: other.Key, Credential: cred.Credential, Version: 1}, nil); err == nil {
		t.Fatal("credential opened another room")
	}
	// 错的房主指纹：握手失败
	if _, err := collaboration.Dial(context.Background(), id, strings.Repeat("0", 64), []string{h.collabServer().Addr().String()}, nil); err == nil {
		t.Fatal("wrong host fingerprint accepted")
	}
}

// 成员在执行中提问，只有拥有者能在自己电脑上回答；断开时已领取的轮次暂停。
func TestCollabQuestionAndDisconnect(t *testing.T) {
	p := newCollabPair(t)
	h, m := p.host, p.member
	bob := call(t, m.discMemberAdd, map[string]any{"discussion": p.disc.Key, "name": "Bob", "agent": "codex", "workspace": p.mw.ID}).(store.Member)
	r := call(t, m.discPost, map[string]any{"discussion": p.disc.Key, "body": "q?", "target": bob.ID, "clientId": "c-1"}).(discPostResult)
	var x store.Execution
	waitFor(t, "running", func() bool {
		m.dispatchAll()
		x, _ = m.store.Execution(r.Execution.ID)
		return x.State == store.ExecRunning
	})
	got := make(chan error, 1)
	go func() {
		_, err := callErr(m.taskAsk, map[string]any{"task": x.TaskID, "caller": x.Worker, "execution": x.ID, "question": "which branch?", "timeoutMs": 8000,
			"choices": []string{"main", "dev"}})
		got <- err
	}()
	var q store.DiscMessage
	waitFor(t, "question on host", func() bool {
		msgs, _ := h.store.DiscMessages(p.disc.Key, 0, 100)
		for _, mm := range msgs {
			if mm.Kind == store.DiscQuestion {
				q = mm
				return true
			}
		}
		return false
	})
	// 选项随问题事件交给房主，再同步回成员的缓存
	if !slices.Equal(q.Choices, []string{"main", "dev"}) {
		t.Fatalf("choices on host %q", q.Choices)
	}
	waitFor(t, "choices in the member's cache", func() bool {
		mq, err := m.store.QuestionByTaskMessage(q.Question)
		return err == nil && slices.Equal(mq.Choices, []string{"main", "dev"})
	})
	if _, err := callErr(h.discAnswer, map[string]any{"question": q.Question, "body": "main"}); err == nil || !strings.Contains(err.Error(), "owner") {
		t.Fatalf("host answered the member's question: %v", err)
	}
	call(t, m.discAnswer, map[string]any{"question": q.Question, "body": "main"})
	if err := <-got; err != nil {
		t.Fatal(err)
	}
	// 成员的 Agent 写的摘要交给房主，再同步回成员的缓存；只有房主能清
	readAll(t, m, x)
	call(t, m.discSummarize, map[string]any{"execution": x.ID, "caller": x.Worker, "body": "use main"})
	waitFor(t, "summary on host and back", func() bool {
		hd, _ := h.store.Discussion(p.disc.Key)
		md, _ := m.store.Discussion(p.disc.Key)
		return hd.Summary == "use main" && hd.SummaryBy == "Bob" && md.Summary == "use main" && md.SummarySeq == hd.SummarySeq && md.SummarySeq > 0
	})
	if _, err := callErr(m.discSummaryClear, map[string]any{"discussion": p.disc.Key}); err == nil {
		t.Fatal("a member cleared the host's summary")
	}
	waitFor(t, "answer on host", func() bool {
		msgs, _ := h.store.DiscMessages(p.disc.Key, 0, 100)
		for _, mm := range msgs {
			if mm.Kind == store.DiscAnswer && mm.AuthorID == m.store.Self() {
				return true
			}
		}
		return false
	})

	// 第二个请求排在后面；房主关共享 → 成员断开，已领取没开始的那一轮暂停，等确认
	r2 := call(t, m.discPost, map[string]any{"discussion": p.disc.Key, "body": "next", "target": bob.ID, "clientId": "c-2"}).(discPostResult)
	waitFor(t, "claimed", func() bool { return m.store.AcceptState(r2.Execution.ID) == store.AcceptAccepted })
	call(t, h.collabShare, map[string]any{"discussion": p.disc.Key, "on": false})
	waitFor(t, "disconnected", func() bool { return !m.roomStatus(p.disc.Key).Connected })
	call(t, m.taskDone, map[string]any{"task": x.TaskID, "caller": x.Worker, "execution": x.ID, "body": "done offline"})
	waitFor(t, "queue paused", func() bool {
		mb, _ := m.store.Member(bob.ID)
		return mb.Paused == collabLostReason
	})
	if pend, _ := m.store.OutboxCount(p.disc.Key); pend == 0 {
		t.Fatal("offline result should wait in the outbox")
	}
	// 重新打开共享：结果补交上去，排着的那一轮等确认
	call(t, h.collabShare, map[string]any{"discussion": p.disc.Key, "on": true})
	m.roomConnFor(p.disc.Key).poke()
	waitFor(t, "result delivered", func() bool {
		hx, _ := h.store.Execution(x.ID)
		return hx.State == store.ExecDone
	})
	m.dispatchAll()
	if x2, _ := m.store.Execution(r2.Execution.ID); x2.State != store.ExecQueued {
		t.Fatalf("second round started without confirmation: %s", x2.State)
	}
	m.setAgent(m.liveSession(x.Worker), term.AgentState{Status: agent.StatusIdle})
	call(t, m.collabResume, map[string]any{"member": bob.ID})
	waitFor(t, "second round runs", func() bool {
		m.dispatchAll()
		x2, _ := m.store.Execution(r2.Execution.ID)
		return x2.State == store.ExecRunning
	})
}

// 房主是不认选项的旧版（hello 没报这项能力）：选项写进问题正文交过去，事件里不带新字段。
func TestCollabChoicesOldHost(t *testing.T) {
	p := newCollabPair(t)
	h, m := p.host, p.member
	bob := call(t, m.discMemberAdd, map[string]any{"discussion": p.disc.Key, "name": "Bob", "agent": "codex", "workspace": p.mw.ID}).(store.Member)
	r := call(t, m.discPost, map[string]any{"discussion": p.disc.Key, "body": "q?", "target": bob.ID, "clientId": "c-1"}).(discPostResult)
	var x store.Execution
	waitFor(t, "running", func() bool {
		m.dispatchAll()
		x, _ = m.store.Execution(r.Execution.ID)
		return x.State == store.ExecRunning
	})
	if err := m.store.SetHostFeatures(p.disc.Key, nil); err != nil {
		t.Fatal(err)
	}
	go callErr(m.taskAsk, map[string]any{"task": x.TaskID, "caller": x.Worker, "execution": x.ID, "question": "which branch?", "timeoutMs": 2000,
		"choices": []string{"main", "dev"}})
	waitFor(t, "question on host", func() bool {
		msgs, _ := h.store.DiscMessages(p.disc.Key, 0, 100)
		for _, mm := range msgs {
			if mm.Kind == store.DiscQuestion {
				if len(mm.Choices) != 0 || mm.Body != "which branch?\n\nOptions:\n1. main\n2. dev" {
					t.Errorf("question for an old host: %q %q", mm.Body, mm.Choices)
				}
				return true
			}
		}
		return false
	})
}

// 跨电脑的成员 @ 成员：成员电脑上的 Bob @ 同一台电脑上的 Carol（照自己发的请求领取），再 @ 房主放开的 Alice（房主执行）；
// 没放开的 @ 不了；房主是旧版（没报这项能力）时拒绝，不把它发成这个人自己的请求。
func TestCollabMemberMention(t *testing.T) {
	p := newCollabPair(t)
	h, m := p.host, p.member
	bob := call(t, m.discMemberAdd, map[string]any{"discussion": p.disc.Key, "name": "Bob", "agent": "codex", "workspace": p.mw.ID}).(store.Member)
	carol := call(t, m.discMemberAdd, map[string]any{"discussion": p.disc.Key, "name": "Carol", "agent": "claude", "workspace": p.mw.ID}).(store.Member)
	alice := addMember(t, h, p.disc, "Alice", "claude")
	waitFor(t, "member sees Alice", func() bool { _, err := m.store.Member(alice.ID); return err == nil })
	r := call(t, m.discPost, map[string]any{"discussion": p.disc.Key, "body": "start", "target": bob.ID, "clientId": "c-1"}).(discPostResult)
	var x store.Execution
	waitFor(t, "Bob running", func() bool {
		m.dispatchAll()
		x, _ = m.store.Execution(r.Execution.ID)
		return x.State == store.ExecRunning
	})
	readAll(t, m, x)
	waitFor(t, "host sees Bob running", func() bool { hx, _ := h.store.Execution(x.ID); return hx.State == store.ExecRunning })
	mention := func(to, body string) (discPostResult, error) {
		v, err := callErr(m.discMention, map[string]any{"execution": x.ID, "caller": x.Worker, "to": to, "body": body})
		if err != nil {
			return discPostResult{}, err
		}
		return v.(discPostResult), nil
	}
	if _, err := mention("Alice", "hi"); err == nil {
		t.Fatal("Bob mentioned the host's agent before it was opened to his owner")
	}
	m.store.SetHostFeatures(p.disc.Key, []string{store.FeatureChoices})
	if _, err := mention("Carol", "hi"); err == nil || !strings.Contains(err.Error(), "older") {
		t.Fatalf("mention with an old host: %v", err)
	}
	m.store.SetHostFeatures(p.disc.Key, store.HostFeatures)
	got, err := mention("Carol", "take the tests")
	if err != nil {
		t.Fatal(err)
	}
	hm, err := h.store.DiscMessage(got.Message.ID)
	if err != nil || hm.Kind != store.DiscDelegate || hm.AuthorMember != bob.ID || hm.AuthorID != m.store.Self() || hm.Target != carol.ID {
		t.Fatalf("host message %+v %v", hm, err)
	}
	// Carol 在成员电脑上：照自己发的请求一样核对意图后领取，同目录时等 Bob（这里直接确认）
	waitFor(t, "Carol claimed", func() bool { return m.store.AcceptState(got.Execution.ID) == store.AcceptAccepted })
	waitFor(t, "Carol runs", func() bool {
		m.dispatchAll()
		cx, _ := m.store.Execution(got.Execution.ID)
		if cx.State == store.ExecHeld {
			callErr(m.discConfirm, map[string]any{"execution": cx.ID})
		}
		return cx.State == store.ExecRunning
	})
	cx, _ := m.store.Execution(got.Execution.ID)
	if cx.Depth != 1 || cx.Root != x.ID {
		t.Fatalf("Carol's round %+v", cx)
	}
	if text := readAll(t, m, cx); !strings.Contains(text, "comes from the member Bob") {
		t.Fatalf("Carol's context:\n%s", text)
	}
	// 房主把 Alice 放开给所有人：Bob 能 @ 她，在房主电脑上执行
	call(t, h.discMemberUpdate, map[string]any{"member": alice.ID, "access": "all"})
	waitFor(t, "member sees Alice opened", func() bool { ma, _ := m.store.Member(alice.ID); return ma.Access == store.AccessAll })
	ga, err := mention("Alice", "review please")
	if err != nil {
		t.Fatal(err)
	}
	waitFor(t, "Alice runs on the host", func() bool {
		h.dispatchAll()
		ax, _ := h.store.Execution(ga.Execution.ID)
		return ax.State == store.ExecRunning && ax.Depth == 1
	})
}

// 只读访客：用访客邀请加入的人能同步消息，不能发言、标关键、带 Agent、传附件；房主直接调写接口也被拒。
func TestCollabGuest(t *testing.T) {
	t.Setenv("POSSE_COLLAB_ADDRS", "127.0.0.1")
	h, _, disc := newDiscDaemon(t)
	m, mw := newTestDaemon(t)
	fakeLaunch(m)
	startCollab(t, h)
	startCollab(t, m)
	call(t, h.collabShare, map[string]any{"discussion": disc.Key, "on": true})
	inv := call(t, h.collabInvite, map[string]any{"discussion": disc.Key, "guest": true}).(map[string]any)
	call(t, m.collabJoin, map[string]any{"invite": inv["invite"]})
	var reqs []store.JoinRequest
	waitFor(t, "join request", func() bool {
		reqs, _ = h.store.JoinRequests(disc.Key, true)
		return len(reqs) == 1
	})
	if reqs[0].Role != store.RoleGuest {
		t.Fatalf("request role %q", reqs[0].Role)
	}
	call(t, h.collabDecide, map[string]any{"request": reqs[0].ID, "approve": true})
	waitFor(t, "guest connected", func() bool { return m.roomStatus(disc.Key).Connected })
	// 读得到
	hp := postTo(t, h, disc, "hello viewers", "", "")
	waitFor(t, "guest syncs", func() bool { _, err := m.store.DiscMessage(hp.Message.ID); return err == nil })
	waitFor(t, "guest sees its own role", func() bool { return m.guestIn(disc.Key) })
	// 写不了：本机先挡
	for name, try := range map[string]func() error{
		"post": func() error {
			_, err := callErr(m.discPost, map[string]any{"discussion": disc.Key, "body": "hi", "clientId": "c-1"})
			return err
		},
		"add agent": func() error {
			_, err := callErr(m.discMemberAdd, map[string]any{"discussion": disc.Key, "name": "Bob", "agent": "codex", "workspace": mw.ID})
			return err
		},
	} {
		if err := try(); err == nil || !strings.Contains(err.Error(), "viewer") {
			t.Fatalf("guest %s: %v", name, err)
		}
	}
	// 绕过本机检查直接调房主：房主也拒
	rc, err := m.mirrorConn(disc)
	if err != nil {
		t.Fatal(err)
	}
	for _, method := range []string{"post", "key", "member.add", "upload.begin", "claim"} {
		err := rc.call(method, map[string]any{"clientId": "c-9", "body": "x", "message": hp.Message.ID, "on": true, "name": "f", "size": 1}, nil)
		if err == nil || !strings.Contains(err.Error(), "viewer") {
			t.Fatalf("host accepted %s from a guest: %v", method, err)
		}
	}
	if msgs, _ := h.store.DiscMessages(disc.Key, 0, 10); len(msgs) != 1 {
		t.Fatalf("%d messages on the host", len(msgs))
	}
	// 普通邀请重新加入：成为普通成员，能发言
	call(t, h.collabRemovePerson, map[string]any{"discussion": disc.Key, "person": m.store.Self()})
	waitFor(t, "guest removed", func() bool { j, _ := m.store.Joined(disc.Key); return j.State != store.JoinedActive })
	call(t, m.collabJoin, map[string]any{"invite": invite(t, h, disc)})
	waitFor(t, "second request", func() bool {
		reqs, _ = h.store.JoinRequests(disc.Key, true)
		return len(reqs) == 1
	})
	call(t, h.collabDecide, map[string]any{"request": reqs[0].ID, "approve": true})
	waitFor(t, "member can post", func() bool {
		if !m.roomStatus(disc.Key).Connected || m.guestIn(disc.Key) {
			return false
		}
		_, err := callErr(m.discPost, map[string]any{"discussion": disc.Key, "body": "now I can talk", "clientId": "c-2"})
		return err == nil
	})
}

// 附件：成员分块上传、房主校验后发布；房主的附件按 ID 下载到成员的缓存；路径穿越的名字被收拾掉。
func TestCollabAttachments(t *testing.T) {
	p := newCollabPair(t)
	h, m := p.host, p.member
	src := filepath.Join(t.TempDir(), "../evil name.txt")
	big := strings.Repeat("0123456789abcdef", 10000) // 160 KB：三块
	if err := os.WriteFile(src, []byte(big), 0o600); err != nil {
		t.Fatal(err)
	}
	r := call(t, m.discPost, map[string]any{"discussion": p.disc.Key, "body": "see file", "clientId": "c-a",
		"attachments": []map[string]string{{"path": src, "name": "../../etc/passwd"}}}).(discPostResult)
	hm, _ := h.store.DiscMessage(r.Message.ID)
	if len(hm.Attachments) != 1 {
		t.Fatalf("host attachments %+v", hm.Attachments)
	}
	ha := hm.Attachments[0]
	if strings.Contains(ha.Path, "..") || !strings.HasPrefix(ha.Path, store.AttachmentDir(p.disc.LocalID)) {
		t.Fatalf("host path %s", ha.Path)
	}
	b, err := os.ReadFile(filepath.Join(p.hw.Path, ha.Path))
	if err != nil || string(b) != big {
		t.Fatalf("host copy: %v", err)
	}
	// 房主发的附件到成员的缓存
	hsrc := filepath.Join(t.TempDir(), "host.txt")
	os.WriteFile(hsrc, []byte("from host"), 0o600)
	hr := call(t, h.discPost, map[string]any{"discussion": p.disc.Key, "body": "host file", "attachments": []map[string]string{{"path": hsrc}}}).(discPostResult)
	waitFor(t, "member downloads", func() bool {
		mm, err := m.store.DiscMessage(hr.Message.ID)
		if err != nil || len(mm.Attachments) == 0 {
			return false
		}
		b, err := os.ReadFile(filepath.Join(m.cfg.Home, mm.Attachments[0].Path))
		return err == nil && string(b) == "from host"
	})
	mm, _ := m.store.DiscMessage(hr.Message.ID)
	if !strings.HasPrefix(mm.Attachments[0].Path, filepath.Join("collab", store.RoomDir(p.disc.Key), "attachments")) {
		t.Fatalf("member cache path %s", mm.Attachments[0].Path)
	}
	// 配额满了拒绝，草稿（本机文件）不受影响
	h.store.SetCollabQuota(10)
	if _, err := callErr(m.discPost, map[string]any{"discussion": p.disc.Key, "body": "again", "clientId": "c-b",
		"attachments": []map[string]string{{"path": hsrc}}}); err == nil || !strings.Contains(err.Error(), "quota") {
		t.Fatalf("quota: %v", err)
	}
}

// 非房主不能归档；有成员电脑上没结束的请求时房主也不能归档。
func TestCollabArchiveRules(t *testing.T) {
	p := newCollabPair(t)
	h, m := p.host, p.member
	if _, err := callErr(m.discArchive, map[string]any{"discussion": p.disc.Key, "archived": true}); err == nil {
		t.Fatal("member archived the host's discussion")
	}
	bob := call(t, m.discMemberAdd, map[string]any{"discussion": p.disc.Key, "name": "Bob", "agent": "codex", "workspace": p.mw.ID}).(store.Member)
	r := call(t, m.discPost, map[string]any{"discussion": p.disc.Key, "body": "x", "target": bob.ID, "clientId": "c-1"}).(discPostResult)
	waitFor(t, "claimed", func() bool { hx, _ := h.store.Execution(r.Execution.ID); return hx.State != store.ExecQueued })
	if _, err := callErr(h.discArchive, map[string]any{"discussion": p.disc.Key, "archived": true}); err == nil {
		t.Fatal("archived with a member's request in flight")
	}
	// 移除成员后他的执行记访问已撤销，不再挡归档
	call(t, h.collabRemovePerson, map[string]any{"discussion": p.disc.Key, "person": m.store.Self()})
	call(t, h.discArchive, map[string]any{"discussion": p.disc.Key, "archived": true})
}

// 访问设置：拥有者放开给某人后，那个人能 @ 他的 Agent，在拥有者电脑上执行；房主改了访问设置（冒充放开），
// 拥有者按本机确认过的那份核对，不领取。房主自己的 Agent 放开给所有人，成员也能 @ 它。
func TestCollabAgentAccess(t *testing.T) {
	p := newCollabPair(t)
	h, m := p.host, p.member
	hostFP, memberFP := h.store.Self(), m.store.Self()
	bob := call(t, m.discMemberAdd, map[string]any{"discussion": p.disc.Key, "name": "Bob", "agent": "codex", "workspace": p.mw.ID}).(store.Member)
	if _, err := callErr(h.discPost, map[string]any{"discussion": p.disc.Key, "body": "hi", "target": bob.ID}); err == nil {
		t.Fatal("host invoked the member's agent before it was allowed")
	}

	// 成员把 Bob 放开给房主
	up := call(t, m.discMemberUpdate, map[string]any{"member": bob.ID, "access": hostFP}).(store.Member)
	if up.Access != hostFP {
		t.Fatalf("member's copy access %q", up.Access)
	}
	waitFor(t, "host sees access", func() bool {
		hb, _ := h.store.Member(bob.ID)
		return hb.Access == hostFP
	})
	r := call(t, h.discPost, map[string]any{"discussion": p.disc.Key, "body": "please check", "target": bob.ID}).(discPostResult)
	if r.Execution == nil || r.Execution.Owner != memberFP {
		t.Fatalf("execution %+v", r.Execution)
	}
	waitFor(t, "member runs the host's request", func() bool {
		m.dispatchAll()
		x, _ := m.store.Execution(r.Execution.ID)
		return x.State == store.ExecRunning
	})
	// 房主仍然不能停止、重试、配置它
	if _, err := callErr(h.discStop, map[string]any{"execution": r.Execution.ID}); err == nil {
		t.Fatal("host stopped the member's agent")
	}
	x, _ := m.store.Execution(r.Execution.ID)
	if ctxText := readAll(t, m, x); !strings.Contains(ctxText, "please check") {
		t.Fatalf("context:\n%s", ctxText)
	}
	call(t, m.taskDone, map[string]any{"task": x.TaskID, "caller": x.Worker, "execution": x.ID, "body": "checked"})
	waitFor(t, "reply on host", func() bool {
		hx, _ := h.store.Execution(x.ID)
		return hx.State == store.ExecDone
	})

	// 成员收回；房主在自己的库里把访问改回放开（冒充拥有者的修改）：房主收下请求，拥有者不领取
	call(t, m.discMemberUpdate, map[string]any{"member": bob.ID, "access": "owner"})
	waitFor(t, "host sees access revoked", func() bool {
		hb, _ := h.store.Member(bob.ID)
		return hb.Access == ""
	})
	if _, err := callErr(h.discPost, map[string]any{"discussion": p.disc.Key, "body": "again", "target": bob.ID}); err == nil {
		t.Fatal("host invoked the member's agent after access was revoked")
	}
	person, _ := h.store.Person(p.disc.Key, memberFP)
	hb, _ := h.store.Member(bob.ID)
	hb.Access = hostFP
	if _, err := h.store.OwnerMemberUpdate(p.disc.Key, memberFP, person.Generation, hb); err != nil {
		t.Fatal(err)
	}
	fr := call(t, h.discPost, map[string]any{"discussion": p.disc.Key, "body": "forged access", "target": bob.ID}).(discPostResult)
	h.taskNotify()
	waitFor(t, "member refuses forged access", func() bool {
		return m.store.AcceptState(fr.Execution.ID) == store.AcceptRefused
	})
	m.dispatchAll()
	if x, _ := m.store.Execution(fr.Execution.ID); x.TaskID != "" {
		t.Fatalf("forged-access request ran: %+v", x)
	}

	// 房主自己的 Agent 放开给所有人：成员 @ 它，房主执行
	alice := addMember(t, h, p.disc, "Alice", "claude")
	if _, err := callErr(m.discPost, map[string]any{"discussion": p.disc.Key, "body": "hi", "target": alice.ID, "clientId": "c-a0"}); err == nil {
		t.Fatal("member invoked the host's agent before it was allowed")
	}
	call(t, h.discMemberUpdate, map[string]any{"member": alice.ID, "access": "all"})
	waitFor(t, "member sees host agent access", func() bool {
		ma, _ := m.store.Member(alice.ID)
		return ma.Access == store.AccessAll
	})
	ra := call(t, m.discPost, map[string]any{"discussion": p.disc.Key, "body": "alice, look", "target": alice.ID, "clientId": "c-a1"}).(discPostResult)
	if ra.Execution == nil {
		t.Fatal("no execution for the host's agent")
	}
	if n, _ := m.store.Intent(p.disc.Key, "c-a1"); n.Member != "" {
		t.Fatalf("intent saved for another person's agent: %+v", n)
	}
	waitFor(t, "host runs the member's request", func() bool {
		h.dispatchAll()
		x, _ := h.store.Execution(ra.Execution.ID)
		return x.State == store.ExecRunning
	})
}
