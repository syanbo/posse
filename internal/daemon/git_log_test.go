package daemon

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"posse/internal/gitx"
	"posse/internal/rpc"
)

func logTestCall(t *testing.T, c *gitReadTestClient, p rpc.GitLogParams) (rpc.GitLogResult, error) {
	t.Helper()
	var out rpc.GitLogResult
	err := mediaTestCall(t.Context(), c.Socket, *c.Desktop, "git.log", p, &out)
	return out, err
}
func commitTestCall(t *testing.T, c *gitReadTestClient, p rpc.GitCommitFilesParams) (rpc.GitCommitFilesResult, error) {
	t.Helper()
	var out rpc.GitCommitFilesResult
	err := mediaTestCall(t.Context(), c.Socket, *c.Desktop, "git.commitFiles", p, &out)
	return out, err
}
func logTestParams(t *testing.T, c *gitReadTestClient, root string) rpc.GitLogParams {
	p := gitFileTestParams(t, c, root)
	return rpc.GitLogParams{Path: root, Expected: p.Expected, Limit: 300}
}
func TestDesktopGitLogAndCommitFiles(t *testing.T) {
	d, c := gitReadClient(t)
	root := readGitRepo(t, filepath.Join(t.TempDir(), "repo"), true)
	first := readGit(t, root, "rev-parse", "HEAD")
	readGit(t, root, "switch", "-qc", "topic+quote'\"$;]雪")
	os.WriteFile(filepath.Join(root, "side"), []byte("side\n"), 0600)
	readGit(t, root, "add", "side")
	readGit(t, root, "commit", "-qm", "Topic needle", "--author=Alice Example <alice@example.invalid>")
	topic := readGit(t, root, "rev-parse", "HEAD")
	readGit(t, root, "switch", "-q", "main")
	readGit(t, root, "mv", "a.txt", "renamed tab\tline\n")
	readGit(t, root, "commit", "-qm", "rename")
	renamed := readGit(t, root, "rev-parse", "HEAD")
	os.WriteFile(filepath.Join(root, "main"), []byte("main\n"), 0600)
	readGit(t, root, "add", "main")
	readGit(t, root, "commit", "-qm", "deadbee message only")
	readGit(t, root, "merge", "--no-ff", "-qm", "merge topic", "topic+quote'\"$;]雪")
	merged := readGit(t, root, "rev-parse", "HEAD")
	p := logTestParams(t, c, root)
	all, err := logTestCall(t, c, p)
	if err != nil || len(all.Commits) != 5 || all.HasMore || all.NextSkip != 5 {
		t.Fatalf("all %+v %v", all, err)
	}
	p.Limit = 2
	seen := map[string]bool{}
	for {
		page, err := logTestCall(t, c, p)
		if err != nil {
			t.Fatal(err)
		}
		for _, commit := range page.Commits {
			if seen[commit.Hash] {
				t.Fatal("duplicate page")
			}
			seen[commit.Hash] = true
		}
		if page.NextSkip != p.Skip+len(page.Commits) {
			t.Fatal("bad next skip")
		}
		if !page.HasMore {
			break
		}
		p.Skip = page.NextSkip
		p.Baseline = page.Baseline
	}
	if len(seen) != 5 {
		t.Fatalf("omitted commits: %v", seen)
	}
	for _, tc := range []struct {
		filter gitx.LogFilter
		hash   string
		count  int
	}{
		{gitx.LogFilter{Branches: []string{"topic+quote'\"$;]雪"}}, topic, 2},
		{gitx.LogFilter{Branches: []string{"HEAD"}}, merged, 5},
		{gitx.LogFilter{Author: "alice"}, topic, 1},
		{gitx.LogFilter{Text: "NEEDLE"}, topic, 1},
		{gitx.LogFilter{Text: topic[:9]}, topic, 1},
		{gitx.LogFilter{Text: "deadbee"}, "", 1},
		{gitx.LogFilter{Path: "renamed tab\tline\n"}, renamed, 1},
	} {
		q := logTestParams(t, c, root)
		q.Filter = tc.filter
		page, err := logTestCall(t, c, q)
		if err != nil || len(page.Commits) != tc.count || tc.hash != "" && page.Commits[0].Hash != tc.hash {
			t.Fatalf("filter %+v => %+v %v", tc.filter, page, err)
		}
		if tc.filter.Text == topic[:9] {
			q.Skip = 1
			q.Baseline = page.Baseline
			next, err := logTestCall(t, c, q)
			if err != nil || len(next.Commits) != 0 || next.HasMore || next.NextSkip != 1 {
				t.Fatalf("hash pagination %+v %v", next, err)
			}
		}
	}
	for _, tc := range []struct {
		hash       string
		status     byte
		path, orig string
		parents    int
	}{
		{first, 'A', "a.txt", "", 0}, {renamed, 'R', "renamed tab\tline\n", "a.txt", 1}, {merged, 'A', "side", "", 2},
	} {
		detail, err := commitTestCall(t, c, rpc.GitCommitFilesParams{Path: root, Expected: p.Expected, Hash: tc.hash})
		if err != nil || detail.Commit.Hash != tc.hash || len(detail.Commit.Parents) != tc.parents || len(detail.Files) != 1 || detail.Files[0].Status != tc.status || detail.Files[0].Path != tc.path || detail.Files[0].Orig != tc.orig {
			t.Fatalf("details %+v %v", detail, err)
		}
	}
	// 相同查询引用变化后不继续旧页，即使选择分支的提交仍可读。
	q := logTestParams(t, c, root)
	q.Limit = 1
	page, err := logTestCall(t, c, q)
	if err != nil {
		t.Fatal(err)
	}
	readGit(t, root, "tag", "new-tag")
	q.Skip = page.NextSkip
	q.Baseline = page.Baseline
	_, err = logTestCall(t, c, q)
	desktopErrorCode(t, err, rpc.CodeConflict)
	for _, method := range []string{"git.log", "git.commitFiles"} {
		desktopErrorCode(t, rpc.Call(t.Context(), c.Socket, method, p, nil), rpc.CodeReadOnly)
		stale := *c.Desktop
		stale.InstanceID = "old"
		desktopErrorCode(t, mediaTestCall(t.Context(), c.Socket, stale, method, p, nil), rpc.CodeConflict)
	}
	d.desktop.mu.Lock()
	owner := d.desktop.owner
	d.desktop.mu.Unlock()
	if owner != "" {
		t.Fatal("viewer acquired control")
	}
}

func TestDesktopGitLogUnbornAndInputs(t *testing.T) {
	_, c := gitReadClient(t)
	root := readGitRepo(t, filepath.Join(t.TempDir(), "repo"), false)
	p := logTestParams(t, c, root)
	for _, refs := range [][]string{nil, {"HEAD"}} {
		p.Filter.Branches = refs
		r, err := logTestCall(t, c, p)
		if err != nil || r.Commits == nil || len(r.Commits) != 0 || r.HasMore {
			t.Fatalf("unborn %+v %v", r, err)
		}
	}
	for _, change := range []func(*rpc.GitLogParams){
		func(p *rpc.GitLogParams) { p.Limit = 1001 }, func(p *rpc.GitLogParams) { p.Skip = 1 }, func(p *rpc.GitLogParams) { p.Skip = -1 }, func(p *rpc.GitLogParams) { p.Filter.Branches = []string{"--all"} }, func(p *rpc.GitLogParams) { p.Filter.Branches = []string{"HEAD:a"} }, func(p *rpc.GitLogParams) { p.Filter.Path = "../a" }, func(p *rpc.GitLogParams) { p.Filter.Text = "x\x00" },
	} {
		q := p
		change(&q)
		_, err := logTestCall(t, c, q)
		desktopErrorCode(t, err, rpc.CodeBadRequest)
	}
	readGit(t, root, "commit", "-qm", "base")
	p.Filter.Branches = []string{"missing-branch"}
	if _, err := logTestCall(t, c, p); err == nil {
		t.Fatal("missing ref hidden")
	}
	p.Filter = gitx.LogFilter{}
	p.Expected.RootInode++
	_, err := logTestCall(t, c, p)
	desktopErrorCode(t, err, rpc.CodeConflict)
}

func TestDesktopGitLogReadFailures(t *testing.T) {
	d, c := gitReadClient(t)
	root := readGitRepo(t, filepath.Join(t.TempDir(), "repo"), true)
	p := logTestParams(t, c, root)
	for _, tc := range []struct{ name, script, contains string }{
		{"permission", "case \" $* \" in *\" log \"*) printf 'permission denied in history\\n' >&2; exit 128;; esac", "permission denied"},
		{"warning", "case \" $* \" in *\" for-each-ref \"*) printf 'warning: ignoring broken ref\\n' >&2; exit 0;; esac", "broken ref"},
		{"malformed", "case \" $* \" in *\" log \"*) printf 'malformed'; exit 0;; esac", gitx.ErrLogOutput.Error()},
		{"budget", "case \" $* \" in *\" log \"*) /bin/dd if=/dev/zero bs=1048576 count=5 2>/dev/null; exit 0;; esac", gitx.ErrReadLimit.Error()},
		{"hash-error", "case \" $* \" in *\" --disambiguate=\"*) printf 'object database is corrupt\\n' >&2; exit 128;; esac", "corrupt"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			gitReadWrapper(t, tc.script)
			q := p
			if tc.name == "hash-error" {
				q.Filter.Text = "deadbee"
			}
			_, err := logTestCall(t, c, q)
			if err == nil || !strings.Contains(err.Error(), tc.contains) {
				t.Fatalf("failure hidden %v", err)
			}
		})
	}
	t.Run("references-change-during-read", func(t *testing.T) {
		t.Setenv("READ_ROOT", root)
		gitReadWrapper(t, "case \" $* \" in *\" log \"*) /usr/bin/git \"$@\" || exit $?; /usr/bin/git -C \"$READ_ROOT\" update-ref refs/heads/moved HEAD; exit 0;; esac")
		_, err := logTestCall(t, c, p)
		desktopErrorCode(t, err, rpc.CodeConflict)
	})
	t.Run("cancel", func(t *testing.T) {
		gitReadWrapper(t, "case \" $* \" in *\" log \"*) exec /bin/sleep 20;; esac")
		ctx, cancel := context.WithTimeout(t.Context(), 150*time.Millisecond)
		defer cancel()
		ctx = context.WithValue(ctx, desktopCallContext{}, desktopRequestContext{identity: *c.Desktop, readOnly: true})
		raw, _ := json.Marshal(p)
		_, err := d.gitLog(ctx, raw)
		if !errors.Is(err, context.DeadlineExceeded) {
			t.Fatalf("cancel %v", err)
		}
	})
	hash := readGit(t, root, "rev-parse", "HEAD")
	t.Run("filters", func(t *testing.T) {
		marker := filepath.Join(t.TempDir(), "filter")
		for _, key := range []string{"filter.danger.clean", "filter.danger.smudge", "diff.danger.textconv", "diff.external", "core.fsmonitor"} {
			readGit(t, root, "config", key, "touch '"+marker+"'")
		}
		os.WriteFile(filepath.Join(root, ".gitattributes"), []byte("* filter=danger diff=danger\n"), 0600)
		gitReadWrapper(t, "test \"$GIT_NO_LAZY_FETCH\" = 1 || exit 91")
		if _, err := logTestCall(t, c, p); err != nil {
			t.Fatal(err)
		}
		if _, err := commitTestCall(t, c, rpc.GitCommitFilesParams{Path: root, Expected: p.Expected, Hash: hash}); err != nil {
			t.Fatal(err)
		}
		if _, err := os.Stat(marker); !os.IsNotExist(err) {
			t.Fatalf("filter executed %v", err)
		}
	})
	t.Run("replacement", func(t *testing.T) {
		next := readGitRepo(t, filepath.Join(t.TempDir(), "next"), true)
		t.Setenv("READ_ROOT", root)
		t.Setenv("READ_NEXT", next)
		t.Setenv("READ_OLD", root+"-old")
		t.Cleanup(func() { os.RemoveAll(root + "-old") })
		gitReadWrapper(t, "case \" $* \" in *\" diff-tree \"*) /usr/bin/git \"$@\" || exit $?; /bin/mv \"$READ_ROOT\" \"$READ_OLD\"; /bin/mv \"$READ_NEXT\" \"$READ_ROOT\"; exit 0;; esac")
		_, err := commitTestCall(t, c, rpc.GitCommitFilesParams{Path: root, Expected: p.Expected, Hash: hash})
		desktopErrorCode(t, err, rpc.CodeConflict)
	})
}
