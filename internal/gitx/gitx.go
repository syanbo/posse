// Package gitx 调用系统 git，保证行为与命令行一致。
package gitx

import (
	"bytes"
	"context"
	"fmt"
	"os/exec"
	"strings"
	"time"
)

func run(ctx context.Context, dir string, args ...string) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, 2*time.Minute)
	defer cancel()
	cmd := exec.CommandContext(ctx, "git", args...)
	cmd.Dir = dir
	// 禁止 git 弹出交互式凭据提示，否则常驻进程会挂住
	cmd.Env = append(cmd.Environ(), "GIT_TERMINAL_PROMPT=0")
	var out, errb bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &errb
	if err := cmd.Run(); err != nil {
		msg := strings.TrimSpace(errb.String())
		if msg == "" {
			msg = err.Error()
		}
		return "", fmt.Errorf("git %s: %s", strings.Join(args, " "), msg)
	}
	return strings.TrimSpace(out.String()), nil
}

// TopLevel 返回 dir 所在仓库的根目录；不是 git 仓库时返回空串。
func TopLevel(ctx context.Context, dir string) string {
	out, err := run(ctx, dir, "rev-parse", "--show-toplevel")
	if err != nil {
		return ""
	}
	return out
}

func CurrentBranch(ctx context.Context, dir string) (string, error) {
	return run(ctx, dir, "rev-parse", "--abbrev-ref", "HEAD")
}

func BranchExists(ctx context.Context, repo, branch string) bool {
	_, err := run(ctx, repo, "rev-parse", "--verify", "--quiet", "refs/heads/"+branch)
	return err == nil
}

// AddWorktree 在 dir 新建 worktree：分支不存在时从 base 创建，存在则直接检出。
func AddWorktree(ctx context.Context, repo, dir, branch, base string) error {
	if BranchExists(ctx, repo, branch) {
		_, err := run(ctx, repo, "worktree", "add", dir, branch)
		return err
	}
	args := []string{"worktree", "add", "-b", branch, dir}
	if base != "" {
		args = append(args, base)
	}
	_, err := run(ctx, repo, args...)
	return err
}

func RemoveWorktree(ctx context.Context, repo, dir string, force bool) error {
	args := []string{"worktree", "remove", dir}
	if force {
		args = append(args, "--force")
	}
	_, err := run(ctx, repo, args...)
	return err
}

func DeleteBranch(ctx context.Context, repo, branch string, force bool) error {
	flag := "-d"
	if force {
		flag = "-D"
	}
	_, err := run(ctx, repo, "branch", flag, branch)
	return err
}

// Dirty 返回未提交改动（含未跟踪文件）的 porcelain 输出，干净时为空。
func Dirty(ctx context.Context, dir string) (string, error) {
	return run(ctx, dir, "status", "--porcelain")
}

// DirtyTracked 只看已跟踪文件的改动；合并只取已提交的内容，未跟踪文件不影响合并结果。
func DirtyTracked(ctx context.Context, dir string) (string, error) {
	return run(ctx, dir, "status", "--porcelain", "--untracked-files=no")
}

// Changes 是 worktree 相对分叉点的改动，包含未提交的部分。
type Changes struct {
	Commits    int  `json:"commits"` // 分叉点之后的提交数
	Files      int  `json:"files"`
	Insertions int  `json:"insertions"`
	Deletions  int  `json:"deletions"`
	Untracked  int  `json:"untracked"`
	Dirty      bool `json:"dirty"` // 已跟踪文件有未提交的改动
}

// DiffSummary 统计 dir 的 HEAD 和工作区相对 base 分叉点的改动；base 之后新增的提交不算在内。
func DiffSummary(ctx context.Context, dir, base string) (Changes, error) {
	var c Changes
	mb, err := run(ctx, dir, "merge-base", base, "HEAD")
	if err != nil {
		return c, err
	}
	if n, err := run(ctx, dir, "rev-list", "--count", mb+"..HEAD"); err == nil {
		fmt.Sscan(n, &c.Commits)
	}
	stat, err := run(ctx, dir, "diff", "--shortstat", mb)
	if err != nil {
		return c, err
	}
	c.Files, c.Insertions, c.Deletions = parseShortStat(stat)
	// -uall 逐个列出未跟踪文件（"??"，与 ls-files --others --exclude-standard 一致），其余行是已跟踪文件的改动
	if out, err := run(ctx, dir, "status", "--porcelain", "-uall"); err == nil {
		for _, line := range strings.Split(out, "\n") {
			switch {
			case line == "":
			case strings.HasPrefix(line, "??"):
				c.Untracked++
			default:
				c.Dirty = true
			}
		}
	}
	return c, nil
}

// parseShortStat 解析 " 3 files changed, 10 insertions(+), 2 deletions(-)"。
func parseShortStat(s string) (files, ins, del int) {
	for _, part := range strings.Split(s, ",") {
		var n int
		var word string
		if _, err := fmt.Sscan(strings.TrimSpace(part), &n, &word); err != nil {
			continue
		}
		switch {
		case strings.HasPrefix(word, "file"):
			files = n
		case strings.HasPrefix(word, "insertion"):
			ins = n
		case strings.HasPrefix(word, "deletion"):
			del = n
		}
	}
	return
}

// Merge 在 repo 当前分支上以 merge commit 合入 branch；冲突时还原，仓库保持合并前的样子。
func Merge(ctx context.Context, repo, branch string) error {
	_, err := run(ctx, repo, "merge", "--no-ff", "--no-edit", branch)
	if err != nil {
		run(context.WithoutCancel(ctx), repo, "merge", "--abort")
	}
	return err
}
