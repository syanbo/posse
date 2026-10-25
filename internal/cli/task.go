package cli

import (
	_ "embed"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"posse/internal/agent"
)

//go:embed task_guide.md
var taskGuide string

type taskMessage struct {
	ID, TaskID, Kind, Body, ReplyTo string
	CreatedAt                       time.Time
	ReadAt                          *time.Time
}

type taskInfo struct {
	ID, Title, Spec, Coordinator, WorkspaceID, Worker, Agent, Model, Effort, Group, Status, Summary, Report string
	Workspace, WorkspaceKind, Branch, BaseRef                                                               string
	CreatedAt                                                                                               time.Time
	EndedAt                                                                                                 *time.Time
	Terminal                                                                                                *terminalInfo
	Questions                                                                                               int
	Messages                                                                                                []taskMessage
	Screen                                                                                                  []string
}

type inboxItem struct {
	taskMessage
	Title, Status, Report, Worker string
}

// caller 是调用方所在的 posse 终端；在 agent 里运行时就是这个 agent 自己。
func caller() string { return os.Getenv("POSSE_TERMINAL_ID") }

func cmdTaskStart(e *env, args []string) error {
	fs := flag.NewFlagSet("task start", flag.ContinueOnError)
	agentName := fs.String("agent", "", "")
	model := fs.String("model", "", "")
	effort := fs.String("effort", "", "")
	title := fs.String("title", "", "")
	spec := fs.String("spec", "", "")
	specFile := fs.String("spec-file", "", "")
	ws := fs.String("workspace", "", "")
	worktree := fs.String("worktree", "", "")
	base := fs.String("base", "", "")
	pos, command, err := splitCommand(fs, args)
	if err != nil {
		return err
	}
	text, err := readSpec(*spec, *specFile)
	if err != nil {
		return err
	}
	if text == "" {
		text = strings.Join(pos, " ")
	}
	cwd, _ := os.Getwd()
	params := map[string]any{"caller": caller(), "cwd": cwd, "agent": *agentName, "model": *model, "effort": *effort, "command": command, "title": *title,
		"spec": text, "workspace": *ws, "worktree": *worktree, "base": *base}
	var t taskInfo
	return e.do("task.start", params, &t, func() { e.printStarted(t) })
}

// readSpec 取 --spec 或 --spec-file（- 表示标准输入）的内容，都没给时返回空。
func readSpec(spec, file string) (string, error) {
	var b []byte
	var err error
	switch file {
	case "":
		return spec, nil
	case "-":
		b, err = io.ReadAll(os.Stdin)
	default:
		b, err = os.ReadFile(file)
	}
	return string(b), err
}

func (e *env) printStarted(t taskInfo) {
	who := agentLabel(t)
	if who == "-" {
		who = "worker"
	}
	fmt.Fprintf(e.out, "started task %s: %s in %s (terminal %s)\n", t.ID, who, t.Workspace, t.Worker)
	if t.Branch != "" && t.WorkspaceKind == "worktree" {
		fmt.Fprintf(e.out, "worktree branch %s from %s\n", t.Branch, t.BaseRef)
	}
	fmt.Fprintln(e.out, "wait for results with: posse task wait")
}

// retryable 是可以重试的任务状态：worker 已经确定不在做了。
var retryable = []string{"failed", "exited", "lost", "stopped"}

// agentLabel 是 agent 名，带上指定的模型和思考深度，如 codex:gpt-5.5/high。
func agentLabel(t taskInfo) string {
	s := dash(t.Agent)
	if t.Model != "" {
		s += ":" + t.Model
	}
	if t.Effort != "" {
		s += "/" + t.Effort
	}
	return s
}

// cmdTaskRetry 用原任务的说明、agent、模型和工作区重新派发；在客户端组合 show 和 start，不改常驻进程。
func cmdTaskRetry(e *env, args []string) error {
	fs := flag.NewFlagSet("task retry", flag.ContinueOnError)
	model := fs.String("model", "", "")
	effort := fs.String("effort", "", "")
	spec := fs.String("spec", "", "")
	specFile := fs.String("spec-file", "", "")
	pos, err := parse(fs, args)
	if err != nil {
		return err
	}
	if err := need(pos, 1, "task retry"); err != nil {
		return err
	}
	var old taskInfo
	if err := e.call("task.show", map[string]any{"task": pos[0], "caller": caller()}, &old); err != nil {
		return err
	}
	params, err := retryParams(old, *model, *effort, *spec, *specFile)
	if err != nil {
		return err
	}
	var t taskInfo
	return e.do("task.start", params, &t, func() {
		fmt.Fprintf(e.out, "retrying %s\n", old.ID)
		e.printStarted(t)
	})
}

func retryParams(old taskInfo, model, effort, spec, specFile string) (map[string]any, error) {
	if !slices.Contains(retryable, old.Status) {
		return nil, fmt.Errorf("task %s is %s; only %s tasks can be retried", old.ID, old.Status, strings.Join(retryable, ", "))
	}
	if old.Agent == "" {
		return nil, fmt.Errorf("task %s ran a command, not an agent; start it again with posse task start", old.ID)
	}
	text, err := readSpec(spec, specFile)
	if err != nil {
		return nil, err
	}
	if strings.TrimSpace(text) == "" {
		text = old.Spec
	}
	title := old.Title
	if !strings.HasSuffix(title, " (retry)") {
		title += " (retry)"
	}
	// 只换模型时沿用原来的思考深度，换不同家的模型时可能要一起改
	if model == "" {
		model = old.Model
	}
	if effort == "" {
		effort = old.Effort
	}
	cwd, _ := os.Getwd()
	return map[string]any{"caller": caller(), "cwd": cwd, "agent": old.Agent, "model": model, "effort": effort, "title": title, "spec": text, "workspace": old.WorkspaceID}, nil
}

// splitCommand 把 "--" 之后的部分当作 worker 要运行的命令，之前的照常解析。
func splitCommand(fs *flag.FlagSet, args []string) ([]string, []string, error) {
	var command []string
	for i, a := range args {
		if a == "--" {
			args, command = args[:i], args[i+1:]
			break
		}
	}
	pos, err := parse(fs, args)
	return pos, command, err
}

func cmdTaskList(e *env, args []string) error {
	fs := flag.NewFlagSet("task list", flag.ContinueOnError)
	all := fs.Bool("all", false, "")
	if _, err := parse(fs, args); err != nil {
		return err
	}
	var ts []taskInfo
	return e.do("task.list", map[string]any{"caller": caller(), "all": *all}, &ts, func() {
		if len(ts) == 0 {
			fmt.Fprintln(e.out, "no tasks")
			return
		}
		w := e.table()
		fmt.Fprintln(w, "TASK\tSTATUS\tAGENT\tWORKSPACE\tAGE\tTITLE")
		for _, t := range ts {
			fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%s\t%s\n", t.ID, taskState(t), agentLabel(t), t.Workspace, age(t.CreatedAt), t.Title)
		}
		w.Flush()
	})
}

// taskState 是任务状态加上 worker 里 agent 的状态和待回复的问题数。
func taskState(t taskInfo) string {
	s := t.Status
	if t.Status == "running" && t.Terminal != nil && t.Terminal.AgentState != nil {
		s += "/" + t.Terminal.AgentState.Status
	}
	if t.Questions > 0 {
		s += fmt.Sprintf(", %d question", t.Questions)
		if t.Questions > 1 {
			s += "s"
		}
	}
	return s
}

func age(t time.Time) string {
	d := time.Since(t)
	switch {
	case d < time.Minute:
		return fmt.Sprintf("%ds", int(d.Seconds()))
	case d < time.Hour:
		return fmt.Sprintf("%dm", int(d.Minutes()))
	case d < 48*time.Hour:
		return fmt.Sprintf("%dh", int(d.Hours()))
	}
	return fmt.Sprintf("%dd", int(d.Hours()/24))
}

func cmdTaskShow(e *env, args []string) error {
	fs := flag.NewFlagSet("task show", flag.ContinueOnError)
	pos, err := parse(fs, args)
	if err != nil {
		return err
	}
	var t taskInfo
	return e.do("task.show", map[string]any{"task": first(pos), "caller": caller()}, &t, func() {
		fmt.Fprintf(e.out, "%s  %s  %s  %s  worker %s", t.ID, taskState(t), agentLabel(t), t.Workspace, t.Worker)
		if t.Terminal == nil {
			fmt.Fprint(e.out, " (closed)")
		}
		fmt.Fprintf(e.out, "\ntitle: %s\n", t.Title)
		if t.Summary != "" {
			fmt.Fprintf(e.out, "summary: %s\n", t.Summary)
		}
		if t.Report != "" {
			fmt.Fprintf(e.out, "report: %s\n", t.Report)
		}
		fmt.Fprintf(e.out, "\nspec:\n%s\n", indent(t.Spec))
		if len(t.Messages) > 0 {
			fmt.Fprintln(e.out, "\nmessages:")
			for _, m := range t.Messages {
				fmt.Fprintf(e.out, "  %s %s %s\n%s\n", m.CreatedAt.Format("15:04"), m.Kind, m.ID, indent2(m.Body, "    "))
			}
		}
		if len(t.Screen) > 0 {
			lines := t.Screen
			if len(lines) > 20 {
				lines = lines[len(lines)-20:]
			}
			fmt.Fprintf(e.out, "\nworker screen (last %d lines):\n%s\n", len(lines), indent(strings.Join(lines, "\n")))
		}
	})
}

func indent(s string) string { return indent2(s, "  ") }

func indent2(s, prefix string) string {
	return prefix + strings.ReplaceAll(strings.TrimRight(s, "\n"), "\n", "\n"+prefix)
}

func first(pos []string) string {
	if len(pos) > 0 {
		return pos[0]
	}
	return ""
}

func cmdTaskWait(e *env, args []string) error { return taskInbox(e, args, true) }

func cmdTaskInbox(e *env, args []string) error { return taskInbox(e, args, false) }

func taskInbox(e *env, args []string, block bool) error {
	fs := flag.NewFlagSet("task wait", flag.ContinueOnError)
	timeout := fs.Duration("timeout", 10*time.Minute, "")
	if _, err := parse(fs, args); err != nil {
		return err
	}
	var items []inboxItem
	params := map[string]any{"caller": caller(), "block": block, "timeoutMs": timeout.Milliseconds()}
	return e.doWait("task.wait", params, &items, func() {
		if len(items) == 0 {
			fmt.Fprintln(e.out, "no new task messages")
			return
		}
		for _, m := range items {
			switch m.Kind {
			case "done":
				fmt.Fprintf(e.out, "%s %s: %s\n%s\n", m.TaskID, m.Status, m.Title, indent(m.Body))
				if m.Report != "" {
					fmt.Fprintf(e.out, "  report: %s\n", m.Report)
				}
			case "question":
				fmt.Fprintf(e.out, "%s question %s: %s\n%s\n", m.TaskID, m.ID, m.Title, indent(m.Body))
				fmt.Fprintf(e.out, "  answer with: posse task reply %s \"<answer>\"\n", m.ID)
			default:
				fmt.Fprintf(e.out, "%s %s: %s\n%s\n", m.TaskID, m.Kind, m.Title, indent(m.Body))
			}
		}
	})
}

func cmdTaskReply(e *env, args []string) error {
	fs := flag.NewFlagSet("task reply", flag.ContinueOnError)
	pos, err := parse(fs, args)
	if err != nil {
		return err
	}
	if err := need(pos, 2, "task reply"); err != nil {
		return err
	}
	params := map[string]any{"message": pos[0], "body": strings.Join(pos[1:], " "), "caller": caller()}
	return e.do("task.reply", params, nil, func() { fmt.Fprintf(e.out, "answered %s\n", pos[0]) })
}

func cmdTaskSend(e *env, args []string) error {
	fs := flag.NewFlagSet("task send", flag.ContinueOnError)
	pos, err := parse(fs, args)
	if err != nil {
		return err
	}
	if err := need(pos, 2, "task send"); err != nil {
		return err
	}
	params := map[string]any{"task": pos[0], "body": strings.Join(pos[1:], " "), "caller": caller()}
	return e.do("task.send", params, nil, func() { fmt.Fprintf(e.out, "sent to %s\n", pos[0]) })
}

func cmdTaskClose(e *env, args []string) error {
	fs := flag.NewFlagSet("task close", flag.ContinueOnError)
	pos, err := parse(fs, args)
	if err != nil {
		return err
	}
	if err := need(pos, 1, "task close"); err != nil {
		return err
	}
	var t taskInfo
	return e.do("task.close", map[string]any{"task": pos[0], "caller": caller()}, &t, func() {
		fmt.Fprintf(e.out, "closed worker of %s (%s)\n", t.ID, t.Status)
	})
}

func cmdTaskDone(e *env, args []string) error {
	fs := flag.NewFlagSet("task done", flag.ContinueOnError)
	summary := fs.String("summary", "", "")
	report := fs.String("report", "", "")
	failed := fs.Bool("failed", false, "")
	pos, err := parse(fs, args)
	if err != nil {
		return err
	}
	task, pos := splitTaskArg(pos)
	if *summary == "" {
		*summary = strings.Join(pos, " ")
	}
	if *report != "" {
		if abs, err := filepath.Abs(*report); err == nil {
			*report = abs
		}
	}
	params := map[string]any{"task": task, "caller": caller(), "summary": *summary, "report": *report, "failed": *failed}
	var t taskInfo
	return e.do("task.done", params, &t, func() { fmt.Fprintf(e.out, "reported %s as %s\n", t.ID, t.Status) })
}

// splitTaskArg 拆出开头可省略的任务 ID（k_ 开头）；省略时常驻进程按调用方的终端找任务。
func splitTaskArg(pos []string) (task string, rest []string) {
	if len(pos) > 0 && strings.HasPrefix(pos[0], "k_") {
		return pos[0], pos[1:]
	}
	return "", pos
}

func cmdTaskAsk(e *env, args []string) error {
	fs := flag.NewFlagSet("task ask", flag.ContinueOnError)
	resume := fs.String("resume", "", "")
	timeout := fs.Duration("timeout", 9*time.Minute, "")
	pos, err := parse(fs, args)
	if err != nil {
		return err
	}
	task, pos := splitTaskArg(pos)
	params := map[string]any{"task": task, "caller": caller(), "question": strings.Join(pos, " "), "resume": *resume,
		"timeoutMs": timeout.Milliseconds()}
	var r struct{ Answer string }
	return e.doWait("task.ask", params, &r, func() { fmt.Fprintln(e.out, r.Answer) })
}

func cmdTaskCheck(e *env, args []string) error {
	fs := flag.NewFlagSet("task check", flag.ContinueOnError)
	pos, err := parse(fs, args)
	if err != nil {
		return err
	}
	var msgs []taskMessage
	return e.do("task.check", map[string]any{"task": first(pos), "caller": caller()}, &msgs, func() {
		if len(msgs) == 0 {
			fmt.Fprintln(e.out, "no new messages from the coordinator")
			return
		}
		for _, m := range msgs {
			label := "note"
			if m.Kind == "answer" {
				label = "answer to " + m.ReplyTo
			}
			fmt.Fprintf(e.out, "%s:\n%s\n", label, indent(m.Body))
		}
	})
}

func cmdTaskClear(e *env, args []string) error {
	var r struct{ Removed int }
	return e.do("task.clear", nil, &r, func() { fmt.Fprintf(e.out, "removed %d finished tasks\n", r.Removed) })
}

func cmdTaskGuide(e *env, args []string) error {
	fs := flag.NewFlagSet("task guide", flag.ContinueOnError)
	skill := fs.Bool("skill", false, "")
	if _, err := parse(fs, args); err != nil {
		return err
	}
	if *skill {
		fmt.Fprint(e.out, agent.SkillFrontmatter())
	}
	fmt.Fprint(e.out, taskGuide)
	return nil
}

// --- worktree 改动

func cmdWorkspaceDiff(e *env, args []string) error {
	fs := flag.NewFlagSet("workspace diff", flag.ContinueOnError)
	pos, err := parse(fs, args)
	if err != nil {
		return err
	}
	if err := need(pos, 1, "workspace diff"); err != nil {
		return err
	}
	var r struct {
		Workspace, Branch, Base                          string
		Commits, Files, Insertions, Deletions, Untracked int
		Dirty                                            bool
	}
	return e.do("workspace.diff", map[string]any{"workspace": pos[0]}, &r, func() {
		fmt.Fprintf(e.out, "%s: branch %s vs %s\n%d commits, %d files changed, +%d -%d", r.Workspace, r.Branch, r.Base,
			r.Commits, r.Files, r.Insertions, r.Deletions)
		if r.Untracked > 0 {
			fmt.Fprintf(e.out, ", %d untracked", r.Untracked)
		}
		if r.Dirty {
			fmt.Fprint(e.out, " (uncommitted changes)")
		}
		fmt.Fprintln(e.out)
	})
}

func cmdWorkspaceMerge(e *env, args []string) error {
	fs := flag.NewFlagSet("workspace merge", flag.ContinueOnError)
	pos, err := parse(fs, args)
	if err != nil {
		return err
	}
	if err := need(pos, 1, "workspace merge"); err != nil {
		return err
	}
	var r struct{ Merged, Into string }
	return e.do("workspace.merge", map[string]any{"workspace": pos[0]}, &r, func() {
		fmt.Fprintf(e.out, "merged %s into %s\n", r.Merged, r.Into)
	})
}
