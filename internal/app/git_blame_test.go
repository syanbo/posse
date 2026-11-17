package app

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/egoist/mygo/ui"
	"posse/internal/files"
	"posse/internal/gitx"
	"posse/internal/rpc"
	"posse/locales"
)

func targetGitBlameHandler(t *testing.T, f *hostControlFixture, id gitx.RepositoryIdentity) {
	t.Helper()
	text := "target committed line\ntarget disk change\n"
	hash := sha256.Sum256([]byte(text))
	ci := targetLogCommit(0)
	f.mu.Lock()
	normal := f.desktopCall
	f.desktopCall = func(ctx context.Context, p rpc.DesktopCall) (any, error) {
		if p.Method == "git.blame" {
			var q rpc.GitBlameParams
			if err := json.Unmarshal(p.Params, &q); err != nil {
				return nil, err
			}
			if q.Expected != id || q.File != "same.txt" {
				return nil, rpc.Errorf(rpc.CodeConflict, "repository changed")
			}
			version := files.Version{Path: filepath.Join(id.Root, q.File), Stamp: files.Stamp{Size: int64(len(text)), Mode: 0600, Ino: 42, ModTime: time.Unix(123, 0)}, Hash: hex.EncodeToString(hash[:])}
			return rpc.GitBlameResult{Identity: id, File: q.File, Head: ci.Hash, Version: version, Lines: []gitx.BlameLine{
				{Hash: ci.Hash, Author: ci.Author, Time: ci.Time, Summary: ci.Subject, Text: "target committed line"},
				{Hash: strings.Repeat("0", 40), Author: "Not Committed Yet", Time: ci.Time, Summary: "Uncommitted", Text: "target disk change"},
			}}, nil
		}
		if p.Method == "git.commitFiles" {
			out, err := normal(ctx, p)
			if err != nil {
				return nil, err
			}
			detail := out.(rpc.GitCommitFilesResult)
			detail.Files = append(detail.Files, gitx.FileChange{Path: "src/detail.txt", Status: 'M'})
			return detail, nil
		}
		return normal(ctx, p)
	}
	f.mu.Unlock()
}

func targetBlameFixture(t *testing.T) (*app, *hostControlFixture, *filesView, *fileDoc, *ui.Tester) {
	t.Helper()
	a, f, v, out := remoteGitFixture(t)
	targetGitLogHandler(t, f, out.Repos[0].Identity, 3)
	targetGitContentHandler(t, f, out.Repos[0].Identity, []byte("target revision\n"), []byte("target worktree\n"))
	targetGitBlameHandler(t, f, out.Repos[0].Identity)
	p := previewApp(t)
	p.host, p.viewID = a.host, "remote"
	p.workspaces, p.projects, p.selected, p.files = a.workspaces, a.projects, a.selected, a.files
	p.indexWorkspaces()
	p.activeTerm = map[string]string{}
	a = p
	discoverTargetGit(t, a, v)
	a.openFile(&a.workspaces[0], filepath.Join(v.root, "same.txt"), 0, [2]int{})
	tt := newTester(a.build, 1200, 760)
	until(t, tt, "target editor", func() bool { return v.cur != nil && v.cur.kind == docText })
	tt.Advance(300 * time.Millisecond)
	tt.Frame()
	return a, f, v, v.cur, tt
}

func openTargetBlame(t *testing.T, a *app, v *filesView, d *fileDoc, tt *ui.Tester) {
	t.Helper()
	if err := tt.Click(locales.In("zh-Hans").FilesAnnotateTip()); err != nil {
		t.Fatal(err)
	}
	until(t, tt, "target annotate", func() bool { return d.blame != nil && d.blame.loaded && !d.blame.loading })
}

func TestTargetGitBlameProduction(t *testing.T) {
	a, _, v, d, tt := targetBlameFixture(t)
	d.ed.Buffer.Set("unsaved editor draft")
	draft := d.ed.Buffer
	openTargetBlame(t, a, v, d, tt)
	if !tt.HasText("target committed line") || !tt.HasText("target disk change") || !tt.HasText("没提交") {
		t.Fatal("target disk blame missing", tt.Texts())
	}
	if err := tt.Click("Target"); err != nil {
		t.Fatal(err)
	}
	until(t, tt, "blame commit detail", func() bool { return d.blame.sel != nil && len(d.blame.sel.files) == 2 })
	if d.blame.sel.diffBase == nil || *d.blame.sel.diffBase != targetLogCommit(0).Parents[0] {
		t.Fatal("commit detail did not receive DiffBase")
	}
	if err := tt.Click("src/detail.txt"); err != nil {
		t.Fatal(err)
	}
	until(t, tt, "blame commit diff", func() bool { return v.cur != nil && v.cur.diff != nil && v.cur.kind == docDiff })
	if v.cur.diff.oldRev != targetLogCommit(0).Parents[0] || v.cur.diff.state.New.String() != "target revision\n" {
		t.Fatal("wrong blame detail diff")
	}
	a.openFile(&a.workspaces[0], d.path, 0, [2]int{})
	tt.Frame()
	if err := tt.Click(locales.In("zh-Hans").FilesCloseAnnotate()); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	if d.blame != nil || d.ed.Buffer != draft || draft.String() != "unsaved editor draft" {
		t.Fatal("closing blame changed dirty buffer")
	}
	openTargetBlame(t, a, v, d, tt)
	if d.ed.Buffer != draft || draft.String() != "unsaved editor draft" {
		t.Fatal("reopening blame changed dirty buffer")
	}
}

func TestTargetGitBlameFailurePreserves(t *testing.T) {
	a, f, v, d, tt := targetBlameFixture(t)
	openTargetBlame(t, a, v, d, tt)
	bd := d.blame
	var calls atomic.Int32
	var fail atomic.Bool
	fail.Store(true)
	f.mu.Lock()
	normal := f.desktopCall
	f.desktopCall = func(ctx context.Context, p rpc.DesktopCall) (any, error) {
		if p.Method == "git.blame" {
			calls.Add(1)
			if fail.Load() {
				return nil, rpc.Errorf(rpc.CodeConflict, "worktree changed")
			}
		}
		return normal(ctx, p)
	}
	f.mu.Unlock()
	a.loadTargetBlame(v, d)
	until(t, tt, "blame error", func() bool { return !bd.loading && bd.err != "" })
	for range 5 {
		tt.Frame()
	}
	if calls.Load() != 1 || !tt.HasText("target committed line") {
		t.Fatal("error discarded lines or retried each frame")
	}
	fail.Store(false)
	a.loadTargetBlame(v, d)
	until(t, tt, "blame retry", func() bool { return !bd.loading && bd.err == "" })
}

func TestTargetGitBlameLateAndResume(t *testing.T) {
	for _, tc := range []struct{ method, action string }{
		{"git.blame", "tab"}, {"git.commitFiles", "tab"}, {"git.blame", "reconnect"}, {"git.blame", "close"}, {"git.commitFiles", "doc"}, {"git.blame", "identity"},
	} {
		t.Run(tc.method+"/"+tc.action, func(t *testing.T) {
			a, f, v, d, tt := targetBlameFixture(t)
			openTargetBlame(t, a, v, d, tt)
			bd := d.blame
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
			if tc.method == "git.blame" {
				a.loadTargetBlame(v, d)
			} else {
				a.selectCommit(bd.root, &bd.sel, targetLogCommit(0))
			}
			<-entered
			seq := bd.read.seq
			detailSeq := uint64(0)
			if bd.sel != nil {
				detailSeq = bd.sel.read.seq
			}
			old := a.request()
			switch tc.action {
			case "tab":
				a.openFile(&a.workspaces[0], filepath.Join(v.root, "other.txt"), 0, [2]int{})
				tt.Frame()
			case "reconnect":
				a.host.detach()
				tt.Frame()
			case "close":
				if err := tt.Click(locales.In("zh-Hans").FilesCloseAnnotate()); err != nil {
					t.Fatal(err)
				}
				tt.Frame()
			case "doc":
				a.dropDoc(v, d)
				tt.Frame()
			case "identity":
				id := v.git.identity
				id.RootInode++
				a.targetGitIdentity(v.git, id)
			}
			a.runPending()
			if tc.method == "git.blame" && bd.read.seq == seq {
				t.Fatal("blame was not invalidated")
			}
			if tc.method == "git.commitFiles" && (bd.sel.files != nil || bd.sel.read.seq == detailSeq) {
				t.Fatal("late detail applied")
			}
			switch tc.action {
			case "tab":
				a.openFile(&a.workspaces[0], d.path, 0, [2]int{})
			case "reconnect":
				if err := a.host.connect(context.Background(), old.client); err != nil {
					t.Fatal(err)
				}
			default:
				return
			}
			until(t, tt, "visible blame resumed", func() bool {
				if calls.Load() < 2 {
					return false
				}
				if tc.method == "git.blame" {
					return bd.loaded && !bd.loading && bd.err == ""
				}
				return bd.sel != nil && !bd.sel.loading && len(bd.sel.files) == 2
			})
		})
	}
}

func TestPreviewTargetGitBlame(t *testing.T) {
	if os.Getenv("POSSE_PREVIEW_DIR") == "" {
		t.Skip("POSSE_PREVIEW_DIR not set")
	}
	for _, dark := range []bool{false, true} {
		a, _, v, d, tt := targetBlameFixture(t)
		openTargetBlame(t, a, v, d, tt)
		a.selectCommit(d.blame.root, &d.blame.sel, targetLogCommit(0))
		until(t, tt, "blame detail", func() bool { return d.blame.sel != nil && !d.blame.sel.loading })
		name := "light"
		if dark {
			name = "dark"
		}
		savePNG(t, newPreviewTester(a, dark), "remote-git-blame-"+name+".png")
	}
}
