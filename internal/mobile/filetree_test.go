package mobile

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"posse/internal/files"
	"posse/internal/gitx"
	"posse/internal/mquic"
	"posse/internal/term"
)

func treeWrite(t *testing.T, root, rel, text string) {
	t.Helper()
	p := filepath.Join(root, rel)
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(text), 0o644); err != nil {
		t.Fatal(err)
	}
}

func treeGit(t *testing.T, root string, args ...string) {
	t.Helper()
	c := exec.Command("git", args...)
	c.Dir = root
	c.Env = append(os.Environ(), "GIT_TRACE2_EVENT=0", "GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@example.com", "GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@example.com")
	if out, err := c.CombinedOutput(); err != nil {
		t.Fatalf("git %v: %v %s", args, err, out)
	}
}

func treeFind(t *testing.T, dirs map[string]treeDirectory, dir, name string) treeEntry {
	t.Helper()
	d, ok := dirs[dir]
	if !ok || d.Error != "" {
		t.Fatalf("directory %q: %+v", dir, d)
	}
	for _, e := range d.Entries {
		if e.Name == name {
			return e
		}
	}
	t.Fatalf("entry %q not found in %q: %+v", name, dir, d)
	return treeEntry{}
}

func TestPhoneFileTree(t *testing.T) {
	root := t.TempDir()
	treeGit(t, root, "init", "-q", "-b", "main")
	for _, p := range []string{"src/modified", "deleted/gone", "tracked.log", "clean"} {
		treeWrite(t, root, p, "before\n")
	}
	treeGit(t, root, "add", ".")
	treeGit(t, root, "commit", "-q", "-m", "init")
	treeWrite(t, root, ".gitignore", "*.log\nignored/\n")
	treeWrite(t, root, "tracked.log", "after\n")
	treeWrite(t, root, "src/modified", "after\n")
	treeWrite(t, root, "added", "new\n")
	treeWrite(t, root, "am", "new\n")
	treeGit(t, root, "add", "added", "am")
	treeWrite(t, root, "am", "edited after staging\n")
	treeWrite(t, root, "new", "untracked\n")
	treeWrite(t, root, "untracked.log", "ignored\n")
	treeWrite(t, root, "ignored/file", "ignored\n")
	if err := os.Remove(filepath.Join(root, "deleted/gone")); err != nil {
		t.Fatal(err)
	}

	srv, err := New(t.TempDir(), &fakeHost{terms: map[string]*term.Terminal{}, dir: root}, -1)
	if err != nil {
		t.Fatal(err)
	}
	srv.port = 0
	defer srv.Close()
	info, err := srv.Pair(false, "127.0.0.1")
	if err != nil {
		t.Fatal(err)
	}
	p := dialPhone(t, srv, info)
	result(t, p.next(p.call("runtime.clientCapabilities.update", map[string]any{"client": "posse-flutter", "version": 1, "capabilities": []string{"posse.git.v1"}, "locale": "en"})))
	r := result(t, p.next(p.call("files.tree", map[string]any{"worktree": "id:p1::w1", "directories": []string{"", "src", "missing", "../bad"}})))
	dirs := r["directories"].(map[string]any)
	rootRows := map[string]map[string]any{}
	for _, row := range dirs[""].(map[string]any)["entries"].([]any) {
		e := row.(map[string]any)
		rootRows[e["name"].(string)] = e
	}
	for name, want := range map[string]string{"src": "M", "deleted": "M", "added": "A", "am": "M", "new": "?", "tracked.log": "M"} {
		if e := rootRows[name]; e["gitStatus"] != want {
			t.Errorf("%s = %v, want %s", name, e, want)
		}
	}
	for _, name := range []string{"untracked.log", "ignored"} {
		if rootRows[name]["ignored"] != true {
			t.Errorf("%s should be ignored: %v", name, rootRows[name])
		}
	}
	if rootRows["tracked.log"]["ignored"] == true || rootRows["clean"]["gitStatus"] != nil || rootRows[".git"] != nil {
		t.Fatalf("tracked/clean/hidden entries: %v", rootRows)
	}
	if d := dirs["src"].(map[string]any); len(d["entries"].([]any)) != 1 || d["error"] != nil {
		t.Fatalf("src = %v", d)
	}
	for _, rel := range []string{"missing", "../bad"} {
		if d := dirs[rel].(map[string]any); d["error"] == nil || len(d["entries"].([]any)) != 0 {
			t.Fatalf("local error %s: %v", rel, d)
		}
	}
	result(t, p.next(p.call("runtime.clientCapabilities.update", map[string]any{"client": "posse-flutter", "version": 1, "capabilities": []string{}, "locale": "zh-Hans"})))
	plain := result(t, p.next(p.call("files.tree", map[string]any{"worktree": "id:p1::w1", "directories": []string{"", "../bad"}})))["directories"].(map[string]any)
	for _, row := range plain[""].(map[string]any)["entries"].([]any) {
		e := row.(map[string]any)
		if e["gitStatus"] != nil || e["ignored"] != nil {
			t.Fatalf("git capability missing: %v", e)
		}
	}
	if plain["../bad"].(map[string]any)["error"] == dirs["../bad"].(map[string]any)["error"] {
		t.Fatal("directory errors did not follow the connection language")
	}
	for _, args := range []map[string]any{
		{"worktree": "missing", "directories": []string{""}},
		{"worktree": "id:p1::w1", "directories": make([]string, treeDirectoriesMax+1)},
	} {
		if bad := p.next(p.call("files.tree", args)); bad["ok"] != false {
			t.Fatalf("accepted invalid request: %v", bad)
		}
	}
}

func TestFileTreeGitFailureRPC(t *testing.T) {
	root := t.TempDir()
	treeGit(t, root, "init", "-q", "-b", "main")
	treeWrite(t, root, "file", "content")
	treeGit(t, root, "add", "file")
	srv, err := New(t.TempDir(), &fakeHost{terms: map[string]*term.Terminal{}, dir: root}, -1)
	if err != nil {
		t.Fatal(err)
	}
	srv.port = 0
	defer srv.Close()
	info, err := srv.Pair(false, "127.0.0.1")
	if err != nil {
		t.Fatal(err)
	}
	p := dialPhone(t, srv, info)
	result(t, p.next(p.call("runtime.clientCapabilities.update", map[string]any{"client": "posse-flutter", "version": 1, "capabilities": []string{"posse.git.v1"}, "locale": "en"})))
	args := map[string]any{"worktree": "id:p1::w1", "directories": []string{""}}
	result(t, p.next(p.call("files.tree", args)))
	treeWrite(t, root, ".git/index", "broken index")
	treeWrite(t, root, "new", "still visible")
	r := result(t, p.next(p.call("files.tree", args)))
	if e, _ := r["gitError"].(string); !strings.HasPrefix(e, "Git status unavailable:") {
		t.Fatalf("gitError = %v", r)
	}
	d := r["directories"].(map[string]any)[""].(map[string]any)
	if d["error"] != nil || len(d["entries"].([]any)) != 2 {
		t.Fatalf("browsing failed: %v", d)
	}
	for _, row := range d["entries"].([]any) {
		e := row.(map[string]any)
		if e["gitStatus"] != nil || e["ignored"] != nil {
			t.Fatalf("stale colors returned: %v", e)
		}
	}
}

func TestFileTreeMultiRepoAndSymlinks(t *testing.T) {
	base := t.TempDir()
	ws := filepath.Join(base, "workspace")
	for _, rel := range []string{"workspace/group/a", "workspace/group/a/nested", "outside"} {
		root := filepath.Join(base, rel)
		treeWrite(t, root, "added", "new\n")
		treeGit(t, root, "init", "-q", "-b", "main")
		treeGit(t, root, "add", "added")
	}
	if err := os.Symlink(filepath.Join(base, "outside"), filepath.Join(ws, "link")); err != nil {
		t.Fatal(err)
	}
	repos := gitx.Discover(t.Context(), ws, true).Repos
	got, err := readFileTree(t.Context(), ws, repos, []string{"", "group", "group/a", "group/a/nested", "link"}, "en")
	if err != nil {
		t.Fatal(err)
	}
	for _, dir := range []string{"group/a", "group/a/nested", "link"} {
		if e := treeFind(t, got.Directories, dir, "added"); e.GitStatus != "A" || e.Ignored {
			t.Fatalf("%s added: %+v", dir, e)
		}
	}
	for _, item := range [][2]string{{"", "group"}, {"group", "a"}} {
		if e := treeFind(t, got.Directories, item[0], item[1]); e.GitStatus != "M" || !e.IsDirectory || e.Ignored {
			t.Fatalf("dirty directory: %+v", e)
		}
	}
	if e := treeFind(t, got.Directories, "", "link"); !e.IsSymlink || !e.IsDirectory || e.GitStatus != "M" || e.Ignored {
		t.Fatalf("linked repo: %+v", e)
	}
}

func TestFileTreeSubdir(t *testing.T) {
	base := t.TempDir()
	repo := filepath.Join(base, "long-repository-name")
	treeWrite(t, repo, "src/deep/added", "new\n")
	treeWrite(t, repo, "src/.gitignore", "*.log\n")
	treeWrite(t, repo, "src/ignored.log", "ignored\n")
	treeGit(t, repo, "init", "-q", "-b", "main")
	treeGit(t, repo, "add", "src/deep/added")
	link := filepath.Join(base, "link")
	if err := os.Symlink(filepath.Join(repo, "src"), link); err != nil {
		t.Fatal(err)
	}
	for _, ws := range []string{filepath.Join(repo, "src"), link} {
		repos := gitx.Discover(t.Context(), ws, true).Repos
		got, err := readFileTree(t.Context(), ws, repos, []string{"", "deep"}, "en")
		if err != nil {
			t.Fatal(err)
		}
		if e := treeFind(t, got.Directories, "deep", "added"); e.GitStatus != "A" || e.Ignored {
			t.Fatalf("subdir %s: %+v", ws, e)
		}
		if e := treeFind(t, got.Directories, "", "deep"); e.GitStatus != "M" || e.Ignored {
			t.Fatalf("subdir parent %s: %+v", ws, e)
		}
		if e := treeFind(t, got.Directories, "", "ignored.log"); !e.Ignored {
			t.Fatalf("subdir ignored %s: %+v", ws, e)
		}
	}
}

func TestFileTreeNonGitAndFailure(t *testing.T) {
	ws := t.TempDir()
	treeWrite(t, ws, "file", "ordinary\n")
	got, err := readFileTree(t.Context(), ws, nil, []string{"", "missing", "../bad"}, "en")
	if err != nil {
		t.Fatal(err)
	}
	if e := treeFind(t, got.Directories, "", "file"); e.GitStatus != "" || e.Ignored {
		t.Fatalf("non-git entry: %+v", e)
	}
	if got.Directories["missing"].Error == "" || got.Directories["../bad"].Error == "" {
		t.Fatal("missing local errors")
	}
	failed, err := readFileTree(t.Context(), ws, []gitx.Repo{{Root: ws}}, []string{""}, "en")
	if err != nil || failed.GitError == "" {
		t.Fatalf("git failure must preserve browsing: %+v, %v", failed, err)
	}
	if e := treeFind(t, failed.Directories, "", "file"); e.GitStatus != "" || e.Ignored {
		t.Fatalf("failed git must omit colors: %+v", e)
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if _, err := readFileTree(ctx, ws, nil, []string{""}, "en"); err != context.Canceled {
		t.Fatalf("canceled request: %v", err)
	}
}

func TestFileTreeCleanNestedAndAliases(t *testing.T) {
	base := t.TempDir()
	ws := filepath.Join(base, "ws")
	repo := filepath.Join(base, "repo")
	nested := filepath.Join(repo, "nested")
	for _, root := range []string{repo, nested} {
		treeWrite(t, root, "tracked.log", "tracked")
		treeGit(t, root, "init", "-q", "-b", "main")
		treeGit(t, root, "add", "tracked.log")
		treeGit(t, root, "commit", "-qm", "initial")
		treeWrite(t, root, ".gitignore", "*.log\n")
		treeGit(t, root, "add", ".gitignore")
		treeGit(t, root, "commit", "-qm", "ignore")
		treeWrite(t, root, "ignored.log", "ignored")
	}
	if err := os.MkdirAll(ws, 0o755); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"a", "z"} {
		if err := os.Symlink(repo, filepath.Join(ws, name)); err != nil {
			t.Fatal(err)
		}
	}
	repos := gitx.Discover(t.Context(), ws, true).Repos
	if len(repos) != 2 || len(repos[0].Aliases) == 0 {
		t.Fatalf("discovery: %+v", repos)
	}
	dirs := []string{"", "a", "z", "a/nested", "z/nested"}
	got, err := readFileTree(t.Context(), ws, repos, dirs, "en")
	if err != nil || got.GitError != "" {
		t.Fatalf("tree: %+v, %v", got, err)
	}
	for _, name := range []string{"a", "z"} {
		if e := treeFind(t, got.Directories, "", name); e.GitStatus != "" || e.Ignored {
			t.Fatalf("clean repo: %+v", e)
		}
		if e := treeFind(t, got.Directories, name, "nested"); e.GitStatus != "" || e.Ignored {
			t.Fatalf("clean nested: %+v", e)
		}
		for _, dir := range []string{name, name + "/nested"} {
			if e := treeFind(t, got.Directories, dir, "tracked.log"); e.Ignored {
				t.Fatalf("tracked via alias: %+v", e)
			}
			if e := treeFind(t, got.Directories, dir, "ignored.log"); !e.Ignored {
				t.Fatalf("ignored via alias: %+v", e)
			}
		}
	}
	treeWrite(t, nested, "tracked.log", "changed")
	got, err = readFileTree(t.Context(), ws, repos, dirs, "en")
	if err != nil || got.GitError != "" {
		t.Fatalf("dirty tree: %+v, %v", got, err)
	}
	for _, name := range []string{"a", "z"} {
		if e := treeFind(t, got.Directories, "", name); e.GitStatus != "M" {
			t.Fatalf("dirty repo alias: %+v", e)
		}
		if e := treeFind(t, got.Directories, name, "nested"); e.GitStatus != "M" {
			t.Fatalf("dirty nested alias: %+v", e)
		}
		if e := treeFind(t, got.Directories, name+"/nested", "tracked.log"); e.GitStatus != "M" || e.Ignored {
			t.Fatalf("dirty alias file: %+v", e)
		}
	}
}

func TestFileTreeListCache(t *testing.T) {
	root := t.TempDir()
	treeGit(t, root, "init", "-q", "-b", "main")
	treeWrite(t, root, "first", "one")
	var cache treeListCache
	var wg sync.WaitGroup
	lists := make([]*files.List, 8)
	for i := range lists {
		wg.Go(func() {
			l, err := cache.get(t.Context(), root, []string{root})
			if err != nil {
				t.Error(err)
				return
			}
			lists[i] = l
		})
	}
	wg.Wait()
	for _, l := range lists {
		if l == nil || l != lists[0] {
			t.Fatal("concurrent reads did not share the list")
		}
	}
	treeWrite(t, root, "second", "two")
	l, err := cache.get(t.Context(), root, []string{root})
	if err != nil || l != lists[0] {
		t.Fatalf("cache miss: %v", err)
	}
	cache.at = time.Now().Add(-treeListAge)
	l, err = cache.get(t.Context(), root, []string{root})
	if err != nil || l == lists[0] || len(l.Files) != 2 {
		t.Fatalf("expired list: %+v %v", l, err)
	}
	other := t.TempDir()
	if _, err = cache.get(t.Context(), other, []string{other}); err == nil || cache.list != nil {
		t.Fatal("failed list was cached")
	}
	treeGit(t, other, "init", "-q", "-b", "main")
	if _, err = cache.get(t.Context(), other, []string{other}); err != nil {
		t.Fatal(err)
	}
	if cache.root != other || len(cache.repos) != 1 {
		t.Fatalf("cache retained previous workspace: %s %v", cache.root, cache.repos)
	}
}

func TestFileTreeReplyMakesProgress(t *testing.T) {
	d := treeDirectory{Entries: []treeEntry{{Name: strings.Repeat("x", 300)}}}
	r := &treeReply{Directories: map[string]treeDirectory{"a": d, "b": d}}
	limit := treeEnvelopeSize("id", &treeReply{Directories: map[string]treeDirectory{"a": d}}) + 1
	page, err := limitTreeReply("id", r, []string{"a", "b"}, limit, "en")
	if err != nil || len(page.Directories) != 1 || len(page.Remaining) != 1 || treeEnvelopeSize("id", page) > limit {
		t.Fatalf("no progress at the boundary: %+v, %v", page, err)
	}
}

func TestFileTreeReplyBudget(t *testing.T) {
	id := strings.Repeat(`id"<&`, 20)
	r := &treeReply{Directories: map[string]treeDirectory{}, GitError: "Git status unavailable: test"}
	var dirs []string
	for i := range 100 {
		rel := fmt.Sprintf("dir-%d", i)
		dirs = append(dirs, rel)
		d := treeDirectory{Entries: []treeEntry{}}
		for j := range 300 {
			d.Entries = append(d.Entries, treeEntry{Name: fmt.Sprintf("%d-%s", j, strings.Repeat("x", 240))})
		}
		r.Directories[rel] = d
	}
	r.Directories["oversized"] = treeDirectory{Entries: []treeEntry{{Name: strings.Repeat("x", treeReplyMax)}}}
	dirs = append(dirs, "oversized")
	seen := map[string]bool{}
	pages := 0
	for len(dirs) > 0 {
		page, err := limitTreeReply(id, r, dirs, treeReplyMax, "en")
		if err != nil {
			t.Fatal(err)
		}
		wire, _ := json.Marshal(map[string]any{"id": id, "ok": true, "result": page})
		if len(wire) > treeReplyMax || len(wire)+17 >= mquic.MaxMessage {
			t.Fatalf("wire too large: %d", len(wire))
		}
		if len(page.Directories) == 0 || page.GitError != r.GitError {
			t.Fatalf("page made no progress: %+v", page)
		}
		for rel, d := range page.Directories {
			if seen[rel] {
				t.Fatalf("duplicate %s", rel)
			}
			seen[rel] = true
			if rel == "oversized" && (d.Error == "" || len(d.Entries) != 0) {
				t.Fatal("oversized directory was not rejected")
			}
		}
		dirs = page.Remaining
		pages++
	}
	if pages < 2 || len(seen) != 101 {
		t.Fatalf("pages=%d seen=%d", pages, len(seen))
	}
}
