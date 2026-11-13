package mobile

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"time"

	"posse/internal/errtext"
	"posse/internal/files"
	"posse/internal/gitx"
)

const (
	treeDirectoriesMax = 256
	// QUIC 的上限为 8 MiB；这里包含 JSON envelope，另留空间给 E2EE 的 17 字节开销。
	treeReplyMax = 7 << 20
	treeListAge  = 10 * time.Second
)

type treeEntry struct {
	Name        string `json:"name"`
	IsDirectory bool   `json:"isDirectory"`
	IsSymlink   bool   `json:"isSymlink"`
	GitStatus   string `json:"gitStatus,omitempty"`
	Ignored     bool   `json:"ignored,omitempty"`
}

type treeDirectory struct {
	Entries []treeEntry `json:"entries"`
	Error   string      `json:"error,omitempty"`
}

type treeReply struct {
	Directories map[string]treeDirectory `json:"directories"`
	GitError    string                   `json:"gitError,omitempty"`
	Remaining   []string                 `json:"remaining,omitempty"`
}

func (c *conn) fileTree(id string, p json.RawMessage) error {
	var a struct {
		Worktree    string   `json:"worktree"`
		Directories []string `json:"directories"`
	}
	if json.Unmarshal(p, &a) != nil || a.Worktree == "" {
		return errorf("bad_params", "invalid file tree parameters")
	}
	if len(a.Directories) > treeDirectoriesMax {
		return errorf("bad_params", "too many file tree directories (maximum %d)", treeDirectoriesMax)
	}
	st := c.srv.host.State()
	w, ok := workspaceOf(st, a.Worktree)
	if !ok {
		return errorf("not_found", "workspace %s not found", a.Worktree)
	}
	ctx, cancel := context.WithTimeout(c.ctx, 20*time.Second)
	defer cancel()
	// 目录浏览不依赖 Git，先取内容；后续 Git 查询超时仍能交回这批目录。
	r, err := readTreeDirectories(ctx, w.Path, a.Directories, c.language())
	if err != nil {
		return err
	}
	if c.hasCapability("posse.git.v1") && len(a.Directories) > 0 {
		repos := c.srv.repos.get(ctx, w.Path, !projectSingleRepo(st, w.ProjectID))
		decorateFileTree(ctx, r, w.Path, repos, c.language(), &c.treeList)
	}
	if err := c.ctx.Err(); err != nil {
		return err
	}
	r, err = limitTreeReply(id, r, a.Directories, treeReplyMax, c.language())
	if err != nil {
		return err
	}
	return c.reply(id, r)
}

func readFileTree(ctx context.Context, root string, repos []gitx.Repo, directories []string, lang errtext.Lang) (*treeReply, error) {
	r, err := readTreeDirectories(ctx, root, directories, lang)
	if err != nil {
		return nil, err
	}
	decorateFileTree(ctx, r, root, repos, lang, &treeListCache{})
	return r, nil
}

func readTreeDirectories(ctx context.Context, root string, directories []string, lang errtext.Lang) (*treeReply, error) {
	r := &treeReply{Directories: make(map[string]treeDirectory, len(directories))}
	for _, rel := range directories {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		if _, ok := r.Directories[rel]; ok {
			continue
		}
		d := treeDirectory{Entries: []treeEntry{}}
		dir, err := joinRel(root, rel, true)
		if err == nil {
			var entries []files.Entry
			entries, _, err = files.ReadDir(dir)
			for _, e := range entries {
				d.Entries = append(d.Entries, treeEntry{Name: e.Name, IsDirectory: e.Dir, IsSymlink: e.Link != ""})
			}
		}
		if err != nil {
			var re *rpcError
			if errors.As(err, &re) {
				d.Error = errtext.MessageIn(lang, re.msg)
			} else {
				d.Error = errtext.TextIn(lang, err)
			}
		}
		r.Directories[rel] = d
	}
	return r, nil
}

func decorateFileTree(ctx context.Context, out *treeReply, root string, repos []gitx.Repo, lang errtext.Lang, cache *treeListCache) {
	marks, list, err := fileTreeState(ctx, root, repos, cache)
	if err != nil {
		out.GitError = errtext.MessageIn(lang, fmt.Sprintf("Git status unavailable: %s", errtext.TextIn(lang, err)))
		return
	}
	for rel, d := range out.Directories {
		if d.Error != "" {
			continue
		}
		dir, _ := joinRel(root, rel, true)
		for i := range d.Entries {
			e := &d.Entries[i]
			abs := filepath.Join(dir, e.Name)
			e.Ignored = list.Ignored(abs, e.IsDirectory)
			if mark := marks[abs]; mark != 0 {
				e.GitStatus = string(mark)
			}
		}
	}
}

func fileTreeState(ctx context.Context, root string, repos []gitx.Repo, cache *treeListCache) (map[string]byte, *files.List, error) {
	if err := ctx.Err(); err != nil {
		return nil, nil, err
	}
	marks := map[string]byte{}
	if len(repos) == 0 {
		return marks, nil, nil
	}
	realRoot, _ := filepath.EvalSymlinks(root)
	shownPath := func(p string) string {
		if realRoot != "" && realRoot != root && (p == realRoot || strings.HasPrefix(p, realRoot+string(filepath.Separator))) {
			return root + p[len(realRoot):]
		}
		return p
	}
	var roots []string
	for i, r := range repos {
		st, err := gitx.ReadStatus(ctx, r.Root)
		if err != nil {
			return nil, nil, err
		}
		st.Entries = slices.DeleteFunc(st.Entries, func(e gitx.Entry) bool {
			return e.Untracked && nestedRepo(repos, i, filepath.Join(r.Root, filepath.FromSlash(strings.TrimSuffix(e.Path, "/"))))
		})
		gitx.MergeRepoTreeMarks(marks, root, r, gitx.RepoTreeMarks(r, st.Entries, shownPath))
		for _, listRoot := range append([]string{r.Root}, r.Aliases...) {
			// 工作区是仓库子目录的软链时，用工作区路径列清单。
			if subdirOf(listRoot, root) != "" {
				listRoot = root
			}
			roots = append(roots, listRoot)
		}
	}
	list, err := cache.get(ctx, root, roots)
	return marks, list, err
}

// 每连接只保留当前工作区的一份清单，生命周期和桌面相同；并发请求合并一次读取，失败不缓存。
type treeListCache struct {
	mu      sync.Mutex
	root    string
	repos   []string
	list    *files.List
	at      time.Time
	loading chan struct{}
}

func (c *treeListCache) get(ctx context.Context, root string, repos []string) (*files.List, error) {
	for {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		c.mu.Lock()
		if c.list != nil && c.root == root && slices.Equal(c.repos, repos) && time.Since(c.at) < treeListAge {
			list := c.list
			c.mu.Unlock()
			return list, nil
		}
		if c.loading != nil {
			done := c.loading
			c.mu.Unlock()
			select {
			case <-done:
				continue
			case <-ctx.Done():
				return nil, ctx.Err()
			}
		}
		c.loading = make(chan struct{})
		c.list = nil
		c.mu.Unlock()
		list, err := files.ListGitRepos(ctx, root, repos)
		if err == nil {
			err = ctx.Err()
		}
		c.mu.Lock()
		if err == nil {
			c.root, c.repos, c.list, c.at = root, slices.Clone(repos), list, time.Now()
		}
		close(c.loading)
		c.loading = nil
		c.mu.Unlock()
		return list, err
	}
}

func treeEnvelopeSize(id string, r *treeReply) int {
	b, _ := json.Marshal(map[string]any{"id": id, "ok": true, "result": r})
	return len(b)
}

// 只拆完整目录，避免客户端把半份目录误当成完整清单；单目录超限时直接给局部错误。
func limitTreeReply(id string, r *treeReply, requested []string, limit int, lang errtext.Lang) (*treeReply, error) {
	out := &treeReply{Directories: map[string]treeDirectory{}, GitError: r.GitError}
	base := treeEnvelopeSize(id, out)
	type row struct {
		rel       string
		dir       treeDirectory
		key, size int
	}
	var rows []row
	seen := map[string]bool{}
	used, count := base, 0
	var remainingBytes int
	for _, rel := range requested {
		if seen[rel] {
			continue
		}
		seen[rel] = true
		d := r.Directories[rel]
		key, _ := json.Marshal(rel)
		data, _ := json.Marshal(d)
		n := len(key) + 1 + len(data)
		if base+n > limit {
			d = treeDirectory{Entries: []treeEntry{}, Error: errtext.MessageIn(lang, "directory is too large to list")}
			data, _ = json.Marshal(d)
			out.Directories[rel] = d
			used += len(key) + 1 + len(data)
			if count > 0 {
				used++
			}
			count++
			continue
		}
		rows = append(rows, row{rel: rel, dir: d, key: len(key), size: n})
		remainingBytes += len(key)
	}
	remainingCount := len(rows)
	remainingSize := func(bytes, count int) int {
		if count == 0 {
			return 0
		}
		return len(`,"remaining":[]`) + bytes + count - 1
	}
	if used+remainingSize(remainingBytes, remainingCount) > limit {
		return nil, errorf("bad_params", "file tree response is too large")
	}
	for _, row := range rows {
		added := row.size
		if count > 0 {
			added++
		}
		if used+added+remainingSize(remainingBytes-row.key, remainingCount-1) <= limit {
			out.Directories[row.rel] = row.dir
			used += added
			count++
			remainingBytes -= row.key
			remainingCount--
		} else {
			out.Remaining = append(out.Remaining, row.rel)
		}
	}
	// 一份目录单独放得下、连剩余目录键却放不下时也必须推进，不能返回原样的 remaining 无限重试。
	if len(out.Directories) == 0 && len(out.Remaining) > 0 {
		rel := out.Remaining[0]
		out.Remaining = out.Remaining[1:]
		out.Directories[rel] = treeDirectory{Entries: []treeEntry{}, Error: errtext.MessageIn(lang, "directory is too large to list")}
	}
	if treeEnvelopeSize(id, out) > limit {
		return nil, errorf("bad_params", "file tree response is too large")
	}
	return out, nil
}
