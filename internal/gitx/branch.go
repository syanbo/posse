package gitx

import (
	"context"
	"errors"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

// Branch 是一个本地或远端分支。
type Branch struct {
	Name     string // 本地分支名，或 origin/main 这样的远端分支
	Remote   bool
	Current  bool
	Upstream string // 跟踪的远端分支
	Ahead    int
	Behind   int
	Gone     bool   // 跟踪的远端分支已被删除
	Worktree string // 在哪个 worktree 里检出（含当前这个），没有为空
	Time     time.Time
}

// Branches 列出本地和远端分支；远端的 HEAD 指针不算分支。
func Branches(ctx context.Context, dir string) ([]Branch, error) {
	out, err := runRaw(ctx, dir, nil, nil, "for-each-ref",
		"--format=%(refname)%1f%(HEAD)%1f%(upstream:short)%1f%(upstream:track,nobracket)%1f%(worktreepath)%1f%(committerdate:unix)",
		"refs/heads", "refs/remotes")
	if err != nil {
		return nil, err
	}
	var list []Branch
	for _, line := range strings.Split(string(out), "\n") {
		p := strings.Split(line, "\x1f")
		if len(p) != 6 {
			continue
		}
		b := Branch{Current: p[1] == "*", Upstream: p[2], Worktree: p[4]}
		switch {
		case strings.HasPrefix(p[0], "refs/heads/"):
			b.Name = strings.TrimPrefix(p[0], "refs/heads/")
		case strings.HasPrefix(p[0], "refs/remotes/"):
			b.Name, b.Remote = strings.TrimPrefix(p[0], "refs/remotes/"), true
			if strings.HasSuffix(b.Name, "/HEAD") {
				continue
			}
		default:
			continue
		}
		b.Ahead, b.Behind, b.Gone = parseTrack(p[3])
		if sec, err := strconv.ParseInt(p[5], 10, 64); err == nil {
			b.Time = time.Unix(sec, 0)
		}
		list = append(list, b)
	}
	return list, nil
}

// Tags 列出标签，新的在前。
func Tags(ctx context.Context, dir string) ([]string, error) {
	out, err := run(ctx, dir, "for-each-ref", "--sort=-creatordate", "--format=%(refname:short)", "refs/tags")
	if err != nil || out == "" {
		return nil, err
	}
	return strings.Split(out, "\n"), nil
}

// parseTrack 解析 "ahead 2, behind 1"、"gone"。
func parseTrack(s string) (ahead, behind int, gone bool) {
	if s == "gone" {
		return 0, 0, true
	}
	for _, part := range strings.Split(s, ",") {
		f := strings.Fields(part)
		if len(f) != 2 {
			continue
		}
		n, _ := strconv.Atoi(f[1])
		switch f[0] {
		case "ahead":
			ahead = n
		case "behind":
			behind = n
		}
	}
	return
}

// RecentBranches 是最近检出过的本地分支，新的在前，最多 n 个；取自 HEAD 的 reflog。
func RecentBranches(ctx context.Context, dir string, n int) []string {
	out, err := runRaw(ctx, dir, nil, nil, "reflog", "show", "--format=%gs", "-n", "200", "HEAD")
	if err != nil {
		return nil
	}
	var list []string
	seen := map[string]bool{}
	for _, line := range strings.Split(string(out), "\n") {
		rest, ok := strings.CutPrefix(line, "checkout: moving from ")
		if !ok {
			continue
		}
		from, to, ok := strings.Cut(rest, " to ")
		if !ok {
			continue
		}
		for _, name := range []string{to, from} {
			if name != "" && !seen[name] && !looksLikeHash(name) {
				seen[name] = true
				list = append(list, name)
			}
		}
		if len(list) >= n {
			return list[:n]
		}
	}
	return list
}

func looksLikeHash(s string) bool {
	if len(s) < 7 || len(s) > 40 {
		return false
	}
	for _, r := range s {
		if !strings.ContainsRune("0123456789abcdef", r) {
			return false
		}
	}
	return true
}

// Fetch 从所有远端拉取。
func Fetch(ctx context.Context, dir string) error {
	_, err := runRaw(ctx, dir, nil, nil, "fetch", "--all")
	return err
}

// Checkout 切换到分支：远端分支检出成同名的本地跟踪分支，本地已经有同名分支时直接切过去。
func Checkout(ctx context.Context, dir string, b Branch) error {
	if !b.Remote {
		_, err := runRaw(ctx, dir, nil, nil, "switch", b.Name)
		return err
	}
	local := remoteShort(b.Name)
	if BranchExists(ctx, dir, local) {
		_, err := runRaw(ctx, dir, nil, nil, "switch", local)
		return err
	}
	_, err := runRaw(ctx, dir, nil, nil, "switch", "--track", b.Name)
	return err
}

// remoteShort 去掉远端名：origin/feat/x → feat/x。
func remoteShort(name string) string {
	_, rest, ok := strings.Cut(name, "/")
	if !ok {
		return name
	}
	return rest
}

// CheckoutRevision 检出标签或某个提交（分离 HEAD）。
func CheckoutRevision(ctx context.Context, dir, rev string) error {
	_, err := runRaw(ctx, dir, nil, nil, "switch", "--detach", rev)
	return err
}

// CreateBranch 从 from（空表示当前 HEAD）新建分支，checkout 为真时切过去。
func CreateBranch(ctx context.Context, dir, name, from string, checkout bool) error {
	args := []string{"branch", name}
	if checkout {
		args = []string{"switch", "-c", name}
	}
	if from != "" {
		args = append(args, from)
	}
	_, err := runRaw(ctx, dir, nil, nil, args...)
	return err
}

// ErrNoUpstream 表示分支没有跟踪的远端分支，没法更新。
var ErrNoUpstream = errors.New("这个分支没有跟踪的远端分支")

// Update 用远端更新分支。当前分支：拉取后把本地提交 rebase 上去，未提交的改动临时搁置再取回；
// 其他分支：只能快进（git fetch 远端分支到本地分支），分叉时报错。
func Update(ctx context.Context, dir string, b Branch) error {
	if b.Upstream == "" {
		return ErrNoUpstream
	}
	if b.Current {
		_, err := runRaw(ctx, dir, nil, nil, "pull", "--rebase", "--autostash")
		return err
	}
	remote, branch, ok := strings.Cut(b.Upstream, "/")
	if !ok {
		return ErrNoUpstream
	}
	_, err := runRaw(ctx, dir, nil, nil, "fetch", remote, branch+":refs/heads/"+b.Name)
	return err
}

// Rebase 把当前分支 rebase 到 onto 上。
func Rebase(ctx context.Context, dir, onto string) error {
	_, err := runRaw(ctx, dir, nil, nil, "rebase", onto)
	return err
}

// CheckoutAndRebase 切到 branch，再把它 rebase 到 onto 上。
func CheckoutAndRebase(ctx context.Context, dir, branch, onto string) error {
	_, err := runRaw(ctx, dir, nil, nil, "rebase", onto, branch)
	return err
}

// MergeInto 把 branch 合并到当前分支。
func MergeInto(ctx context.Context, dir, branch string) error {
	_, err := runRaw(ctx, dir, nil, nil, "merge", "--no-edit", branch)
	return err
}

func RenameBranch(ctx context.Context, dir, old, name string) error {
	_, err := runRaw(ctx, dir, nil, nil, "branch", "-m", old, name)
	return err
}

// NotMerged 报告删除分支失败是不是因为它还没合并（要强制删除才行）。
func NotMerged(err error) bool {
	return err != nil && strings.Contains(err.Error(), "not fully merged")
}

// DeleteRemoteBranch 删除远端分支（origin/x）。
func DeleteRemoteBranch(ctx context.Context, dir, name string) error {
	remote, branch, ok := strings.Cut(name, "/")
	if !ok {
		return errors.New("不是远端分支：" + name)
	}
	_, err := runRaw(ctx, dir, nil, nil, "push", remote, "--delete", branch)
	return err
}

// CommitInfo 是一个提交：日志、比较和推送列表里的一行。
type CommitInfo struct {
	Hash    string
	Parents []string
	Author  string
	Email   string
	Time    time.Time
	Refs    string // 指向它的分支和标签，%D 的格式
	Subject string
}

// commitFormat 和 parseCommits 配套：字段用 0x1f 分隔，记录用 0x1e 结尾。
const commitFormat = "--format=%H%x1f%P%x1f%an%x1f%ae%x1f%at%x1f%D%x1f%s%x1e"

func parseCommits(out []byte) []CommitInfo {
	var list []CommitInfo
	for _, rec := range strings.Split(string(out), "\x1e") {
		rec = strings.TrimLeft(rec, "\n")
		p := strings.Split(rec, "\x1f")
		if len(p) != 7 {
			continue
		}
		c := CommitInfo{Hash: p[0], Author: p[2], Email: p[3], Refs: p[5], Subject: p[6]}
		if p[1] != "" {
			c.Parents = strings.Fields(p[1])
		}
		if sec, err := strconv.ParseInt(p[4], 10, 64); err == nil {
			c.Time = time.Unix(sec, 0)
		}
		list = append(list, c)
	}
	return list
}

// CommitsBetween 是在 to 里、不在 from 里的提交（git log from..to），新的在前，最多 limit 个。
func CommitsBetween(ctx context.Context, dir, from, to string, limit int) ([]CommitInfo, error) {
	out, err := runRaw(ctx, dir, nil, nil, "log", commitFormat, "-n", strconv.Itoa(limit), from+".."+to, "--")
	if err != nil {
		return nil, err
	}
	return parseCommits(out), nil
}

// FileChange 是两个版本之间改了的一个文件。Status 是 A、M、D、R 等。
type FileChange struct {
	Path, Orig string
	Status     byte
}

func parseNameStatus(out []byte) []FileChange {
	var list []FileChange
	f := strings.Split(string(out), "\x00")
	for i := 0; i < len(f); i++ {
		if f[i] == "" {
			continue
		}
		st := f[i][0]
		switch st {
		case 'R', 'C':
			if i+2 < len(f) {
				list = append(list, FileChange{Status: st, Orig: f[i+1], Path: f[i+2]})
				i += 2
			}
		default:
			if i+1 < len(f) {
				list = append(list, FileChange{Status: st, Path: f[i+1]})
				i++
			}
		}
	}
	return list
}

// DiffWithWorkingTree 是工作区（含没提交的改动）和 rev 之间不同的文件；未跟踪的文件不算。
func DiffWithWorkingTree(ctx context.Context, dir, rev string) ([]FileChange, error) {
	out, err := runRaw(ctx, dir, nil, nil, "diff", "--name-status", "-z", "--find-renames", rev, "--")
	if err != nil {
		return nil, err
	}
	return parseNameStatus(out), nil
}

// CommitFiles 是一个提交改的文件（合并提交相对第一个父提交）。
func CommitFiles(ctx context.Context, dir, hash string) ([]FileChange, error) {
	out, err := runRaw(ctx, dir, nil, nil, "diff-tree", "-r", "--root", "--no-commit-id", "--name-status", "-z", "--find-renames", "-m", "--first-parent", hash)
	if err != nil {
		return nil, err
	}
	return parseNameStatus(out), nil
}

// WorktreeOf 把 for-each-ref 给的 worktree 路径（已解析符号链接）和 dir 比较，报告分支是否检出在别的 worktree 里。
func WorktreeOf(b Branch, dir string) (other string, ok bool) {
	if b.Worktree == "" {
		return "", false
	}
	real, err := filepath.EvalSymlinks(dir)
	if err != nil {
		real = dir
	}
	if b.Worktree == real || b.Worktree == dir {
		return "", false
	}
	return b.Worktree, true
}
