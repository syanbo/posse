package gitx

import (
	"context"
	"strings"
	"sync"
	"testing"
)

func TestReadOnly(t *testing.T) {
	for _, c := range []struct {
		args []string
		want bool
	}{
		{[]string{"status", "--porcelain=v2"}, true},
		{[]string{"-c", "commit.cleanup=whitespace", "rebase", "-i"}, false},
		{[]string{"branch", "-r", "--contains", "abc", "--format=%(refname)"}, true},
		{[]string{"branch", "-D", "feat"}, false},
		{[]string{"branch", "new"}, false},
		{[]string{"stash", "list"}, true},
		{[]string{"stash", "push", "-u"}, false},
		{[]string{"hash-object", "--stdin"}, true},
		{[]string{"hash-object", "-w", "--stdin"}, false},
		{[]string{"fetch", "--all"}, false},
		{[]string{"commit", "-q", "-F", "-"}, false},
	} {
		if got := readOnly(c.args); got != c.want {
			t.Errorf("readOnly(%v) = %v", c.args, got)
		}
	}
}

// 控制台只收改动仓库的命令（开始和结束各一次）和失败的命令，成功的查询不收。
func TestTrace(t *testing.T) {
	dir := t.TempDir()
	gitIn(t, dir, "init", "-q")
	var mu sync.Mutex
	var runs []Run
	Trace = func(r Run) { mu.Lock(); runs = append(runs, r); mu.Unlock() }
	defer func() { Trace = nil }()
	ctx := context.Background()
	runRaw(ctx, dir, nil, nil, "status", "--porcelain=v2")
	runRaw(ctx, dir, nil, nil, "branch", "-D", "nope")
	runRaw(ctx, dir, nil, nil, "log", "-1")
	if len(runs) != 3 {
		t.Fatalf("runs %+v", runs)
	}
	if runs[0].Done || !runs[1].Done || runs[0].ID != runs[1].ID || !strings.Contains(runs[1].Err, "nope") {
		t.Fatalf("branch -D %+v", runs[:2])
	}
	if r := runs[2]; r.Args[0] != "log" || r.Err == "" {
		t.Fatalf("failed query %+v", r)
	}
	// 操作里的命令连查询一起记，带着操作名，同一个操作的 OpID 相同
	runs = nil
	op := WithOp(ctx, "提交")
	runRaw(op, dir, nil, nil, "status", "--porcelain=v2")
	runRaw(op, dir, nil, nil, "branch", "x")
	if len(runs) != 4 || runs[0].Op != "提交" || runs[0].OpID == 0 || runs[3].OpID != runs[0].OpID {
		t.Fatalf("op runs %+v", runs)
	}
	// 另一条执行路径（run）也记；操作的结果单独记一条，没有命令行
	runs = nil
	TopLevel(op, dir)
	OpResult(op, dir, "范围里有合并提交")
	if len(runs) != 3 || runs[1].Args[0] != "rev-parse" || len(runs[2].Args) != 0 || runs[2].Err != "范围里有合并提交" || runs[2].OpID != runs[0].OpID {
		t.Fatalf("run() and op result %+v", runs)
	}
}

func TestChangesRepo(t *testing.T) {
	for _, c := range []struct {
		args []string
		want bool
	}{
		{[]string{"commit", "-m", "x"}, true},
		{[]string{"-c", "user.name=t", "commit", "-qm", "x"}, true},
		{[]string{"-C", "/repo", "push"}, true},
		{[]string{"--no-optional-locks", "status", "--porcelain"}, false},
		{[]string{"rev-parse", "--show-toplevel"}, false},
		{[]string{"remote", "-v"}, false},
		{[]string{"remote", "add", "fork", "url"}, true},
		{[]string{"config", "user.name"}, false},
		{[]string{"ls-remote", "origin"}, false},
		{[]string{"add", "-n", "."}, false},
		{[]string{"add", "."}, true},
		{[]string{"stash"}, true},
		{[]string{"stash", "list"}, false},
		{[]string{"branch", "--show-current"}, false},
		{[]string{"branch", "-D", "old"}, true},
		{[]string{"tag", "-l"}, false},
		{[]string{"tag", "v1"}, true},
		{[]string{"worktree", "list"}, false},
		{[]string{"worktree", "add", "../x"}, true},
		{[]string{"apply", "--check", "p.diff"}, false},
		{[]string{"maintenance", "run", "--auto"}, false},
		{nil, false},
	} {
		if got := ChangesRepo(c.args); got != c.want {
			t.Errorf("ChangesRepo(%q) = %v", c.args, got)
		}
	}
}
