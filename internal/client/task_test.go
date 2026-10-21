package client

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"hive/internal/rpc"
)

type testTask struct {
	ID, Worker, Status, Summary, Report, Workspace, Branch string
	Questions                                              int
}

type testInboxItem struct {
	ID, TaskID, Kind, Body, Status string
}

func addFolderProject(t *testing.T, c *Client) string {
	t.Helper()
	dir := t.TempDir()
	if err := c.Call(context.Background(), "project.add", map[string]any{"path": dir, "name": filepath.Base(dir)}, nil); err != nil {
		t.Fatal(err)
	}
	return filepath.Base(dir) + "/main"
}

func startTask(t *testing.T, c *Client, params map[string]any) testTask {
	t.Helper()
	var task testTask
	if err := c.Call(context.Background(), "task.start", params, &task); err != nil {
		t.Fatal(err)
	}
	return task
}

func inbox(t *testing.T, c *Client, caller string, block bool) []testInboxItem {
	t.Helper()
	var items []testInboxItem
	if err := c.Call(context.Background(), "task.wait", map[string]any{"caller": caller, "block": block, "timeoutMs": 5000}, &items); err != nil {
		t.Fatal(err)
	}
	return items
}

func rpcCode(err error) string {
	var re *rpc.Error
	if errors.As(err, &re) {
		return re.Code
	}
	return ""
}

func TestTaskLifecycle(t *testing.T) {
	c := startDaemon(t)
	ctx := context.Background()
	ws := addFolderProject(t, c)
	sleeper := []string{"/bin/sh", "-c", "echo task=$HIVE_TASK_ID; sleep 30"}
	task := startTask(t, c, map[string]any{"workspace": ws, "command": sleeper, "spec": "count the files\nmore detail"})
	if task.Status != "running" || task.Worker == "" {
		t.Fatalf("start = %+v", task)
	}

	// worker 提问后阻塞，派发方从收件箱读到问题并回复
	answer := make(chan string, 1)
	go func() {
		var r struct{ Answer string }
		if err := c.Call(ctx, "task.ask", map[string]any{"task": task.ID, "caller": task.Worker, "question": "which dir?"}, &r); err != nil {
			answer <- "error: " + err.Error()
			return
		}
		answer <- r.Answer
	}()
	items := inbox(t, c, "", true)
	if len(items) != 1 || items[0].Kind != "question" || items[0].Body != "which dir?" {
		t.Fatalf("inbox = %+v", items)
	}
	var tasks []testTask
	c.Call(ctx, "task.list", map[string]any{}, &tasks)
	if len(tasks) != 1 || tasks[0].Questions != 1 {
		t.Fatalf("open question not counted: %+v", tasks)
	}
	if err := c.Call(ctx, "task.reply", map[string]any{"message": items[0].ID, "body": "the root"}, nil); err != nil {
		t.Fatal(err)
	}
	select {
	case a := <-answer:
		if a != "the root" {
			t.Fatalf("ask got %q", a)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("ask did not return after the reply")
	}
	if err := c.Call(ctx, "task.reply", map[string]any{"message": items[0].ID, "body": "again"}, nil); rpcCode(err) != rpc.CodeConflict {
		t.Fatalf("second reply = %v", err)
	}

	// 补充说明只读一次
	if err := c.Call(ctx, "task.send", map[string]any{"task": task.ID, "body": "skip hidden files"}, nil); err != nil {
		t.Fatal(err)
	}
	var notes []testInboxItem
	c.Call(ctx, "task.check", map[string]any{"task": task.ID, "caller": task.Worker}, &notes)
	if len(notes) != 1 || notes[0].Body != "skip hidden files" {
		t.Fatalf("check = %+v", notes)
	}
	c.Call(ctx, "task.check", map[string]any{"task": task.ID, "caller": task.Worker}, &notes)
	if len(notes) != 0 {
		t.Fatalf("note delivered twice: %+v", notes)
	}

	// 只有 worker 自己能汇报，且只能汇报一次；不给任务 ID 时按调用方终端找
	if err := c.Call(ctx, "task.done", map[string]any{"task": task.ID, "caller": "t_other", "summary": "x"}, nil); rpcCode(err) != rpc.CodeConflict {
		t.Fatalf("done from another terminal = %v", err)
	}
	if err := c.Call(ctx, "task.done", map[string]any{"caller": task.Worker, "summary": "42 files", "report": "/tmp/r.md"}, nil); err != nil {
		t.Fatal(err)
	}
	if err := c.Call(ctx, "task.done", map[string]any{"task": task.ID, "caller": task.Worker, "summary": "again"}, nil); rpcCode(err) != rpc.CodeConflict {
		t.Fatalf("second done = %v", err)
	}
	items = inbox(t, c, "", true)
	if len(items) != 1 || items[0].Kind != "done" || items[0].Body != "42 files" || items[0].Status != "done" {
		t.Fatalf("done inbox = %+v", items)
	}
	var shown struct {
		testTask
		Screen []string
	}
	// worker 的 shell 起来、echo 出来要一点时间，机器忙时上面那串调用做完它还没输出
	for deadline := time.Now().Add(5 * time.Second); ; time.Sleep(20 * time.Millisecond) {
		c.Call(ctx, "task.show", map[string]any{"task": task.ID}, &shown)
		if strings.Contains(strings.Join(shown.Screen, "\n"), "task="+task.ID) || time.Now().After(deadline) {
			break
		}
	}
	if !strings.Contains(strings.Join(shown.Screen, "\n"), "task="+task.ID) {
		t.Fatalf("worker did not get HIVE_TASK_ID; screen %q", shown.Screen)
	}

	// worker 没汇报就退出：派发方收到 exit
	quitter := startTask(t, c, map[string]any{"workspace": ws, "command": []string{"/bin/sh", "-c", "exit 3"}, "spec": "quit"})
	items = inbox(t, c, "", true)
	if len(items) != 1 || items[0].Kind != "exit" || items[0].TaskID != quitter.ID || !strings.Contains(items[0].Body, "code 3") {
		t.Fatalf("exit inbox = %+v", items)
	}

	// 主动关闭：记为已停止，不当作意外退出
	stopped := startTask(t, c, map[string]any{"workspace": ws, "command": sleeper, "spec": "stop me"})
	var closed testTask
	if err := c.Call(ctx, "task.close", map[string]any{"task": stopped.ID}, &closed); err != nil {
		t.Fatal(err)
	}
	if closed.Status != "stopped" {
		t.Fatalf("closed task status %s", closed.Status)
	}
	if items := inbox(t, c, "", false); len(items) != 0 {
		t.Fatalf("close produced messages: %+v", items)
	}
	if err := c.Call(ctx, "task.wait", map[string]any{"block": true, "timeoutMs": 1000}, nil); rpcCode(err) != rpc.CodeConflict {
		t.Fatalf("wait with nothing running = %v", err)
	}
}

func TestTaskAskResumeAndDepth(t *testing.T) {
	c := startDaemon(t)
	ctx := context.Background()
	ws := addFolderProject(t, c)
	sleeper := []string{"/bin/sh", "-c", "sleep 30"}
	a := startTask(t, c, map[string]any{"workspace": ws, "command": sleeper, "spec": "a"})

	err := c.Call(ctx, "task.ask", map[string]any{"task": a.ID, "caller": a.Worker, "question": "ok?", "timeoutMs": 200}, nil)
	if rpcCode(err) != rpc.CodeTimeout || !strings.Contains(err.Error(), "--resume") {
		t.Fatalf("ask timeout = %v", err)
	}
	items := inbox(t, c, "", false)
	if len(items) != 1 {
		t.Fatalf("question not recorded: %+v", items)
	}
	c.Call(ctx, "task.reply", map[string]any{"message": items[0].ID, "body": "yes"}, nil)
	var r struct{ Answer string }
	if err := c.Call(ctx, "task.ask", map[string]any{"task": a.ID, "caller": a.Worker, "resume": items[0].ID}, &r); err != nil || r.Answer != "yes" {
		t.Fatalf("resume = %q, %v", r.Answer, err)
	}

	// worker 派给 worker：到第三层为止。派发方是 worker 时，回复权限也只属于它
	b := startTask(t, c, map[string]any{"caller": a.Worker, "workspace": ws, "command": sleeper, "spec": "b"})
	if err := c.Call(ctx, "task.send", map[string]any{"task": b.ID, "caller": "t_stranger", "body": "x"}, nil); rpcCode(err) != rpc.CodeConflict {
		t.Fatalf("send from a stranger = %v", err)
	}
	cc := startTask(t, c, map[string]any{"caller": b.Worker, "command": sleeper, "spec": "c"})
	err = c.Call(ctx, "task.start", map[string]any{"caller": cc.Worker, "command": sleeper, "spec": "d"}, nil)
	if rpcCode(err) != rpc.CodeConflict {
		t.Fatalf("fourth level = %v", err)
	}
	if cc.Workspace != ws {
		t.Fatalf("worker without workspace started in %s, want the coordinator's %s", cc.Workspace, ws)
	}
}

func git(t *testing.T, dir string, args ...string) string {
	t.Helper()
	out, err := exec.Command("git", append([]string{"-C", dir}, args...)...).CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
	return strings.TrimSpace(string(out))
}

func TestTaskWorktreeMerge(t *testing.T) {
	for _, k := range []string{"GIT_AUTHOR_NAME", "GIT_COMMITTER_NAME"} {
		t.Setenv(k, "hive test")
	}
	for _, k := range []string{"GIT_AUTHOR_EMAIL", "GIT_COMMITTER_EMAIL"} {
		t.Setenv(k, "hive@example.com")
	}
	c := startDaemon(t)
	ctx := context.Background()
	repo := t.TempDir()
	git(t, repo, "init", "-q", "-b", "main")
	os.WriteFile(filepath.Join(repo, "a.txt"), []byte("a\n"), 0o644)
	git(t, repo, "add", ".")
	git(t, repo, "commit", "-q", "-m", "init")
	name := filepath.Base(repo)
	if err := c.Call(ctx, "project.add", map[string]any{"path": repo, "name": name}, nil); err != nil {
		t.Fatal(err)
	}
	task := startTask(t, c, map[string]any{"workspace": name + "/main", "worktree": "feat-x", "command": []string{"/bin/sh", "-c", "sleep 30"}, "spec": "add b"})
	if task.Workspace != name+"/feat-x" || task.Branch != "feat-x" {
		t.Fatalf("worktree task = %+v", task)
	}
	wt := filepath.Join(c.Home, "worktrees", name, "feat-x")
	// 只有未跟踪文件、没有提交时无可合并；已跟踪文件有改动时要求先提交
	os.WriteFile(filepath.Join(wt, "b.txt"), []byte("b\n"), 0o644)
	if err := c.Call(ctx, "workspace.merge", map[string]any{"workspace": task.Workspace}, nil); rpcCode(err) != rpc.CodeConflict || !strings.Contains(err.Error(), "nothing to merge") {
		t.Fatalf("merge without commits = %v", err)
	}
	git(t, wt, "add", ".")
	git(t, wt, "commit", "-q", "-m", "add b")
	os.WriteFile(filepath.Join(wt, "a.txt"), []byte("changed\n"), 0o644)
	if err := c.Call(ctx, "workspace.merge", map[string]any{"workspace": task.Workspace}, nil); rpcCode(err) != rpc.CodeConflict || !strings.Contains(err.Error(), "uncommitted") {
		t.Fatalf("merge with uncommitted changes = %v", err)
	}
	git(t, wt, "checkout", "--", "a.txt")
	// 未跟踪的 setup 产物不挡合并
	os.WriteFile(filepath.Join(wt, ".setup-marker"), []byte("x\n"), 0o644)
	var diff struct{ Commits, Files, Insertions, Untracked int }
	if err := c.Call(ctx, "workspace.diff", map[string]any{"workspace": task.Workspace}, &diff); err != nil {
		t.Fatal(err)
	}
	if diff.Commits != 1 || diff.Files != 1 || diff.Insertions != 1 || diff.Untracked != 1 {
		t.Fatalf("diff = %+v", diff)
	}
	if err := c.Call(ctx, "workspace.merge", map[string]any{"workspace": task.Workspace}, nil); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(repo, "b.txt")); err != nil {
		t.Fatalf("merge did not bring b.txt: %v", err)
	}
	if log := git(t, repo, "log", "--oneline", "-1"); !strings.Contains(log, "Merge branch 'feat-x'") {
		t.Fatalf("last commit %q", log)
	}
}
