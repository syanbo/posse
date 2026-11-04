package mobile

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"posse/internal/errtext"
	"posse/internal/gitx"
)

func (c *conn) gitStatus(id string, p json.RawMessage) error {
	if !c.hasCapability("posse.git.v1") {
		return errorf("method_not_found", "Unknown method: git.status")
	}
	var a wtParams
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
	repos := c.srv.repos.get(ctx, w.Path, !projectSingleRepo(st, w.ProjectID))
	// 旧版 App 只认一个仓库：给第一个，报错照旧
	if !c.hasCapability("posse.git.v2") {
		r, err := readRepo(ctx, w.Path, repos, 0)
		if err != nil {
			return errorf("git", "%v", err)
		}
		return c.reply(id, r)
	}
	out := make([]map[string]any, max(len(repos), 1))
	errs := make([]error, len(out))
	var wg sync.WaitGroup
	sem := make(chan struct{}, 4)
	for i := range out {
		wg.Add(1)
		go func() {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()
			out[i], errs[i] = readRepo(ctx, w.Path, repos, i)
			if errs[i] != nil {
				out[i]["error"] = errtext.TextIn(c.language(), errs[i])
			}
		}()
	}
	wg.Wait()
	// 只有一个仓库（或一个也没有）时和旧版一样整个报错；多个时出错的那个带上报错，别的照常
	if len(out) == 1 && errs[0] != nil {
		return errorf("git", "%v", errs[0])
	}
	return c.reply(id, map[string]any{"repos": out})
}

// readRepo 读 repos[i] 的状态，写成手机的格式：repo 是 git.diff 用来指回这个仓库的键，
// dir、sub 让手机把仓库里的路径换成工作区里的路径（打开文件用）。
// repos 为空（工作区不在任何仓库里）时照旧对工作区目录读，报 git 的错。
func readRepo(ctx context.Context, ws string, repos []gitx.Repo, i int) (map[string]any, error) {
	root := ws
	if len(repos) > 0 {
		root = repos[i].Root
	}
	r := map[string]any{"repo": repoKey(ws, root), "name": repoName(ws, root), "files": []map[string]any{}}
	if rel, err := filepath.Rel(ws, root); err == nil && rel != "." && !upward(rel) {
		r["dir"] = filepath.ToSlash(rel)
	} else if sub := subdirOf(root, ws); sub != "" {
		r["sub"] = sub
	}
	st, err := gitx.ReadStatus(ctx, root)
	if err != nil {
		return r, err
	}
	stats, _ := gitx.NumStatHEAD(ctx, root)
	files := []map[string]any{}
	for _, e := range st.Entries {
		if e.Untracked && nestedRepo(repos, i, filepath.Join(root, filepath.FromSlash(strings.TrimSuffix(e.Path, "/")))) {
			continue
		}
		s := stats[e.Path]
		files = append(files, map[string]any{
			"path": e.Path, "orig": e.Orig,
			"x": string(rune(e.X)), "y": string(rune(e.Y)),
			"untracked": e.Untracked, "conflict": e.Conflict, "submodule": e.Submodule,
			"added": s.Added, "deleted": s.Deleted, "binary": s.Binary,
		})
	}
	r["branch"], r["upstream"] = st.Head, st.Upstream
	r["ahead"], r["behind"], r["operation"] = st.Ahead, st.Behind, st.Operation
	r["files"] = files
	return r, nil
}

// subdirOf 是工作区 ws 在仓库 root 里的路径（斜杠分隔）；ws 不在 root 下面、或就是 root 时为空。
// 字面路径对不上时按真实路径算：工作区可能是指向仓库子目录的软链，这时仓库根目录是 git 给的真实路径。
func subdirOf(root, ws string) string {
	sub, err := filepath.Rel(root, ws)
	if err != nil || upward(sub) {
		rr, err1 := filepath.EvalSymlinks(root)
		wr, err2 := filepath.EvalSymlinks(ws)
		if err1 != nil || err2 != nil {
			return ""
		}
		if sub, err = filepath.Rel(rr, wr); err != nil || upward(sub) {
			return ""
		}
	}
	if sub == "." {
		return ""
	}
	return filepath.ToSlash(sub)
}

// nestedRepo 报告外层仓库报成未跟踪目录的 abs 其实是找到的另一个仓库：它的改动在它自己那一组里。
func nestedRepo(repos []gitx.Repo, i int, abs string) bool {
	for j, r := range repos {
		if j == i {
			continue
		}
		for _, p := range append([]string{r.Root}, r.Aliases...) {
			if p == abs {
				return true
			}
		}
	}
	return false
}

// repoKey 是仓库根目录相对工作区的路径（工作区本身是 "."，工作区在仓库里时是 ".."、"../.."），
// 工作区里的仓库各不相同。
func repoKey(ws, root string) string {
	rel, err := filepath.Rel(ws, root)
	if err != nil {
		return root
	}
	return filepath.ToSlash(rel)
}

// repoName 是给人看的仓库名，和桌面一样：工作区里的写相对路径，工作区本身和上级仓库写目录名。
func repoName(ws, root string) string {
	rel, err := filepath.Rel(ws, root)
	if err != nil || rel == "." || upward(rel) {
		return filepath.Base(root)
	}
	return filepath.ToSlash(rel)
}

// upward 报告相对路径 rel 走到了上级目录（..foo 这种目录名不算）。
func upward(rel string) bool {
	return rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator))
}

func projectSingleRepo(st State, id string) bool {
	for _, p := range st.Projects {
		if p.ID == id {
			return p.SingleRepo
		}
	}
	return false
}

// repoCache 记着各工作区找到的仓库：手机每 3 秒读一次改动，不能每次都扫一遍目录。
// 和桌面一样，工作区目录的修改时间变了才重扫；没变的也每分钟重扫一次（深处新建的仓库），撞了扫描上限的不重扫。
type repoCache struct {
	mu   sync.Mutex
	dirs map[string]*repoScan
}

type repoScan struct {
	mu    sync.Mutex // 同一个工作区同时只扫一次
	multi bool
	mtime time.Time
	at    time.Time
	found gitx.Discovery
}

const repoRescan = time.Minute

func (rc *repoCache) get(ctx context.Context, dir string, multi bool) []gitx.Repo {
	rc.mu.Lock()
	if rc.dirs == nil {
		rc.dirs = map[string]*repoScan{}
	}
	s := rc.dirs[dir]
	if s == nil {
		s = &repoScan{}
		rc.dirs[dir] = s
	}
	rc.mu.Unlock()
	s.mu.Lock()
	defer s.mu.Unlock()
	var mtime time.Time
	if fi, err := os.Stat(dir); err == nil {
		mtime = fi.ModTime()
	}
	fresh := !s.at.IsZero() && s.multi == multi && s.mtime.Equal(mtime) &&
		(s.found.Partial || time.Since(s.at) < repoRescan)
	if fresh {
		return s.found.Repos
	}
	found := gitx.Discover(ctx, dir, multi)
	// 请求中途取消的扫描不完整（Discover 不标 Partial）：不记下，有上次的结果就还用上次的
	if ctx.Err() != nil {
		if !s.at.IsZero() && s.multi == multi {
			return s.found.Repos
		}
		return found.Repos
	}
	s.found, s.multi, s.mtime, s.at = found, multi, mtime, time.Now()
	return found.Repos
}

func (c *conn) gitDiff(id string, p json.RawMessage) error {
	if !c.hasCapability("posse.git.v1") {
		return errorf("method_not_found", "Unknown method: git.diff")
	}
	var a struct {
		wtParams
		Path string `json:"path"`
		Orig string `json:"orig"`
		// Repo 是 git.status 给的仓库键；旧版 App 不带，用第一个仓库
		Repo *string `json:"repo"`
	}
	if json.Unmarshal(p, &a) != nil || a.Worktree == "" {
		return c.fail(id, "bad_params", "worktree")
	}
	rel, ok := cleanRepoPath(a.Path)
	if !ok {
		return c.fail(id, "bad_params", "path")
	}
	st := c.srv.host.State()
	w, found := workspaceOf(st, a.Worktree)
	if !found {
		return errorf("not_found", "workspace %s not found", a.Worktree)
	}
	ctx, cancel := context.WithTimeout(c.ctx, 20*time.Second)
	defer cancel()
	root := w.Path
	if repos := c.srv.repos.get(ctx, w.Path, !projectSingleRepo(st, w.ProjectID)); len(repos) > 0 {
		root = repos[0].Root
		if a.Repo != nil {
			root = ""
			for _, r := range repos {
				if repoKey(w.Path, r.Root) == *a.Repo {
					root = r.Root
				}
			}
			if root == "" {
				return errorf("not_found", "repository %s not found", *a.Repo)
			}
		}
	}
	text, truncated, err := gitx.DiffText(ctx, root, rel)
	if err != nil {
		return errorf("git", "%v", err)
	}
	reply := map[string]any{"path": rel, "diff": text, "truncated": truncated}
	if imageMime[strings.ToLower(path.Ext(rel))] {
		old := rel
		if o, ok := cleanRepoPath(a.Orig); ok {
			old = o
		}
		reply["images"] = imageSides(ctx, root, old, rel)
	}
	c.replyBulk(id, reply, nil)
	return nil
}

// imageMime 是手机能画出来的图片：diff 时把改之前（HEAD）和现在（工作区）两张图都带上。
var imageMime = map[string]bool{".png": true, ".jpg": true, ".jpeg": true, ".gif": true, ".webp": true, ".bmp": true}

// imageSides 读图片 diff 的两边，base64；这一边没有（新文件、已删除）是 nil，超过 imageReadMax 只报大小。
func imageSides(ctx context.Context, dir, old, rel string) map[string]any {
	side := func(b []byte, err error) any {
		if err != nil {
			return nil
		}
		if len(b) > imageReadMax {
			return map[string]any{"size": len(b), "tooLarge": true}
		}
		return map[string]any{"size": len(b), "content": b}
	}
	before, err := gitx.Show(ctx, dir, "HEAD", old)
	out := map[string]any{"old": side(before, err)}
	now, err := readInside(dir, rel)
	out["new"] = side(now, err)
	return out
}

// readInside 读仓库 dir 里的 rel，解析软链后不在仓库里的不读：路径检查只挡得住字面的 ..，
// 指向仓库外面的软链（leak.png -> ~/secret）会把外面的文件交给手机。
func readInside(dir, rel string) ([]byte, error) {
	root, err := filepath.EvalSymlinks(dir)
	if err != nil {
		return nil, err
	}
	p, err := filepath.EvalSymlinks(filepath.Join(dir, filepath.FromSlash(rel)))
	if err != nil {
		return nil, err
	}
	if r, err := filepath.Rel(root, p); err != nil || upward(r) || r == "." {
		return nil, errors.New("outside the repository")
	}
	return os.ReadFile(p)
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
