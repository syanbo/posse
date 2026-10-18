package gitx

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"
)

// 界面的 Git 操作（窗口进程调用）：状态、改动、提交、搁置。dir 是仓库根目录（TopLevel），
// 路径都相对它、用 "/" 分隔：porcelain 输出的路径就是相对根目录的。

// runRaw 原样返回 git 的标准输出（不去首尾空白），stdin 非空时作为输入，env 追加到环境变量。
func runRaw(ctx context.Context, dir string, stdin []byte, env []string, args ...string) ([]byte, error) {
	limit := 2 * time.Minute
	if len(args) > 0 && (args[0] == "fetch" || args[0] == "pull" || args[0] == "push") {
		limit = 10 * time.Minute
	}
	ctx, cancel := context.WithTimeout(ctx, limit)
	defer cancel()
	cmd := exec.CommandContext(ctx, "git", args...)
	cmd.Dir = dir
	// 新会话里跑：没有控制终端，ssh 要口令或确认主机指纹时直接失败，不会在 /dev/tty 上一直等。
	// 取消、超时时给整组发 SIGTERM，连同 git 拉起的 ssh：git 收到 SIGTERM 会删掉自己的 .lock 再退出，SIGKILL 会把锁留下；
	// 2 秒还没退（或 ssh 拿着输出管道不放）由 WaitDelay 强杀
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	cmd.Cancel = func() error { return syscall.Kill(-cmd.Process.Pid, syscall.SIGTERM) }
	cmd.WaitDelay = 2 * time.Second
	// 不拿可选的锁：每 1.5 秒的 status 会顺手重写 index，和终端里 agent 的 git 命令抢 index.lock
	cmd.Env = append(append(cmd.Environ(), "GIT_TERMINAL_PROMPT=0", "LC_ALL=C", "GIT_OPTIONAL_LOCKS=0"), env...)
	if stdin != nil {
		cmd.Stdin = bytes.NewReader(stdin)
	}
	var out, errb bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &errb
	rec := traceStart(dir, args)
	if err := cmd.Run(); err != nil {
		msg := withoutHints(errb.String())
		if msg == "" {
			msg = strings.TrimSpace(out.String())
		}
		if msg == "" {
			msg = err.Error()
		}
		if ctx.Err() != nil && !errors.Is(ctx.Err(), context.DeadlineExceeded) {
			msg = "已取消"
		}
		traceEnd(rec, out.Bytes(), errb.Bytes(), msg)
		return out.Bytes(), &Error{Args: args, Msg: msg}
	}
	traceEnd(rec, out.Bytes(), errb.Bytes(), "")
	return out.Bytes(), nil
}

// withoutHints 去掉 git 报错里的 "hint:" 行：那是教命令行怎么做的，界面上照着做不了，还把真正的原因挤到后面。
func withoutHints(msg string) string {
	var keep []string
	for _, line := range strings.Split(msg, "\n") {
		if !strings.HasPrefix(line, "hint:") {
			keep = append(keep, line)
		}
	}
	return strings.TrimSpace(strings.Join(keep, "\n"))
}

// Error 是 git 命令失败：Msg 是 git 自己的报错，界面直接显示它。
type Error struct {
	Args []string
	Msg  string
}

func (e *Error) Error() string { return e.Msg }

// Entry 是状态里的一个文件。X、Y 是暂存区和工作区的状态字母（porcelain 的 XY），"." 表示没变。
type Entry struct {
	Path      string
	Orig      string // 重命名、复制前的路径
	X, Y      byte
	Untracked bool
	Conflict  bool
}

// Staged 报告暂存区有这个文件的改动。
func (e Entry) Staged() bool { return !e.Untracked && !e.Conflict && e.X != '.' }

// Unstaged 报告工作区有没暂存的改动。
func (e Entry) Unstaged() bool { return !e.Untracked && !e.Conflict && e.Y != '.' }

// Status 是一个工作区的 git 状态。
type Status struct {
	Head      string // 当前分支；分离 HEAD 时为空
	Commit    string // HEAD 的提交，还没有提交时为空
	Upstream  string
	Ahead     int
	Behind    int
	Entries   []Entry
	Operation string // 进行中的 rebase、merge、cherry-pick、revert，没有时为空
}

// ReadStatus 读状态：逐个列出未跟踪文件，和 WebStorm 的改动列表一样。
func ReadStatus(ctx context.Context, dir string) (*Status, error) {
	out, err := runRaw(ctx, dir, nil, nil, "status", "--porcelain=v2", "-z", "--branch", "--untracked-files=all")
	if err != nil {
		return nil, err
	}
	st := parseStatus(out)
	st.Operation = inProgress(ctx, dir)
	return st, nil
}

func parseStatus(out []byte) *Status {
	st := &Status{}
	fields := strings.Split(string(out), "\x00")
	for i := 0; i < len(fields); i++ {
		f := fields[i]
		switch {
		case f == "":
		case strings.HasPrefix(f, "# branch.oid "):
			if oid := strings.TrimPrefix(f, "# branch.oid "); oid != "(initial)" {
				st.Commit = oid
			}
		case strings.HasPrefix(f, "# branch.head "):
			if h := strings.TrimPrefix(f, "# branch.head "); h != "(detached)" {
				st.Head = h
			}
		case strings.HasPrefix(f, "# branch.upstream "):
			st.Upstream = strings.TrimPrefix(f, "# branch.upstream ")
		case strings.HasPrefix(f, "# branch.ab "):
			fmt.Sscanf(strings.TrimPrefix(f, "# branch.ab "), "+%d -%d", &st.Ahead, &st.Behind)
		case strings.HasPrefix(f, "1 "):
			// 1 XY sub mH mI mW hH hI path
			p := strings.SplitN(f, " ", 9)
			if len(p) == 9 {
				st.Entries = append(st.Entries, Entry{Path: p[8], X: p[1][0], Y: p[1][1]})
			}
		case strings.HasPrefix(f, "2 "):
			// 2 XY sub mH mI mW hH hI Xscore path，-z 时原路径是下一个字段
			p := strings.SplitN(f, " ", 10)
			if len(p) == 10 {
				e := Entry{Path: p[9], X: p[1][0], Y: p[1][1]}
				if i+1 < len(fields) {
					e.Orig = fields[i+1]
					i++
				}
				st.Entries = append(st.Entries, e)
			}
		case strings.HasPrefix(f, "u "):
			// u XY sub m1 m2 m3 mW h1 h2 h3 path
			p := strings.SplitN(f, " ", 11)
			if len(p) == 11 {
				st.Entries = append(st.Entries, Entry{Path: p[10], X: p[1][0], Y: p[1][1], Conflict: true})
			}
		case strings.HasPrefix(f, "? "):
			st.Entries = append(st.Entries, Entry{Path: f[2:], X: '?', Y: '?', Untracked: true})
		}
	}
	return st
}

// gitPath 返回 .git 里的路径（worktree 的 .git 是文件，要问 git）。
func gitPath(ctx context.Context, dir, name string) string {
	out, err := run(ctx, dir, "rev-parse", "--git-path", name)
	if err != nil {
		return ""
	}
	if !filepath.IsAbs(out) {
		out = filepath.Join(dir, out)
	}
	return out
}

// inProgress 返回进行中的操作。
func inProgress(ctx context.Context, dir string) string {
	exists := func(name string) bool {
		p := gitPath(ctx, dir, name)
		if p == "" {
			return false
		}
		_, err := os.Stat(p)
		return err == nil
	}
	switch {
	case exists("rebase-merge"), exists("rebase-apply"):
		return "rebase"
	case exists("MERGE_HEAD"):
		return "merge"
	case exists("CHERRY_PICK_HEAD"):
		return "cherry-pick"
	case exists("REVERT_HEAD"):
		return "revert"
	}
	return ""
}

// LineStats 是一个文件增删的行数；二进制文件 Binary 为真。
type LineStats struct {
	Added, Deleted int
	Binary         bool
}

// NumStat 统计改动行数：staged 为真时是暂存区相对 HEAD，否则是工作区相对暂存区。键是新路径。
func NumStat(ctx context.Context, dir string, staged bool) (map[string]LineStats, error) {
	args := []string{"diff", "--numstat", "-z", "--find-renames"}
	if staged {
		args = append(args, "--cached")
	}
	out, err := runRaw(ctx, dir, nil, nil, args...)
	if err != nil {
		return nil, err
	}
	return parseNumStat(out), nil
}

// NumStatHEAD 是工作区相对 HEAD 的增删行数（暂存的和没暂存的合在一起）；还没有提交时退回暂存区相对空树。
func NumStatHEAD(ctx context.Context, dir string) (map[string]LineStats, error) {
	out, err := runRaw(ctx, dir, nil, nil, "diff", "HEAD", "--numstat", "-z", "--find-renames")
	if err != nil {
		return NumStat(ctx, dir, true)
	}
	return parseNumStat(out), nil
}

func parseNumStat(out []byte) map[string]LineStats {
	m := map[string]LineStats{}
	fields := strings.Split(string(out), "\x00")
	for i := 0; i < len(fields); i++ {
		parts := strings.SplitN(fields[i], "\t", 3)
		if len(parts) != 3 {
			continue
		}
		var s LineStats
		if parts[0] == "-" {
			s.Binary = true
		} else {
			s.Added, _ = strconv.Atoi(parts[0])
			s.Deleted, _ = strconv.Atoi(parts[1])
		}
		path := parts[2]
		if path == "" && i+2 < len(fields) {
			// 重命名：路径为空，后面两个字段是旧路径和新路径
			path = fields[i+2]
			i += 2
		}
		m[path] = s
	}
	return m
}

// DiffText 是工作区相对 HEAD 的统一 diff。rel 相对仓库根。没有提交时退回工作区 diff。
// 未跟踪文件用 --no-index 对空文件比较。超过 200KB 截断，truncated 为真。
func DiffText(ctx context.Context, dir, rel string) (text string, truncated bool, err error) {
	out, err := diffStdout(ctx, dir, "diff", "--no-ext-diff", "HEAD", "--", literal(rel)[0])
	if err != nil {
		out, err = diffStdout(ctx, dir, "diff", "--no-ext-diff", "--", literal(rel)[0])
	}
	if err == nil && len(out) == 0 {
		if alt, altErr := diffStdout(ctx, dir, "diff", "--no-ext-diff", "--no-index", "--", "/dev/null", rel); altErr == nil && len(alt) > 0 {
			out = alt
		}
	}
	const max = 200_000
	if len(out) > max {
		out = out[:max]
		truncated = true
	}
	return string(out), truncated, err
}

func diffStdout(ctx context.Context, dir string, args ...string) ([]byte, error) {
	out, err := runRaw(ctx, dir, nil, nil, args...)
	if err != nil && len(out) == 0 {
		return nil, err
	}
	return out, nil
}

// ErrNotFound 表示这个版本里没有这个文件。
var ErrNotFound = errors.New("not in this revision")

// Show 读文件在某个版本里的内容：rev 为空时读暂存区。
func Show(ctx context.Context, dir, rev, path string) ([]byte, error) {
	out, err := runRaw(ctx, dir, nil, nil, "show", rev+":"+filepath.ToSlash(path))
	if err != nil {
		var ge *Error
		if errors.As(err, &ge) && (strings.Contains(ge.Msg, "does not exist") || strings.Contains(ge.Msg, "exists on disk, but not in") ||
			strings.Contains(ge.Msg, "invalid object name") || strings.Contains(ge.Msg, "bad revision")) {
			return nil, ErrNotFound
		}
		return nil, err
	}
	return out, nil
}

func pathArgs(cmd []string, paths []string) []string {
	return append(append(cmd, "--"), literal(paths...)...)
}

// literal 让 git 按字面认路径：app/[id]/page.tsx 当通配符会连带匹配 app/i/page.tsx。
// 不用 GIT_LITERAL_PATHSPECS：stash push -u 内部要用 pathspec 魔法，设了就清不掉未跟踪文件。
func literal(paths ...string) []string {
	out := make([]string, len(paths))
	for i, p := range paths {
		out[i] = ":(literal)" + p
	}
	return out
}

// Stage 把文件加入暂存区（含未跟踪文件和删除）。
func Stage(ctx context.Context, dir string, paths ...string) error {
	_, err := runRaw(ctx, dir, nil, nil, pathArgs([]string{"add", "-A"}, paths)...)
	return err
}

// Unstage 把文件移出暂存区，工作区不变；还没有提交时用 rm --cached。
func Unstage(ctx context.Context, dir string, paths ...string) error {
	if _, err := run(ctx, dir, "rev-parse", "--verify", "--quiet", "HEAD"); err != nil {
		_, err := runRaw(ctx, dir, nil, nil, pathArgs([]string{"rm", "--cached", "-r", "-q"}, paths)...)
		return err
	}
	_, err := runRaw(ctx, dir, nil, nil, pathArgs([]string{"restore", "--staged"}, paths)...)
	return err
}

// Rollback 把已跟踪的文件恢复成 HEAD 的样子，暂存区和工作区都丢掉改动；
// 重命名的文件连原路径一起恢复，新文件不在这里处理（见 RemoveNew）。
func Rollback(ctx context.Context, dir string, paths ...string) error {
	_, err := runRaw(ctx, dir, nil, nil, pathArgs([]string{"restore", "--source=HEAD", "--staged", "--worktree"}, paths)...)
	return err
}

// RemoveNew 丢掉新文件：移出暂存区（如果在里面）并删除磁盘上的文件。
func RemoveNew(ctx context.Context, dir string, paths ...string) error {
	runRaw(ctx, dir, nil, nil, pathArgs([]string{"rm", "--cached", "-q", "--ignore-unmatch"}, paths)...)
	for _, p := range paths {
		if err := os.Remove(filepath.Join(dir, filepath.FromSlash(p))); err != nil && !os.IsNotExist(err) {
			return err
		}
	}
	return nil
}

// Commit 提交：all 为真时连同没暂存的已跟踪文件改动一起提交（git commit -a），amend 修改上一次提交。
func Commit(ctx context.Context, dir, message string, all, amend bool) error {
	args := []string{"commit", "-F", "-"}
	if all {
		args = append(args, "-a")
	}
	if amend {
		args = append(args, "--amend")
	}
	_, err := runRaw(ctx, dir, []byte(message), nil, args...)
	return err
}

// ErrStagedChange 表示文件在暂存区里有改动：只提交一处时不知道该以哪个版本为准，不做。
var ErrStagedChange = errors.New("这个文件有已暂存的改动，到改动页里整个提交它")

// CommitChange 只提交文件里的一处改动（照 WebStorm 行号旁弹窗的"提交此更改"）：content 是 HEAD 的内容
// 只换上这一处之后的样子。在临时的暂存区里提交（钩子照常执行），工作区不动；
// 真正的暂存区里这个文件换成刚提交的版本，剩下的改动仍然是未暂存的。
func CommitChange(ctx context.Context, dir, path string, content []byte, message string) error {
	tree, err := runRaw(ctx, dir, nil, nil, "ls-tree", "-z", "HEAD", "--", path)
	if err != nil {
		return err
	}
	// mode blob 对象\t路径
	meta, _, _ := strings.Cut(string(tree), "\t")
	f := strings.Fields(meta)
	if len(f) != 3 || f[1] != "blob" {
		return errors.New("上一次提交里没有这个文件")
	}
	mode, headBlob := f[0], f[2]
	staged, err := runRaw(ctx, dir, nil, nil, "ls-files", "-s", "-z", "--", literal(path)[0])
	if err != nil {
		return err
	}
	if s := strings.Fields(string(staged)); len(s) < 2 || s[1] != headBlob {
		return ErrStagedChange
	}
	out, err := runRaw(ctx, dir, content, nil, "hash-object", "-w", "--stdin", "--path", path)
	if err != nil {
		return err
	}
	blob := strings.TrimSpace(string(out))
	tmp, err := os.MkdirTemp("", "hive-index-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(tmp)
	env := []string{"GIT_INDEX_FILE=" + filepath.Join(tmp, "index")}
	info := mode + "," + blob + "," + path
	if _, err := runRaw(ctx, dir, nil, env, "read-tree", "HEAD"); err != nil {
		return err
	}
	if _, err := runRaw(ctx, dir, nil, env, "update-index", "--cacheinfo", info); err != nil {
		return err
	}
	if _, err := runRaw(ctx, dir, []byte(message), env, "commit", "-q", "-F", "-"); err != nil {
		return err
	}
	_, err = runRaw(ctx, dir, nil, nil, "update-index", "--cacheinfo", info)
	return err
}

// CommitPaths 只提交 paths 这些文件（照 WebStorm 勾选文件提交）：add 里的新文件先加入 Git，
// 再 commit --only，暂存区里别的文件原样留着、不进这次提交。amend 且 paths 为空时只改提交信息。
func CommitPaths(ctx context.Context, dir, message string, paths, add []string, amend bool) error {
	if len(add) > 0 {
		if err := Stage(ctx, dir, add...); err != nil {
			return err
		}
	}
	args := []string{"commit", "-F", "-", "--only"}
	if amend {
		args = append(args, "--amend")
	}
	_, err := runRaw(ctx, dir, []byte(message), nil, pathArgs(args, paths)...)
	return err
}

// LastMessage 是上一次提交的完整提交信息，修改上一次提交时填进输入框。
func LastMessage(ctx context.Context, dir string) string {
	out, err := runRaw(ctx, dir, nil, nil, "log", "-1", "--format=%B")
	if err != nil {
		return ""
	}
	return strings.TrimRight(string(out), "\n")
}

// Stash 是一次搁置。
type Stash struct {
	Ref     string // stash@{n}
	Message string
	When    time.Time
}

// StashPush 搁置全部改动，含未跟踪文件。
func StashPush(ctx context.Context, dir, message string) error {
	args := []string{"stash", "push", "--include-untracked"}
	if message != "" {
		args = append(args, "-m", message)
	}
	_, err := runRaw(ctx, dir, nil, nil, args...)
	return err
}

func Stashes(ctx context.Context, dir string) ([]Stash, error) {
	out, err := runRaw(ctx, dir, nil, nil, "stash", "list", "-z", "--format=%gd%x1f%ct%x1f%gs")
	if err != nil {
		return nil, err
	}
	var list []Stash
	for _, rec := range strings.Split(string(out), "\x00") {
		p := strings.SplitN(rec, "\x1f", 3)
		if len(p) != 3 {
			continue
		}
		sec, _ := strconv.ParseInt(p[1], 10, 64)
		list = append(list, Stash{Ref: p[0], Message: p[2], When: time.Unix(sec, 0)})
	}
	return list, nil
}

// StashPop 取回搁置的改动并删掉这次搁置；有冲突时 git 会保留它。
func StashPop(ctx context.Context, dir, ref string) error {
	_, err := runRaw(ctx, dir, nil, nil, "stash", "pop", "--index", ref)
	return err
}

func StashDrop(ctx context.Context, dir, ref string) error {
	_, err := runRaw(ctx, dir, nil, nil, "stash", "drop", ref)
	return err
}
