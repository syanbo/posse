package daemon

// 多 agent 编排：派发方（一个 agent，或在界面里操作的用户）把任务派给新开的 worker agent。
// worker 用 posse task ask/check/done 和派发方通信。有新消息时 posse 给对方一行提醒，让它去读收件箱：
// 有 hook 的 agent 在工具做完或本轮结束时由 hook 交给它（takeNudge），否则等它空闲时往终端里输入（deliver）。

import (
	"context"
	"encoding/json"
	"fmt"
	"slices"
	"strings"
	"time"

	"posse/internal/agent"
	"posse/internal/gitx"
	"posse/internal/rpc"
	"posse/internal/store"
)

const (
	// worker 还能再派 worker，层数超过这个值就拒绝，防止失控地递归派发
	maxTaskDepth = 3
	// 用户最近这么久内在终端里打过字，就先不往里注入提醒，免得和用户输入混在一起
	nudgeQuiet = 5 * time.Second
	// 低于常见 agent 的 shell 工具 10 分钟上限，超时前能把续等命令交给 worker
	defaultAskTimeout = 9 * time.Minute
)

type taskInfo struct {
	store.Task
	Workspace     string          `json:"workspace"`
	WorkspaceKind string          `json:"workspaceKind,omitempty"`
	Branch        string          `json:"branch,omitempty"`
	BaseRef       string          `json:"baseRef,omitempty"`
	Terminal      *terminalInfo   `json:"terminal,omitempty"` // worker 终端还在时的状态
	Questions     int             `json:"questions"`          // 还没回复的问题
	Messages      []store.Message `json:"messages,omitempty"`
	Screen        []string        `json:"screen,omitempty"`
}

// taskInfo 的 idx 为 nil 时逐条查询工作区和项目，列表接口传入批量查好的 idx。
func (d *Daemon) taskInfo(t store.Task, questions map[string]int, idx *workspaceIndex) taskInfo {
	info := taskInfo{Task: t, Questions: questions[t.ID]}
	var w store.Workspace
	found := false
	if idx != nil {
		w, found = idx.workspaces[t.WorkspaceID]
	} else if ws, err := d.store.Workspace(t.WorkspaceID); err == nil {
		w, found = ws, true
	}
	if found {
		label := ""
		if idx != nil {
			label = idx.label(w)
		} else {
			label = d.workspaceLabel(w)
		}
		info.Workspace, info.WorkspaceKind, info.Branch, info.BaseRef = label, w.Kind, w.Branch, w.BaseRef
	} else {
		info.Workspace = "(removed workspace)"
	}
	if s := d.liveSession(t.Worker); s != nil {
		ti := d.terminalInfo(s)
		info.Terminal = &ti
	}
	return info
}

// taskChanged 返回一个在任务或消息下次变化时关闭的 channel。
func (d *Daemon) taskChanged() <-chan struct{} {
	d.tmu.Lock()
	defer d.tmu.Unlock()
	return d.tchanged
}

// waitingOn 记下 caller 开始等任务，返回的函数在等完时调用。
func (d *Daemon) waitingOn(caller string) func() {
	d.tmu.Lock()
	if d.waiting == nil {
		d.waiting = map[string]int{}
	}
	d.waiting[caller]++
	d.tmu.Unlock()
	return func() {
		d.tmu.Lock()
		if d.waiting[caller]--; d.waiting[caller] <= 0 {
			delete(d.waiting, caller)
		}
		d.tmu.Unlock()
	}
}

// waitingTasks 报告终端是不是正阻塞在 task wait 里。
func (d *Daemon) waitingTasks(terminalID string) bool {
	d.tmu.Lock()
	defer d.tmu.Unlock()
	return d.waiting[terminalID] > 0
}

func (d *Daemon) taskNotify() {
	d.tmu.Lock()
	close(d.tchanged)
	d.tchanged = make(chan struct{})
	d.tmu.Unlock()
}

// --- 派发

type taskStartParams struct {
	Caller    string `json:"caller"` // 调用方终端（POSSE_TERMINAL_ID），即派发方
	Cwd       string `json:"cwd"`
	Workspace string `json:"workspace"`
	Worktree  string `json:"worktree"` // 为 worker 新建这个名字的 git worktree
	Base      string `json:"base"`
	Agent     string `json:"agent"`
	agent.Model
	Command []string `json:"command"` // 不用 agent 时直接运行的命令
	Title   string   `json:"title"`
	Spec    string   `json:"spec"`
	Group   string   `json:"group"`
	Cols    int      `json:"cols"`
	Rows    int      `json:"rows"`
}

func (d *Daemon) taskStart(ctx context.Context, raw json.RawMessage) (any, error) {
	var p taskStartParams
	if err := rpc.Decode(raw, &p); err != nil {
		return nil, err
	}
	p.Spec = strings.TrimSpace(p.Spec)
	if p.Spec == "" {
		return nil, rpc.Errorf(rpc.CodeBadRequest, "spec required")
	}
	switch {
	case p.Agent == "" && len(p.Command) == 0:
		return nil, rpc.Errorf(rpc.CodeBadRequest, "agent required (%s)", strings.Join(agent.Known, ", "))
	case p.Agent != "" && len(p.Command) > 0:
		return nil, rpc.Errorf(rpc.CodeBadRequest, "agent and command are exclusive")
	case p.Model != (agent.Model{}) && p.Agent == "":
		return nil, rpc.Errorf(rpc.CodeBadRequest, "model and effort require agent")
	case p.Agent != "" && d.checkEnabled(p.Agent) != nil:
		return nil, d.checkEnabled(p.Agent)
	case p.Agent != "" && slices.Contains(agent.Known, p.Agent) && !d.agentInstalled(p.Agent):
		have := "none of " + strings.Join(agent.Known, ", ")
		if got := d.enabledAgents(); len(got) > 0 {
			have = strings.Join(got, ", ")
		}
		return nil, rpc.Errorf(rpc.CodeBadRequest, "%s is not installed on this Mac (installed: %s)", p.Agent, have)
	}
	if depth := d.taskDepth(p.Caller); depth >= maxTaskDepth {
		return nil, rpc.Errorf(rpc.CodeConflict, "workers are already nested %d deep; do this task yourself", depth)
	}
	w, err := d.taskWorkspace(ctx, p)
	if err != nil {
		return nil, err
	}
	t := store.Task{ID: store.NewID("k_"), Title: p.Title, Spec: p.Spec, Coordinator: p.Caller, WorkspaceID: w.ID,
		Agent: p.Agent, Model: p.Model.Name, Effort: p.Model.Effort, Group: p.Group, Status: store.TaskRunning, CreatedAt: time.Now()}
	if t.Title == "" {
		t.Title = firstLine(p.Spec, 60)
	}
	env := []string{"POSSE_TASK_ID=" + t.ID}
	var s *session
	if p.Agent != "" {
		m := p.Model
		m.Worker = true
		s, err = d.startAgent(w, p.Agent, m, workerPrompt(t, w), p.Cols, p.Rows, env...)
	} else {
		s, err = d.startTerminal(w, p.Command, "", p.Cols, p.Rows, env...)
	}
	if err != nil {
		return nil, err
	}
	t.Worker = s.ID
	if err := d.store.AddTask(t); err != nil {
		d.closeSession(s, 2*time.Second)
		return nil, err
	}
	d.taskNotify()
	return d.taskInfo(t, nil, nil), nil
}

// taskWorkspace 决定 worker 在哪里工作：指定的工作区，否则派发方所在的工作区；
// 要求 worktree 时在其所属项目里新建一个。
func (d *Daemon) taskWorkspace(ctx context.Context, p taskStartParams) (store.Workspace, error) {
	var w store.Workspace
	var err error
	if p.Workspace != "" {
		w, err = d.workspace(p.Workspace)
	} else {
		w, err = d.currentWorkspace(p.Caller, p.Cwd)
	}
	if err != nil || p.Worktree == "" {
		return w, err
	}
	proj, err := d.store.FindProject(w.ProjectID)
	if err != nil {
		return w, err
	}
	base := p.Base
	if base == "" && w.Kind == "worktree" {
		// 从派发方所在的分支分出去，而不是项目目录当前的分支
		base = w.Branch
	}
	nw, _, err := d.createWorkspace(ctx, proj, workspaceCreateParams{Name: p.Worktree, Mode: "worktree", Base: base})
	return nw, err
}

// taskDepth 是终端在派发链上的层数：不是 worker 为 0，用户派发的 worker 为 1。
func (d *Daemon) taskDepth(terminalID string) int {
	depth := 0
	for id := terminalID; id != "" && depth <= maxTaskDepth; depth++ {
		ts, err := d.store.Tasks(store.TaskFilter{Worker: id})
		if err != nil || len(ts) == 0 {
			break
		}
		id = ts[len(ts)-1].Coordinator
	}
	return depth
}

// workerPrompt 是交给 worker 的完整提示：怎么和派发方通信，然后是任务本身。
func workerPrompt(t store.Task, w store.Workspace) string {
	who := "another agent"
	if t.Coordinator == "" {
		who = "the user, who follows this task in the Posse window"
	}
	var b strings.Builder
	fmt.Fprintf(&b, "You are a worker dispatched through Posse. Task ID: %s.\n", t.ID)
	fmt.Fprintf(&b, "Your coordinator is %s. It cannot see this terminal, so reach it only with these shell commands:\n\n", who)
	fmt.Fprintf(&b, "posse task ask %s \"<question>\"\n", t.ID)
	b.WriteString("  Ask the coordinator and wait; the command prints the answer. Use it instead of asking here or with a question tool: nobody is watching this terminal. It waits up to 9 minutes, so give your shell tool a 10-minute timeout; if it times out, run the command it prints to keep waiting on the same question.\n\n")
	fmt.Fprintf(&b, "posse task check %s\n", t.ID)
	b.WriteString("  Read follow-up notes from the coordinator. Run it at natural checkpoints and once more right before reporting.\n\n")
	fmt.Fprintf(&b, "posse task done %s --summary \"<3 sentences: what you did, what you found, what is left>\" [--report <file>] [--failed]\n", t.ID)
	b.WriteString("  Report once when you finish, and once after each follow-up. Pass --failed when the task could not be completed; never hide a failure in prose. Add --report only for a file you actually wrote.\n")
	fmt.Fprintf(&b, "  Write a report file under $POSSE_NOTES/tasks/%s/ (a git-ignored folder at the project root that outlives worktrees) unless the coordinator named another place.\n\n", t.ID)
	b.WriteString("Stay inside the task: edit only what it lets you change, and ask before going beyond it. In the summary, name the evidence that shows the result, such as the command you ran and what it printed.\n")
	b.WriteString("After `posse task done`, stop and wait at the prompt: no new work, no polling. If the user talks to you directly, do what they say.\n")
	fmt.Fprintf(&b, "The coordinator may send follow-up work for the same task later. Posse then gives you a reminder line: run `posse task check %s`, do the follow-up, and report it with `posse task done` again.\n", t.ID)
	if w.Kind == "worktree" {
		fmt.Fprintf(&b, "\nYou work in your own git worktree on branch %s, created from %s. Commit your changes on this branch before reporting.\n", w.Branch, w.BaseRef)
	}
	b.WriteString("\n=== TASK ===\n")
	b.WriteString(t.Spec)
	return b.String()
}

func firstLine(s string, n int) string {
	line, _, _ := strings.Cut(strings.TrimSpace(s), "\n")
	return agent.Truncate(line, n)
}

// --- 查询

type taskListParams struct {
	Caller string `json:"caller"`
	All    bool   `json:"all"`
}

// taskList 在 agent 终端里默认只列它派发的任务，在终端之外或带 all 时列出全部。
func (d *Daemon) taskList(ctx context.Context, raw json.RawMessage) (any, error) {
	var p taskListParams
	if err := rpc.Decode(raw, &p); err != nil {
		return nil, err
	}
	f := store.TaskFilter{}
	if p.Caller != "" && !p.All {
		f.Coordinator = &p.Caller
	}
	ts, err := d.store.Tasks(f)
	if err != nil {
		return nil, err
	}
	questions, err := d.store.OpenQuestions()
	if err != nil {
		return nil, err
	}
	idx, err := d.loadWorkspaceIndex(true)
	if err != nil {
		return nil, err
	}
	out := []taskInfo{}
	for _, t := range ts {
		out = append(out, d.taskInfo(t, questions, idx))
	}
	return out, nil
}

type taskSel struct {
	Task   string `json:"task"`
	Caller string `json:"caller"`
}

func (d *Daemon) taskShow(ctx context.Context, raw json.RawMessage) (any, error) {
	var p taskSel
	if err := rpc.Decode(raw, &p); err != nil {
		return nil, err
	}
	t, err := d.resolveTask(p.Task, p.Caller)
	if err != nil {
		return nil, err
	}
	questions, err := d.store.OpenQuestions()
	if err != nil {
		return nil, err
	}
	info := d.taskInfo(t, questions, nil)
	if info.Messages, err = d.store.Messages(t.ID); err != nil {
		return nil, err
	}
	if s := d.liveSession(t.Worker); s != nil {
		info.Screen = trimEmpty(s.Screen())
	}
	return info, nil
}

func trimEmpty(lines []string) []string {
	for len(lines) > 0 && lines[len(lines)-1] == "" {
		lines = lines[:len(lines)-1]
	}
	for len(lines) > 0 && lines[0] == "" {
		lines = lines[1:]
	}
	return lines
}

// resolveTask 按 ID 找任务；不给 ID 时取调用方作为 worker 正在做的任务。
func (d *Daemon) resolveTask(sel, caller string) (store.Task, error) {
	if sel != "" {
		t, err := d.store.Task(sel)
		if err == store.ErrNotFound {
			return t, rpc.Errorf(rpc.CodeNotFound, "task %s not found", sel)
		}
		return t, err
	}
	if caller != "" {
		if ts, err := d.store.Tasks(store.TaskFilter{Worker: caller, Running: true}); err == nil && len(ts) > 0 {
			return ts[len(ts)-1], nil
		}
	}
	return store.Task{}, rpc.Errorf(rpc.CodeBadRequest, "task id required")
}

// 汇报和提问必须来自 worker 自己的终端；终端之外（用户手动）不受限
func authorizeWorker(t store.Task, caller string) error {
	if caller != "" && caller != t.Worker {
		return rpc.Errorf(rpc.CodeConflict, "only task %s's worker terminal (%s) can do this; you are %s", t.ID, t.Worker, caller)
	}
	return nil
}

// 回复和指挥必须来自派发方；用户（终端之外或界面）可以接手任何任务
func authorizeCoordinator(t store.Task, caller string) error {
	if caller != "" && caller != t.Coordinator {
		return rpc.Errorf(rpc.CodeConflict, "task %s was dispatched by %s, not you (%s)", t.ID, dash(t.Coordinator), caller)
	}
	return nil
}

func dash(s string) string {
	if s == "" {
		return "the user"
	}
	return s
}

// --- worker 一侧

type taskDoneParams struct {
	Task    string `json:"task"`
	Caller  string `json:"caller"`
	Summary string `json:"summary"`
	Report  string `json:"report"`
	Failed  bool   `json:"failed"`
}

func (d *Daemon) taskDone(ctx context.Context, raw json.RawMessage) (any, error) {
	var p taskDoneParams
	if err := rpc.Decode(raw, &p); err != nil {
		return nil, err
	}
	t, err := d.resolveTask(p.Task, p.Caller)
	if err != nil {
		return nil, err
	}
	if err := authorizeWorker(t, p.Caller); err != nil {
		return nil, err
	}
	if p.Summary = strings.TrimSpace(p.Summary); p.Summary == "" {
		return nil, rpc.Errorf(rpc.CodeBadRequest, "summary required")
	}
	status := store.TaskDone
	if p.Failed {
		status = store.TaskFailed
	}
	ok, err := d.store.FinishTask(t.ID, status, p.Summary, p.Report, time.Now())
	if err != nil {
		return nil, err
	}
	if !ok {
		return nil, rpc.Errorf(rpc.CodeConflict, "task %s is already %s", t.ID, t.Status)
	}
	done := store.Message{ID: store.NewID("m_"), TaskID: t.ID, Kind: "done", Body: p.Summary, Outcome: status, CreatedAt: time.Now()}
	if err := d.postMessage(t, done); err != nil {
		return nil, err
	}
	t, _ = d.store.Task(t.ID)
	return d.taskInfo(t, nil, nil), nil
}

type taskAskParams struct {
	Task      string `json:"task"`
	Caller    string `json:"caller"`
	Question  string `json:"question"`
	Resume    string `json:"resume"` // 接着等之前提过的问题
	TimeoutMs int    `json:"timeoutMs"`
}

type taskAskResult struct {
	Task     string `json:"task"`
	Question string `json:"question"`
	Answer   string `json:"answer"`
}

// taskAsk 记下 worker 的问题并阻塞到派发方回复；超时后问题仍在，可以用 resume 接着等。
func (d *Daemon) taskAsk(ctx context.Context, raw json.RawMessage) (any, error) {
	var p taskAskParams
	if err := rpc.Decode(raw, &p); err != nil {
		return nil, err
	}
	t, err := d.resolveTask(p.Task, p.Caller)
	if err != nil {
		return nil, err
	}
	if err := authorizeWorker(t, p.Caller); err != nil {
		return nil, err
	}
	var q store.Message
	if p.Resume != "" {
		q, err = d.store.Message(p.Resume)
		if err != nil || q.TaskID != t.ID || q.Kind != "question" {
			return nil, rpc.Errorf(rpc.CodeNotFound, "question %s not found in task %s", p.Resume, t.ID)
		}
	} else {
		if t.Status != store.TaskRunning {
			return nil, rpc.Errorf(rpc.CodeConflict, "task %s is already %s", t.ID, t.Status)
		}
		if p.Question = strings.TrimSpace(p.Question); p.Question == "" {
			return nil, rpc.Errorf(rpc.CodeBadRequest, "question required")
		}
		q = store.Message{ID: store.NewID("m_"), TaskID: t.ID, Kind: "question", Body: p.Question, CreatedAt: time.Now()}
		if err := d.store.AddMessage(q); err != nil {
			return nil, err
		}
		d.taskNotify()
		go d.deliver(t.Coordinator)
	}
	timeout := defaultAskTimeout
	if p.TimeoutMs > 0 {
		timeout = min(time.Duration(p.TimeoutMs)*time.Millisecond, time.Hour)
	}
	deadline := time.NewTimer(timeout)
	defer deadline.Stop()
	for {
		changed := d.taskChanged()
		if a, err := d.store.Answer(q.ID); err == nil {
			d.store.MarkRead([]string{a.ID}, time.Now())
			return taskAskResult{Task: t.ID, Question: q.ID, Answer: a.Body}, nil
		}
		if cur, err := d.store.Task(t.ID); err == nil && cur.Status != store.TaskRunning {
			return nil, rpc.Errorf(rpc.CodeConflict, "task %s was %s before an answer arrived", t.ID, cur.Status)
		}
		select {
		case <-changed:
		case <-ctx.Done():
			// 常驻进程退出时问题已经记下了，调用方要改成 resume 接着等，不能再问一遍
			e := rpc.Errorf(rpc.CodeRestarting, "daemon is shutting down; keep waiting with: posse task ask %s --resume %s", t.ID, q.ID)
			e.Data, _ = json.Marshal(map[string]string{"resume": q.ID})
			return nil, e
		case <-deadline.C:
			return nil, rpc.Errorf(rpc.CodeTimeout, "no answer yet to question %s; keep waiting with: posse task ask %s --resume %s", q.ID, t.ID, q.ID)
		}
	}
}

// taskCheck 取出派发方发给 worker 的未读回复和补充说明。
func (d *Daemon) taskCheck(ctx context.Context, raw json.RawMessage) (any, error) {
	var p taskSel
	if err := rpc.Decode(raw, &p); err != nil {
		return nil, err
	}
	t, err := d.resolveTask(p.Task, p.Caller)
	if err != nil {
		return nil, err
	}
	if err := authorizeWorker(t, p.Caller); err != nil {
		return nil, err
	}
	msgs, err := d.store.Inbox(store.Inbox{TaskID: t.ID, Unread: true})
	if err != nil {
		return nil, err
	}
	d.markRead(msgs)
	return orEmpty(msgs), nil
}

func orEmpty(msgs []store.Message) []store.Message {
	if msgs == nil {
		return []store.Message{}
	}
	return msgs
}

func (d *Daemon) markRead(msgs []store.Message) {
	var ids []string
	for _, m := range msgs {
		ids = append(ids, m.ID)
	}
	if len(ids) > 0 {
		d.store.MarkRead(ids, time.Now())
		d.taskNotify()
	}
}

// --- 派发方一侧

type taskWaitParams struct {
	Caller    string `json:"caller"`
	Block     bool   `json:"block"`
	TimeoutMs int    `json:"timeoutMs"`
}

// inboxItem 是派发方收件箱里的一条消息，附带任务的当前状态。
type inboxItem struct {
	store.Message
	Title  string `json:"title"`
	Status string `json:"status"`
	Report string `json:"report,omitempty"`
	Worker string `json:"worker"`
}

// taskWait 取出派发方的未读消息（完成、提问、worker 退出）；block 时阻塞到有消息为止。
func (d *Daemon) taskWait(ctx context.Context, raw json.RawMessage) (any, error) {
	var p taskWaitParams
	if err := rpc.Decode(raw, &p); err != nil {
		return nil, err
	}
	timeout := 10 * time.Minute
	if p.TimeoutMs > 0 {
		timeout = time.Duration(p.TimeoutMs) * time.Millisecond
	}
	deadline := time.NewTimer(timeout)
	defer deadline.Stop()
	if p.Block && p.Caller != "" {
		defer d.waitingOn(p.Caller)()
	}
	for {
		changed := d.taskChanged()
		msgs, err := d.store.Inbox(store.Inbox{Coordinator: &p.Caller, Unread: true})
		if err != nil {
			return nil, err
		}
		if len(msgs) > 0 || !p.Block {
			d.markRead(msgs)
			out := []inboxItem{}
			for _, m := range msgs {
				item := inboxItem{Message: m}
				if t, err := d.store.Task(m.TaskID); err == nil {
					item.Title, item.Status, item.Report, item.Worker = t.Title, t.Status, t.Report, t.Worker
				}
				if m.Outcome != "" {
					item.Status = m.Outcome
				}
				out = append(out, item)
			}
			return out, nil
		}
		running, err := d.store.Tasks(store.TaskFilter{Coordinator: &p.Caller, Running: true})
		if err != nil {
			return nil, err
		}
		if len(running) == 0 {
			return nil, rpc.Errorf(rpc.CodeConflict, "no running tasks and no unread messages; nothing to wait for")
		}
		select {
		case <-changed:
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-deadline.C:
			return nil, rpc.Errorf(rpc.CodeTimeout, "no new task messages within %s; %d tasks still running", timeout, len(running))
		}
	}
}

type taskReplyParams struct {
	Message string `json:"message"`
	Caller  string `json:"caller"`
	Body    string `json:"body"`
}

func (d *Daemon) taskReply(ctx context.Context, raw json.RawMessage) (any, error) {
	var p taskReplyParams
	if err := rpc.Decode(raw, &p); err != nil {
		return nil, err
	}
	q, err := d.store.Message(p.Message)
	if err != nil || q.Kind != "question" {
		return nil, rpc.Errorf(rpc.CodeNotFound, "question %s not found", p.Message)
	}
	t, err := d.store.Task(q.TaskID)
	if err != nil {
		return nil, err
	}
	if err := authorizeCoordinator(t, p.Caller); err != nil {
		return nil, err
	}
	if p.Body = strings.TrimSpace(p.Body); p.Body == "" {
		return nil, rpc.Errorf(rpc.CodeBadRequest, "reply text required")
	}
	if _, err := d.store.Answer(q.ID); err == nil {
		return nil, rpc.Errorf(rpc.CodeConflict, "question %s already answered", q.ID)
	}
	d.store.MarkRead([]string{q.ID}, time.Now())
	if err := d.addMessage(t, "answer", p.Body, q.ID); err != nil {
		return nil, err
	}
	return map[string]string{"task": t.ID, "question": q.ID}, nil
}

type taskSendParams struct {
	Task   string `json:"task"`
	Caller string `json:"caller"`
	Body   string `json:"body"`
}

// taskSend 给 worker 补充说明；worker 在检查点用 posse task check 读取，空闲时 posse 会提醒它。
func (d *Daemon) taskSend(ctx context.Context, raw json.RawMessage) (any, error) {
	var p taskSendParams
	if err := rpc.Decode(raw, &p); err != nil {
		return nil, err
	}
	t, err := d.resolveTask(p.Task, "")
	if err != nil {
		return nil, err
	}
	if err := authorizeCoordinator(t, p.Caller); err != nil {
		return nil, err
	}
	if p.Body = strings.TrimSpace(p.Body); p.Body == "" {
		return nil, rpc.Errorf(rpc.CodeBadRequest, "message text required")
	}
	res := map[string]string{"task": t.ID}
	reopened := false
	if t.Status != store.TaskRunning {
		if reopened, err = d.reopenTask(t); err != nil {
			return nil, err
		}
		if reopened {
			res["reopened"] = t.Status // 重新打开前的状态，CLI 据此告诉派发方会再收到一次汇报
		}
	}
	if err := d.addMessage(t, "note", p.Body, ""); err != nil {
		if reopened {
			// 消息没记下，worker 不会被提醒：退回原来的状态，免得任务一直挂在运行中
			d.store.FinishTask(t.ID, t.Status, t.Summary, t.Report, time.Now())
		}
		return nil, err
	}
	if reopened && !d.workerAlive(t) {
		// 检查之后、重新打开之前 agent 退出了：那时任务不在运行中，workerGone 没管它
		d.workerGone(t.Worker, "worker agent exited before reporting")
	}
	return res, nil
}

// reopenTask 让已汇报的任务回到运行中：worker 汇报后停在输入框，追加的活接着交给它，
// 保留它已有的上下文。worker 的 agent 已经退出、换成了别的 agent 时只能重派。
// 同时有别人刚把它重新打开时返回 false，消息照常追加。
func (d *Daemon) reopenTask(t store.Task) (bool, error) {
	if t.Status != store.TaskDone && t.Status != store.TaskFailed {
		return false, rpc.Errorf(rpc.CodeConflict, "task %s is %s; its worker is gone, use `posse task retry %s`", t.ID, t.Status, t.ID)
	}
	s := d.liveSession(t.Worker)
	if s == nil || !s.Running() {
		return false, rpc.Errorf(rpc.CodeConflict, "the worker terminal of task %s is closed; use `posse task retry %s`", t.ID, t.ID)
	}
	if !d.workerAlive(t) {
		return false, rpc.Errorf(rpc.CodeConflict, "the worker agent of task %s is no longer running in its terminal; use `posse task retry %s`", t.ID, t.ID)
	}
	ok, err := d.store.ReopenTask(t.ID)
	if err != nil || ok {
		return ok, err
	}
	if cur, err := d.store.Task(t.ID); err == nil && cur.Status == store.TaskRunning {
		return false, nil
	}
	return false, rpc.Errorf(rpc.CodeConflict, "task %s changed state; run `posse task show %s`", t.ID, t.ID)
}

// workerAlive 是任务的 worker 终端里还在运行派它时的那家 agent。
func (d *Daemon) workerAlive(t store.Task) bool {
	s := d.liveSession(t.Worker)
	if s == nil || !s.Running() {
		return false
	}
	name := s.agentName()
	return name != "" && (t.Agent == "" || name == t.Agent)
}

// taskClose 关掉 worker 终端；任务还没汇报就记为已停止。worktree 留着，由用户决定合并或删除。
func (d *Daemon) taskClose(ctx context.Context, raw json.RawMessage) (any, error) {
	var p taskSel
	if err := rpc.Decode(raw, &p); err != nil {
		return nil, err
	}
	t, err := d.resolveTask(p.Task, "")
	if err != nil {
		return nil, err
	}
	if err := authorizeCoordinator(t, p.Caller); err != nil {
		return nil, err
	}
	// 先改状态，worker 终端退出时就不会再当作意外退出通知派发方
	if t.Status == store.TaskRunning {
		if _, err := d.store.FinishTask(t.ID, store.TaskStopped, "", "", time.Now()); err != nil {
			return nil, err
		}
	}
	d.closeTerminal(t.Worker, 2*time.Second)
	d.taskNotify()
	t, _ = d.store.Task(t.ID)
	return d.taskInfo(t, nil, nil), nil
}

func (d *Daemon) taskClear(ctx context.Context, raw json.RawMessage) (any, error) {
	n, err := d.store.DeleteEndedTasks()
	if err != nil {
		return nil, err
	}
	d.taskNotify()
	return map[string]int64{"removed": n}, nil
}

func (d *Daemon) addMessage(t store.Task, kind, body, replyTo string) error {
	return d.postMessage(t, store.Message{ID: store.NewID("m_"), TaskID: t.ID, Kind: kind, Body: body, ReplyTo: replyTo, CreatedAt: time.Now()})
}

func (d *Daemon) postMessage(t store.Task, m store.Message) error {
	if err := d.store.AddMessage(m); err != nil {
		return err
	}
	d.taskNotify()
	if store.ToCoordinator(m.Kind) {
		go d.deliver(t.Coordinator)
	} else {
		go d.deliver(t.Worker)
	}
	return nil
}

// workerGone 在 worker 终端或其中的 agent 退出时调用：还没汇报的任务记为意外退出并通知派发方。
func (d *Daemon) workerGone(terminalID, reason string) {
	if d.stopping.Load() {
		return
	}
	ts, err := d.store.Tasks(store.TaskFilter{Worker: terminalID, Running: true})
	if err != nil {
		return
	}
	for _, t := range ts {
		if ok, err := d.store.FinishTask(t.ID, store.TaskExited, "", "", time.Now()); err == nil && ok {
			d.addMessage(t, "exit", reason, "")
		}
	}
}

// deliver 在终端里的 agent 空闲时输入一行提醒，告诉它有未读的任务消息。hook 已经交过的不再输入。
// agent 正在工作时什么也不做，等它空闲（hooks 或标题报告 idle）时会再调用。
func (d *Daemon) deliver(terminalID string) {
	if terminalID == "" || d.stopping.Load() {
		return
	}
	s := d.liveSession(terminalID)
	if s == nil || s.agentName() == "" || !s.Running() || s.Agent().Status != agent.StatusIdle && !agent.Parked(s.Agent()) {
		return
	}
	if wait := nudgeQuiet - time.Since(s.LastInput()); wait > 0 {
		time.AfterFunc(wait, func() { d.deliver(terminalID) })
		return
	}
	// 取提醒和记已提醒在锁里，hook 就不会再交同一条；打字在锁外，免得别的终端的 hook 跟着等
	text, _ := d.takeNudge(terminalID)
	if text == "" {
		return
	}
	if err := s.Inject([]byte(text)); err != nil {
		return
	}
	// 和 terminal send 一样：回车与文本分开到达，TUI 才会当作提交
	time.Sleep(150 * time.Millisecond)
	s.Inject([]byte("\r"))
}

// nudgeText 是给这个终端里的 agent 的一行提醒和它涵盖的消息；没有未提醒过的消息时为空。调用方持有 nudgeMu。
func (d *Daemon) nudgeText(terminalID string) (text string, ids []string) {
	var notes []string
	msgs, _ := d.store.Inbox(store.Inbox{Coordinator: &terminalID, Unread: true, Unnudged: true})
	for _, m := range msgs {
		ids = append(ids, m.ID)
		switch m.Kind {
		case "done":
			status := "finished"
			if m.Outcome == store.TaskFailed {
				status = "failed"
			} else if t, err := d.store.Task(m.TaskID); m.Outcome == "" && err == nil && t.Status == store.TaskFailed {
				status = "failed"
			}
			notes = append(notes, m.TaskID+" "+status)
		case "question":
			notes = append(notes, m.TaskID+" asked a question")
		case "exit":
			notes = append(notes, m.TaskID+"'s worker exited")
		}
	}
	if len(notes) > 0 {
		if len(notes) > 3 {
			notes = append(notes[:3], fmt.Sprintf("%d more", len(notes)-3))
		}
		text = "Posse: " + strings.Join(notes, ", ") + ". Run `posse task inbox` to read."
	} else {
		ts, _ := d.store.Tasks(store.TaskFilter{Worker: terminalID, Running: true})
		for _, t := range ts {
			msgs, _ := d.store.Inbox(store.Inbox{TaskID: t.ID, Unread: true, Unnudged: true})
			for _, m := range msgs {
				ids = append(ids, m.ID)
			}
			if len(msgs) > 0 {
				text = fmt.Sprintf("Posse: your coordinator sent a message for task %s. Run `posse task check %s`.", t.ID, t.ID)
			}
		}
	}
	return text, ids
}

// takeNudge 取走给这个终端的提醒并记为已提醒，返回它涵盖的消息。
func (d *Daemon) takeNudge(terminalID string) (string, []string) {
	d.nudgeMu.Lock()
	defer d.nudgeMu.Unlock()
	text, ids := d.nudgeText(terminalID)
	if text != "" {
		d.store.MarkNudged(ids, time.Now())
	}
	return text, ids
}

// stopDeliveryGrace 是本轮结束时交了提醒后，等 agent 接着做（发出下一个 hook 事件）的时间。测试里改短。
var stopDeliveryGrace = 90 * time.Second

// checkStopDelivery 在交了提醒的 Stop 之后 stopDeliveryGrace 调用：这期间没再收到 hook 事件，
// 说明 agent 没接着做（回应没送到、或它没理会），把提醒退回去，状态记回空闲，由 deliver 往终端里打字。
func (d *Daemon) checkStopDelivery(s *session, seq uint64, ids []string) {
	s.mu.Lock()
	quiet := s.hookSeq == seq
	s.mu.Unlock()
	if !quiet || !s.Running() || d.stopping.Load() {
		return
	}
	d.store.UnmarkNudged(ids)
	st := s.Agent()
	st.Status, st.At = agent.StatusIdle, time.Now()
	d.setAgent(s, st)
}

// --- worktree 改动与合并

type workspaceDiffResult struct {
	Workspace string `json:"workspace"`
	Branch    string `json:"branch"`
	Base      string `json:"base"`
	gitx.Changes
}

func (d *Daemon) worktreeOf(sel string) (store.Workspace, store.Project, error) {
	w, err := d.workspace(sel)
	if err != nil {
		return w, store.Project{}, err
	}
	if w.Kind != "worktree" {
		return w, store.Project{}, rpc.Errorf(rpc.CodeBadRequest, "workspace %s is not a git worktree", d.workspaceLabel(w))
	}
	p, err := d.store.FindProject(w.ProjectID)
	return w, p, err
}

func (d *Daemon) workspaceDiff(ctx context.Context, raw json.RawMessage) (any, error) {
	var p workspaceSel
	if err := rpc.Decode(raw, &p); err != nil {
		return nil, err
	}
	w, _, err := d.worktreeOf(p.Workspace)
	if err != nil {
		return nil, err
	}
	c, err := gitx.DiffSummary(ctx, w.Path, w.BaseRef)
	if err != nil {
		return nil, rpc.Errorf(rpc.CodeConflict, "%v", err)
	}
	return workspaceDiffResult{Workspace: d.workspaceLabel(w), Branch: w.Branch, Base: w.BaseRef, Changes: c}, nil
}

// workspaceMerge 把 worktree 的分支合进项目目录；要求两边已跟踪的文件都没有未提交的改动、
// 分支有新提交、项目目录正好在基准分支上。
func (d *Daemon) workspaceMerge(ctx context.Context, raw json.RawMessage) (any, error) {
	var p workspaceSel
	if err := rpc.Decode(raw, &p); err != nil {
		return nil, err
	}
	w, proj, err := d.worktreeOf(p.Workspace)
	if err != nil {
		return nil, err
	}
	c, err := gitx.DiffSummary(ctx, w.Path, w.BaseRef)
	if err != nil {
		return nil, rpc.Errorf(rpc.CodeConflict, "%v", err)
	}
	if c.Dirty {
		return nil, rpc.Errorf(rpc.CodeConflict, "worktree %s has uncommitted changes; commit them first", w.Name)
	}
	if c.Commits == 0 {
		msg := fmt.Sprintf("nothing to merge: %s has no commits since %s", w.Branch, w.BaseRef)
		if c.Untracked > 0 {
			msg += fmt.Sprintf(" (%d untracked files are not committed)", c.Untracked)
		}
		return nil, rpc.Errorf(rpc.CodeConflict, "%s", msg)
	}
	if cur, err := gitx.CurrentBranch(ctx, proj.Path); err != nil || cur != w.BaseRef {
		return nil, rpc.Errorf(rpc.CodeConflict, "project directory is on branch %s, not %s; switch it before merging", cur, w.BaseRef)
	}
	// 未跟踪文件和合入的文件撞名时 git 会拒绝合并，由下面的还原兜底
	if out, err := gitx.DirtyTracked(ctx, proj.Path); err != nil || out != "" {
		return nil, rpc.Errorf(rpc.CodeConflict, "project directory has uncommitted changes; commit or stash them first")
	}
	if err := gitx.Merge(ctx, proj.Path, w.Branch); err != nil {
		return nil, rpc.Errorf(rpc.CodeConflict, "merge failed and was rolled back: %v", err)
	}
	return map[string]string{"merged": w.Branch, "into": w.BaseRef}, nil
}
