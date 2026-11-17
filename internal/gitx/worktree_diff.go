package gitx

import (
	"context"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// dirty 禁止 Git 调用子模块 status，但保留 gitlink 的结构及 HEAD 变化。
// 子模块脏状态只来自自己的隔离读取，不能把目标配置带进子进程。
func worktreeDiff(ctx context.Context, n *worktreeNode, commit string) ([]FileChange, error) {
	dir, err := os.MkdirTemp("", "posse-worktree-compare-")
	if err != nil {
		return nil, err
	}
	defer os.RemoveAll(dir)
	gitDir := filepath.Join(dir, ".git")
	for _, sub := range []string{"objects/info", "refs", "info"} {
		if err := os.MkdirAll(filepath.Join(gitDir, sub), 0700); err != nil {
			return nil, err
		}
	}
	if err := os.Symlink(filepath.Join(n.id.CommonDir, "objects"), filepath.Join(gitDir, "source-objects")); err != nil {
		return nil, err
	}
	config := "[core]\nrepositoryformatversion = 0\nbare = false\n"
	if len(commit) == 64 {
		config = "[core]\nrepositoryformatversion = 1\nbare = false\n[extensions]\nobjectformat = sha256\n"
	}
	for name, data := range map[string]string{"config": config, "HEAD": commit + "\n", "objects/info/alternates": "../source-objects\n", "info/attributes": n.attributeFile} {
		if err := os.WriteFile(filepath.Join(gitDir, name), []byte(data), 0600); err != nil {
			return nil, err
		}
	}
	ctx = context.WithValue(ctx, readIsolatedKey{}, true)
	ctx = context.WithValue(ctx, readStrictKey{}, true)
	env := []string{"GIT_DIR=" + gitDir, "GIT_WORK_TREE=" + n.id.Root, "GIT_INDEX_FILE=" + filepath.Join(gitDir, "index"), "GIT_NO_REPLACE_OBJECTS=1"}
	if len(n.index) > 0 {
		if _, err := runRead(ctx, dir, n.index, env, "update-index", "-z", "--index-info"); err != nil {
			return nil, err
		}
	} else {
		if _, err := runRead(ctx, dir, nil, env, "read-tree", "--empty"); err != nil {
			return nil, err
		}
	}
	args := append([]string{}, n.args...)
	// 只读 diff 不会写回换行；关闭未来检出的换行告警，保留严格 stderr 检查。
	args = append(args, "-c", "core.safecrlf=false", "diff", "--no-ext-diff", "--no-textconv", "--ignore-submodules=dirty", "--name-status", "-z", "--find-renames", commit, "--")
	raw, err := runRead(ctx, dir, nil, env, args...)
	if err != nil {
		return nil, err
	}
	changes, err := parseHistoryChanges(raw)
	if err != nil {
		return nil, err
	}
	if n.status {
		// 子模块 status 保留暂存改动，即使工作树又改回 HEAD；净 diff 不能替代它。
		cachedArgs := append([]string{}, n.args...)
		cachedArgs = append(cachedArgs, "diff", "--cached", "--no-ext-diff", "--no-textconv", "--ignore-submodules=dirty", "--name-status", "-z", "--find-renames", commit, "--")
		cached, err := runRead(ctx, dir, nil, env, cachedArgs...)
		if err != nil {
			return nil, err
		}
		staged, err := parseHistoryChanges(cached)
		if err != nil {
			return nil, err
		}
		seen := map[string]bool{}
		for _, c := range changes {
			seen[c.Path] = true
		}
		for _, c := range staged {
			if !seen[c.Path] {
				changes = append(changes, c)
				seen[c.Path] = true
			}
		}
	}
	tree, err := worktreeTree(ctx, n.id.Root, commit)
	if err != nil {
		return nil, err
	}
	indexed := map[string]string{}
	for _, e := range n.entries {
		indexed[e.name] = e.mode
	}
	filtered := changes[:0]
	present := map[string]bool{}
	for _, c := range changes {
		old := c.Path
		if c.Orig != "" {
			old = c.Orig
		}
		if c.Status != 'T' && n.ignore(c.Path) == "all" && (tree[old].mode == "160000" || indexed[c.Path] == "160000") {
			continue
		}
		filtered = append(filtered, c)
		present[c.Path] = true
	}
	changes = filtered
	for i := range n.entries {
		e := &n.entries[i]
		if e.child == nil {
			continue
		}
		ignore := n.ignore(e.name)
		subChanges, err := worktreeDiff(ctx, e.child, e.child.head)
		if err != nil {
			return nil, err
		}
		e.file.Dirty = ignore != "dirty" && ignore != "all" && (len(subChanges) > 0 || ignore == "none" && len(e.child.untracked) > 0)
		if e.file.Dirty && !present[e.name] {
			changes = append(changes, FileChange{Path: e.name, Status: 'M'})
			present[e.name] = true
		}
	}
	sort.Slice(changes, func(i, j int) bool { return changes[i].Path < changes[j].Path })
	return changes, nil
}

type worktreeBase struct{ mode, oid string }

func worktreeTree(ctx context.Context, dir, commit string) (map[string]worktreeBase, error) {
	raw, err := historyRead(ctx, dir, nil, "ls-tree", "-r", "-z", commit)
	if err != nil {
		return nil, err
	}
	result := map[string]worktreeBase{}
	if len(raw) == 0 {
		return result, nil
	}
	if raw[len(raw)-1] != 0 {
		return nil, ErrWorktreeOutput
	}
	for _, record := range strings.Split(string(raw[:len(raw)-1]), "\x00") {
		meta, name, ok := strings.Cut(record, "\t")
		f := strings.Fields(meta)
		if !ok || len(f) != 3 || !ValidBlameFile(name) || !ValidBlobOID(f[2]) || len(f[2]) != len(commit) {
			return nil, ErrWorktreeOutput
		}
		switch f[0] {
		case "100644", "100755", "120000":
			if f[1] != "blob" {
				return nil, ErrWorktreeOutput
			}
		case "160000":
			if f[1] != "commit" {
				return nil, ErrWorktreeOutput
			}
		default:
			return nil, ErrWorktreeUnsupported
		}
		if _, ok := result[name]; ok {
			return nil, ErrWorktreeOutput
		}
		result[name] = worktreeBase{f[0], f[2]}
	}
	return result, nil
}
