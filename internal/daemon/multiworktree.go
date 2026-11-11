package daemon

import (
	"context"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"posse/internal/gitx"
	"posse/internal/rpc"
	"posse/internal/store"
)

// 多仓库目录上的 worktree 工作区：项目目录下有几个仓库（gitx.Discover 找到的），
// 给选中的每个仓库在工作区里同样的相对位置建一个 worktree，各仓库用同一个分支名、
// 从各自当时所在的分支开出来。工作区里只有这些仓库，项目目录里别的文件不带过来。

// addRepoWorktrees 给 w 建选中的仓库的 worktree，填上 w.Repos；中途失败时撤掉已经建好的。
// 返回的 undo 撤掉这次建的全部（worktree、这次新开的分支、目录），记录写不进数据库时用。
func (d *Daemon) addRepoWorktrees(ctx context.Context, proj store.Project, w *store.Workspace, names []string, base string) (undo func(), err error) {
	found := gitx.Discover(ctx, proj.Path, !proj.SingleRepo)
	byRel := map[string]string{}
	for _, r := range found.Repos {
		rel, err := filepath.Rel(proj.Path, r.Root)
		if err != nil || upward(rel) {
			continue
		}
		byRel[filepath.ToSlash(rel)] = r.Real
	}
	var repos []store.WorkspaceRepo
	for _, n := range names {
		rel := filepath.ToSlash(filepath.Clean(n))
		src, ok := byRel[rel]
		if !ok {
			return nil, rpc.Errorf(rpc.CodeBadRequest, "%s is not a git repository in project %s", n, proj.Name)
		}
		if slices.ContainsFunc(repos, func(r store.WorkspaceRepo) bool { return r.Path == rel }) {
			continue
		}
		b := base
		if b == "" {
			b, _ = gitx.CurrentBranch(ctx, src)
		}
		repos = append(repos, store.WorkspaceRepo{Path: rel, Source: src, Base: b})
	}
	if len(repos) == 0 {
		return nil, rpc.Errorf(rpc.CodeBadRequest, "no repositories selected")
	}
	// 外层仓库先建：嵌套仓库的 worktree 放在外层的 worktree 里面
	slices.SortFunc(repos, func(a, b store.WorkspaceRepo) int { return strings.Compare(repoSortKey(a.Path), repoSortKey(b.Path)) })
	if _, err := os.Stat(w.Path); err == nil {
		return nil, rpc.Errorf(rpc.CodeConflict, "%s already exists", w.Path)
	}
	type made struct {
		repo      store.WorkspaceRepo
		newBranch bool
	}
	var done []made
	undo = func() {
		ctx := context.WithoutCancel(ctx)
		for i := len(done) - 1; i >= 0; i-- {
			r := done[i].repo
			gitx.RemoveWorktree(ctx, r.Source, repoDir(w.Path, r.Path), true)
			if done[i].newBranch {
				gitx.DeleteBranch(ctx, r.Source, w.Branch, true)
			}
		}
		os.RemoveAll(w.Path)
	}
	for _, r := range repos {
		dir := repoDir(w.Path, r.Path)
		if err := os.MkdirAll(filepath.Dir(dir), 0o755); err != nil {
			undo()
			return nil, err
		}
		// 外层仓库跟踪着这个位置：软链（指向这个仓库）检出成了软链，子模块检出成空目录。
		// 软链换成这个仓库的 worktree；外层看到的这处改动由 repoDirty 略过
		if fi, err := os.Lstat(dir); err == nil && fi.Mode()&os.ModeSymlink != 0 {
			os.Remove(dir)
		}
		existed := gitx.BranchExists(ctx, r.Source, w.Branch)
		if err := gitx.AddWorktree(ctx, r.Source, dir, w.Branch, r.Base); err != nil {
			undo()
			return nil, rpc.Errorf(rpc.CodeConflict, "%s: %v", r.Path, err)
		}
		done = append(done, made{r, !existed})
	}
	w.Repos = repos
	return undo, nil
}

// repoSortKey 让 "." 排最前，父目录排在子目录前面。
func repoSortKey(rel string) string {
	if rel == "." {
		return ""
	}
	return rel
}

func repoDir(ws, rel string) string {
	return filepath.Join(ws, filepath.FromSlash(rel))
}

// upward 报告相对路径 rel 走到了上级目录（..foo 这种目录名不算）。
func upward(rel string) bool {
	return rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator))
}

// nestedIn 是 repos 里放在 rel 这个仓库里面的仓库，相对 rel 的路径。
func nestedIn(repos []store.WorkspaceRepo, rel string) []string {
	var out []string
	for _, r := range repos {
		if r.Path == rel {
			continue
		}
		switch {
		case rel == ".":
			out = append(out, r.Path)
		case strings.HasPrefix(r.Path, rel+"/"):
			out = append(out, strings.TrimPrefix(r.Path, rel+"/"))
		}
	}
	return out
}

// repoDirty 是 worktree 里未提交的改动（git status --porcelain -uall），放在它里面的另一个仓库的 worktree 不算：
// 未跟踪的嵌套仓库（?? inner/），以及外层跟踪着的子模块、软链（内层有了新提交是 M，换成 worktree 的软链是 T，内层删掉后是 D）。
// -uall 逐个列出未跟踪目录里的文件，嵌套仓库仍是单独一行 inner/，不会和旁边的文件并成一行。
func repoDirty(ctx context.Context, w store.Workspace, r store.WorkspaceRepo) (string, error) {
	out, err := gitx.DirtyAll(ctx, repoDir(w.Path, r.Path))
	if err != nil || out == "" {
		return out, err
	}
	inner := nestedIn(w.Repos, r.Path)
	var keep []string
	for _, line := range strings.Split(out, "\n") {
		// 输出整体去掉了首尾空白，第一行开头的空格（" M x"）可能没了，路径从第 3 个字符起去空白取
		if len(line) > 2 && slices.ContainsFunc(inner, func(n string) bool {
			p := strings.TrimSpace(line[2:])
			return p == n+"/" || p == n || strings.HasPrefix(p, n+"/")
		}) {
			continue
		}
		keep = append(keep, line)
	}
	return strings.Join(keep, "\n"), nil
}

// reposDirty 报告多仓库工作区里有没有哪个仓库有未提交的改动。
func reposDirty(ctx context.Context, w store.Workspace) bool {
	for _, r := range w.Repos {
		if out, err := repoDirty(ctx, w, r); err == nil && out != "" {
			return true
		}
	}
	return false
}

// removeRepoWorktrees 删多仓库 worktree 工作区：先查所有仓库都没有未提交的改动，再从里往外逐个删，
// 最后删掉工作区目录（只剩建 worktree 时补的空目录；force 时连同别的文件）。left 是没删掉的工作区目录（里面还有别的文件）。
func (d *Daemon) removeRepoWorktrees(ctx context.Context, w store.Workspace, force, deleteBranch bool) (left string, err error) {
	// 仓库已经没了（项目目录被删或挪走）的：git 删不了它的 worktree，force 时跳过，最后只删 Posse 建的残留目录
	var gone []string
	for _, r := range w.Repos {
		if repoGone(r.Source) {
			gone = append(gone, r.Source)
		}
	}
	if len(gone) > 0 && !force {
		return "", rpc.Errorf(rpc.CodeConflict, "repository %s no longer exists; pass force to remove the workspace anyway", strings.Join(gone, ", "))
	}
	if !force {
		var dirty []string
		for _, r := range w.Repos {
			if _, err := os.Stat(repoDir(w.Path, r.Path)); err != nil {
				continue
			}
			if out, err := repoDirty(ctx, w, r); err == nil && out != "" {
				dirty = append(dirty, r.Path+":\n"+out)
			}
		}
		if len(dirty) > 0 {
			return "", rpc.Errorf(rpc.CodeConflict, "worktree has uncommitted changes; pass force to discard:\n%s", strings.Join(dirty, "\n"))
		}
	}
	d.closeWorkspaceTerminals(w.ID)
	var errs []string
	for i := len(w.Repos) - 1; i >= 0; i-- {
		r := w.Repos[i]
		dir := repoDir(w.Path, r.Path)
		if slices.Contains(gone, r.Source) {
			continue
		}
		if _, err := os.Stat(dir); err == nil {
			// 里面放过别的仓库的 worktree：它刚被删掉，外层看到的是删掉的子模块、软链或空目录，
			// 前面已经查过没有别的改动，强制删
			if err := gitx.RemoveWorktree(ctx, r.Source, dir, force || len(nestedIn(w.Repos, r.Path)) > 0); err != nil {
				errs = append(errs, fmt.Sprintf("%s: %v", r.Path, err))
				continue
			}
		} else {
			// 目录已经被删掉了：清掉仓库里的 worktree 记录
			gitx.PruneWorktrees(ctx, r.Source)
		}
		if deleteBranch && w.Branch != "" {
			if err := gitx.DeleteBranch(ctx, r.Source, w.Branch, force); err != nil {
				errs = append(errs, fmt.Sprintf("%s: worktree removed but %v", r.Path, err))
			}
		}
	}
	if len(errs) > 0 {
		return "", rpc.Errorf(rpc.CodeConflict, "%s", strings.Join(errs, "\n"))
	}
	if len(gone) > 0 {
		d.removeLeftover(w.Path)
	} else if force {
		os.RemoveAll(w.Path)
	} else {
		removeEmptyDirs(w.Path)
	}
	if _, err := os.Stat(w.Path); err == nil {
		left = w.Path
	}
	return left, nil
}

// removeEmptyDirs 从下往上删掉 dir 里的空目录（含 dir 本身），有文件的留着。
func removeEmptyDirs(dir string) {
	var dirs []string
	filepath.WalkDir(dir, func(p string, e fs.DirEntry, err error) error {
		if err == nil && e.IsDir() {
			dirs = append(dirs, p)
		}
		return nil
	})
	for i := len(dirs) - 1; i >= 0; i-- {
		os.Remove(dirs[i])
	}
}

// repoChanges 是多仓库 worktree 工作区里一个仓库相对它的基准分支的改动。
type repoChanges struct {
	Path string `json:"path"`
	Base string `json:"base"`
	gitx.Changes
	Error string `json:"error,omitempty"`
}

// repoDiff 是一个仓库相对基准分支的改动；未跟踪文件不算放在它里面的另一个仓库的 worktree。
func repoDiff(ctx context.Context, w store.Workspace, r store.WorkspaceRepo) (gitx.Changes, error) {
	c, err := gitx.DiffSummary(ctx, repoDir(w.Path, r.Path), r.Base)
	if err != nil || len(nestedIn(w.Repos, r.Path)) == 0 {
		return c, err
	}
	c.Untracked, c.Dirty = 0, false
	if out, err := repoDirty(ctx, w, r); err == nil {
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

// diffRepos 统计各仓库的改动，返回各仓库的和合计。
func diffRepos(ctx context.Context, w store.Workspace) ([]repoChanges, gitx.Changes) {
	var out []repoChanges
	var sum gitx.Changes
	for _, r := range w.Repos {
		rc := repoChanges{Path: r.Path, Base: r.Base}
		c, err := repoDiff(ctx, w, r)
		if err != nil {
			rc.Error = err.Error()
		} else {
			rc.Changes = c
			sum.Commits += c.Commits
			sum.Files += c.Files
			sum.Insertions += c.Insertions
			sum.Deletions += c.Deletions
			sum.Untracked += c.Untracked
			sum.Dirty = sum.Dirty || c.Dirty
		}
		out = append(out, rc)
	}
	return out, sum
}

// mergeRepos 把各仓库的分支合回各自的基准分支：合进检出了基准分支的那个 worktree
// （一般是项目里的仓库；从多仓库 worktree 派出去的 worker，基准是派发方的分支，合进派发方的 worktree）。
// 先查完所有仓库再动手：有一个不能合（没提交的改动、没有哪里检出了基准分支）就一个都不合；没有新提交的仓库跳过。
func mergeRepos(ctx context.Context, w store.Workspace) (map[string]any, error) {
	type target struct {
		repo store.WorkspaceRepo
		dir  string
	}
	var todo []target
	var problems []string
	untracked := 0
	for _, r := range w.Repos {
		c, err := repoDiff(ctx, w, r)
		switch {
		case err != nil:
			problems = append(problems, fmt.Sprintf("%s: %v", r.Path, err))
			continue
		case c.Dirty:
			problems = append(problems, fmt.Sprintf("worktree %s has uncommitted changes; commit them first", r.Path))
			continue
		case c.Commits == 0:
			untracked += c.Untracked
			continue
		}
		dir := gitx.CheckedOutAt(ctx, r.Source, r.Base)
		if dir == "" {
			cur, _ := gitx.CurrentBranch(ctx, r.Source)
			problems = append(problems, fmt.Sprintf("repository %s is on branch %s, not %s; switch it before merging", r.Path, cur, r.Base))
			continue
		}
		if out, err := gitx.DirtyTracked(ctx, dir); err != nil || out != "" {
			problems = append(problems, fmt.Sprintf("repository %s has uncommitted changes in %s; commit or stash them first", r.Path, dir))
			continue
		}
		// 合并会冲突的先挡掉：一个仓库合了、下一个才冲突，就成了只合了一半
		// 查不了（git 太旧、命令失败）也挡住：不知道会不会冲突就不动手
		if files, err := gitx.MergeConflicts(ctx, dir, r.Base, w.Branch); err != nil {
			problems = append(problems, fmt.Sprintf("repository %s: %v", r.Path, err))
			continue
		} else if len(files) > 0 {
			problems = append(problems, fmt.Sprintf("repository %s would conflict when merging into %s: %s", r.Path, r.Base, strings.Join(files, ", ")))
			continue
		}
		todo = append(todo, target{r, dir})
	}
	if len(problems) > 0 {
		return nil, rpc.Errorf(rpc.CodeConflict, "nothing merged:\n%v", strings.Join(problems, "\n"))
	}
	if len(todo) == 0 {
		if untracked > 0 {
			return nil, rpc.Errorf(rpc.CodeConflict, "nothing to merge: %s has no commits in any repository (%d untracked files are not committed)", w.Branch, untracked)
		}
		return nil, rpc.Errorf(rpc.CodeConflict, "nothing to merge: %s has no commits in any repository", w.Branch)
	}
	var merged []string
	for _, t := range todo {
		if err := gitx.Merge(ctx, t.dir, w.Branch); err != nil {
			if len(merged) > 0 {
				return nil, rpc.Errorf(rpc.CodeConflict, "merge into %s failed and was rolled back (already merged: %s): %v", t.repo.Path, strings.Join(merged, ", "), err)
			}
			return nil, rpc.Errorf(rpc.CodeConflict, "merge into %s failed and was rolled back: %v", t.repo.Path, err)
		}
		merged = append(merged, t.repo.Path)
	}
	return map[string]any{"merged": w.Branch, "repos": merged}, nil
}
