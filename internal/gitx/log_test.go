package gitx

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// history 在 newRepo 上再做三个提交：c1 改 a，c2 加 c.txt，c3 改 a；返回仓库和三个提交的哈希。
func history(t *testing.T) (string, []string) {
	t.Helper()
	dir := newRepo(t)
	var hashes []string
	for _, step := range []struct{ file, text, msg string }{
		{"a.txt", "one\n", "c1: 改 a"},
		{"c.txt", "c\n", "c2: 加 c"},
		{"a.txt", "two\n", "c3: 再改 a"},
	} {
		write(t, filepath.Join(dir, step.file), step.text)
		gitIn(t, dir, "add", step.file)
		gitIn(t, dir, "commit", "-q", "-m", step.msg)
		hashes = append(hashes, gitIn(t, dir, "rev-parse", "HEAD"))
	}
	return dir, hashes
}

func subjects(t *testing.T, dir string) string {
	return strings.ReplaceAll(gitIn(t, dir, "log", "--format=%s"), "\n", " | ")
}

func TestLogFilters(t *testing.T) {
	ctx := context.Background()
	dir, h := history(t)
	all, err := Log(ctx, dir, LogFilter{}, 0, 100)
	if err != nil || len(all) != 4 || all[0].Hash != h[2] || len(all[0].Parents) != 1 {
		t.Fatalf("log %d %v", len(all), err)
	}
	if !strings.Contains(all[0].Refs, "main") {
		t.Fatalf("refs %q", all[0].Refs)
	}
	page, _ := Log(ctx, dir, LogFilter{}, 1, 2)
	if len(page) != 2 || page[0].Hash != h[1] {
		t.Fatalf("paging %+v", page)
	}
	if got, _ := Log(ctx, dir, LogFilter{Path: "a.txt"}, 0, 100); len(got) != 3 {
		t.Fatalf("file history %d", len(got))
	}
	if got, _ := Log(ctx, dir, LogFilter{Text: "加 C"}, 0, 100); len(got) != 1 || got[0].Hash != h[1] {
		t.Fatalf("grep %+v", got)
	}
	if got, _ := Log(ctx, dir, LogFilter{Text: h[0][:8]}, 0, 100); len(got) != 1 || got[0].Hash != h[0] {
		t.Fatalf("hash %+v", got)
	}
	if got, err := Log(ctx, dir, LogFilter{Text: "abcdef0"}, 0, 100); err != nil || len(got) != 0 {
		t.Fatalf("unknown hash %+v %v", got, err)
	}
	if got, _ := Log(ctx, dir, LogFilter{Author: "T"}, 0, 100); len(got) != 4 {
		t.Fatalf("author %d", len(got))
	}
}

func TestRewriteHistory(t *testing.T) {
	ctx := context.Background()
	dir, h := history(t)
	write(t, filepath.Join(dir, "dirty.txt"), "x\n") // 未跟踪的文件不影响改写
	write(t, filepath.Join(dir, "src", "b.go"), "package src // dirty\n")
	if err := Reword(ctx, dir, h[0], "c1: 改 a 的第一行\n\n正文"); err != nil {
		t.Fatal(err)
	}
	if s := subjects(t, dir); s != "c3: 再改 a | c2: 加 c | c1: 改 a 的第一行 | base" {
		t.Fatalf("after reword: %s", s)
	}
	head := gitIn(t, dir, "rev-parse", "HEAD")
	if err := Reword(ctx, dir, head, "c3: amend"); err != nil {
		t.Fatal(err)
	}
	newC2 := gitIn(t, dir, "rev-parse", "HEAD~1")
	if err := SquashIntoParent(ctx, dir, newC2, "c1+c2"); err != nil {
		t.Fatal(err)
	}
	if s := subjects(t, dir); s != "c3: amend | c1+c2 | base" {
		t.Fatalf("after squash: %s", s)
	}
	if err := Drop(ctx, dir, gitIn(t, dir, "rev-parse", "HEAD")); err != nil {
		t.Fatal(err)
	}
	if s := subjects(t, dir); s != "c1+c2 | base" {
		t.Fatalf("after drop: %s", s)
	}
	// 没提交的改动在改写前后都在
	if b, _ := os.ReadFile(filepath.Join(dir, "src", "b.go")); string(b) != "package src // dirty\n" {
		t.Fatalf("dirty change lost: %q", b)
	}
	// 合并提交在范围里时拒绝
	gitIn(t, dir, "switch", "-q", "-c", "side", "HEAD~1")
	write(t, filepath.Join(dir, "s.txt"), "s\n")
	gitIn(t, dir, "add", "s.txt")
	gitIn(t, dir, "commit", "-q", "-m", "side")
	gitIn(t, dir, "switch", "-q", "main")
	gitIn(t, dir, "merge", "-q", "--no-ff", "--no-edit", "side")
	if err := Reword(ctx, dir, gitIn(t, dir, "rev-parse", "HEAD~1"), "x"); err != ErrRewriteMerges {
		t.Fatalf("merge in range: %v", err)
	}
	if err := Drop(ctx, dir, gitIn(t, dir, "rev-parse", "side")); err != ErrNotOnBranch && err != ErrRewriteMerges {
		t.Fatalf("side commit: %v", err)
	}
}

func TestCherryPickRevertResetBlame(t *testing.T) {
	ctx := context.Background()
	dir, h := history(t)
	gitIn(t, dir, "switch", "-q", "-c", "other", h[0])
	if err := CherryPick(ctx, dir, h[1]); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(dir, "c.txt")); err != nil {
		t.Fatal("cherry-pick did not bring c.txt")
	}
	if err := RevertCommit(ctx, dir, "HEAD"); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(dir, "c.txt")); !os.IsNotExist(err) {
		t.Fatal("revert did not remove c.txt")
	}
	if err := Reset(ctx, dir, h[0], "hard"); err != nil {
		t.Fatal(err)
	}
	if gitIn(t, dir, "rev-parse", "HEAD") != h[0] {
		t.Fatal("reset did not move HEAD")
	}
	gitIn(t, dir, "switch", "-q", "main")
	write(t, filepath.Join(dir, "a.txt"), "two\nlocal\n")
	lines, err := Blame(ctx, dir, "a.txt")
	if err != nil || len(lines) != 2 {
		t.Fatalf("blame %+v %v", lines, err)
	}
	if lines[0].Hash != h[2] || lines[0].Summary != "c3: 再改 a" || lines[0].Author != "t" || lines[0].Time.IsZero() {
		t.Fatalf("blame line 1 %+v", lines[0])
	}
	if strings.Trim(lines[1].Hash, "0") != "" || lines[1].Text != "local" {
		t.Fatalf("uncommitted line %+v", lines[1])
	}
}

// 改写不受用户配置影响：# 开头的行保留，缩写待办和 autoSquash 不让改写失效或误伤别的提交。
func TestRewriteIgnoresUserConfig(t *testing.T) {
	ctx := context.Background()
	dir, h := history(t)
	gitIn(t, dir, "config", "rebase.abbreviateCommands", "true")
	gitIn(t, dir, "config", "rebase.autoSquash", "true")
	write(t, filepath.Join(dir, "c.txt"), "fix\n")
	gitIn(t, dir, "commit", "-q", "-am", "fixup! c2: 加 c")
	msg := "#123 修 a\n\n# 不是注释\n正文"
	if err := Reword(ctx, dir, h[0], msg); err != nil {
		t.Fatal(err)
	}
	if got := gitIn(t, dir, "log", "--format=%B", "-n", "1", "HEAD~3"); got != msg {
		t.Fatalf("message = %q", got)
	}
	if s := subjects(t, dir); s != "fixup! c2: 加 c | c3: 再改 a | c2: 加 c | #123 修 a | base" {
		t.Fatalf("history: %s", s)
	}
}
