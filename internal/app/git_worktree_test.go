package app

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/egoist/mygo/ui"
	"posse/internal/files"
	"posse/internal/gitx"
	"posse/internal/rpc"
	"posse/locales"
)

type worktreeTestState struct {
	mu                  sync.Mutex
	text, baseline      string
	fail                bool
	compares, resources int
}

func worktreeTestVersion(root, path, text string, mode os.FileMode) files.Version {
	h := sha256.Sum256([]byte(text))
	return files.Version{Path: filepath.Join(root, path), Stamp: files.Stamp{Size: int64(len(text)), Mode: mode, Ino: 42}, Hash: hex.EncodeToString(h[:])}
}

func worktreeFixture(t *testing.T) (*app, *hostControlFixture, *filesView, *fileDoc, *ui.Tester, *worktreeTestState) {
	t.Helper()
	a, f, v, draft, tt := targetCompareFixture(t, 3)
	id := v.git.identity
	st := &worktreeTestState{text: "target working file\n", baseline: strings.Repeat("b", 64)}
	f.mu.Lock()
	normal := f.desktopCall
	f.desktopCall = func(ctx context.Context, p rpc.DesktopCall) (any, error) {
		st.mu.Lock()
		text, baseline, fail := st.text, st.baseline, st.fail
		if p.Method == "git.worktreeCompare" {
			st.compares++
		}
		if p.Method == "files.resourceRange" {
			st.resources++
		}
		st.mu.Unlock()
		version := worktreeTestVersion(id.Root, "src/renamed.txt", text, 0600)
		switch p.Method {
		case "git.worktreeCompare":
			var q rpc.GitWorktreeCompareParams
			if err := json.Unmarshal(p.Params, &q); err != nil {
				return nil, err
			}
			if q.Expected != id || q.Path != id.Root || fail || q.Baseline != "" && q.Baseline != baseline {
				return nil, rpc.Errorf(rpc.CodeConflict, "%s", gitx.ErrWorktreeChanged)
			}
			return rpc.GitWorktreeCompareResult{Identity: id, Revision: q.Revision, WorktreeComparison: gitx.WorktreeComparison{
				Commit: strings.Repeat("a", 40), Baseline: baseline, Files: []gitx.WorktreeFile{
					{FileChange: gitx.FileChange{Path: "src/renamed.txt", Orig: "old.txt", Status: 'R'}, Kind: "file", Version: version, BaseMode: "100644", BaseOID: strings.Repeat("c", 40)},
					{FileChange: gitx.FileChange{Path: "link.png", Status: 'M'}, Kind: "symlink", Link: "../actual-link-target", Version: worktreeTestVersion(id.Root, "link.png", "../actual-link-target", os.ModeSymlink|0777), BaseMode: "120000", BaseOID: strings.Repeat("d", 40)},
					{FileChange: gitx.FileChange{Path: "module", Status: 'M'}, Kind: "gitlink", OID: strings.Repeat("e", 40), Dirty: true, Version: worktreeTestVersion(id.Root, "module", "gitlink", os.ModeDir|0755), BaseMode: "160000", BaseOID: strings.Repeat("f", 40)},
					{FileChange: gitx.FileChange{Path: "gone.txt", Status: 'D'}, Kind: "missing", Version: files.Version{Path: filepath.Join(id.Root, "gone.txt"), Stamp: files.Stamp{Missing: true}}, BaseMode: "100644", BaseOID: strings.Repeat("c", 40)},
					{FileChange: gitx.FileChange{Path: "directory.txt", Status: 'D'}, Kind: "directory", Version: worktreeTestVersion(id.Root, "directory.txt", "directory", os.ModeDir|0755), BaseMode: "100644", BaseOID: strings.Repeat("c", 40)},
				},
			}}, nil
		case "files.resourceRange":
			var q rpc.FileResourceRangeParams
			if err := json.Unmarshal(p.Params, &q); err != nil {
				return nil, err
			}
			if q.Path != "src/renamed.txt" {
				return nil, rpc.Errorf(rpc.CodeInternal, "special entry read as resource: %s", q.Path)
			}
			end := min(int64(len(text)), q.Offset+int64(q.Limit))
			return rpc.FileRangeResult{Stamp: version.Stamp, Data: []byte(text)[q.Offset:end], EOF: end == int64(len(text))}, nil
		case "files.resourceVersion":
			return version, nil
		case "files.entry":
			return files.EntryVersion{Path: version.Path, Stamp: version.Stamp}, nil
		case "git.file":
			var q rpc.GitFileParams
			if err := json.Unmarshal(p.Params, &q); err != nil {
				return nil, err
			}
			if q.File == "module" {
				return nil, rpc.Errorf(rpc.CodeInternal, "gitlink read as blob")
			}
			if q.File == "src/renamed.txt" && q.Revision == strings.Repeat("a", 40) {
				return nil, rpc.Errorf(rpc.CodeInternal, "rename ignored original path")
			}
		}
		return normal(ctx, p)
	}
	f.mu.Unlock()
	return a, f, v, draft, tt, st
}

func openWorktreeComparison(t *testing.T, a *app, v *filesView, tt *ui.Tester) *fileDoc {
	t.Helper()
	a.openCompare(&a.workspaces[0], "feature-target", "target-main", true)
	d := v.cur
	until(t, tt, "worktree comparison", func() bool { return !d.comp.loading && (d.comp.loaded || d.comp.err != "") })
	if d.comp.err != "" {
		t.Fatal(d.comp.err)
	}
	return d
}

func clickWorktreeFile(t *testing.T, v *filesView, tt *ui.Tester, path string) *fileDoc {
	t.Helper()
	if err := tt.Click(path); err != nil {
		t.Fatal(err)
	}
	until(t, tt, "worktree diff", func() bool {
		return v.cur.diff != nil && !v.cur.diff.loading && (v.cur.diff.loaded || v.cur.diff.compareErr != "")
	})
	return v.cur
}

func TestTargetGitWorktreeProduction(t *testing.T) {
	a, _, v, draft, tt, st := worktreeFixture(t)
	draft.ed.Buffer.Set("unsaved worktree draft")
	buffer := draft.ed.Buffer
	a.openBranches(&a.workspaces[0])
	until(t, tt, "branches", func() bool { return tt.HasText("feature-target") })
	if err := tt.Click("feature-target"); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	if err := tt.Click(locales.In("zh-Hans").BranchCompareWorkspace()); err != nil {
		t.Fatal(err)
	}
	until(t, tt, "worktree menu comparison", func() bool { return v.cur.comp != nil && v.cur.comp.loaded })
	owner := v.cur
	d := clickWorktreeFile(t, v, tt, "src/renamed.txt")
	if d.kind != docDiff || d.diff.oldRev != strings.Repeat("a", 40) || d.diff.orig != "old.txt" || d.diff.state.New.String() != "target working file\n" {
		t.Fatalf("wrong worktree diff: %v %s", d.kind, d.err)
	}
	if draft.ed.Buffer != buffer || buffer.String() != "unsaved worktree draft" {
		t.Fatal("dirty editor overwritten")
	}
	st.mu.Lock()
	before := st.compares
	st.mu.Unlock()
	for range 3 {
		a.reloadTargetDiff(v, d)
		until(t, tt, "single file poll", func() bool { return !d.diff.loading })
	}
	st.mu.Lock()
	after := st.compares
	st.mu.Unlock()
	if after != before {
		t.Fatal("regular file poll rescanned whole comparison")
	}
	for _, path := range []string{"link.png", "module", "gone.txt", "directory.txt"} {
		v.cur = owner
		tt.Frame()
		d = clickWorktreeFile(t, v, tt, path)
		if d.kind != docDiff || d.diff.compareErr != "" {
			t.Fatalf("%s: kind=%v err=%s", path, d.kind, d.err)
		}
		switch path {
		case "link.png":
			if d.diff.state.New.String() != "../actual-link-target" {
				t.Fatal("symlink followed instead of showing link")
			}
		case "module":
			if !tt.HasText(locales.In("zh-Hans").LogUncommitted()) {
				t.Fatal("submodule dirty status is not visible")
			}
			if d.diff.state.Old.String() != "Subproject commit "+strings.Repeat("f", 40)+"\n" || d.diff.state.New.String() != "Subproject commit "+strings.Repeat("e", 40)+"-dirty\n" {
				t.Fatal("gitlink OID or dirty state missing")
			}
		default:
			if d.diff.state.New.String() != "" {
				t.Fatal("deleted or directory side has file content")
			}
		}
		st.mu.Lock()
		before = st.compares
		resources := st.resources
		st.mu.Unlock()
		for range 3 {
			a.reloadTargetDiff(v, d)
			tt.Frame()
		}
		st.mu.Lock()
		after = st.compares
		newResources := st.resources
		st.mu.Unlock()
		if after != before || resources != newResources {
			t.Fatal("special entry poll performed a scan or resource read")
		}
		a.diffToEditor(&a.workspaces[0], v, d)
		if v.cur != d {
			t.Fatal("special entry editor shortcut followed entry")
		}
	}
}

func TestTargetGitWorktreeRefreshBaseline(t *testing.T) {
	for _, loaded := range []bool{false, true} {
		t.Run(map[bool]string{false: "list-open", true: "shown-poll"}[loaded], func(t *testing.T) {
			a, _, v, _, tt, st := worktreeFixture(t)
			owner := openWorktreeComparison(t, a, v, tt)
			var d *fileDoc
			if loaded {
				d = clickWorktreeFile(t, v, tt, "src/renamed.txt")
			}
			st.mu.Lock()
			st.text = "new target bytes after listing\n"
			st.baseline = strings.Repeat("c", 64)
			st.mu.Unlock()
			if loaded {
				a.reloadTargetDiff(v, d)
				until(t, tt, "stale diff", func() bool { return !d.diff.loading && d.diff.compareErr != "" })
				if d.diff.state.New.String() != "target working file\n" {
					t.Fatal("poll replaced old comparison content")
				}
			} else {
				d = clickWorktreeFile(t, v, tt, "src/renamed.txt")
			}
			if d.diff.compareErr == "" {
				t.Fatal("changed worktree shown under old baseline")
			}
			st.mu.Lock()
			before := st.compares
			st.mu.Unlock()
			for range 4 {
				a.reloadTargetDiff(v, d)
				tt.Frame()
			}
			st.mu.Lock()
			after := st.compares
			st.mu.Unlock()
			if before != after {
				t.Fatal("stale comparison retried automatically")
			}
			oldKey := d.path
			if err := tt.Click(locales.In("zh-Hans").CompareRefresh()); err != nil {
				t.Fatal(err)
			}
			until(t, tt, "comparison refreshed", func() bool {
				return v.cur == owner && !owner.comp.loading && owner.comp.worktree.Baseline == strings.Repeat("c", 64)
			})
			d = clickWorktreeFile(t, v, tt, "src/renamed.txt")
			if d.path == oldKey || d.diff.compareErr != "" || d.diff.state.New.String() != "new target bytes after listing\n" {
				t.Fatal("same OID/path reused old baseline")
			}
		})
	}
}

func TestTargetGitWorktreeFailureAndLate(t *testing.T) {
	t.Run("failure", func(t *testing.T) {
		a, _, v, _, tt, st := worktreeFixture(t)
		d := openWorktreeComparison(t, a, v, tt)
		st.mu.Lock()
		st.fail = true
		st.mu.Unlock()
		a.loadCompare(v, d)
		until(t, tt, "list failed", func() bool { return !d.comp.loading && d.comp.err != "" })
		if len(d.comp.changes) != 5 || !tt.HasText("renamed.txt") {
			t.Fatal("failed refresh lost file list")
		}
		st.mu.Lock()
		before := st.compares
		st.fail = false
		st.mu.Unlock()
		for range 4 {
			tt.Frame()
		}
		st.mu.Lock()
		after := st.compares
		st.mu.Unlock()
		if before != after {
			t.Fatal("failed list retried per frame")
		}
		if err := tt.Click(locales.In("zh-Hans").CommonRefresh()); err != nil {
			t.Fatal(err)
		}
		until(t, tt, "explicit retry", func() bool { return !d.comp.loading && d.comp.err == "" })
		st.mu.Lock()
		st.fail = true
		st.mu.Unlock()
		failed := clickWorktreeFile(t, v, tt, "src/renamed.txt")
		if failed.diff.compareErr == "" {
			t.Fatal("baseline validation error was hidden")
		}
		old := failed.diff
		st.mu.Lock()
		st.fail = false
		st.mu.Unlock()
		if err := tt.Click(locales.In("zh-Hans").CompareRefresh()); err != nil {
			t.Fatal(err)
		}
		until(t, tt, "same baseline refreshed", func() bool { return v.cur == d && !d.comp.loading })
		fresh := clickWorktreeFile(t, v, tt, "src/renamed.txt")
		if fresh.diff == old || !fresh.diff.loaded || fresh.diff.compareErr != "" {
			t.Fatal("same baseline kept failed diff instead of retrying")
		}
	})
	for _, tc := range []struct{ method, action string }{{"git.worktreeCompare", "tab"}, {"git.worktreeCompare", "close"}, {"git.worktreeCompare", "reconnect"}, {"files.resourceRange", "tab"}, {"files.resourceRange", "close"}} {
		t.Run(tc.method+"/"+tc.action, func(t *testing.T) {
			a, f, v, draft, tt, _ := worktreeFixture(t)
			d := openWorktreeComparison(t, a, v, tt)
			entered, release := make(chan struct{}), make(chan struct{})
			defer close(release)
			var calls atomic.Int32
			f.mu.Lock()
			normal := f.desktopCall
			f.desktopCall = func(ctx context.Context, p rpc.DesktopCall) (any, error) {
				if p.Method == tc.method && calls.Add(1) == 1 {
					close(entered)
					<-release
				}
				return normal(ctx, p)
			}
			f.mu.Unlock()
			if tc.method == "git.worktreeCompare" {
				a.loadCompare(v, d)
			} else {
				specs := d.comp.worktreeSpecs(appL())
				a.openDiff(&a.workspaces[0], specs[0], specs, true)
				d = v.cur
			}
			<-entered
			old := a.request()
			switch tc.action {
			case "tab":
				v.cur = draft
				tt.Frame()
			case "close":
				a.dropDoc(v, d)
				tt.Frame()
			case "reconnect":
				a.host.detach()
				tt.Frame()
			}
			if d.diff != nil && (d.diff.loading || d.diff.loaded) {
				t.Fatal("hidden diff was not canceled")
			}
			if d.comp != nil && d.comp.loading {
				t.Fatal("hidden list was not canceled")
			}
			if tc.action == "close" {
				return
			}
			if tc.action == "reconnect" {
				if err := a.host.connect(context.Background(), old.client); err != nil {
					t.Fatal(err)
				}
			} else {
				v.cur = d
			}
			until(t, tt, "worktree read resumed", func() bool {
				if calls.Load() < 2 {
					return false
				}
				if d.diff != nil {
					return d.diff.loaded && !d.diff.loading
				}
				return d.comp.loaded && !d.comp.loading
			})
		})
	}
}

func TestPreviewTargetGitWorktree(t *testing.T) {
	if os.Getenv("POSSE_PREVIEW_DIR") == "" {
		t.Skip("POSSE_PREVIEW_DIR not set")
	}
	for _, dark := range []bool{false, true} {
		a, _, v, _, tt, _ := worktreeFixture(t)
		owner := openWorktreeComparison(t, a, v, tt)
		name := "light"
		if dark {
			name = "dark"
		}
		savePNG(t, newPreviewTester(a, dark), "remote-git-worktree-"+name+".png")
		v.cur = owner
		tt.Frame()
		clickWorktreeFile(t, v, tt, "module")
		savePNG(t, newPreviewTester(a, dark), "remote-git-worktree-module-"+name+".png")
	}
}
