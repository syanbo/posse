package gitx

import (
	"context"
	"errors"
	"strings"
)

// PushPlan 是一次推送：本地分支推到哪个远端的哪个分支，会推哪些提交。
type PushPlan struct {
	Branch       string
	Remote       string
	RemoteBranch string
	NewBranch    bool // 远端还没有跟踪分支：推送时设上游
	Commits      []CommitInfo
}

// PushPreview 算出推送 branch 会推什么：有跟踪分支时是它之后的提交；没有时推到默认远端的同名分支，
// 列出所有远端分支里都没有的提交。
func PushPreview(ctx context.Context, dir, branch string) (PushPlan, error) {
	plan := PushPlan{Branch: branch}
	if up, err := run(ctx, dir, "rev-parse", "--abbrev-ref", branch+"@{upstream}"); err == nil && up != "" {
		plan.Remote, plan.RemoteBranch, _ = strings.Cut(up, "/")
		cs, err := CommitsBetween(ctx, dir, up, branch, 500)
		plan.Commits = cs
		return plan, err
	}
	remote, err := defaultRemote(ctx, dir)
	if err != nil {
		return plan, err
	}
	plan.Remote, plan.RemoteBranch, plan.NewBranch = remote, branch, true
	out, err := runRaw(ctx, dir, nil, nil, "log", commitFormat, "-n", "500", branch, "--not", "--remotes", "--")
	if err != nil {
		return plan, err
	}
	plan.Commits = parseCommits(out)
	return plan, nil
}

// defaultRemote 优先用 origin，否则用第一个远端。
func defaultRemote(ctx context.Context, dir string) (string, error) {
	out, err := run(ctx, dir, "remote")
	if err != nil {
		return "", err
	}
	names := strings.Fields(out)
	if len(names) == 0 {
		return "", errors.New("这个仓库没有配置远端")
	}
	for _, n := range names {
		if n == "origin" {
			return n, nil
		}
	}
	return names[0], nil
}

// Push 按计划推送；force 时用 --force-with-lease：远端有你没拉到的提交时会被拒绝，不会覆盖别人的提交。
// tags 为真时一起推送指向这些提交的附注标签。
func Push(ctx context.Context, dir string, plan PushPlan, force, tags bool) error {
	args := []string{"push"}
	if force {
		args = append(args, "--force-with-lease")
	}
	if plan.NewBranch {
		args = append(args, "--set-upstream")
	}
	if tags {
		args = append(args, "--follow-tags")
	}
	args = append(args, plan.Remote, "refs/heads/"+plan.Branch+":refs/heads/"+plan.RemoteBranch)
	_, err := runRaw(ctx, dir, nil, nil, args...)
	return err
}

// Rejected 报告推送是不是因为远端有本地没有的提交被拒绝（要先更新，或者强制推送）。
func Rejected(err error) bool {
	var ge *Error
	return errors.As(err, &ge) && (strings.Contains(ge.Msg, "[rejected]") || strings.Contains(ge.Msg, "stale info") ||
		strings.Contains(ge.Msg, "fetch first") || strings.Contains(ge.Msg, "non-fast-forward"))
}
