package gitx

import (
	"context"
	"errors"
	"fmt"
	"strings"
)

// PushPlan 是一次推送：本地分支推到哪个远端的哪个分支，会推哪些提交。
type PushPlan struct {
	Branch       string
	Remote       string
	RemoteBranch string
	NewBranch    bool // 远端还没有这个分支
	SetUpstream  bool // 推送时设上游：还没有跟踪分支，或跟踪的远端分支已被删除
	Gone         bool // 跟踪的远端分支已被删除
	Commits      []CommitInfo
}

// PushPreview 算出推送 branch 会推什么，目标和 git push 一样：远端按 branch.<名>.pushRemote、remote.pushDefault、
// 跟踪的远端的顺序；推到跟踪的远端时是跟踪的那个分支，推到别的远端（fork）时是同名分支。
// 远端已有这个分支时列出它之后的提交，没有时列出所有远端分支里都没有的提交。
func PushPreview(ctx context.Context, dir, branch string) (PushPlan, error) {
	plan := PushPlan{Branch: branch}
	out, err := runRaw(ctx, dir, nil, nil, "for-each-ref", "--format=%(push:remotename)%00%(push:remoteref)%00%(upstream:remotename)%00%(upstream:remoteref)%00%(upstream:track)", "refs/heads/"+branch)
	if err != nil {
		return plan, err
	}
	f := strings.Split(strings.TrimSpace(string(out)), "\x00")
	if len(f) != 5 {
		return plan, fmt.Errorf("no such branch: %s", branch)
	}
	pushRemote, pushRef, upRemote, upRef := f[0], f[1], f[2], f[3]
	plan.Gone = f[4] == "[gone]"
	plan.SetUpstream = upRef == "" || plan.Gone
	plan.Remote, plan.RemoteBranch = pushRemote, branch
	switch {
	case pushRef != "":
		plan.RemoteBranch = strings.TrimPrefix(pushRef, "refs/heads/")
	case pushRemote == upRemote && upRef != "":
		plan.RemoteBranch = strings.TrimPrefix(upRef, "refs/heads/")
	}
	if plan.Remote == "" {
		if plan.Remote, err = defaultRemote(ctx, dir); err != nil {
			return plan, err
		}
	}
	tracking := "refs/remotes/" + plan.Remote + "/" + plan.RemoteBranch
	if _, err := run(ctx, dir, "rev-parse", "--verify", "--quiet", tracking); err == nil {
		cs, err := CommitsBetween(ctx, dir, tracking, branch, 500)
		plan.Commits = cs
		return plan, err
	}
	plan.NewBranch = true
	out, err = runRaw(ctx, dir, nil, nil, "log", commitFormat, "-n", "500", branch, "--not", "--remotes", "--")
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
		return "", errors.New("this repository has no remote")
	}
	for _, n := range names {
		if n == "origin" {
			return n, nil
		}
	}
	return names[0], nil
}

// Push 按计划推送；force 时用 --force-with-lease 加 --force-if-includes：远端有你没合进来的提交时会被拒绝，
// 不会覆盖别人的提交。只用 lease 不够：自动 fetch 把远端引用刷新到别人的提交后，lease 照样放行。
// tags 为真时一起推送指向这些提交的附注标签。
func Push(ctx context.Context, dir string, plan PushPlan, force, tags bool) error {
	args := []string{"push"}
	if force {
		args = append(args, "--force-with-lease", "--force-if-includes")
	}
	if plan.SetUpstream {
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
