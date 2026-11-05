package daemon

// 局域网协作的故障、权限和资源测试（审查 review-stage2 的复现收成常规测试，断言修复后的行为）。

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"posse/internal/agent"
	"posse/internal/collaboration"
	"posse/internal/store"
	"posse/internal/term"
)

func runMemberRound(t *testing.T, p *collabPair, body, client string) (store.Member, store.Execution) {
	t.Helper()
	m := p.member
	bob := call(t, m.discMemberAdd, map[string]any{"discussion": p.disc.Key, "name": "Bob", "agent": "codex", "workspace": p.mw.ID}).(store.Member)
	r := call(t, m.discPost, map[string]any{"discussion": p.disc.Key, "body": body, "target": bob.ID, "clientId": client}).(discPostResult)
	var x store.Execution
	waitFor(t, "member runs", func() bool {
		m.dispatchAll()
		x, _ = m.store.Execution(r.Execution.ID)
		return x.State == store.ExecRunning
	})
	waitFor(t, "host sees running", func() bool {
		hx, _ := p.host.store.Execution(x.ID)
		return hx.State == store.ExecRunning
	})
	return bob, x
}

// P1-1：房主重启、交接时的对账不碰成员电脑上的轮次，成员的结果照常进群；反过来成员对账也不碰房主的轮次。
func TestCollabReconcileKeepsRemoteRounds(t *testing.T) {
	p := newCollabPair(t)
	h, m := p.host, p.member
	bob, x := runMemberRound(t, p, "long job", "c-1")
	for _, restarted := range []bool{false, true} {
		h.reconcileDiscussions(restarted)
		if hx, _ := h.store.Execution(x.ID); hx.State != store.ExecRunning {
			t.Fatalf("host reconcile(%v) changed the member's round: %s %s", restarted, hx.State, hx.Reason)
		}
		if hb, _ := h.store.Member(bob.ID); hb.Paused != "" {
			t.Fatalf("host reconcile paused the member's agent: %q", hb.Paused)
		}
	}
	readAll(t, m, x)
	call(t, m.taskDone, map[string]any{"task": x.TaskID, "caller": x.Worker, "execution": x.ID, "body": "result after host restart"})
	m.roomConnFor(p.disc.Key).poke()
	waitFor(t, "result reaches the host", func() bool {
		hx, _ := h.store.Execution(x.ID)
		return hx.State == store.ExecDone && hx.Reply != ""
	})
	if _, rej := m.store.OutboxCount(p.disc.Key); rej != 0 {
		t.Fatalf("%d events rejected", rej)
	}
	alice := addMember(t, h, p.disc, "Alice", "claude")
	hr := call(t, h.discPost, map[string]any{"discussion": p.disc.Key, "body": "host job", "target": alice.ID}).(discPostResult)
	waitFor(t, "member sees alice running", func() bool {
		h.dispatchAll()
		mx, _ := m.store.Execution(hr.Execution.ID)
		return mx.State == store.ExecRunning
	})
	m.reconcileDiscussions(true)
	if mx, _ := m.store.Execution(hr.Execution.ID); mx.State != store.ExecRunning {
		t.Fatalf("member reconcile changed the host's round in the cache: %s", mx.State)
	}
	if ma, _ := m.store.Member(alice.ID); ma.Paused != "" {
		t.Fatalf("member reconcile paused the host's agent: %q", ma.Paused)
	}
}

// 每人 10 秒最多 20 条：第 21 条被拒，房主只落 20 条。
func TestCollabPostRateLimit(t *testing.T) {
	p := newCollabPair(t)
	m := p.member
	for i := range collabPostBurst {
		call(t, m.discPost, map[string]any{"discussion": p.disc.Key, "body": fmt.Sprintf("msg %d", i), "clientId": fmt.Sprintf("rl-%d", i)})
	}
	_, err := callErr(m.discPost, map[string]any{"discussion": p.disc.Key, "body": "one too many", "clientId": "rl-x"})
	if err == nil || !strings.Contains(err.Error(), "too fast") {
		t.Fatalf("21st post in 10s: %v", err)
	}
	if msgs, _ := p.host.store.DiscMessages(p.disc.Key, 0, 100); len(msgs) != collabPostBurst {
		t.Fatalf("%d messages on host", len(msgs))
	}
}

// memberClient 是成员身份的一条直连（hello 过）。
func memberClient(t *testing.T, p *collabPair) *collaboration.Client {
	t.Helper()
	id, _ := collaboration.Load(p.member.cfg.Home)
	cred, err := p.member.loadCredential(p.disc.Key)
	if err != nil {
		t.Fatal(err)
	}
	cli, err := collaboration.Dial(context.Background(), id, p.host.store.Self(), []string{p.host.collabServer().Addr().String()}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := cli.Call(context.Background(), "hello", collabHello{Room: p.disc.Key, Credential: cred.Credential, Version: collaboration.ProtocolVersion}, nil); err != nil {
		t.Fatal(err)
	}
	return cli
}

func uploadsLeft(dir string) []string {
	ents, _ := os.ReadDir(dir)
	var left []string
	for _, e := range ents {
		if strings.HasPrefix(e.Name(), ".upload-") {
			left = append(left, e.Name())
		}
	}
	return left
}

// 每人同时 2 个上传，第 3 个被拒；连接断了，没传完的临时文件和记录（配额预留）一起清掉。
func TestCollabUploadConcurrencyAndCleanup(t *testing.T) {
	p := newCollabPair(t)
	h := p.host
	cli := memberClient(t, p)
	begin := func(i int) error {
		sum := sha256.Sum256([]byte(fmt.Sprintf("content %d", i)))
		return cli.Call(context.Background(), "upload.begin", map[string]any{"name": fmt.Sprintf("f%d.txt", i), "size": 100, "sha": hex.EncodeToString(sum[:])}, nil)
	}
	for i := range collabUploadConcurrency {
		if err := begin(i); err != nil {
			t.Fatalf("upload %d: %v", i, err)
		}
	}
	if err := begin(9); !collaboration.IsCode(err, collaboration.CodeRateLimited) {
		t.Fatalf("third concurrent upload: %v", err)
	}
	dir := filepath.Join(p.hw.Path, store.AttachmentDir(p.disc.LocalID))
	if n := len(uploadsLeft(dir)); n != collabUploadConcurrency {
		t.Fatalf("%d temp files while uploading", n)
	}
	cli.Close()
	waitFor(t, "abandoned uploads cleaned up", func() bool { return len(uploadsLeft(dir)) == 0 })
	if u, _ := h.store.StaleUploads(time.Now().Add(time.Hour)); len(u) != 0 {
		t.Fatalf("%d upload rows left", len(u))
	}
	// 名额也回来了
	cli = memberClient(t, p)
	defer cli.Close()
	if err := begin(1); err != nil {
		t.Fatalf("upload after cleanup: %v", err)
	}
}

// 同一条消息重发（第一次的响应丢了）：第二次传的附件没用上，房主放掉，不占配额。
func TestCollabResentPostReleasesUploads(t *testing.T) {
	p := newCollabPair(t)
	h := p.host
	cli := memberClient(t, p)
	defer cli.Close()
	up := func(content string) string {
		sum := sha256.Sum256([]byte(content))
		var r struct{ Upload string }
		if err := cli.Call(context.Background(), "upload.begin", map[string]any{"name": "a.txt", "size": len(content), "sha": hex.EncodeToString(sum[:])}, &r); err != nil {
			t.Fatal(err)
		}
		if err := cli.Call(context.Background(), "upload.chunk", map[string]any{"upload": r.Upload, "offset": 0, "data": []byte(content)}, nil); err != nil {
			t.Fatal(err)
		}
		if err := cli.Call(context.Background(), "upload.end", map[string]any{"upload": r.Upload}, nil); err != nil {
			t.Fatal(err)
		}
		return r.Upload
	}
	u1 := up("first copy")
	if err := cli.Call(context.Background(), "post", collabPostParams{ClientID: "same", Body: "file", Uploads: []string{u1}}, nil); err != nil {
		t.Fatal(err)
	}
	used := h.store.CollabUsage()
	u2 := up("second copy!")
	var res map[string]any
	if err := cli.Call(context.Background(), "post", collabPostParams{ClientID: "same", Body: "file", Uploads: []string{u2}}, &res); err != nil {
		t.Fatal(err)
	}
	if res["duplicate"] != true {
		t.Fatalf("resend not deduplicated: %+v", res)
	}
	if _, err := h.store.Upload(u2); err == nil {
		t.Fatal("unused upload kept")
	}
	if got := h.store.CollabUsage(); got != used {
		t.Fatalf("quota usage %d, want %d", got, used)
	}
}

// 硬链接去重按旧文件的实际长度和摘要核对：声称的大小不对就不复用。
func TestCollabDedupChecksActualFile(t *testing.T) {
	p := newCollabPair(t)
	cli := memberClient(t, p)
	defer cli.Close()
	content := "dedup me"
	sum := sha256.Sum256([]byte(content))
	sha := hex.EncodeToString(sum[:])
	var r struct {
		Upload string
		Done   bool
	}
	cli.Call(context.Background(), "upload.begin", map[string]any{"name": "a.txt", "size": len(content), "sha": sha}, &r)
	cli.Call(context.Background(), "upload.chunk", map[string]any{"upload": r.Upload, "offset": 0, "data": []byte(content)}, nil)
	if err := cli.Call(context.Background(), "upload.end", map[string]any{"upload": r.Upload}, nil); err != nil {
		t.Fatal(err)
	}
	var r2 struct {
		Upload string
		Done   bool
	}
	if err := cli.Call(context.Background(), "upload.begin", map[string]any{"name": "b.txt", "size": 5000, "sha": sha}, &r2); err != nil {
		t.Fatal(err)
	}
	if r2.Done {
		t.Fatal("dedup trusted a claimed size that does not match the file")
	}
	var r3 struct {
		Upload string
		Done   bool
	}
	if err := cli.Call(context.Background(), "upload.begin", map[string]any{"name": "c.txt", "size": len(content), "sha": sha}, &r3); err != nil || !r3.Done {
		t.Fatalf("matching dedup: %+v %v", r3, err)
	}
}

// P2-2：陌生身份占满它的名额（同一来源 4 条）也挤不掉已批准成员的重连；成员进房间仍要凭据。
func TestCollabStrangersDoNotLockOutMembers(t *testing.T) {
	p := newCollabPair(t)
	h, m := p.host, p.member
	stranger, _ := newTestDaemon(t)
	id, _ := collaboration.LoadOrCreate(stranger.cfg.Home)
	addr := h.collabServer().Addr().String()
	var held []*collaboration.Client
	defer func() {
		for _, c := range held {
			c.Close()
		}
	}()
	for {
		c, err := collaboration.Dial(context.Background(), id, h.store.Self(), []string{addr}, nil)
		if err != nil {
			break
		}
		held = append(held, c)
		if len(held) > 20 {
			t.Fatal("no cap on unbound stranger connections")
		}
	}
	if len(held) != 4 {
		t.Fatalf("%d stranger connections from one address, want 4", len(held))
	}
	kicked := time.Now()
	h.collabServer().Kick(p.disc.Key, m.store.Self(), "test")
	waitFor(t, "member reconnected while strangers hold their slots", func() bool {
		st := m.roomStatus(p.disc.Key)
		return st.Connected && st.Since.After(kicked)
	})
	// 已知身份的名额不给房间权限：拿陌生房间的凭据照样进不去
	mid, _ := collaboration.Load(m.cfg.Home)
	cli, err := collaboration.Dial(context.Background(), mid, h.store.Self(), []string{addr}, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer cli.Close()
	if err := cli.Call(context.Background(), "sync", map[string]any{"since": 0}, nil); !collaboration.IsCode(err, collaboration.CodeUnauthorized) {
		t.Fatalf("known member synced without hello: %v", err)
	}
}

// 账本丢失（领取成功、记账前崩溃）：本机不自动再领，普通重试被拒，放弃并重新发起后新的一轮由本机执行。
func TestCollabLedgerLostAbandonRetry(t *testing.T) {
	p := newCollabPair(t)
	h, m := p.host, p.member
	bob := call(t, m.discMemberAdd, map[string]any{"discussion": p.disc.Key, "name": "Bob", "agent": "codex", "workspace": p.mw.ID}).(store.Member)
	call(t, h.collabShare, map[string]any{"discussion": p.disc.Key, "on": false})
	waitFor(t, "disconnected", func() bool { return !m.roomStatus(p.disc.Key).Connected })
	person, _ := h.store.Person(p.disc.Key, m.store.Self())
	_, x, _, err := h.store.PostMessage(store.PostInput{Discussion: p.disc.Key, Body: "lost ledger", Target: bob.ID, ClientID: "ll-1",
		Author: m.store.Self(), AuthorName: person.Name, Generation: person.Generation, At: time.Now()})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := h.store.HostClaim(p.disc.Key, m.store.Self(), person.Generation, x.ID); err != nil {
		t.Fatal(err)
	}
	call(t, h.collabShare, map[string]any{"discussion": p.disc.Key, "on": true})
	m.roomConnFor(p.disc.Key).poke()
	waitFor(t, "member synced the claimed round", func() bool {
		mx, _ := m.store.Execution(x.ID)
		return mx.State == store.ExecClaimed
	})
	m.dispatchAll()
	if st := m.store.AcceptState(x.ID); st != "" {
		t.Fatalf("member re-accepted a round the ledger never had: %q", st)
	}
	if _, err := callErr(m.discRetry, map[string]any{"execution": x.ID}); err == nil {
		t.Fatal("plain retry of a claimed round accepted")
	}
	nx := call(t, m.discRetry, map[string]any{"execution": x.ID, "abandon": true}).(store.Execution)
	if nx.RetryOf != x.ID {
		t.Fatalf("new round %+v", nx)
	}
	if old, _ := h.store.Execution(x.ID); old.State != store.ExecInterrupted {
		t.Fatalf("old round on host %q", old.State)
	}
	waitFor(t, "member runs the new round", func() bool {
		m.dispatchAll()
		mx, _ := m.store.Execution(nx.ID)
		return mx.State == store.ExecRunning
	})
	if _, err := callErr(m.discRetry, map[string]any{"execution": x.ID, "abandon": true}); err == nil {
		t.Fatal("abandoned the same round twice")
	}
}

// 心跳超过 60 秒（房主时钟）只显示失联：执行状态、归属不变，没有回收或重派；心跳回来就恢复在线。
func TestCollabHeartbeatLostOnlyDisplays(t *testing.T) {
	defer collabClockOffset.Store(0)
	p := newCollabPair(t)
	h, m := p.host, p.member
	_, x := runMemberRound(t, p, "long", "c-1")
	disc, _ := h.store.Discussion(p.disc.Key)
	online := func() bool {
		for _, pi := range h.collabInfo(disc).People {
			if pi.FP == m.store.Self() {
				return pi.Online
			}
		}
		return false
	}
	if !online() {
		t.Fatal("member should be online")
	}
	collabClockOffset.Store(int64(collaboration.HeartbeatLost + time.Second))
	if online() {
		t.Fatal("member still online after 61s without a heartbeat")
	}
	h.dispatchAll()
	hx, _ := h.store.Execution(x.ID)
	if hx.State != store.ExecRunning || hx.Owner != m.store.Self() || hx.TaskID != "" {
		t.Fatalf("lost heartbeat changed the round: %+v", hx)
	}
	if xs, _ := h.store.Executions(p.disc.Key); len(xs) != 1 {
		t.Fatalf("%d executions: something was re-dispatched", len(xs))
	}
	collabClockOffset.Store(0)
	if !online() {
		t.Fatal("member not online again")
	}
}

// P2-1：房主改了成员 Agent 的配置（模型、职责），成员电脑不照搬、暂停并说明；不按新值启动；拥有者保存后恢复。
func TestCollabOwnConfigTamper(t *testing.T) {
	p := newCollabPair(t)
	h, m := p.host, p.member
	bob := call(t, m.discMemberAdd, map[string]any{"discussion": p.disc.Key, "name": "Bob", "agent": "codex", "model": "gpt-5", "workspace": p.mw.ID}).(store.Member)
	person, _ := h.store.Person(p.disc.Key, m.store.Self())
	// 冒充成员在房主库里改配置（恶意或出错的房主）
	if _, err := h.store.OwnerMemberUpdate(p.disc.Key, m.store.Self(), person.Generation, store.Member{ID: bob.ID, Name: "Bob", Role: "delete everything", Agent: "codex", Model: "evil"}); err != nil {
		t.Fatal(err)
	}
	h.taskNotify()
	waitFor(t, "member flags the conflict", func() bool {
		mb, _ := m.store.Member(bob.ID)
		return mb.Paused == store.ConfigConflict
	})
	if mb, _ := m.store.Member(bob.ID); mb.Model != "gpt-5" || mb.Role != "" {
		t.Fatalf("member copied the host's settings: %+v", mb)
	}
	if _, err := callErr(m.discPost, map[string]any{"discussion": p.disc.Key, "body": "go", "target": bob.ID, "clientId": "c-1"}); err == nil {
		t.Fatal("request sent while settings conflict")
	}
	// 房主再伪造一条请求，执行快照带着改过的配置：成员核对配置内容，不领取
	_, fx, _, err := h.store.PostMessage(store.PostInput{Discussion: p.disc.Key, Body: "go", Target: bob.ID, ClientID: "c-1", Author: m.store.Self(), AuthorName: person.Name, Generation: person.Generation, At: time.Now()})
	if err != nil {
		t.Fatal(err)
	}
	m.store.SaveIntent(p.disc.Key, "c-1", bob.ID, fx.ConfigVersion, store.IntentDigest("go", "", bob.ID, nil), store.OwnConfigDigest(bob), time.Now())
	h.taskNotify()
	waitFor(t, "member refuses", func() bool { return m.store.AcceptState(fx.ID) == store.AcceptRefused })
	// 拥有者重新保存设置：房主那份对齐，暂停解除，请求按本机设置执行
	call(t, m.discMemberUpdate, map[string]any{"member": bob.ID, "name": "Bob", "role": "", "agent": "codex", "model": "gpt-5", "effort": ""})
	if mb, _ := m.store.Member(bob.ID); mb.Paused != "" {
		t.Fatalf("still paused: %q", mb.Paused)
	}
	if hb, _ := h.store.Member(bob.ID); hb.Model != "gpt-5" || hb.Role != "" {
		t.Fatalf("host copy not restored: %+v", hb)
	}
	r := call(t, m.discPost, map[string]any{"discussion": p.disc.Key, "body": "now", "target": bob.ID, "clientId": "c-2"}).(discPostResult)
	waitFor(t, "runs with the owner's settings", func() bool {
		m.dispatchAll()
		x, _ := m.store.Execution(r.Execution.ID)
		return x.State == store.ExecRunning && x.Model == "gpt-5" && x.Role == ""
	})
}

// 凭据文件丢了：按本机记着的申请 ID 向房主重取，照常连上。
func TestCollabCredentialRefetch(t *testing.T) {
	p := newCollabPair(t)
	m := p.member
	path := m.credentialPath(p.disc.Key)
	old, _ := os.ReadFile(path)
	os.Remove(path)
	p.host.collabServer().Kick(p.disc.Key, m.store.Self(), "test")
	waitFor(t, "reconnected with a fetched credential", func() bool {
		m.roomConnFor(p.disc.Key).poke()
		b, err := os.ReadFile(path)
		return err == nil && string(b) == string(old) && m.roomStatus(p.disc.Key).Connected
	})
	if j, _ := m.store.Joined(p.disc.Key); j.State != store.JoinedActive {
		t.Fatalf("state %s", j.State)
	}
}

// 房主不在线时退出：本机停用并记住（不领取、不开始新轮次，重启不恢复）；本机已做完的结果连上后先交上去，再撤销。
func TestCollabOfflineLeave(t *testing.T) {
	p := newCollabPair(t)
	h, m := p.host, p.member
	bob, x := runMemberRound(t, p, "job", "c-1")
	call(t, h.collabShare, map[string]any{"discussion": p.disc.Key, "on": false})
	waitFor(t, "disconnected", func() bool { return !m.roomStatus(p.disc.Key).Connected })
	res := call(t, m.collabLeave, map[string]any{"discussion": p.disc.Key}).(map[string]any)
	if res["pending"] != true {
		t.Fatalf("offline leave %+v", res)
	}
	if j, _ := m.store.Joined(p.disc.Key); j.State != joinedLeaving {
		t.Fatalf("state %s", j.State)
	}
	if mb, _ := m.store.Member(bob.ID); mb.Paused != collabLeavingReason {
		t.Fatalf("agent not paused: %q", mb.Paused)
	}
	if _, err := callErr(m.discPost, map[string]any{"discussion": p.disc.Key, "body": "x", "clientId": "c-2"}); err == nil {
		t.Fatal("posted while leaving")
	}
	readAll(t, m, x)
	call(t, m.taskDone, map[string]any{"task": x.TaskID, "caller": x.Worker, "execution": x.ID, "body": "finished offline"})
	// 常驻进程重启（对账）也不会让它恢复
	m.reconcileDiscussions(true)
	if j, _ := m.store.Joined(p.disc.Key); j.State != joinedLeaving {
		t.Fatalf("restart resumed: %s", j.State)
	}
	call(t, h.collabShare, map[string]any{"discussion": p.disc.Key, "on": true})
	m.roomConnFor(p.disc.Key).poke()
	waitFor(t, "result delivered, then left", func() bool {
		hx, _ := h.store.Execution(x.ID)
		pe, _ := h.store.Person(p.disc.Key, m.store.Self())
		return hx.State == store.ExecDone && pe.State == store.PersonLeft
	})
	if j, _ := m.store.Joined(p.disc.Key); j.State != store.PersonLeft {
		t.Fatalf("member state %s", j.State)
	}
}

// P3-3：交接（连接循环被取消）不暂停已领取、还没开始的轮次；恢复后也要先补齐增量才算连上。
func TestCollabHandoffDoesNotPause(t *testing.T) {
	p := newCollabPair(t)
	m := p.member
	bob, x := runMemberRound(t, p, "first", "c-1")
	r2 := call(t, m.discPost, map[string]any{"discussion": p.disc.Key, "body": "second", "target": bob.ID, "clientId": "c-2"}).(discPostResult)
	waitFor(t, "second accepted", func() bool { return m.store.AcceptState(r2.Execution.ID) == store.AcceptAccepted })
	m.collabPause(false)
	if mb, _ := m.store.Member(bob.ID); mb.Paused != "" {
		t.Fatalf("handoff paused the queue: %q", mb.Paused)
	}
	m.collabPause(true)
	waitFor(t, "reconnected", func() bool { return m.roomStatus(p.disc.Key).Connected })
	readAll(t, m, x)
	call(t, m.taskDone, map[string]any{"task": x.TaskID, "caller": x.Worker, "execution": x.ID, "body": "one"})
	m.setAgent(m.liveSession(x.Worker), term.AgentState{Status: agent.StatusIdle})
	waitFor(t, "second round runs without confirmation", func() bool {
		m.dispatchAll()
		x2, _ := m.store.Execution(r2.Execution.ID)
		return x2.State == store.ExecRunning
	})
}
