package mobile

import (
	"context"
	"encoding/json"
	"path"
	"strings"
	"time"

	"hive/internal/gitx"
)

func (c *conn) gitStatus(id string, p json.RawMessage) error {
	if !c.hasCapability("hive.git.v1") {
		return errorf("method_not_found", "Unknown method: git.status")
	}
	var a wtParams
	if json.Unmarshal(p, &a) != nil || a.Worktree == "" {
		return c.fail(id, "bad_params", "worktree")
	}
	w, ok := workspaceOf(c.srv.host.State(), a.Worktree)
	if !ok {
		return errorf("not_found", "workspace %s not found", a.Worktree)
	}
	ctx, cancel := context.WithTimeout(c.ctx, 20*time.Second)
	defer cancel()
	st, err := gitx.ReadStatus(ctx, w.Path)
	if err != nil {
		return errorf("git", "%v", err)
	}
	stats, _ := gitx.NumStatHEAD(ctx, w.Path)
	files := []map[string]any{}
	for _, e := range st.Entries {
		s := stats[e.Path]
		files = append(files, map[string]any{
			"path": e.Path, "orig": e.Orig,
			"x": string(rune(e.X)), "y": string(rune(e.Y)),
			"untracked": e.Untracked, "conflict": e.Conflict,
			"added": s.Added, "deleted": s.Deleted, "binary": s.Binary,
		})
	}
	return c.reply(id, map[string]any{
		"branch": st.Head, "upstream": st.Upstream,
		"ahead": st.Ahead, "behind": st.Behind, "operation": st.Operation,
		"files": files,
	})
}

func (c *conn) gitDiff(id string, p json.RawMessage) error {
	if !c.hasCapability("hive.git.v1") {
		return errorf("method_not_found", "Unknown method: git.diff")
	}
	var a struct {
		wtParams
		Path string `json:"path"`
	}
	if json.Unmarshal(p, &a) != nil || a.Worktree == "" {
		return c.fail(id, "bad_params", "worktree")
	}
	rel, ok := cleanRepoPath(a.Path)
	if !ok {
		return c.fail(id, "bad_params", "path")
	}
	w, found := workspaceOf(c.srv.host.State(), a.Worktree)
	if !found {
		return errorf("not_found", "workspace %s not found", a.Worktree)
	}
	ctx, cancel := context.WithTimeout(c.ctx, 20*time.Second)
	defer cancel()
	text, truncated, err := gitx.DiffText(ctx, w.Path, rel)
	if err != nil {
		return errorf("git", "%v", err)
	}
	return c.reply(id, map[string]any{"path": rel, "diff": text, "truncated": truncated})
}

func cleanRepoPath(p string) (string, bool) {
	if p == "" || strings.ContainsAny(p, "\x00\n\r") {
		return "", false
	}
	p = strings.ReplaceAll(p, "\\", "/")
	if strings.HasPrefix(p, "/") || path.IsAbs(p) {
		return "", false
	}
	c := path.Clean(p)
	if c == "." || c == ".." || strings.HasPrefix(c, "../") {
		return "", false
	}
	return c, true
}
