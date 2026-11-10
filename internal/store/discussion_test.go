package store

import (
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

func openTestStore(t *testing.T) *Store {
	t.Helper()
	s, err := Open(filepath.Join(t.TempDir(), "posse.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	return s
}

// render 是测试用的上下文写法：每条消息一行，记下结束位置。
func render(src ContextSource) (string, []ContextEnd) {
	var b strings.Builder
	var ends []ContextEnd
	add := func(m DiscMessage) {
		fmt.Fprintf(&b, "#%d %s\n", m.Seq, m.Body)
		ends = append(ends, ContextEnd{Seq: m.Seq, End: b.Len()})
	}
	add(src.Request)
	for _, m := range src.Unreceived {
		add(m)
	}
	return b.String(), ends
}

func newDiscussion(t *testing.T, s *Store) (Discussion, Member) {
	t.Helper()
	d := Discussion{Key: "host/" + NewID("d_"), Host: "host", Title: "t", ProjectID: "p", CreatedAt: time.Now()}
	d.LocalID = strings.TrimPrefix(d.Key, "host/")
	if err := s.CreateDiscussion(d); err != nil {
		t.Fatal(err)
	}
	m := Member{ID: NewID("dmb_"), Discussion: d.Key, Name: "A", Agent: "claude", WorkspaceID: "w", CreatedAt: time.Now()}
	if err := s.AddMember(m); err != nil {
		t.Fatal(err)
	}
	return d, m
}

func post(t *testing.T, s *Store, d Discussion, body, target, client string) (DiscMessage, *Execution) {
	t.Helper()
	m, x, _, err := s.PostMessage(PostInput{Discussion: d.Key, Body: body, Target: target, ClientID: client, At: time.Now()})
	if err != nil {
		t.Fatal(err)
	}
	return m, x
}

func TestPostDedupAndSeq(t *testing.T) {
	s := openTestStore(t)
	d, m := newDiscussion(t, s)
	a, xa := post(t, s, d, "hello", m.ID, "c1")
	b, xb, dup, err := s.PostMessage(PostInput{Discussion: d.Key, Body: "hello", Target: m.ID, ClientID: "c1", At: time.Now()})
	if err != nil || !dup || b.ID != a.ID || xb == nil || xb.ID != xa.ID {
		t.Fatalf("dup post: %v %v %+v", err, dup, b)
	}
	c, xc := post(t, s, d, "plain", "", "c2")
	if xc != nil {
		t.Fatal("plain message created an execution")
	}
	if c.Seq != a.Seq+1 {
		t.Fatalf("seq %d after %d", c.Seq, a.Seq)
	}
	q, _ := s.MemberQueue(m.ID)
	if len(q) != 1 {
		t.Fatalf("queue %d", len(q))
	}
}

func claim(t *testing.T, s *Store, x string, task string) (Execution, bool) {
	t.Helper()
	got, ok, err := s.ClaimExecution(ClaimInput{Exec: x, TaskID: task, Render: render, At: time.Now()})
	if err != nil {
		t.Fatal(err)
	}
	return got, ok
}

// 两代调度器同时领取同一个排队的轮次：只有一个成功。
func TestClaimOnlyOnce(t *testing.T) {
	s := openTestStore(t)
	d, m := newDiscussion(t, s)
	_, x := post(t, s, d, "do", m.ID, "")
	var wins int
	var mu sync.Mutex
	var wg sync.WaitGroup
	for i := range 8 {
		wg.Go(func() {
			_, ok, err := s.ClaimExecution(ClaimInput{Exec: x.ID, TaskID: fmt.Sprint("k_", i), Render: render, At: time.Now()})
			if err != nil {
				t.Error(err)
			}
			if ok {
				mu.Lock()
				wins++
				mu.Unlock()
			}
		})
	}
	wg.Wait()
	if wins != 1 {
		t.Fatalf("%d claims won", wins)
	}
	// 同一成员的第二条请求在第一轮结束前领不到
	_, x2 := post(t, s, d, "next", m.ID, "")
	if _, ok := claim(t, s, x2.ID, "k_x"); ok {
		t.Fatal("claimed while another round is active")
	}
}

func startRound(t *testing.T, s *Store, x *Execution, taskID, worker string) Execution {
	t.Helper()
	got, ok := claim(t, s, x.ID, taskID)
	if !ok {
		t.Fatal("claim failed")
	}
	if err := s.BindWorker(x.ID, Task{ID: taskID, Title: "t", Spec: "s", WorkspaceID: "w", Worker: worker, Agent: "claude", Status: TaskRunning, DiscussionID: got.Discussion, CreatedAt: time.Now()}); err != nil {
		t.Fatal(err)
	}
	got, _ = s.Execution(x.ID)
	return got
}

func finish(s *Store, x Execution, worker, body string) (DiscMessage, error) {
	return s.FinishExecution(FinishInput{RoundCheck: RoundCheck{Exec: x.ID, TaskID: x.TaskID, Worker: worker}, Body: body, Summary: body,
		TaskMsg: Message{ID: NewID("m_"), TaskID: x.TaskID, Kind: "done", Body: body, Outcome: TaskDone, CreatedAt: time.Now()}, At: time.Now()})
}

func TestFinishChecksRoundAndContext(t *testing.T) {
	s := openTestStore(t)
	d, m := newDiscussion(t, s)
	_, x := post(t, s, d, "first", m.ID, "")
	r := startRound(t, s, x, "k_1", "t_1")
	if r.State != ExecRunning || r.ContextLen == 0 {
		t.Fatalf("round %+v", r)
	}
	// 上下文没读完不能汇报完成
	if _, err := finish(s, r, "t_1", "answer"); !errors.Is(err, ErrNotAcked) {
		t.Fatalf("finish before ack: %v", err)
	}
	// 别的终端、错误的轮次都被拒
	if _, err := s.AckContext(r.ID, 0, r.ContextLen); err != nil {
		t.Fatal(err)
	}
	if _, err := finish(s, r, "t_other", "answer"); !errors.Is(err, ErrStaleRound) {
		t.Fatalf("finish from other worker: %v", err)
	}
	bad := r
	bad.ID = "x_nope"
	if _, err := finish(s, bad, "t_1", "answer"); !errors.Is(err, ErrStaleRound) {
		t.Fatalf("finish with wrong execution: %v", err)
	}
	reply, err := finish(s, r, "t_1", "full answer\nwith detail")
	if err != nil {
		t.Fatal(err)
	}
	if reply.Kind != DiscReply || reply.Body != "full answer\nwith detail" || reply.AuthorName != "A" {
		t.Fatalf("reply %+v", reply)
	}
	// 内容不同的重复汇报被拒；内容一样的重跑（响应丢了）幂等成功、返回原来那条，不产生第二条回复
	if _, err := finish(s, r, "t_1", "again"); !errors.Is(err, ErrConflict) {
		t.Fatalf("second finish with another body: %v", err)
	}
	again, err := s.FinishExecution(FinishInput{RoundCheck: RoundCheck{Exec: r.ID, TaskID: r.TaskID, Worker: "t_1"}, Body: "full answer\nwith detail", Summary: "x",
		TaskMsg: Message{ID: NewID("m_"), TaskID: r.TaskID, Kind: "done", Body: "x", Outcome: TaskDone, CreatedAt: time.Now()}, At: time.Now()})
	if !errors.Is(err, ErrAlreadyReported) || again.ID != reply.ID {
		t.Fatalf("same-body rerun: %v %+v", err, again)
	}
	if msgs, _ := s.DiscMessages(d.Key, 0, 10); len(msgs) != 2 {
		t.Fatalf("%d messages after reruns", len(msgs))
	}
	mem, _ := s.Member(m.ID)
	if mem.RecvSeq < 1 || mem.TaskID != "k_1" {
		t.Fatalf("member %+v", mem)
	}
	task, _ := s.Task("k_1")
	if task.Status != TaskDone || task.DiscussionID != d.Key {
		t.Fatalf("task %+v", task)
	}
}

// 第二轮沿用 worker：重开任务和派发在同一事务里；第一轮迟到的汇报不能结束第二轮。
func TestReuseAndLateReport(t *testing.T) {
	s := openTestStore(t)
	d, m := newDiscussion(t, s)
	_, x1 := post(t, s, d, "one", m.ID, "")
	r1 := startRound(t, s, x1, "k_1", "t_1")
	s.AckContext(r1.ID, 0, r1.ContextLen)
	if _, err := finish(s, r1, "t_1", "r1"); err != nil {
		t.Fatal(err)
	}
	_, x2 := post(t, s, d, "two", m.ID, "")
	note := Message{ID: NewID("m_"), TaskID: "k_1", Kind: "note", Body: "new round", CreatedAt: time.Now()}
	r2, ok, err := s.ClaimExecution(ClaimInput{Exec: x2.ID, TaskID: "k_1", Reuse: true, Note: note, Render: render, At: time.Now()})
	if err != nil || !ok || r2.State != ExecRunning || r2.Worker != "t_1" {
		t.Fatalf("reuse claim: %v %v %+v", err, ok, r2)
	}
	if task, _ := s.Task("k_1"); task.Status != TaskRunning {
		t.Fatalf("task not reopened: %s", task.Status)
	}
	// 只含第一轮之后的新消息
	page, _, _ := s.ReadContext(r2.ID, 0, 1<<20)
	if strings.Contains(page.Text, "one") || !strings.Contains(page.Text, "two") {
		t.Fatalf("context %q", page.Text)
	}
	s.AckContext(r2.ID, 0, r2.ContextLen)
	// 第一轮迟到的汇报（不同内容被拒，同样内容幂等成功）都不碰正在做第二轮的任务
	if _, err := finish(s, r1, "t_1", "late"); !errors.Is(err, ErrConflict) {
		t.Fatalf("late report of round 1: %v", err)
	}
	if _, err := finish(s, r1, "t_1", "r1"); !errors.Is(err, ErrAlreadyReported) {
		t.Fatalf("rerun of round 1: %v", err)
	}
	if task, _ := s.Task("k_1"); task.Status != TaskRunning {
		t.Fatalf("round 2 task ended by a round 1 report: %s", task.Status)
	}
	if x, _ := s.Execution(r2.ID); x.State != ExecRunning {
		t.Fatalf("round 2 is %s", x.State)
	}
	if _, err := finish(s, r2, "t_1", "r2"); err != nil {
		t.Fatal(err)
	}
}

func TestAnswerOnce(t *testing.T) {
	s := openTestStore(t)
	d, m := newDiscussion(t, s)
	_, x := post(t, s, d, "q", m.ID, "")
	r := startRound(t, s, x, "k_1", "t_1")
	qm := Message{ID: NewID("m_"), TaskID: "k_1", Kind: "question", Body: "which?", CreatedAt: time.Now()}
	q, err := s.AskExecution(AskInput{RoundCheck: RoundCheck{Exec: r.ID, TaskID: "k_1", Worker: "t_1"}, TaskMsg: qm, At: time.Now()})
	if err != nil || q.Kind != DiscQuestion {
		t.Fatalf("ask: %v %+v", err, q)
	}
	if x, _ := s.Execution(r.ID); x.State != ExecWaiting {
		t.Fatalf("state %s", x.State)
	}
	var ok int
	var mu sync.Mutex
	var wg sync.WaitGroup
	for i := range 6 {
		wg.Go(func() {
			_, err := s.AnswerQuestion(AnswerInput{Question: qm.ID, Body: fmt.Sprint("a", i),
				TaskMsg: Message{ID: NewID("m_"), TaskID: "k_1", Kind: "answer", Body: "a", ReplyTo: qm.ID, CreatedAt: time.Now()}, At: time.Now()})
			if err == nil {
				mu.Lock()
				ok++
				mu.Unlock()
			} else if !errors.Is(err, ErrConflict) {
				t.Error(err)
			}
		})
	}
	wg.Wait()
	if ok != 1 {
		t.Fatalf("%d answers accepted", ok)
	}
	if x, _ := s.Execution(r.ID); x.State != ExecRunning {
		t.Fatalf("state after answer %s", x.State)
	}
	if a, err := s.Answer(qm.ID); err != nil || a.Kind != "answer" {
		t.Fatalf("task answer %v", err)
	}
}

// 归档和发送并发：只有一方成功；有排队、进行中、等待回答的请求时不能归档；恢复不重跑。
func TestArchiveRules(t *testing.T) {
	s := openTestStore(t)
	d, m := newDiscussion(t, s)
	_, x := post(t, s, d, "q", m.ID, "")
	if err := s.SetArchived(d.Key, true, time.Now()); !errors.Is(err, ErrBusyDiscuss) {
		t.Fatalf("archive with queued: %v", err)
	}
	s.CancelExecution(x.ID)
	var wg sync.WaitGroup
	var archErr, postErr error
	wg.Go(func() { archErr = s.SetArchived(d.Key, true, time.Now()) })
	wg.Go(func() {
		_, _, _, postErr = s.PostMessage(PostInput{Discussion: d.Key, Body: "race", Target: m.ID, At: time.Now()})
	})
	wg.Wait()
	switch {
	case archErr == nil && postErr == nil:
		t.Fatal("both archive and post succeeded")
	case archErr == nil && !errors.Is(postErr, ErrArchived):
		t.Fatalf("post after archive: %v", postErr)
	case postErr == nil && !errors.Is(archErr, ErrBusyDiscuss):
		t.Fatalf("archive after post: %v", archErr)
	}
	if archErr != nil {
		q, _ := s.MemberQueue(m.ID)
		for _, e := range q {
			s.CancelExecution(e.ID)
		}
		if err := s.SetArchived(d.Key, true, time.Now()); err != nil {
			t.Fatal(err)
		}
	}
	// 归档后只读
	if _, _, _, err := s.PostMessage(PostInput{Discussion: d.Key, Body: "x", At: time.Now()}); !errors.Is(err, ErrArchived) {
		t.Fatalf("post to archived: %v", err)
	}
	msg, _ := s.DiscMessages(d.Key, 0, 1)
	if err := s.SetKeyMessage(msg[0].ID, true, time.Now()); !errors.Is(err, ErrArchived) {
		t.Fatalf("key in archived: %v", err)
	}
	if err := s.SetArchived(d.Key, false, time.Now()); err != nil {
		t.Fatal(err)
	}
	if q, _ := s.MemberQueue(m.ID); len(q) != 0 {
		t.Fatalf("unarchive revived %d requests", len(q))
	}
}

// 等回答的轮次和受阻的队列都挡住归档。
func TestArchiveBlockedByWaitingAndBlocked(t *testing.T) {
	s := openTestStore(t)
	d, m := newDiscussion(t, s)
	_, x := post(t, s, d, "q", m.ID, "")
	r := startRound(t, s, x, "k_1", "t_1")
	qm := Message{ID: NewID("m_"), TaskID: "k_1", Kind: "question", Body: "?", CreatedAt: time.Now()}
	s.AskExecution(AskInput{RoundCheck: RoundCheck{Exec: r.ID, TaskID: "k_1"}, TaskMsg: qm, At: time.Now()})
	if err := s.SetArchived(d.Key, true, time.Now()); !errors.Is(err, ErrBusyDiscuss) {
		t.Fatalf("archive while waiting: %v", err)
	}
	s.EndTask("k_1", TaskExited, nil, ExecInterrupted, "worker exited", true, time.Now())
	_, x2 := post(t, s, d, "next", m.ID, "")
	s.SetMemberPaused(m.ID, "")
	if ok, _ := s.BlockExecution(x2.ID, m.ID, "worker gone"); !ok {
		t.Fatal("block")
	}
	if err := s.SetArchived(d.Key, true, time.Now()); !errors.Is(err, ErrBusyDiscuss) {
		t.Fatalf("archive while blocked: %v", err)
	}
}

func TestEndTaskInterruptsAndPauses(t *testing.T) {
	s := openTestStore(t)
	d, m := newDiscussion(t, s)
	_, x := post(t, s, d, "q", m.ID, "")
	startRound(t, s, x, "k_1", "t_1")
	_, x2 := post(t, s, d, "q2", m.ID, "")
	exit := Message{ID: NewID("m_"), TaskID: "k_1", Kind: "exit", Body: "gone", CreatedAt: time.Now()}
	if ok, err := s.EndTask("k_1", TaskExited, &exit, ExecInterrupted, "worker exited", true, time.Now()); err != nil || !ok {
		t.Fatalf("end: %v %v", err, ok)
	}
	if got, _ := s.Execution(x.ID); got.State != ExecInterrupted {
		t.Fatalf("state %s", got.State)
	}
	mem, _ := s.Member(m.ID)
	if mem.Paused == "" {
		t.Fatal("member not paused")
	}
	if _, ok := claim(t, s, x2.ID, "k_2"); ok {
		t.Fatal("claimed from a paused member")
	}
	// 重新启动并继续：解开旧 worker，队列接着走
	if _, err := s.ReleaseWorker(m.ID, true); err != nil {
		t.Fatal(err)
	}
	if _, ok := claim(t, s, x2.ID, "k_2"); !ok {
		t.Fatal("claim after restart")
	}
}

// 两轮之间清理任务：成员当前绑定的任务跳过，解绑后的可以清。
func TestClearKeepsBoundTask(t *testing.T) {
	s := openTestStore(t)
	d, m := newDiscussion(t, s)
	_, x := post(t, s, d, "q", m.ID, "")
	r := startRound(t, s, x, "k_1", "t_1")
	s.AckContext(r.ID, 0, r.ContextLen)
	finish(s, r, "t_1", "done")
	s.AddTask(Task{ID: "k_other", Title: "o", Spec: "o", WorkspaceID: "w", Worker: "t_9", Status: TaskDone, CreatedAt: time.Now()})
	n, err := s.DeleteEndedTasks()
	if err != nil || n != 1 {
		t.Fatalf("cleared %d: %v", n, err)
	}
	if _, err := s.Task("k_1"); err != nil {
		t.Fatal("bound task was cleared")
	}
	s.ReleaseWorker(m.ID, false)
	if n, _ := s.DeleteEndedTasks(); n != 1 {
		t.Fatalf("released task not cleared: %d", n)
	}
	// 讨论历史还在
	msgs, _ := s.DiscMessages(d.Key, 0, 10)
	if len(msgs) != 2 || msgs[1].Body != "done" {
		t.Fatalf("history %+v", msgs)
	}
}

// 移除成员要先处理请求；重新添加的同名成员是新 ID，不继承旧 worker 和队列。
func TestRemoveAndReAddMember(t *testing.T) {
	s := openTestStore(t)
	d, m := newDiscussion(t, s)
	_, x := post(t, s, d, "q", m.ID, "")
	if _, err := s.RemoveMember(m.ID, time.Now()); !errors.Is(err, ErrBusyMember) {
		t.Fatalf("remove busy: %v", err)
	}
	s.CancelExecution(x.ID)
	if _, err := s.RemoveMember(m.ID, time.Now()); err != nil {
		t.Fatal(err)
	}
	if _, _, _, err := s.PostMessage(PostInput{Discussion: d.Key, Body: "x", Target: m.ID, At: time.Now()}); !errors.Is(err, ErrMemberGone) {
		t.Fatalf("post to removed: %v", err)
	}
	n := Member{ID: NewID("dmb_"), Discussion: d.Key, Name: "A", Agent: "claude", WorkspaceID: "w", CreatedAt: time.Now()}
	s.AddMember(n)
	got, _ := s.Member(n.ID)
	if got.TaskID != "" || got.RecvSeq != 0 {
		t.Fatalf("re-added member inherited state: %+v", got)
	}
	if r, _ := s.RetryExecution(x.ID, "x_retry", time.Now()); r.ID != "" {
		t.Fatal("retried a removed member's request")
	}
}

// 只有连续确认的部分推进接收位置；重复确认幂等；读页落在字符边界上。
func TestAckContiguous(t *testing.T) {
	s := openTestStore(t)
	d, m := newDiscussion(t, s)
	post(t, s, d, strings.Repeat("中", 10), "", "")
	post(t, s, d, "second", "", "")
	_, x := post(t, s, d, "req", m.ID, "")
	r := startRound(t, s, x, "k_1", "t_1")
	p1, _, _ := s.ReadContext(r.ID, 0, 9)
	if !strings.HasPrefix("#3 req\n#1 "+strings.Repeat("中", 10), p1.Text) || p1.Complete {
		t.Fatalf("page %q", p1.Text)
	}
	// 跳过一段的确认不推进
	if a, _ := s.AckContext(r.ID, p1.To+5, p1.To+10); a != 0 {
		t.Fatalf("gap ack advanced to %d", a)
	}
	s.AckContext(r.ID, 0, p1.To)
	s.AckContext(r.ID, 0, p1.To)
	mem, _ := s.Member(m.ID)
	if mem.RecvSeq != 0 {
		t.Fatalf("recv %d after partial read", mem.RecvSeq)
	}
	for from := p1.To; ; {
		p, _, _ := s.ReadContext(r.ID, from, 7)
		if !strings.HasPrefix(p.Text, "") || len(p.Text) == 0 && !p.Complete {
			t.Fatal("empty page")
		}
		s.AckContext(r.ID, p.From, p.To)
		if p.Complete {
			break
		}
		from = p.To
	}
	mem, _ = s.Member(m.ID)
	if mem.RecvSeq != 3 {
		t.Fatalf("recv %d", mem.RecvSeq)
	}
}

// 对账：普通重启中断进行中的轮次、启动一半的记失败、暂停有排队的成员；交接过来的 worker 还在就照常。
func TestReconcile(t *testing.T) {
	s := openTestStore(t)
	d, m := newDiscussion(t, s)
	m2 := Member{ID: NewID("dmb_"), Discussion: d.Key, Name: "B", Agent: "codex", WorkspaceID: "w", CreatedAt: time.Now()}
	s.AddMember(m2)
	m3 := Member{ID: NewID("dmb_"), Discussion: d.Key, Name: "C", Agent: "codex", WorkspaceID: "w", CreatedAt: time.Now()}
	s.AddMember(m3)
	_, xa := post(t, s, d, "a", m.ID, "")
	startRound(t, s, xa, "k_a", "t_a")
	_, xq := post(t, s, d, "a2", m.ID, "")
	_, xb := post(t, s, d, "b", m2.ID, "")
	claim(t, s, xb.ID, "k_b") // starting，没绑上
	_, xc := post(t, s, d, "c", m3.ID, "")
	startRound(t, s, xc, "k_c", "t_c")
	alive := func(w string) bool { return w == "t_c" }
	r, err := s.ReconcileExecutions(alive, true, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if r.Interrupted != 1 || r.Failed != 1 {
		t.Fatalf("result %+v", r)
	}
	if x, _ := s.Execution(xa.ID); x.State != ExecInterrupted {
		t.Fatalf("a %s", x.State)
	}
	if x, _ := s.Execution(xb.ID); x.State != ExecFailed {
		t.Fatalf("b %s", x.State)
	}
	if x, _ := s.Execution(xc.ID); x.State != ExecRunning {
		t.Fatalf("c %s", x.State)
	}
	if x, _ := s.Execution(xq.ID); x.State != ExecQueued {
		t.Fatalf("queued %s", x.State)
	}
	if mm, _ := s.Member(m.ID); mm.Paused == "" {
		t.Fatal("member with queue not paused")
	}
}

// 一轮只能重试一次：重复点、CLI 重跑不会多排。
func TestRetryOnce(t *testing.T) {
	s := openTestStore(t)
	d, m := newDiscussion(t, s)
	_, x := post(t, s, d, "q", m.ID, "")
	s.CancelExecution(x.ID)
	if _, err := s.RetryExecution(x.ID, "x_r1", time.Now()); err != nil {
		t.Fatal(err)
	}
	if _, err := s.RetryExecution(x.ID, "x_r2", time.Now()); !errors.Is(err, ErrRetried) {
		t.Fatalf("second retry: %v", err)
	}
	if q, _ := s.MemberQueue(m.ID); len(q) != 1 {
		t.Fatalf("%d queued after two retries", len(q))
	}
}

// 立即应用：有请求时什么都不改；没有请求时改配置和解绑在一起。
func TestUpdateMemberApplyNowAtomic(t *testing.T) {
	s := openTestStore(t)
	d, m := newDiscussion(t, s)
	_, x := post(t, s, d, "q", m.ID, "")
	r := startRound(t, s, x, "k_1", "t_1")
	nm := m
	nm.Model = "sonnet"
	if _, _, err := s.UpdateMemberApplyNow(nm); !errors.Is(err, ErrBusyMember) {
		t.Fatalf("apply now while running: %v", err)
	}
	if got, _ := s.Member(m.ID); got.Model != "" || got.ConfigVersion != 1 || got.TaskID != "k_1" {
		t.Fatalf("member changed by a rejected apply: %+v", got)
	}
	s.AckContext(r.ID, 0, r.ContextLen)
	finish(s, r, "t_1", "ok")
	old, got, err := s.UpdateMemberApplyNow(nm)
	if err != nil || old.TaskID != "k_1" || got.TaskID != "" || got.Model != "sonnet" || got.ConfigVersion != 2 {
		t.Fatalf("apply now: %v old %+v new %+v", err, old, got)
	}
}

// 摘要：成员在自己的一轮里保存；之后落后很多的成员用摘要代替它覆盖的消息，读完算接收过；落后不多的照常逐条领取。
func TestSummaryStandsInForOldMessages(t *testing.T) {
	s := openTestStore(t)
	d, a := newDiscussion(t, s)
	b := Member{ID: NewID("dmb_"), Discussion: d.Key, Name: "B", Agent: "codex", WorkspaceID: "w", CreatedAt: time.Now()}
	if err := s.AddMember(b); err != nil {
		t.Fatal(err)
	}
	for i := range SummarySkip + 5 {
		post(t, s, d, fmt.Sprint("chat ", i), "", "")
	}
	_, x := post(t, s, d, "summarize please", a.ID, "")
	r := startRound(t, s, x, "k_1", "t_1")
	in := SummaryInput{RoundCheck: RoundCheck{Exec: r.ID, TaskID: "k_1", Worker: "t_1"}, Body: "we decided X", At: time.Now()}
	if _, err := s.SetSummary(in); !errors.Is(err, ErrNotAcked) {
		t.Fatalf("summary before reading the request: %v", err)
	}
	s.AckContext(r.ID, 0, r.ContextLen)
	in.Worker = "t_other"
	if _, err := s.SetSummary(in); !errors.Is(err, ErrStaleRound) {
		t.Fatalf("summary from another terminal: %v", err)
	}
	in.Worker = "t_1"
	got, err := s.SetSummary(in)
	if err != nil || got.Summary != "we decided X" || got.SummaryBy != "A" || got.SummarySeq != int64(SummarySkip+6) || got.SummaryAt == nil {
		t.Fatalf("summary %+v %v", got, err)
	}
	if _, err := finish(s, r, "t_1", "saved"); err != nil {
		t.Fatal(err)
	}
	// B 从没接收过：摘要覆盖的 26 条不再逐条给，只给摘要之后的
	post(t, s, d, "after the summary", "", "")
	_, xb := post(t, s, d, "your turn", b.ID, "")
	var src ContextSource
	rb, ok, err := s.ClaimExecution(ClaimInput{Exec: xb.ID, TaskID: "k_2", At: time.Now(), Render: func(cs ContextSource) (string, []ContextEnd) {
		src = cs
		return render(cs)
	}})
	if err != nil || !ok {
		t.Fatalf("claim: %v %v", err, ok)
	}
	if src.Skipped != SummarySkip+6 || src.SkippedTo != got.SummarySeq || len(src.Unreceived) != 2 || src.Unreceived[0].Body != "saved" {
		t.Fatalf("skipped %d to %d, unreceived %d", src.Skipped, src.SkippedTo, len(src.Unreceived))
	}
	s.AckContext(rb.ID, 0, rb.ContextLen)
	if mb, _ := s.Member(b.ID); mb.RecvSeq != src.Request.Seq {
		t.Fatalf("recv %d, want %d", mb.RecvSeq, src.Request.Seq)
	}
	// 用户清掉摘要
	if err := s.ClearSummary(d.Key); err != nil {
		t.Fatal(err)
	}
	if cur, _ := s.Discussion(d.Key); cur.Summary != "" || cur.SummarySeq != 0 || cur.SummaryAt != nil {
		t.Fatalf("after clear %+v", cur)
	}
}

// 落后不多（不超过 SummarySkip 条）时照常逐条领取，摘要只是多一段。
func TestSummaryKeepsShortBacklog(t *testing.T) {
	s := openTestStore(t)
	d, a := newDiscussion(t, s)
	b := Member{ID: NewID("dmb_"), Discussion: d.Key, Name: "B", Agent: "codex", WorkspaceID: "w", CreatedAt: time.Now()}
	if err := s.AddMember(b); err != nil {
		t.Fatal(err)
	}
	post(t, s, d, "chat", "", "")
	_, x := post(t, s, d, "summarize", a.ID, "")
	r := startRound(t, s, x, "k_1", "t_1")
	s.AckContext(r.ID, 0, r.ContextLen)
	if _, err := s.SetSummary(SummaryInput{RoundCheck: RoundCheck{Exec: r.ID, TaskID: "k_1", Worker: "t_1"}, Body: "short", At: time.Now()}); err != nil {
		t.Fatal(err)
	}
	finish(s, r, "t_1", "saved")
	_, xb := post(t, s, d, "your turn", b.ID, "")
	var src ContextSource
	if _, ok, err := s.ClaimExecution(ClaimInput{Exec: xb.ID, TaskID: "k_2", At: time.Now(), Render: func(cs ContextSource) (string, []ContextEnd) {
		src = cs
		return render(cs)
	}}); err != nil || !ok {
		t.Fatalf("claim: %v %v", err, ok)
	}
	if src.Skipped != 0 || len(src.Unreceived) != 3 || src.Discussion.Summary != "short" {
		t.Fatalf("skipped %d, unreceived %d", src.Skipped, len(src.Unreceived))
	}
}
