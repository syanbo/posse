package cli

import (
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// 讨论（docs/features/discussions.md）：用户在讨论里 @ 成员发请求；成员的专用 worker 用
// posse discussion read 领取请求、posse task done --execution 汇报完整回复。

type discussionInfo struct {
	Key, LocalID, Title, Description, ProjectName, Status string
	MsgSeq                                                int64
	CreatedAt                                             time.Time
}

type discSummary struct {
	discussionInfo
	Last *struct {
		Seq        int64
		AuthorName string
		Body       string
	}
	Questions, Active, Pending, Members int
}

type discMember struct {
	ID, Name, Role, Agent, Model, Effort, WorkspaceLabel, TaskID, Worker, Paused string
	RemovedAt                                                                    *time.Time
}

type discMsg struct {
	ID, Kind, AuthorName, AuthorAgent, Body, Quote, Target, TargetName, Execution, Question, Outcome string
	Seq                                                                                              int64
	Attachments                                                                                      []struct{ Name, Path string }
	KeyAt                                                                                            *time.Time
	CreatedAt                                                                                        time.Time
}

type discExec struct {
	ID, Member, Message, State, Reason, TaskID, Worker, Agent, WorkspaceLabel string
}

func discKey(pos []string, name string) (string, error) {
	if len(pos) == 0 {
		if d := os.Getenv("POSSE_DISCUSSION"); d != "" {
			return d, nil
		}
		return "", need(pos, 1, name)
	}
	return pos[0], nil
}

func cmdDiscussionList(e *env, args []string) error {
	fs := flag.NewFlagSet("discussion list", flag.ContinueOnError)
	if _, err := parse(fs, args); err != nil {
		return err
	}
	var r struct{ Items []discSummary }
	return e.do("discussion.list", nil, &r, func() {
		if len(r.Items) == 0 {
			fmt.Fprintln(e.out, "no discussions")
			return
		}
		w := e.table()
		fmt.Fprintln(w, "DISCUSSION\tSTATUS\tPROJECT\tMEMBERS\tRUNNING\tQUEUED\tQUESTIONS\tTITLE")
		for _, d := range r.Items {
			fmt.Fprintf(w, "%s\t%s\t%s\t%d\t%d\t%d\t%d\t%s\n", d.Key, d.Status, d.ProjectName, d.Members, d.Active, d.Pending, d.Questions, d.Title)
		}
		w.Flush()
	})
}

func cmdDiscussionCreate(e *env, args []string) error {
	fs := flag.NewFlagSet("discussion create", flag.ContinueOnError)
	title := fs.String("title", "", "")
	desc := fs.String("description", "", "")
	project := fs.String("project", "", "")
	ws := fs.String("workspace", "", "")
	pos, err := parse(fs, args)
	if err != nil {
		return err
	}
	if *title == "" {
		*title = strings.Join(pos, " ")
	}
	if *project == "" && *ws == "" {
		*ws = os.Getenv("POSSE_WORKSPACE_ID")
	}
	var d discussionInfo
	return e.do("discussion.create", map[string]any{"title": *title, "description": *desc, "project": *project, "workspace": *ws}, &d, func() {
		fmt.Fprintf(e.out, "created discussion %s: %s\n", d.Key, d.Title)
		fmt.Fprintf(e.out, "add members with: posse discussion add-member %s --name N --agent claude|codex|grok|cursor\n", d.Key)
	})
}

func cmdDiscussionShow(e *env, args []string) error {
	fs := flag.NewFlagSet("discussion show", flag.ContinueOnError)
	limit := fs.Int("limit", 30, "")
	pos, err := parse(fs, args)
	if err != nil {
		return err
	}
	key, err := discKey(pos, "discussion show")
	if err != nil {
		return err
	}
	var v struct {
		Discussion discussionInfo
		Members    []discMember
		Messages   []discMsg
		Executions []discExec
		More       bool
	}
	return e.do("discussion.show", map[string]any{"discussion": key, "limit": *limit}, &v, func() {
		fmt.Fprintf(e.out, "%s  %s  %s\ntitle: %s\n", v.Discussion.Key, v.Discussion.Status, v.Discussion.ProjectName, v.Discussion.Title)
		if v.Discussion.Description != "" {
			fmt.Fprintf(e.out, "description: %s\n", v.Discussion.Description)
		}
		fmt.Fprintln(e.out, "\nmembers:")
		for _, m := range v.Members {
			state := ""
			if m.RemovedAt != nil {
				state = " (removed)"
			} else if m.Paused != "" {
				state = " (paused: " + m.Paused + ")"
			}
			fmt.Fprintf(e.out, "  %s  %s  %s  %s%s\n", m.ID, m.Name, m.Agent, m.WorkspaceLabel, state)
		}
		execs := map[string]discExec{}
		for _, x := range v.Executions {
			execs[x.ID] = x
		}
		fmt.Fprintln(e.out, "\nmessages:")
		if v.More {
			fmt.Fprintln(e.out, "  (earlier messages: posse discussion read "+v.Discussion.Key+" --from 1)")
		}
		for _, m := range v.Messages {
			who := discAuthor(m)
			line := fmt.Sprintf("  #%d %s %s", m.Seq, m.CreatedAt.Format("15:04"), who)
			if m.TargetName != "" {
				line += " → @" + m.TargetName
			}
			if m.KeyAt != nil {
				line += " [key]"
			}
			if x, ok := execs[m.Execution]; ok && m.Kind == "user" {
				line += fmt.Sprintf(" [%s %s]", x.ID, x.State)
			}
			fmt.Fprintf(e.out, "%s (%s)\n%s\n", line, m.ID, indent2(m.Body, "    "))
			for _, a := range m.Attachments {
				fmt.Fprintf(e.out, "    attachment: %s\n", a.Name)
			}
		}
	})
}

func discAuthor(m discMsg) string {
	switch m.Kind {
	case "reply":
		s := m.AuthorName + " replied"
		if m.Outcome == "failed" {
			s += " (failed)"
		}
		return s
	case "question":
		return m.AuthorName + " asked"
	case "answer":
		return "you answered"
	}
	return "you"
}

func cmdDiscussionAddMember(e *env, args []string) error {
	fs := flag.NewFlagSet("discussion add-member", flag.ContinueOnError)
	name := fs.String("name", "", "")
	role := fs.String("role", "", "")
	agentName := fs.String("agent", "", "")
	model := fs.String("model", "", "")
	effort := fs.String("effort", "", "")
	ws := fs.String("workspace", "", "")
	worktree := fs.String("worktree", "", "")
	pos, err := parse(fs, args)
	if err != nil {
		return err
	}
	key, err := discKey(pos, "discussion add-member")
	if err != nil {
		return err
	}
	var m discMember
	return e.do("discussion.member.add", map[string]any{"discussion": key, "name": *name, "role": *role, "agent": *agentName, "model": *model, "effort": *effort,
		"workspace": *ws, "worktree": *worktree}, &m, func() {
		fmt.Fprintf(e.out, "added %s (%s) as %s in %s\n", m.Name, m.Agent, m.ID, m.WorkspaceLabel)
	})
}

func cmdDiscussionUpdateMember(e *env, args []string) error {
	fs := flag.NewFlagSet("discussion update-member", flag.ContinueOnError)
	for _, f := range []string{"name", "role", "agent", "model", "effort", "workspace", "worktree"} {
		fs.String(f, "", "")
	}
	applyNow := fs.Bool("apply-now", false, "")
	pos, err := parse(fs, args)
	if err != nil {
		return err
	}
	if err := need(pos, 1, "discussion update-member"); err != nil {
		return err
	}
	var m discMember
	params := map[string]any{"member": pos[0], "applyNow": *applyNow}
	// 只带给了的旗标：没给的由常驻进程沿用原值
	fs.Visit(func(f *flag.Flag) {
		if f.Name != "apply-now" {
			params[f.Name] = f.Value.String()
		}
	})
	return e.do("discussion.member.update", params, &m, func() {
		fmt.Fprintf(e.out, "updated %s (%s) in %s\n", m.Name, m.Agent, m.WorkspaceLabel)
		if m.TaskID != "" {
			fmt.Fprintln(e.out, "its current worker keeps the old settings; pass --apply-now to restart it")
		}
	})
}

func cmdDiscussionIsolate(e *env, args []string) error {
	fs := flag.NewFlagSet("discussion isolate", flag.ContinueOnError)
	worktree := fs.String("worktree", "", "")
	pos, err := parse(fs, args)
	if err != nil {
		return err
	}
	if err := need(pos, 1, "discussion isolate"); err != nil {
		return err
	}
	var m discMember
	return e.do("discussion.isolate", map[string]any{"execution": pos[0], "worktree": *worktree}, &m, func() {
		fmt.Fprintf(e.out, "%s now works in %s; the request runs there\n", m.Name, m.WorkspaceLabel)
	})
}

func cmdDiscussionRemoveMember(e *env, args []string) error {
	fs := flag.NewFlagSet("discussion remove-member", flag.ContinueOnError)
	pos, err := parse(fs, args)
	if err != nil {
		return err
	}
	if err := need(pos, 1, "discussion remove-member"); err != nil {
		return err
	}
	return e.do("discussion.member.remove", map[string]any{"member": pos[0]}, nil, func() {
		fmt.Fprintf(e.out, "removed %s; its messages keep its name\n", pos[0])
	})
}

func cmdDiscussionRestart(e *env, args []string) error {
	fs := flag.NewFlagSet("discussion restart", flag.ContinueOnError)
	pos, err := parse(fs, args)
	if err != nil {
		return err
	}
	if err := need(pos, 1, "discussion restart"); err != nil {
		return err
	}
	return e.do("discussion.member.restart", map[string]any{"member": pos[0]}, nil, func() {
		fmt.Fprintf(e.out, "restarting %s; its queued requests continue in a new worker\n", pos[0])
	})
}

func cmdDiscussionPost(e *env, args []string) error {
	fs := flag.NewFlagSet("discussion post", flag.ContinueOnError)
	to := fs.String("to", "", "")
	quote := fs.String("quote", "", "")
	client := fs.String("client-id", "", "")
	var attach multiFlag
	fs.Var(&attach, "attach", "")
	pos, err := parse(fs, args)
	if err != nil {
		return err
	}
	if err := need(pos, 2, "discussion post"); err != nil {
		return err
	}
	var atts []map[string]string
	for _, a := range attach {
		abs, err := filepath.Abs(a)
		if err != nil {
			return err
		}
		atts = append(atts, map[string]string{"path": abs})
	}
	var r struct {
		Message   discMsg
		Execution *discExec
		Duplicate bool
	}
	params := map[string]any{"discussion": pos[0], "body": strings.Join(pos[1:], " "), "target": *to, "quote": *quote, "clientId": *client, "attachments": atts}
	return e.do("discussion.post", params, &r, func() {
		if r.Duplicate {
			fmt.Fprintf(e.out, "already sent as #%d (%s)\n", r.Message.Seq, r.Message.ID)
		} else {
			fmt.Fprintf(e.out, "posted #%d (%s)\n", r.Message.Seq, r.Message.ID)
		}
		if r.Execution != nil {
			fmt.Fprintf(e.out, "request %s to @%s is %s\n", r.Execution.ID, r.Message.TargetName, r.Execution.State)
		}
	})
}

func cmdDiscussionAnswer(e *env, args []string) error {
	fs := flag.NewFlagSet("discussion answer", flag.ContinueOnError)
	pos, err := parse(fs, args)
	if err != nil {
		return err
	}
	if err := need(pos, 2, "discussion answer"); err != nil {
		return err
	}
	return e.do("discussion.answer", map[string]any{"question": pos[0], "body": strings.Join(pos[1:], " ")}, nil, func() {
		fmt.Fprintf(e.out, "answered %s\n", pos[0])
	})
}

// cmdDiscussionRead 是 worker 领取这一轮的请求和上下文；输出之后才确认接收，确认丢了下次重读同一页。
// 不带 --execution 时按消息序号补读讨论（不算接收）。
func cmdDiscussionRead(e *env, args []string) error {
	fs := flag.NewFlagSet("discussion read", flag.ContinueOnError)
	exec := fs.String("execution", "", "")
	cursor := fs.Int("cursor", 0, "")
	from := fs.Int64("from", 0, "")
	to := fs.Int64("to", 0, "")
	pos, err := parse(fs, args)
	if err != nil {
		return err
	}
	if *exec == "" {
		key, err := discKey(pos, "discussion read")
		if err != nil {
			return err
		}
		var r struct {
			Text       string
			Next, Last int64
		}
		return e.do("discussion.read", map[string]any{"discussion": key, "from": *from, "to": *to, "caller": caller()}, &r, func() {
			fmt.Fprint(e.out, r.Text)
			if r.Next > 0 {
				fmt.Fprintf(e.out, "\n(more: posse discussion read %s --from %d --to %d)\n", key, r.Next, max(*to, r.Last))
			}
		})
	}
	var page struct {
		Execution       string
		Text            string
		From, To, Total int
		Complete        bool
	}
	if err := e.do("discussion.read", map[string]any{"execution": *exec, "cursor": *cursor, "caller": caller()}, &page, func() {
		fmt.Fprint(e.out, page.Text)
		if !strings.HasSuffix(page.Text, "\n") {
			fmt.Fprintln(e.out)
		}
		if page.Complete {
			fmt.Fprintf(e.out, "[context complete: %d bytes]\n", page.Total)
		} else {
			fmt.Fprintf(e.out, "[context continues: bytes %d-%d of %d; read the rest with: posse discussion read --execution %s --cursor %d]\n",
				page.From, page.To, page.Total, page.Execution, page.To)
		}
	}); err != nil {
		return err
	}
	// 输出之后才确认：确认只说明这一页交到了 agent 手里
	if err := e.call("discussion.ack", map[string]any{"execution": *exec, "from": page.From, "to": page.To, "caller": caller()}, nil); err != nil {
		fmt.Fprintf(e.errOut, "posse: could not confirm receipt (%v); run the same command again\n", err)
		return err
	}
	return nil
}

func cmdDiscussionPending(e *env, args []string) error {
	fs := flag.NewFlagSet("discussion pending", flag.ContinueOnError)
	pos, err := parse(fs, args)
	if err != nil {
		return err
	}
	var r struct {
		Task, Discussion, Execution, State string
		Read                               bool
	}
	return e.do("discussion.pending", map[string]any{"task": first(pos), "caller": caller()}, &r, func() {
		if r.Execution == "" {
			fmt.Fprintf(e.out, "no request in progress for task %s; wait at the prompt for the next one\n", r.Task)
			return
		}
		fmt.Fprintf(e.out, "task %s, execution %s (%s)\n", r.Task, r.Execution, r.State)
		if !r.Read {
			fmt.Fprintf(e.out, "read it with: posse discussion read --execution %s\n", r.Execution)
		}
		fmt.Fprintf(e.out, "report with: posse task done %s --execution %s --body-file <file>\n", r.Task, r.Execution)
	})
}

func cmdDiscussionArchive(e *env, args []string) error { return discArchive(e, args, true) }
func cmdDiscussionRestore(e *env, args []string) error { return discArchive(e, args, false) }

func discArchive(e *env, args []string, archived bool) error {
	fs := flag.NewFlagSet("discussion archive", flag.ContinueOnError)
	pos, err := parse(fs, args)
	if err != nil {
		return err
	}
	if err := need(pos, 1, "discussion archive"); err != nil {
		return err
	}
	var d discussionInfo
	return e.do("discussion.archive", map[string]any{"discussion": pos[0], "archived": archived}, &d, func() {
		fmt.Fprintf(e.out, "%s is %s\n", d.Key, d.Status)
	})
}

func cmdDiscussionSearch(e *env, args []string) error {
	fs := flag.NewFlagSet("discussion search", flag.ContinueOnError)
	pos, err := parse(fs, args)
	if err != nil {
		return err
	}
	if err := need(pos, 2, "discussion search"); err != nil {
		return err
	}
	var hits []discMsg
	return e.do("discussion.search", map[string]any{"discussion": pos[0], "query": strings.Join(pos[1:], " ")}, &hits, func() {
		if len(hits) == 0 {
			fmt.Fprintln(e.out, "no matches")
			return
		}
		for _, m := range hits {
			fmt.Fprintf(e.out, "#%d %s %s: %s\n", m.Seq, m.CreatedAt.Format("01-02 15:04"), discAuthor(m), firstLineOf(m.Body, 100))
		}
	})
}

func firstLineOf(s string, n int) string {
	line, _, _ := strings.Cut(strings.TrimSpace(s), "\n")
	if r := []rune(line); len(r) > n {
		return string(r[:n]) + "…"
	}
	return line
}

func cmdDiscussionKey(e *env, args []string) error {
	fs := flag.NewFlagSet("discussion key", flag.ContinueOnError)
	off := fs.Bool("off", false, "")
	pos, err := parse(fs, args)
	if err != nil {
		return err
	}
	if err := need(pos, 1, "discussion key"); err != nil {
		return err
	}
	return e.do("discussion.key", map[string]any{"message": pos[0], "on": !*off}, nil, func() {
		if *off {
			fmt.Fprintf(e.out, "%s is no longer a key message\n", pos[0])
		} else {
			fmt.Fprintf(e.out, "marked %s as a key message\n", pos[0])
		}
	})
}

// discExecCmd 是对一轮执行的操作：cancel、stop、retry、confirm。
func discExecCmd(method, verb string) func(e *env, args []string) error {
	return func(e *env, args []string) error {
		fs := flag.NewFlagSet("discussion "+verb, flag.ContinueOnError)
		pos, err := parse(fs, args)
		if err != nil {
			return err
		}
		if err := need(pos, 1, "discussion "+verb); err != nil {
			return err
		}
		var x discExec
		return e.do(method, map[string]any{"execution": pos[0]}, &x, func() {
			fmt.Fprintf(e.out, "%s: %s\n", x.ID, x.State)
		})
	}
}
