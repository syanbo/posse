package gitx

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"
)

// newRepo 建一个有一次提交的仓库：a.txt、src/b.go、old.txt。
func newRepo(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	gitIn(t, dir, "init", "-q", "-b", "main")
	gitIn(t, dir, "config", "user.email", "t@example.com")
	gitIn(t, dir, "config", "user.name", "t")
	write(t, filepath.Join(dir, "a.txt"), "one\ntwo\nthree\n")
	write(t, filepath.Join(dir, "src", "b.go"), "package src\n")
	write(t, filepath.Join(dir, "old.txt"), "rename me\n")
	gitIn(t, dir, "add", ".")
	gitIn(t, dir, "commit", "-q", "-m", "base")
	return dir
}

func entry(st *Status, path string) (Entry, bool) {
	i := slices.IndexFunc(st.Entries, func(e Entry) bool { return e.Path == path })
	if i < 0 {
		return Entry{}, false
	}
	return st.Entries[i], true
}

func TestReadStatus(t *testing.T) {
	ctx := context.Background()
	dir := newRepo(t)
	write(t, filepath.Join(dir, "a.txt"), "one\nTWO\nthree\nfour\n")            // 未暂存的修改
	write(t, filepath.Join(dir, "src", "b.go"), "package src\n\nfunc B() {}\n") // 暂存后再改
	gitIn(t, dir, "add", "src/b.go")
	write(t, filepath.Join(dir, "src", "b.go"), "package src\n\nfunc B() {}\nfunc C() {}\n")
	gitIn(t, dir, "mv", "old.txt", "new name.txt")            // 重命名，路径带空格
	write(t, filepath.Join(dir, "docs", "new.md"), "# new\n") // 未跟踪，在新目录里
	st, err := ReadStatus(ctx, dir)
	if err != nil {
		t.Fatal(err)
	}
	if st.Head != "main" || st.Commit == "" || st.Operation != "" {
		t.Fatalf("branch %+v", st)
	}
	a, _ := entry(st, "a.txt")
	if a.Staged() || !a.Unstaged() || a.Y != 'M' {
		t.Errorf("a.txt %+v", a)
	}
	b, _ := entry(st, "src/b.go")
	if !b.Staged() || !b.Unstaged() {
		t.Errorf("src/b.go %+v", b)
	}
	r, _ := entry(st, "new name.txt")
	if r.X != 'R' || r.Orig != "old.txt" {
		t.Errorf("rename %+v", r)
	}
	u, ok := entry(st, "docs/new.md")
	if !ok || !u.Untracked || u.Staged() || u.Unstaged() {
		t.Errorf("untracked %+v %v", u, ok)
	}
	stats, err := NumStat(ctx, dir, false)
	if err != nil {
		t.Fatal(err)
	}
	if s := stats["a.txt"]; s.Added != 2 || s.Deleted != 1 {
		t.Errorf("numstat a.txt %+v", s)
	}
	staged, _ := NumStat(ctx, dir, true)
	if _, ok := staged["new name.txt"]; !ok {
		t.Errorf("staged numstat lacks rename: %v", staged)
	}
}

func TestShowAndRollback(t *testing.T) {
	ctx := context.Background()
	dir := newRepo(t)
	write(t, filepath.Join(dir, "a.txt"), "changed\n")
	gitIn(t, dir, "add", "a.txt")
	write(t, filepath.Join(dir, "a.txt"), "changed again\n")
	if b, err := Show(ctx, dir, "HEAD", "a.txt"); err != nil || string(b) != "one\ntwo\nthree\n" {
		t.Fatalf("HEAD %q %v", b, err)
	}
	if b, err := Show(ctx, dir, "", "a.txt"); err != nil || string(b) != "changed\n" {
		t.Fatalf("index %q %v", b, err)
	}
	if _, err := Show(ctx, dir, "HEAD", "nope.txt"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("missing file: %v", err)
	}
	if err := Rollback(ctx, dir, "a.txt"); err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(filepath.Join(dir, "a.txt")); string(b) != "one\ntwo\nthree\n" {
		t.Fatalf("after rollback %q", b)
	}
	st, _ := ReadStatus(ctx, dir)
	if len(st.Entries) != 0 {
		t.Fatalf("still changed: %+v", st.Entries)
	}
	// 新文件：移出暂存区并删除
	write(t, filepath.Join(dir, "n.txt"), "new\n")
	gitIn(t, dir, "add", "n.txt")
	if err := RemoveNew(ctx, dir, "n.txt"); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(dir, "n.txt")); !os.IsNotExist(err) {
		t.Fatal("new file not deleted")
	}
	if st, _ := ReadStatus(ctx, dir); len(st.Entries) != 0 {
		t.Fatalf("index still has it: %+v", st.Entries)
	}
}

func TestStageCommitAmend(t *testing.T) {
	ctx := context.Background()
	dir := newRepo(t)
	write(t, filepath.Join(dir, "a.txt"), "x\n")
	write(t, filepath.Join(dir, "c.txt"), "untracked\n")
	if err := Stage(ctx, dir, "c.txt"); err != nil {
		t.Fatal(err)
	}
	// 只提交已暂存的：a.txt 的修改留在工作区
	if err := Commit(ctx, dir, "feat: 加 c\n\n正文", false, false); err != nil {
		t.Fatal(err)
	}
	if LastMessage(ctx, dir) != "feat: 加 c\n\n正文" {
		t.Fatalf("message %q", LastMessage(ctx, dir))
	}
	st, _ := ReadStatus(ctx, dir)
	if a, ok := entry(st, "a.txt"); !ok || !a.Unstaged() {
		t.Fatalf("a.txt should still be changed: %+v", st.Entries)
	}
	// 全部提交并修改上一次提交
	if err := Commit(ctx, dir, "feat: 加 c 并改 a", true, true); err != nil {
		t.Fatal(err)
	}
	if n := gitIn(t, dir, "rev-list", "--count", "HEAD"); n != "2" {
		t.Fatalf("amend made a new commit: %s commits", n)
	}
	if st, _ := ReadStatus(ctx, dir); len(st.Entries) != 0 {
		t.Fatalf("not clean: %+v", st.Entries)
	}
	if err := Unstage(ctx, dir, "a.txt"); err != nil {
		t.Fatal(err)
	}
	if err := Commit(ctx, dir, "nothing", false, false); err == nil {
		t.Fatal("empty commit should fail")
	}
}

func TestStashRoundTrip(t *testing.T) {
	ctx := context.Background()
	dir := newRepo(t)
	write(t, filepath.Join(dir, "a.txt"), "x\n")
	write(t, filepath.Join(dir, "u.txt"), "untracked\n")
	if err := StashPush(ctx, dir, "试一下"); err != nil {
		t.Fatal(err)
	}
	if st, _ := ReadStatus(ctx, dir); len(st.Entries) != 0 {
		t.Fatalf("stash left changes: %+v", st.Entries)
	}
	list, err := Stashes(ctx, dir)
	if err != nil || len(list) != 1 || list[0].Ref != "stash@{0}" || list[0].When.IsZero() {
		t.Fatalf("stashes %+v %v", list, err)
	}
	if err := StashPop(ctx, dir, list[0].Ref); err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(filepath.Join(dir, "u.txt")); string(b) != "untracked\n" {
		t.Fatal("untracked file not restored")
	}
	if list, _ := Stashes(ctx, dir); len(list) != 0 {
		t.Fatal("stash not dropped after pop")
	}
}

func TestInProgressMerge(t *testing.T) {
	ctx := context.Background()
	dir := newRepo(t)
	gitIn(t, dir, "switch", "-q", "-c", "side")
	write(t, filepath.Join(dir, "a.txt"), "side\n")
	gitIn(t, dir, "commit", "-q", "-am", "side")
	gitIn(t, dir, "switch", "-q", "main")
	write(t, filepath.Join(dir, "a.txt"), "main\n")
	gitIn(t, dir, "commit", "-q", "-am", "main")
	runRaw(ctx, dir, nil, nil, "merge", "side")
	st, err := ReadStatus(ctx, dir)
	if err != nil {
		t.Fatal(err)
	}
	a, _ := entry(st, "a.txt")
	if st.Operation != "merge" || !a.Conflict {
		t.Fatalf("op %q a %+v", st.Operation, a)
	}
}

// 勾选提交：只提交选中的文件（含新文件和删除），暂存区里没选的留着；只改提交信息也行。
func TestCommitPaths(t *testing.T) {
	ctx := context.Background()
	dir := newRepo(t)
	write(t, filepath.Join(dir, "a.txt"), "changed\n")
	gitIn(t, dir, "add", "a.txt") // 暂存了但这次不勾
	write(t, filepath.Join(dir, "src", "b.go"), "package src // changed\n")
	os.Remove(filepath.Join(dir, "old.txt"))
	write(t, filepath.Join(dir, "new.txt"), "new\n")
	if err := CommitPaths(ctx, dir, "pick", []string{"src/b.go", "old.txt", "new.txt"}, []string{"new.txt"}, false); err != nil {
		t.Fatal(err)
	}
	files := gitIn(t, dir, "show", "--name-status", "--format=", "HEAD")
	for _, want := range []string{"M\tsrc/b.go", "D\told.txt", "A\tnew.txt"} {
		if !strings.Contains(files, want) {
			t.Fatalf("commit lacks %q:\n%s", want, files)
		}
	}
	if strings.Contains(files, "a.txt") {
		t.Fatalf("unpicked a.txt committed:\n%s", files)
	}
	if st := gitIn(t, dir, "status", "--porcelain"); !strings.Contains(st, "M  a.txt") {
		t.Fatalf("a.txt should stay staged: %q", st)
	}
	// 只改提交信息
	if err := CommitPaths(ctx, dir, "pick 2", nil, nil, true); err != nil {
		t.Fatal(err)
	}
	if msg := gitIn(t, dir, "log", "-1", "--format=%s"); msg != "pick 2" {
		t.Fatalf("amend message %q", msg)
	}
	if st := gitIn(t, dir, "status", "--porcelain"); !strings.Contains(st, "M  a.txt") {
		t.Fatalf("amend should leave a.txt staged: %q", st)
	}
	// 相对 HEAD 的行数：a.txt 改了一行
	if s, _ := NumStatHEAD(ctx, dir); s["a.txt"].Added != 1 || s["a.txt"].Deleted != 3 {
		t.Fatalf("numstat HEAD %+v", s["a.txt"])
	}
}

// 路径里的 [ ] * 按字面处理：回滚、提交 app/[id]/page.tsx 不能连带 app/i/page.tsx。
func TestLiteralPathspecs(t *testing.T) {
	ctx := context.Background()
	dir := newRepo(t)
	for _, p := range []string{"app/[id]/page.tsx", "app/i/page.tsx", "app/d/page.tsx", "x*.txt", "xy.txt"} {
		write(t, filepath.Join(dir, p), "base\n")
	}
	gitIn(t, dir, "add", ".")
	gitIn(t, dir, "commit", "-q", "-m", "routes")
	for _, p := range []string{"app/[id]/page.tsx", "app/i/page.tsx", "app/d/page.tsx", "x*.txt", "xy.txt"} {
		write(t, filepath.Join(dir, p), "changed\n")
	}
	if err := Rollback(ctx, dir, "app/[id]/page.tsx"); err != nil {
		t.Fatal(err)
	}
	for p, want := range map[string]string{"app/[id]/page.tsx": "base\n", "app/i/page.tsx": "changed\n", "app/d/page.tsx": "changed\n"} {
		if b, _ := os.ReadFile(filepath.Join(dir, p)); string(b) != want {
			t.Errorf("%s = %q, want %q", p, b, want)
		}
	}
	if err := CommitPaths(ctx, dir, "only star", []string{"x*.txt"}, nil, false); err != nil {
		t.Fatal(err)
	}
	if got := gitIn(t, dir, "show", "--name-only", "--format=", "HEAD"); got != "x*.txt" {
		t.Errorf("committed %q", got)
	}
}

// 轮询读状态不重写 index，不和终端里的 git 命令抢 index.lock。
func TestReadStatusNoIndexWrite(t *testing.T) {
	dir := newRepo(t)
	index := filepath.Join(dir, ".git", "index")
	// 让 index 里的 stat 信息过时，git status 默认会顺手刷新写回
	old := time.Now().Add(-time.Hour)
	os.Chtimes(filepath.Join(dir, "a.txt"), old, old)
	before, _ := os.Stat(index)
	time.Sleep(10 * time.Millisecond)
	if _, err := ReadStatus(context.Background(), dir); err != nil {
		t.Fatal(err)
	}
	after, _ := os.Stat(index)
	if !after.ModTime().Equal(before.ModTime()) {
		t.Fatal("ReadStatus rewrote .git/index")
	}
}

// 只提交一处改动：提交里只有这一处，工作区不动，剩下的改动仍是未暂存的；暂存区有这个文件的改动时拒绝。
func TestCommitChange(t *testing.T) {
	ctx := context.Background()
	dir := newRepo(t)
	write(t, filepath.Join(dir, "a.txt"), "ONE\ntwo\nTHREE\n")
	write(t, filepath.Join(dir, "src", "b.go"), "package src // staged\n")
	gitIn(t, dir, "add", "src/b.go")
	if err := CommitChange(ctx, dir, "a.txt", []byte("ONE\ntwo\nthree\n"), "只改第一行"); err != nil {
		t.Fatal(err)
	}
	if got := gitIn(t, dir, "show", "HEAD:a.txt"); got != "ONE\ntwo\nthree" {
		t.Fatalf("committed %q", got)
	}
	if got := gitIn(t, dir, "show", "--name-only", "--format=%s", "HEAD"); got != "只改第一行\n\na.txt" {
		t.Fatalf("commit %q", got)
	}
	if b, _ := os.ReadFile(filepath.Join(dir, "a.txt")); string(b) != "ONE\ntwo\nTHREE\n" {
		t.Fatalf("worktree changed: %q", b)
	}
	st, _ := ReadStatus(ctx, dir)
	if a, _ := entry(st, "a.txt"); a.Staged() || !a.Unstaged() {
		t.Errorf("a.txt %+v", a)
	}
	if b, _ := entry(st, "src/b.go"); !b.Staged() {
		t.Errorf("other staged file lost: %+v", b)
	}
	if err := CommitChange(ctx, dir, "src/b.go", []byte("x\n"), "x"); !errors.Is(err, ErrStagedChange) {
		t.Fatalf("staged file: %v", err)
	}
}
