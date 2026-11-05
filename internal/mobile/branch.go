package mobile

import (
	"context"
	"encoding/json"
	"errors"
	"path/filepath"
	"strings"
	"time"

	"posse/internal/gitx"
)

// 手机切换分支（posse.git.v3）：git.branches 列分支，git.checkout 切过去。规则照桌面的分支弹窗：
// 在别的 worktree 检出着的不切（手机跳到那个工作区），合并、变基进行中不切，工作区里有 agent 正在工作时先问。
// 切换记进桌面 Git 日志的操作记录（Host.RecordGit）。

type branchParams struct {
	wtParams
	Repo *string `json:"repo"`
}

func (c *conn) gitBranches(id string, p json.RawMessage) error {
	if !c.hasCapability("posse.git.v3") {
		return errorf("method_not_found", "Unknown method: git.branches")
	}
	var a branchParams
	if json.Unmarshal(p, &a) != nil || a.Worktree == "" {
		return c.fail(id, "bad_params", "worktree")
	}
	st := c.srv.host.State()
	w, ok := workspaceOf(st, a.Worktree)
	if !ok {
		return errorf("not_found", "workspace %s not found", a.Worktree)
	}
	ctx, cancel := context.WithTimeout(c.ctx, 20*time.Second)
	defer cancel()
	root, err := c.repoRoot(ctx, st, w, a.Repo)
	if err != nil {
		return err
	}
	list, err := gitx.Branches(ctx, root)
	if err != nil {
		return errorf("git", "%v", err)
	}
	status, err := gitx.ReadStatus(ctx, root)
	if err != nil {
		return errorf("git", "%v", err)
	}
	out := []map[string]any{}
	for _, b := range list {
		m := map[string]any{
			"name": b.Name, "remote": b.Remote, "current": b.Current,
			"upstream": b.Upstream, "ahead": b.Ahead, "behind": b.Behind, "gone": b.Gone,
			"time": b.Time.Unix(),
		}
		if other, ok := elsewhere(list, b, root); ok {
			m["elsewhere"] = other
			if ow, ok := c.workspaceAt(ctx, st, other); ok {
				m["workspace"] = worktreeID(ow)
			}
		}
		out = append(out, m)
	}
	return c.reply(id, map[string]any{"head": status.Head, "operation": status.Operation, "branches": out})
}

// elsewhere 报告切到 b 会撞上别的 worktree：本地分支看它自己，远端分支看检出时用的同名本地分支。
func elsewhere(list []gitx.Branch, b gitx.Branch, root string) (string, bool) {
	if b.Remote {
		local := gitx.LocalOf(b.Name)
		for _, l := range list {
			if !l.Remote && l.Name == local {
				return gitx.WorktreeOf(l, root)
			}
		}
		return "", false
	}
	return gitx.WorktreeOf(b, root)
}

// workspaceAt 找检出在 dir 的 Posse 工作区：工作区目录本身，或工作区里找到的一个仓库（多仓库的项目目录、
// 多仓库的 worktree 工作区）。只认找到的仓库，项目目录里别的工具建的 worktree（.claude/worktrees/x）不算项目的主工作区。
func (c *conn) workspaceAt(ctx context.Context, st State, dir string) (Workspace, bool) {
	real := func(p string) string {
		if r, err := filepath.EvalSymlinks(p); err == nil {
			return r
		}
		return filepath.Clean(p)
	}
	dir = real(dir)
	var best Workspace
	bestLen := -1
	for _, w := range st.Workspaces {
		wp := real(w.Path)
		match := wp == dir
		if !match && strings.HasPrefix(dir, wp+string(filepath.Separator)) {
			for _, r := range c.srv.repos.get(ctx, w.Path, !projectSingleRepo(st, w.ProjectID)) {
				if r.Real == dir || real(r.Root) == dir {
					match = true
					break
				}
			}
		}
		// 套在一起的工作区（项目目录里放着别的工作区）取最具体的那个
		if match && len(wp) > bestLen {
			best, bestLen = w, len(wp)
		}
	}
	return best, bestLen >= 0
}

func (c *conn) gitCheckout(id string, p json.RawMessage) error {
	if !c.hasCapability("posse.git.v3") {
		return errorf("method_not_found", "Unknown method: git.checkout")
	}
	var a struct {
		branchParams
		Name   string `json:"name"`
		Remote bool   `json:"remote"`
		// Force 是用户看过"agent 正在工作"的提醒后仍要切换
		Force bool `json:"force"`
	}
	if json.Unmarshal(p, &a) != nil || a.Worktree == "" || a.Name == "" {
		return c.fail(id, "bad_params", "worktree")
	}
	state := c.srv.host.State()
	w, ok := workspaceOf(state, a.Worktree)
	if !ok {
		return errorf("not_found", "workspace %s not found", a.Worktree)
	}
	ctx, cancel := context.WithTimeout(c.ctx, 2*time.Minute)
	defer cancel()
	root, err := c.repoRoot(ctx, state, w, a.Repo)
	if err != nil {
		return err
	}
	list, err := gitx.Branches(ctx, root)
	if err != nil {
		return errorf("git", "%v", err)
	}
	var b gitx.Branch
	found := false
	for _, x := range list {
		if x.Name == a.Name && x.Remote == a.Remote {
			b, found = x, true
		}
	}
	if !found {
		return errorf("not_found", "branch %s not found", a.Name)
	}
	if b.Current {
		return c.reply(id, map[string]any{"switched": false, "head": b.Name})
	}
	if other, ok := elsewhere(list, b, root); ok {
		return errorf("checked_out", "branch %s is checked out in another worktree: %s", a.Name, other)
	}
	if st, err := gitx.ReadStatus(ctx, root); err == nil && st.Operation != "" {
		return errorf("conflict", "cannot switch branches while a %s is in progress", st.Operation)
	}
	if !a.Force {
		for _, t := range w.Terminals {
			if t.Running && t.Agent != "" && t.Status == "working" {
				return c.reply(id, map[string]any{"switched": false, "confirm": t.Agent})
			}
		}
	}
	args := gitx.CheckoutArgs(ctx, root, b)
	start := time.Now()
	err = gitx.Checkout(ctx, root, b)
	rec := GitRecord{WorkspaceID: w.ID, Dir: root, Args: args, Start: start, Took: time.Since(start), Device: c.device.Name}
	var ge *gitx.Error
	if errors.As(err, &ge) {
		rec.Args, rec.Err = ge.Args, ge.Msg
	} else if err != nil {
		rec.Err = err.Error()
	}
	c.srv.host.RecordGit(rec)
	if err != nil {
		return errorf("git", "%s", rec.Err)
	}
	head, _ := gitx.HeadRef(root)
	return c.reply(id, map[string]any{"switched": true, "head": head})
}

// GitRecord 是手机做的一次 git 操作，交给常驻进程记进桌面 Git 日志的操作记录。
type GitRecord struct {
	WorkspaceID string
	Dir         string // 仓库根目录
	Args        []string
	Start       time.Time
	Took        time.Duration
	Err         string // git 的报错，成功为空
	Device      string // 哪台手机
}
