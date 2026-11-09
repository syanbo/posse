package daemon

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"posse/internal/agent"
	"posse/internal/rpc"
	"posse/internal/store"
	"posse/internal/term"
)

// newDiscDaemon 是带讨论的测试常驻进程：成员的 worker 是 /bin/cat，冒充 agent。
func newDiscDaemon(t *testing.T) (*Daemon, store.Workspace, store.Discussion) {
	t.Helper()
	d, w := newTestDaemon(t)
	var mu sync.Mutex
	d.disc.launch = func(w store.Workspace, name string, m agent.Model, prompt string, env ...string) (*session, error) {
		mu.Lock()
		defer mu.Unlock()
		// 调用方持有交接读锁：用不加锁的拉起
		s, err := d.launchTerminalLocked(w, []string{"/bin/cat"}, name, 100, 20, store.Terminal{}, env...)
		if err == nil {
			// 冒充 agent 启动完停在输入框
			d.setAgent(s, term.AgentState{Status: agent.StatusWorking})
		}
		return s, err
	}
	disc := call(t, d.discCreate, map[string]any{"title": "design", "workspace": w.ID}).(store.Discussion)
	return d, w, disc
}

func callErr(h func(context.Context, json.RawMessage) (any, error), params map[string]any) (any, error) {
	raw, _ := json.Marshal(params)
	return h(context.Background(), raw)
}

func addMember(t *testing.T, d *Daemon, disc store.Discussion, name, ag string) store.Member {
	t.Helper()
	return call(t, d.discMemberAdd, map[string]any{"discussion": disc.Key, "name": name, "agent": ag}).(store.Member)
}

func postTo(t *testing.T, d *Daemon, disc store.Discussion, body, target, client string) discPostResult {
	t.Helper()
	return call(t, d.discPost, map[string]any{"discussion": disc.Key, "body": body, "target": target, "clientId": client}).(discPostResult)
}

func waitExec(t *testing.T, d *Daemon, id string, states ...string) store.Execution {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	var x store.Execution
	for time.Now().Before(deadline) {
		d.dispatchAll()
		x, _ = d.store.Execution(id)
		for _, s := range states {
			if x.State == s {
				return x
			}
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("execution %s is %s (%s), want %v", id, x.State, x.Reason, states)
	return x
}

// readAll 模拟 worker 读完上下文：一页一页读，读到就确认。
func readAll(t *testing.T, d *Daemon, x store.Execution) string {
	t.Helper()
	var b strings.Builder
	cursor := 0
	for {
		page := call(t, d.discRead, map[string]any{"execution": x.ID, "cursor": cursor, "caller": x.Worker}).(store.ContextPage)
		b.WriteString(page.Text)
		call(t, d.discAck, map[string]any{"execution": x.ID, "from": page.From, "to": page.To, "caller": x.Worker})
		if page.Complete {
			return b.String()
		}
		cursor = page.To
	}
}

func TestDiscussionRoundTrip(t *testing.T) {
	d, _, disc := newDiscDaemon(t)
	if !strings.Contains(disc.Key, "/d_") || len(strings.SplitN(disc.Key, "/", 2)[0]) != 64 {
		t.Fatalf("room key %q", disc.Key)
	}
	a := addMember(t, d, disc, "Alice", "claude")
	b := addMember(t, d, disc, "Bob", "codex")

	// 普通发言不唤醒任何人
	plain := postTo(t, d, disc, "just thinking out loud", "", "c0")
	if plain.Execution != nil {
		t.Fatal("plain message created an execution")
	}
	d.dispatchAll()
	if ts, _ := d.store.Tasks(store.TaskFilter{}); len(ts) != 0 {
		t.Fatalf("plain message started %d workers", len(ts))
	}

	// @A 只执行 A；重复点发送不重复执行
	r1 := postTo(t, d, disc, "please review the plan", a.ID, "c1")
	again := postTo(t, d, disc, "please review the plan", a.ID, "c1")
	if !again.Duplicate || again.Execution.ID != r1.Execution.ID {
		t.Fatalf("duplicate send: %+v", again)
	}
	x1 := waitExec(t, d, r1.Execution.ID, store.ExecRunning)
	task, _ := d.store.Task(x1.TaskID)
	if task.DiscussionID != disc.Key || task.Coordinator != "" || task.Worker != x1.Worker {
		t.Fatalf("task %+v", task)
	}
	if !strings.Contains(task.Spec, "--execution "+x1.ID) || strings.Contains(task.Spec, "please review the plan") {
		t.Fatalf("prompt should name the execution and not carry the request:\n%s", task.Spec)
	}
	if ts, _ := d.store.Tasks(store.TaskFilter{}); len(ts) != 1 {
		t.Fatalf("%d workers for one request", len(ts))
	}

	// 别人的终端读不了；缺 execution、错误的 execution、没读完都不能汇报
	if _, err := callErr(d.discRead, map[string]any{"execution": x1.ID, "caller": "t_other"}); err == nil {
		t.Fatal("other terminal read the context")
	}
	if _, err := callErr(d.taskDone, map[string]any{"task": task.ID, "caller": task.Worker, "body": "x"}); err == nil || !strings.Contains(err.Error(), "--execution") {
		t.Fatalf("done without execution: %v", err)
	}
	if _, err := callErr(d.taskDone, map[string]any{"task": task.ID, "caller": task.Worker, "execution": x1.ID, "body": "x"}); err == nil || !strings.Contains(err.Error(), "not read") {
		t.Fatalf("done before reading: %v", err)
	}
	ctxText := readAll(t, d, x1)
	if !strings.Contains(ctxText, "please review the plan") || !strings.Contains(ctxText, "just thinking out loud") {
		t.Fatalf("context:\n%s", ctxText)
	}
	if _, err := callErr(d.taskDone, map[string]any{"task": task.ID, "caller": task.Worker, "execution": "x_wrong", "body": "x"}); err == nil {
		t.Fatal("done with a wrong execution")
	}
	if _, err := callErr(d.taskDone, map[string]any{"task": task.ID, "caller": task.Worker, "execution": x1.ID, "summary": "only summary"}); err == nil {
		t.Fatal("done without a body")
	}
	call(t, d.taskDone, map[string]any{"task": task.ID, "caller": task.Worker, "execution": x1.ID, "body": "## Review\nLooks good, two nits.", "summary": "looks good"})
	x1 = waitExec(t, d, x1.ID, store.ExecDone)
	reply, _ := d.store.DiscMessage(x1.Reply)
	if reply.Body != "## Review\nLooks good, two nits." || reply.AuthorName != "Alice" {
		t.Fatalf("reply %+v", reply)
	}

	// 引用 A 的回复交给 B：B 的上下文里有引用和共同历史
	r2 := call(t, d.discPost, map[string]any{"discussion": disc.Key, "body": "check Alice's nits", "target": b.ID, "quote": reply.ID}).(discPostResult)
	x2 := waitExec(t, d, r2.Execution.ID, store.ExecRunning)
	if x2.TaskID == x1.TaskID {
		t.Fatal("B reused A's worker")
	}
	bText := readAll(t, d, x2)
	if !strings.Contains(bText, "## Quoted message") || !strings.Contains(bText, "Looks good, two nits.") {
		t.Fatalf("B context:\n%s", bText)
	}
	// 迟到的 A 第一轮汇报不能结束 B，也不能再结束 A
	if _, err := callErr(d.taskDone, map[string]any{"task": task.ID, "caller": task.Worker, "execution": x1.ID, "body": "late"}); err == nil {
		t.Fatal("late report accepted")
	}
	// 未读不受 worker 读取影响：讨论里没有任何读标记，ui.json 里记（这里只确认消息照常在）
	msgs, _ := d.store.DiscMessages(disc.Key, 0, 100)
	if len(msgs) != 4 {
		t.Fatalf("%d messages", len(msgs))
	}
}

// 同一成员连发两条：严格顺序，上一轮 done 之后还要等 worker 空闲才派下一轮；各自一条回复。
func TestDiscussionSerialPerMember(t *testing.T) {
	d, _, disc := newDiscDaemon(t)
	a := addMember(t, d, disc, "Alice", "claude")
	r1 := postTo(t, d, disc, "first", a.ID, "")
	r2 := postTo(t, d, disc, "second", a.ID, "")
	x1 := waitExec(t, d, r1.Execution.ID, store.ExecRunning)
	d.dispatchAll()
	if x, _ := d.store.Execution(r2.Execution.ID); x.State != store.ExecQueued {
		t.Fatalf("second started while first runs: %s", x.State)
	}
	readAll(t, d, x1)
	call(t, d.taskDone, map[string]any{"task": x1.TaskID, "caller": x1.Worker, "execution": x1.ID, "body": "one"})
	s := d.liveSession(x1.Worker)
	d.dispatchAll()
	if x, _ := d.store.Execution(r2.Execution.ID); x.State != store.ExecQueued {
		t.Fatalf("dispatched before the worker went idle: %s", x.State)
	}
	// task.send 不能绕过讨论队列
	if _, err := callErr(d.taskSend, map[string]any{"task": x1.TaskID, "body": "sneak"}); err == nil {
		t.Fatal("task.send on a discussion task")
	}
	d.setAgent(s, term.AgentState{Status: agent.StatusIdle})
	x2 := waitExec(t, d, r2.Execution.ID, store.ExecRunning)
	if x2.TaskID != x1.TaskID || x2.Worker != x1.Worker {
		t.Fatal("second round did not reuse the worker")
	}
	// 提醒带上新的 execution
	waitScreen(t, s, "--execution "+x2.ID)
	if _, err := callErr(d.taskCheck, map[string]any{"task": x1.TaskID, "caller": x1.Worker, "execution": x1.ID}); err == nil {
		t.Fatal("check with the previous execution")
	}
	notes := call(t, d.taskCheck, map[string]any{"task": x1.TaskID, "caller": x1.Worker, "execution": x2.ID}).([]store.Message)
	if len(notes) != 1 || !strings.Contains(notes[0].Body, x2.ID) {
		t.Fatalf("notes %+v", notes)
	}
	text := readAll(t, d, x2)
	if strings.Contains(text, "first") {
		t.Fatalf("second round got already received messages:\n%s", text)
	}
	call(t, d.taskDone, map[string]any{"task": x1.TaskID, "caller": x1.Worker, "execution": x2.ID, "body": "two"})
	waitExec(t, d, x2.ID, store.ExecDone)
	var replies int
	msgs, _ := d.store.DiscMessages(disc.Key, 0, 100)
	for _, m := range msgs {
		if m.Kind == store.DiscReply {
			replies++
		}
	}
	if replies != 2 {
		t.Fatalf("%d replies", replies)
	}
}

// 问题在群里回答（任务页也走同一事务，只接受一次）；ask --resume 带着轮次接着等。
func TestDiscussionQuestion(t *testing.T) {
	d, _, disc := newDiscDaemon(t)
	a := addMember(t, d, disc, "Alice", "claude")
	r := postTo(t, d, disc, "do it", a.ID, "")
	x := waitExec(t, d, r.Execution.ID, store.ExecRunning)
	if _, err := callErr(d.taskAsk, map[string]any{"task": x.TaskID, "caller": x.Worker, "question": "which?"}); err == nil {
		t.Fatal("ask without execution")
	}
	type res struct {
		v   any
		err error
	}
	got := make(chan res, 1)
	go func() {
		v, err := callErr(d.taskAsk, map[string]any{"task": x.TaskID, "caller": x.Worker, "execution": x.ID, "question": "which branch?", "timeoutMs": 5000})
		got <- res{v, err}
	}()
	var q store.DiscMessage
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		msgs, _ := d.store.DiscMessages(disc.Key, 0, 100)
		if last := msgs[len(msgs)-1]; last.Kind == store.DiscQuestion {
			q = last
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if q.ID == "" {
		t.Fatal("question not posted")
	}
	if xs, _ := d.store.Execution(x.ID); xs.State != store.ExecWaiting {
		t.Fatalf("state %s", xs.State)
	}
	// 普通群消息不是答案
	postTo(t, d, disc, "unrelated", "", "")
	var wg sync.WaitGroup
	var oks int
	var mu sync.Mutex
	for i := range 4 {
		wg.Go(func() {
			var err error
			if i%2 == 0 {
				_, err = callErr(d.discAnswer, map[string]any{"question": q.Question, "body": "main"})
			} else {
				_, err = callErr(d.taskReply, map[string]any{"message": q.Question, "body": "main"})
			}
			if err == nil {
				mu.Lock()
				oks++
				mu.Unlock()
			}
		})
	}
	wg.Wait()
	if oks != 1 {
		t.Fatalf("%d answers accepted", oks)
	}
	r2 := <-got
	if r2.err != nil || r2.v.(taskAskResult).Answer != "main" {
		t.Fatalf("ask result %+v %v", r2.v, r2.err)
	}
	if xs, _ := d.store.Execution(x.ID); xs.State != store.ExecRunning {
		t.Fatalf("state after answer %s", xs.State)
	}
}

// 执行中 worker 退出：本轮中断，后续请求暂停，「重新启动并继续」后由新 worker 接着做。
func TestDiscussionWorkerExit(t *testing.T) {
	d, _, disc := newDiscDaemon(t)
	a := addMember(t, d, disc, "Alice", "claude")
	r1 := postTo(t, d, disc, "one", a.ID, "")
	x1 := waitExec(t, d, r1.Execution.ID, store.ExecRunning)
	r2 := postTo(t, d, disc, "two", a.ID, "")
	d.workerGone(x1.Worker, "worker agent exited before reporting")
	waitExec(t, d, x1.ID, store.ExecInterrupted)
	d.dispatchAll()
	// 排队的停在队列里（或在这期间被认成 worker 不在而受阻），都要用户重新启动
	if x, _ := d.store.Execution(r2.Execution.ID); x.State != store.ExecQueued && x.State != store.ExecBlocked {
		t.Fatalf("queued request ran after an interruption: %s", x.State)
	}
	if m, _ := d.store.Member(a.ID); m.Paused == "" {
		t.Fatal("member not paused")
	}
	// 中断后归档被挡（还有排队的）
	if _, err := callErr(d.discArchive, map[string]any{"discussion": disc.Key, "archived": true}); err == nil {
		t.Fatal("archived with a paused queue")
	}
	call(t, d.discMemberRestart, map[string]any{"member": a.ID})
	x2 := waitExec(t, d, r2.Execution.ID, store.ExecRunning)
	if x2.TaskID == x1.TaskID {
		t.Fatal("restart reused the dead worker")
	}
	if old := d.liveSession(x1.Worker); old != nil {
		t.Fatal("old worker terminal still open")
	}
	// 重试中断的那一轮：另排一轮，不自动重跑
	nx := call(t, d.discRetry, map[string]any{"execution": x1.ID}).(store.Execution)
	if nx.RetryOf != x1.ID || nx.State != store.ExecQueued {
		t.Fatalf("retry %+v", nx)
	}
}

// 两轮之间 worker 被关掉（终端没了）：下一次派发受阻，等用户重新启动。
func TestDiscussionBlockedBetweenRounds(t *testing.T) {
	d, _, disc := newDiscDaemon(t)
	a := addMember(t, d, disc, "Alice", "claude")
	r1 := postTo(t, d, disc, "one", a.ID, "")
	x1 := waitExec(t, d, r1.Execution.ID, store.ExecRunning)
	readAll(t, d, x1)
	call(t, d.taskDone, map[string]any{"task": x1.TaskID, "caller": x1.Worker, "execution": x1.ID, "body": "ok"})
	// 终端直接关掉（不经 task close）
	d.closeSession(d.liveSession(x1.Worker), time.Second)
	r2 := postTo(t, d, disc, "two", a.ID, "")
	x2 := waitExec(t, d, r2.Execution.ID, store.ExecBlocked)
	if x2.Reason == "" {
		t.Fatal("no reason")
	}
	// 已完成的一轮不受影响
	if x, _ := d.store.Execution(x1.ID); x.State != store.ExecDone {
		t.Fatalf("done round changed to %s", x.State)
	}
	call(t, d.discMemberRestart, map[string]any{"member": a.ID})
	waitExec(t, d, r2.Execution.ID, store.ExecRunning)
}

// 停止进行中的一轮：关掉专用 worker、解开绑定，下一轮新开 worker 而不是受阻；任务页的关闭也一样。
func TestDiscussionStop(t *testing.T) {
	d, _, disc := newDiscDaemon(t)
	a := addMember(t, d, disc, "Alice", "claude")
	r1 := postTo(t, d, disc, "one", a.ID, "")
	x1 := waitExec(t, d, r1.Execution.ID, store.ExecRunning)
	call(t, d.discStop, map[string]any{"execution": x1.ID})
	if x, _ := d.store.Execution(x1.ID); x.State != store.ExecStopped {
		t.Fatalf("state %s", x.State)
	}
	r2 := postTo(t, d, disc, "two", a.ID, "")
	x2 := waitExec(t, d, r2.Execution.ID, store.ExecRunning)
	call(t, d.taskClose, map[string]any{"task": x2.TaskID})
	if x, _ := d.store.Execution(x2.ID); x.State != store.ExecStopped {
		t.Fatalf("task close: %s", x.State)
	}
	r3 := postTo(t, d, disc, "three", a.ID, "")
	waitExec(t, d, r3.Execution.ID, store.ExecRunning)
}

// 同一真实目录里有别的讨论 worker 在执行：派发前停下等确认；确认后照常，独立 worktree 不提示。
func TestDiscussionConcurrentDirectory(t *testing.T) {
	d, _, disc := newDiscDaemon(t)
	a := addMember(t, d, disc, "Alice", "claude")
	b := addMember(t, d, disc, "Bob", "codex")
	ra := postTo(t, d, disc, "edit", a.ID, "")
	waitExec(t, d, ra.Execution.ID, store.ExecRunning)
	rb := postTo(t, d, disc, "edit too", b.ID, "")
	xb := waitExec(t, d, rb.Execution.ID, store.ExecHeld)
	if !strings.HasPrefix(xb.Reason, "concurrent:Alice") {
		t.Fatalf("reason %q", xb.Reason)
	}
	call(t, d.discConfirm, map[string]any{"execution": xb.ID})
	xb = waitExec(t, d, xb.ID, store.ExecRunning)
	// B 后面的一条在 A、B 都在执行时又停下；两边都结束后不再需要确认，自动回到队列
	rb2 := postTo(t, d, disc, "later", b.ID, "")
	ra2 := postTo(t, d, disc, "later", a.ID, "")
	xa := waitExec(t, d, ra.Execution.ID, store.ExecRunning)
	readAll(t, d, xa)
	call(t, d.taskDone, map[string]any{"task": xa.TaskID, "caller": xa.Worker, "execution": xa.ID, "body": "a"})
	readAll(t, d, xb)
	call(t, d.taskDone, map[string]any{"task": xb.TaskID, "caller": xb.Worker, "execution": xb.ID, "body": "b"})
	d.setAgent(d.liveSession(xa.Worker), term.AgentState{Status: agent.StatusIdle})
	waitExec(t, d, ra2.Execution.ID, store.ExecRunning)
	if x, _ := d.store.Execution(rb2.Execution.ID); x.State != store.ExecHeld && x.State != store.ExecQueued {
		t.Fatalf("b2 %s", x.State)
	}
}

// 交接过来、还没拿到锁的新进程不派发、不对账；拿到锁后对账保留交接过来的 worker。
func TestDiscussionWaitLock(t *testing.T) {
	d, _, disc := newDiscDaemon(t)
	a := addMember(t, d, disc, "Alice", "claude")
	b := addMember(t, d, disc, "Bob", "codex")
	ra := postTo(t, d, disc, "a", a.ID, "")
	xa := waitExec(t, d, ra.Execution.ID, store.ExecRunning)
	d.disc.waitLock.Store(true)
	rb := postTo(t, d, disc, "b", b.ID, "")
	d.dispatchAll()
	if x, _ := d.store.Execution(rb.Execution.ID); x.State != store.ExecQueued {
		t.Fatalf("dispatched before the lock: %s", x.State)
	}
	// 交接期间旧进程也不派发
	d.disc.waitLock.Store(false)
	d.handoff.mu.Lock()
	d.handoff.on = true
	d.handoff.mu.Unlock()
	d.dispatchAll()
	if x, _ := d.store.Execution(rb.Execution.ID); x.State != store.ExecQueued {
		t.Fatalf("dispatched during handoff: %s", x.State)
	}
	d.handoff.mu.Lock()
	d.handoff.on = false
	d.handoff.mu.Unlock()
	d.reconcileDiscussions(false)
	if x, _ := d.store.Execution(xa.ID); x.State != store.ExecRunning {
		t.Fatalf("handed-off round was %s after reconcile", x.State)
	}
	if x, _ := d.store.Execution(rb.Execution.ID); x.State != store.ExecQueued {
		t.Fatalf("queue changed by handoff reconcile: %s", x.State)
	}
	if m, _ := d.store.Member(b.ID); m.Paused != "" {
		t.Fatal("handoff paused the queue")
	}
}

// 上下文超过一页时分页读，读不全不能汇报；读到的页才推进。
func TestDiscussionContextPaging(t *testing.T) {
	d, _, disc := newDiscDaemon(t)
	a := addMember(t, d, disc, "Alice", "claude")
	long := strings.Repeat("长消息。", 4000) // 约 48 KiB
	postTo(t, d, disc, long, "", "")
	r := postTo(t, d, disc, "summarize", a.ID, "")
	x := waitExec(t, d, r.Execution.ID, store.ExecRunning)
	page := call(t, d.discRead, map[string]any{"execution": x.ID, "caller": x.Worker}).(store.ContextPage)
	if page.Complete || len(page.Text) > discReadBudget {
		t.Fatalf("first page %d bytes, complete %v", len(page.Text), page.Complete)
	}
	call(t, d.discAck, map[string]any{"execution": x.ID, "from": page.From, "to": page.To, "caller": x.Worker})
	if _, err := callErr(d.taskDone, map[string]any{"task": x.TaskID, "caller": x.Worker, "execution": x.ID, "body": "x"}); err == nil {
		t.Fatal("done after one page")
	}
	text := readAll(t, d, x)
	if !strings.Contains(text, long) {
		t.Fatal("pages lost text")
	}
	call(t, d.taskDone, map[string]any{"task": x.TaskID, "caller": x.Worker, "execution": x.ID, "body": "x"})
}

// 附件复制成快照；超数量、超大小、读不到时报错，不假装附上。
func TestDiscussionAttachments(t *testing.T) {
	d, w, disc := newDiscDaemon(t)
	src := filepath.Join(t.TempDir(), "notes.md")
	os.WriteFile(src, []byte("# hi"), 0o644)
	r := call(t, d.discPost, map[string]any{"discussion": disc.Key, "body": "see file", "attachments": []map[string]any{{"path": src}}}).(discPostResult)
	if len(r.Message.Attachments) != 1 {
		t.Fatalf("attachments %+v", r.Message.Attachments)
	}
	att := r.Message.Attachments[0]
	abs := filepath.Join(w.Path, att.Path)
	if b, err := os.ReadFile(abs); err != nil || string(b) != "# hi" || !strings.HasPrefix(att.Path, ".posse/discussions/"+disc.LocalID+"/attachments/") {
		t.Fatalf("snapshot %s: %v", att.Path, err)
	}
	os.WriteFile(src, []byte("changed"), 0o644)
	if b, _ := os.ReadFile(abs); string(b) != "# hi" {
		t.Fatal("snapshot follows the source")
	}
	if _, err := callErr(d.discPost, map[string]any{"discussion": disc.Key, "body": "x", "attachments": []map[string]any{{"path": "/nope/missing"}}}); err == nil {
		t.Fatal("missing attachment accepted")
	}
	var many []map[string]any
	for range discMaxAttachments + 1 {
		many = append(many, map[string]any{"path": src})
	}
	if _, err := callErr(d.discPost, map[string]any{"discussion": disc.Key, "body": "x", "attachments": many}); err == nil {
		t.Fatal("too many attachments accepted")
	}
	big := filepath.Join(t.TempDir(), "big.bin")
	f, _ := os.Create(big)
	f.Truncate(discMaxAttachSize + 1)
	f.Close()
	if _, err := callErr(d.discPost, map[string]any{"discussion": disc.Key, "body": "x", "attachments": []map[string]any{{"path": big}}}); err == nil {
		t.Fatal("oversized attachment accepted")
	}
	if got := safeFileName("../../etc/passwd"); got != "passwd" {
		t.Fatalf("safe name %q", got)
	}
	// 附件丢了：上下文里标出来，历史照样在
	a := addMember(t, d, disc, "Alice", "claude")
	os.Remove(abs)
	q := call(t, d.discPost, map[string]any{"discussion": disc.Key, "body": "read it", "target": a.ID, "quote": r.Message.ID}).(discPostResult)
	x := waitExec(t, d, q.Execution.ID, store.ExecRunning)
	if text := readAll(t, d, x); !strings.Contains(text, abs+" (notes.md, 4 bytes) (missing)") {
		t.Fatalf("context:\n%s", text)
	}
}

// 归档后只读，恢复后能继续；列出已归档讨论的附件供清理。
func TestDiscussionArchive(t *testing.T) {
	d, _, disc := newDiscDaemon(t)
	a := addMember(t, d, disc, "Alice", "claude")
	r := postTo(t, d, disc, "x", a.ID, "")
	x := waitExec(t, d, r.Execution.ID, store.ExecRunning)
	if _, err := callErr(d.discArchive, map[string]any{"discussion": disc.Key, "archived": true}); err == nil {
		t.Fatal("archived with a running request")
	}
	readAll(t, d, x)
	call(t, d.taskDone, map[string]any{"task": x.TaskID, "caller": x.Worker, "execution": x.ID, "body": "ok"})
	call(t, d.discArchive, map[string]any{"discussion": disc.Key, "archived": true})
	for name, h := range map[string]func(context.Context, json.RawMessage) (any, error){"post": d.discPost, "member": d.discMemberAdd} {
		if _, err := callErr(h, map[string]any{"discussion": disc.Key, "body": "x", "name": "C", "agent": "claude"}); err == nil {
			t.Fatalf("%s in an archived discussion", name)
		}
	}
	// 归档不关其他终端：worker 还在
	if s := d.liveSession(x.Worker); s == nil {
		t.Fatal("archive closed the worker")
	}
	call(t, d.discAttachments, map[string]any{"discussion": disc.Key})
	call(t, d.discArchive, map[string]any{"discussion": disc.Key, "archived": false})
	postTo(t, d, disc, "back", "", "")
}

// 项目的工作区删掉：运行中的轮次经 workerGone 中断，队列因工作区不在暂停；讨论记录还在。
func TestDiscussionWorkspaceRemoved(t *testing.T) {
	d, w, disc := newDiscDaemon(t)
	a := addMember(t, d, disc, "Alice", "claude")
	r := postTo(t, d, disc, "x", a.ID, "")
	x := waitExec(t, d, r.Execution.ID, store.ExecRunning)
	d.closeWorkspaceTerminals(w.ID)
	d.workerGone(x.Worker, "worker terminal exited (code 0) before reporting")
	waitExec(t, d, x.ID, store.ExecInterrupted)
	d.store.DeleteWorkspace(w.ID)
	r2 := postTo(t, d, disc, "y", a.ID, "")
	call(t, d.discMemberRestart, map[string]any{"member": a.ID})
	x2 := waitExec(t, d, r2.Execution.ID, store.ExecFailed)
	if x2.Reason != "workspace was removed" {
		t.Fatalf("reason %q", x2.Reason)
	}
	msgs, _ := d.store.DiscMessages(disc.Key, 0, 10)
	if len(msgs) != 2 {
		t.Fatalf("history lost: %d", len(msgs))
	}
}

// 普通任务不带 execution 照旧可以 ask/check/done/send（回归）。
func TestPlainTaskUnchanged(t *testing.T) {
	d, w := newTestDaemon(t)
	info := call(t, d.taskStart, map[string]any{"workspace": w.ID, "command": []string{"/bin/sh", "-c", "sleep 30"}, "spec": "x"}).(taskInfo)
	call(t, d.taskCheck, map[string]any{"task": info.ID, "caller": info.Worker})
	call(t, d.taskDone, map[string]any{"task": info.ID, "caller": info.Worker, "summary": "ok"})
	if _, err := callErr(d.taskSend, map[string]any{"task": info.ID, "body": "more"}); err != nil {
		var re *rpc.Error
		// sh 不是 agent，重开会被拒，但不是讨论的拒绝
		if !errors.As(err, &re) || strings.Contains(err.Error(), "discussion") {
			t.Fatal(err)
		}
	}
}

// 改配置：已有 worker 继续用旧配置；有排队请求时立即应用被拒；没有请求时立即应用关掉旧 worker，下一轮按新配置新开。
func TestDiscussionApplyNow(t *testing.T) {
	d, _, disc := newDiscDaemon(t)
	var models []string
	launch := d.disc.launch
	d.disc.launch = func(w store.Workspace, name string, m agent.Model, prompt string, env ...string) (*session, error) {
		models = append(models, m.Name)
		return launch(w, name, m, prompt, env...)
	}
	a := call(t, d.discMemberAdd, map[string]any{"discussion": disc.Key, "name": "Alice", "agent": "claude", "model": "haiku"}).(store.Member)
	r1 := postTo(t, d, disc, "one", a.ID, "")
	x1 := waitExec(t, d, r1.Execution.ID, store.ExecRunning)
	readAll(t, d, x1)
	call(t, d.taskDone, map[string]any{"task": x1.TaskID, "caller": x1.Worker, "execution": x1.ID, "body": "ok"})
	// 只改配置：旧 worker 照旧
	m := call(t, d.discMemberUpdate, map[string]any{"member": a.ID, "model": "sonnet"}).(store.Member)
	if m.ConfigVersion != 2 || m.Model != "sonnet" || m.Role != a.Role || m.TaskID != x1.TaskID {
		t.Fatalf("member after update %+v", m)
	}
	d.setAgent(d.liveSession(x1.Worker), term.AgentState{Status: agent.StatusIdle})
	r2 := postTo(t, d, disc, "two", a.ID, "")
	x2 := waitExec(t, d, r2.Execution.ID, store.ExecRunning)
	if x2.Worker != x1.Worker || x2.Model != "haiku" || x2.ConfigVersion != 1 {
		t.Fatalf("reused worker should keep the old config: %+v", x2)
	}
	// 有进行中的请求：立即应用被拒
	if _, err := callErr(d.discMemberUpdate, map[string]any{"member": a.ID, "model": "opus", "applyNow": true}); err == nil {
		t.Fatal("apply now with an active request")
	}
	readAll(t, d, x2)
	call(t, d.taskDone, map[string]any{"task": x2.TaskID, "caller": x2.Worker, "execution": x2.ID, "body": "ok"})
	call(t, d.discMemberUpdate, map[string]any{"member": a.ID, "model": "opus", "applyNow": true})
	if s := d.liveSession(x1.Worker); s != nil {
		t.Fatal("old worker still open after apply now")
	}
	if mm, _ := d.store.Member(a.ID); mm.TaskID != "" || mm.Worker != "" {
		t.Fatalf("member still bound: %+v", mm)
	}
	r3 := postTo(t, d, disc, "three", a.ID, "")
	x3 := waitExec(t, d, r3.Execution.ID, store.ExecRunning)
	if x3.Worker == x1.Worker || x3.Model != "opus" || x3.ConfigVersion != 3 {
		t.Fatalf("new worker config %+v", x3)
	}
	if len(models) != 2 || models[0] != "haiku" || models[1] != "opus" {
		t.Fatalf("launched with %v", models)
	}
	// 历史里两轮记着各自的配置
	if old, _ := d.store.Execution(x1.ID); old.Model != "haiku" {
		t.Fatalf("round 1 config rewritten: %+v", old)
	}
}

// 故障注入：终端拉起之后登记失败。刚拉起的终端被关掉，这一轮记失败、队列暂停，不自动再拉起。
func TestDiscussionBindFailure(t *testing.T) {
	for _, mode := range []string{"stale", "error"} {
		t.Run(mode, func(t *testing.T) {
			d, _, disc := newDiscDaemon(t)
			launches := 0
			var started *session
			launch := d.disc.launch
			d.disc.launch = func(w store.Workspace, name string, m agent.Model, prompt string, env ...string) (*session, error) {
				launches++
				s, err := launch(w, name, m, prompt, env...)
				started = s
				var taskID string
				for _, e := range env {
					if v, ok := strings.CutPrefix(e, "POSSE_TASK_ID="); ok {
						taskID = v
					}
				}
				switch mode {
				case "stale":
					// 拉起期间轮次被别人改掉（例如对账判成启动失败）
					xs, _ := d.store.ActiveExecutions()
					for _, x := range xs {
						d.store.StartFailed(x.ID, "worker start was interrupted", false)
					}
				case "error":
					// 任务 ID 被占：登记任务的插入失败
					d.store.AddTask(store.Task{ID: taskID, Title: "x", Spec: "x", WorkspaceID: w.ID, Worker: "t_other", Status: store.TaskDone, CreatedAt: time.Now()})
				}
				return s, err
			}
			a := addMember(t, d, disc, "Alice", "claude")
			r := postTo(t, d, disc, "go", a.ID, "")
			x := waitExec(t, d, r.Execution.ID, store.ExecFailed)
			if started == nil || d.liveSession(started.ID) != nil {
				t.Fatal("orphan worker terminal left open")
			}
			for range 5 {
				d.dispatchAll()
			}
			if launches != 1 {
				t.Fatalf("launched %d times", launches)
			}
			if mm, _ := d.store.Member(a.ID); mm.Paused == "" || mm.TaskID != "" {
				t.Fatalf("member after failed bind %+v", mm)
			}
			if x.Reason == "" {
				t.Fatal("no failure reason")
			}
		})
	}
}

// P1-1：讨论 worker 不能以用户身份发言、给别的成员排请求、回答、改成员或归档；不是 worker 的终端和终端之外照常。
func TestDiscussionWorkerCannotActAsUser(t *testing.T) {
	d, w, disc := newDiscDaemon(t)
	a := addMember(t, d, disc, "Alice", "claude")
	b := addMember(t, d, disc, "Bob", "codex")
	r := postTo(t, d, disc, "go", a.ID, "")
	x := waitExec(t, d, r.Execution.ID, store.ExecRunning)
	for name, c := range map[string]struct {
		h      func(context.Context, json.RawMessage) (any, error)
		params map[string]any
	}{
		"post":    {d.discPost, map[string]any{"discussion": disc.Key, "body": "Bob, do X", "target": b.ID}},
		"answer":  {d.discAnswer, map[string]any{"question": "m_x", "body": "yes"}},
		"key":     {d.discKey, map[string]any{"message": r.Message.ID, "on": true}},
		"cancel":  {d.discCancel, map[string]any{"execution": x.ID}},
		"stop":    {d.discStop, map[string]any{"execution": x.ID}},
		"member":  {d.discMemberAdd, map[string]any{"discussion": disc.Key, "name": "C", "agent": "claude"}},
		"remove":  {d.discMemberRemove, map[string]any{"member": b.ID}},
		"archive": {d.discArchive, map[string]any{"discussion": disc.Key, "archived": true}},
		"create":  {d.discCreate, map[string]any{"title": "x", "workspace": w.ID}},
	} {
		c.params["caller"] = x.Worker
		if _, err := callErr(c.h, c.params); err == nil || !strings.Contains(err.Error(), "task worker") {
			t.Errorf("%s from the worker: %v", name, err)
		}
	}
	if q, _ := d.store.MemberQueue(b.ID); len(q) != 0 {
		t.Fatalf("worker queued a request for Bob: %+v", q)
	}
	// 用户自己的终端（不是 worker）可以
	user, err := d.startTerminal(w, []string{"/bin/cat"}, "", 80, 20)
	if err != nil {
		t.Fatal(err)
	}
	call(t, d.discPost, map[string]any{"discussion": disc.Key, "body": "from my shell", "caller": user.ID})
}

// P2-1：打开讨论只带读进来那一页消息涉及的执行和没结束的执行，不全量。
func TestDiscussionShowPagesExecutions(t *testing.T) {
	d, _, disc := newDiscDaemon(t)
	a := addMember(t, d, disc, "Alice", "claude")
	var first store.Execution
	for i := range 4 {
		r := postTo(t, d, disc, fmt.Sprint("req ", i), a.ID, "")
		if i == 0 {
			first = *r.Execution
		}
		call(t, d.discCancel, map[string]any{"execution": r.Execution.ID})
	}
	for range 10 {
		postTo(t, d, disc, "chat", "", "")
	}
	live := postTo(t, d, disc, "now", a.ID, "")
	v := call(t, d.discShow, map[string]any{"discussion": disc.Key, "limit": 5}).(discView)
	ids := map[string]bool{}
	for _, x := range v.Executions {
		ids[x.ID] = true
	}
	if ids[first.ID] || !ids[live.Execution.ID] || len(v.Executions) != 1 {
		t.Fatalf("executions on the first page: %v", ids)
	}
	if !v.More || v.MoreAfter {
		t.Fatalf("more %v moreAfter %v", v.More, v.MoreAfter)
	}
	old := call(t, d.discShow, map[string]any{"discussion": disc.Key, "around": 2, "limit": 4}).(discView)
	if old.Messages[0].Seq != 1 || !old.MoreAfter || len(old.Executions) < 2 {
		t.Fatalf("around page: %d msgs from %d, moreAfter %v, %d execs", len(old.Messages), old.Messages[0].Seq, old.MoreAfter, len(old.Executions))
	}
	next := call(t, d.discShow, map[string]any{"discussion": disc.Key, "after": old.Messages[len(old.Messages)-1].Seq, "limit": 4}).(discView)
	if next.Messages[0].Seq != old.Messages[len(old.Messages)-1].Seq+1 {
		t.Fatalf("after page starts at %d", next.Messages[0].Seq)
	}
}

// P2-3：worker 终端的状态每次都带（不靠讨论版本），终端关了马上看得到。
func TestDiscussionLiveWorkers(t *testing.T) {
	d, _, disc := newDiscDaemon(t)
	a := addMember(t, d, disc, "Alice", "claude")
	r := postTo(t, d, disc, "go", a.ID, "")
	x := waitExec(t, d, r.Execution.ID, store.ExecRunning)
	v := call(t, d.discShow, map[string]any{"discussion": disc.Key, "since": 1 << 40}).(discView)
	if v.Live[x.Worker] == "" || v.Live[x.Worker] == "gone" {
		t.Fatalf("live %v", v.Live)
	}
	rev := v.Discussion.Rev
	d.closeSession(d.liveSession(x.Worker), time.Second)
	v = call(t, d.discShow, map[string]any{"discussion": disc.Key, "since": rev}).(discView)
	if v.Live[x.Worker] != "gone" {
		t.Fatalf("closed worker shows %q", v.Live[x.Worker])
	}
}

// P2-4：同一轮同样内容的 task done 重跑成功（不报不是当前轮）；不同内容被拒；都不结束下一轮。
func TestDiscussionDoneRerun(t *testing.T) {
	d, _, disc := newDiscDaemon(t)
	a := addMember(t, d, disc, "Alice", "claude")
	r := postTo(t, d, disc, "one", a.ID, "")
	x := waitExec(t, d, r.Execution.ID, store.ExecRunning)
	readAll(t, d, x)
	done := map[string]any{"task": x.TaskID, "caller": x.Worker, "execution": x.ID, "body": "answer"}
	call(t, d.taskDone, done)
	call(t, d.taskDone, done)
	d.setAgent(d.liveSession(x.Worker), term.AgentState{Status: agent.StatusIdle})
	r2 := postTo(t, d, disc, "two", a.ID, "")
	x2 := waitExec(t, d, r2.Execution.ID, store.ExecRunning)
	call(t, d.taskDone, done)
	if _, err := callErr(d.taskDone, map[string]any{"task": x.TaskID, "caller": x.Worker, "execution": x.ID, "body": "changed"}); err == nil {
		t.Fatal("different body for a reported round accepted")
	}
	if cur, _ := d.store.Execution(x2.ID); cur.State != store.ExecRunning {
		t.Fatalf("round 2 is %s after round 1 reruns", cur.State)
	}
	if task, _ := d.store.Task(x.TaskID); task.Status != store.TaskRunning {
		t.Fatalf("task is %s", task.Status)
	}
	var replies int
	msgs, _ := d.store.DiscMessages(disc.Key, 0, 50)
	for _, m := range msgs {
		if m.Kind == store.DiscReply {
			replies++
		}
	}
	if replies != 1 {
		t.Fatalf("%d replies", replies)
	}
}

// P2-5：拉起 worker 和登记任务在同一段交接读锁里：交接（写锁）只能在登记提交之后进来，
// 拿到写锁时终端已经有任务，不会把没登记的终端交出去。
func TestDiscussionLaunchAndBindUnderStartLock(t *testing.T) {
	d, _, disc := newDiscDaemon(t)
	launch := d.disc.launch
	started, release := make(chan string, 1), make(chan struct{})
	d.disc.launch = func(w store.Workspace, name string, m agent.Model, prompt string, env ...string) (*session, error) {
		s, err := launch(w, name, m, prompt, env...)
		started <- s.ID
		<-release
		return s, err
	}
	a := addMember(t, d, disc, "Alice", "claude")
	postTo(t, d, disc, "go", a.ID, "")
	go d.dispatchAll()
	worker := <-started
	locked := make(chan []store.Task, 1)
	go func() {
		d.handoff.startMu.Lock()
		ts, _ := d.store.Tasks(store.TaskFilter{Worker: worker})
		d.handoff.startMu.Unlock()
		locked <- ts
	}()
	select {
	case <-locked:
		t.Fatal("handoff lock taken while the worker was launched but not bound")
	case <-time.After(200 * time.Millisecond):
	}
	close(release)
	if ts := <-locked; len(ts) != 1 {
		t.Fatalf("worker had %d tasks when handoff took the lock", len(ts))
	}
}

// 提醒降级：讨论任务没有进行中的轮次时，提醒指向 discussion pending，不给会被拒的 task check。
func TestDiscussionNudgeWithoutRound(t *testing.T) {
	d, _, disc := newDiscDaemon(t)
	a := addMember(t, d, disc, "Alice", "claude")
	r := postTo(t, d, disc, "go", a.ID, "")
	x := waitExec(t, d, r.Execution.ID, store.ExecRunning)
	d.store.AddMessage(store.Message{ID: store.NewID("m_"), TaskID: x.TaskID, Kind: "note", Body: "during", CreatedAt: time.Now()})
	text, ids := d.nudgeText(x.Worker)
	d.store.MarkRead(ids, time.Now())
	if !strings.Contains(text, "--execution "+x.ID) {
		t.Fatalf("nudge during a round: %q", text)
	}
	// 轮次结束后还有一条没读的消息（比如回答晚到）
	readAll(t, d, x)
	call(t, d.taskDone, map[string]any{"task": x.TaskID, "caller": x.Worker, "execution": x.ID, "body": "ok"})
	d.store.AddMessage(store.Message{ID: store.NewID("m_"), TaskID: x.TaskID, Kind: "note", Body: "late", CreatedAt: time.Now()})
	d.store.ReopenTask(x.TaskID)
	text, _ = d.nudgeText(x.Worker)
	if !strings.Contains(text, "posse discussion pending "+x.TaskID) {
		t.Fatalf("nudge without a round: %q", text)
	}
}

// 后台命令让 OSC 保持 working；已汇报回答的 Stop 应放行下一轮，且提醒实际送到终端。
func TestDiscussionCodexBackgroundDoesNotBlockQueue(t *testing.T) {
	d, _, disc := newDiscDaemon(t)
	m := addMember(t, d, disc, "Codex", "codex")
	r1 := postTo(t, d, disc, "first", m.ID, "")
	r2 := postTo(t, d, disc, "second", m.ID, "")
	x1 := waitExec(t, d, r1.Execution.ID, store.ExecRunning)
	s := d.liveSession(x1.Worker)
	s.setOSC("codex", agent.StatusWorking)
	hook := func(event, turn string) {
		call(t, d.hookEvent, map[string]any{"terminalId": s.ID, "agent": "codex", "event": event, "payload": map[string]any{"turn_id": turn}})
	}
	queued := func(id string) {
		t.Helper()
		d.dispatchAll()
		if x, _ := d.store.Execution(id); x.State != store.ExecQueued {
			t.Fatalf("premature dispatch: %+v", x)
		}
	}
	hook("UserPromptSubmit", "turn-1")
	readAll(t, d, x1)
	call(t, d.taskDone, map[string]any{"task": x1.TaskID, "caller": x1.Worker, "execution": x1.ID, "body": "one"})
	queued(r2.Execution.ID)
	hook("Stop", "old-turn")
	queued(r2.Execution.ID)
	hook("Stop", "turn-1")
	x2 := waitExec(t, d, r2.Execution.ID, store.ExecRunning)
	waitScreen(t, s, "--execution "+x2.ID)
	if st := s.Agent(); st.Status != agent.StatusWorking {
		t.Fatalf("background status changed: %+v", st)
	}
	r3 := postTo(t, d, disc, "third", m.ID, "")
	hook("UserPromptSubmit", "turn-2")
	readAll(t, d, x2)
	call(t, d.taskDone, map[string]any{"task": x2.TaskID, "caller": x2.Worker, "execution": x2.ID, "body": "two"})
	hook("Stop", "turn-1")
	queued(r3.Execution.ID)
	hook("Stop", "turn-2")
	x3 := waitExec(t, d, r3.Execution.ID, store.ExecRunning)
	waitScreen(t, s, "--execution "+x3.ID)
}

func TestDiscussionCodexStopBeforeReport(t *testing.T) {
	d, _, disc := newDiscDaemon(t)
	m := addMember(t, d, disc, "Codex", "codex")
	r1 := postTo(t, d, disc, "first", m.ID, "")
	r2 := postTo(t, d, disc, "second", m.ID, "")
	x1 := waitExec(t, d, r1.Execution.ID, store.ExecRunning)
	s := d.liveSession(x1.Worker)
	s.setOSC("codex", agent.StatusWorking)
	for _, event := range []string{"UserPromptSubmit", "Stop"} {
		call(t, d.hookEvent, map[string]any{"terminalId": s.ID, "agent": "codex", "event": event, "payload": map[string]any{"turn_id": "turn-1"}})
	}
	d.dispatchAll()
	if x, _ := d.store.Execution(r2.Execution.ID); x.State != store.ExecQueued {
		t.Fatal("Stop bypassed report")
	}
	readAll(t, d, x1)
	call(t, d.taskDone, map[string]any{"task": x1.TaskID, "caller": x1.Worker, "execution": x1.ID, "body": "one"})
	waitExec(t, d, r2.Execution.ID, store.ExecRunning)
}

func TestDiscussionCodexContinuedStop(t *testing.T) {
	s := &session{agent: "codex"}
	raw := json.RawMessage(`{"turn_id":"turn-1"}`)
	s.discussionHook("codex", "UserPromptSubmit", raw, false)
	s.discussionHook("codex", "Stop", raw, true)
	if s.discussionReady() {
		t.Fatal("continued Stop released the queue")
	}
	s.discussionHook("codex", "Stop", raw, false)
	if !s.discussionReady() {
		t.Fatal("final Stop did not release the queue")
	}
	finish, ok := s.reserveDiscussion()
	if !ok {
		t.Fatal("ready turn could not be reserved")
	}
	finish(true)
	s.discussionHook("codex", "Stop", raw, false)
	if s.discussionReady() {
		t.Fatal("duplicate Stop reused a consumed turn")
	}
	if !s.discussionDelivery() {
		t.Fatal("claimed request cannot be delivered")
	}
	s.discussionHook("codex", "UserPromptSubmit", json.RawMessage(`{"turn_id":"turn-2"}`), false)
	if s.discussionDelivery() || s.discussionReady() {
		t.Fatal("new turn inherited readiness")
	}
}

// 汇报后用户在 worker 里自己问了一轮：那一轮结束就放行，不因它没汇报卡住队列。
func TestDiscussionCodexUserTurnAfterReport(t *testing.T) {
	d, _, disc := newDiscDaemon(t)
	m := addMember(t, d, disc, "Codex", "codex")
	r1 := postTo(t, d, disc, "first", m.ID, "")
	x1 := waitExec(t, d, r1.Execution.ID, store.ExecRunning)
	s := d.liveSession(x1.Worker)
	s.setOSC("codex", agent.StatusWorking)
	hook := func(event, turn string) {
		call(t, d.hookEvent, map[string]any{"terminalId": s.ID, "agent": "codex", "event": event, "payload": map[string]any{"turn_id": turn}})
	}
	hook("UserPromptSubmit", "turn-1")
	readAll(t, d, x1)
	call(t, d.taskDone, map[string]any{"task": x1.TaskID, "caller": x1.Worker, "execution": x1.ID, "body": "one"})
	hook("Stop", "turn-1")
	hook("UserPromptSubmit", "user-turn")
	r2 := postTo(t, d, disc, "second", m.ID, "")
	d.dispatchAll()
	if x, _ := d.store.Execution(r2.Execution.ID); x.State != store.ExecQueued {
		t.Fatalf("dispatched during user turn: %+v", x)
	}
	hook("Stop", "user-turn")
	x2 := waitExec(t, d, r2.Execution.ID, store.ExecRunning)
	waitScreen(t, s, "--execution "+x2.ID)
}

// Esc 中断只发 Interrupt，不发 Stop：同样算这一轮结束。
func TestDiscussionCodexInterruptReleases(t *testing.T) {
	for _, turn := range []string{"turn-1", ""} {
		s := &session{agent: "codex"}
		s.discussionHook("codex", "UserPromptSubmit", json.RawMessage(`{"turn_id":"turn-1"}`), false)
		s.discussionHook("codex", "Interrupt", json.RawMessage(`{"turn_id":"`+turn+`"}`), false)
		if !s.discussionReady() {
			t.Fatalf("Interrupt %q did not release the queue", turn)
		}
	}
	s := &session{agent: "codex"}
	s.discussionHook("codex", "UserPromptSubmit", json.RawMessage(`{"turn_id":"turn-2"}`), false)
	s.discussionHook("codex", "Interrupt", json.RawMessage(`{"turn_id":"turn-1"}`), false)
	if s.discussionReady() {
		t.Fatal("stale Interrupt released a newer turn")
	}
}
