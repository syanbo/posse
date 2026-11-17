package app

import (
	"bytes"
	"context"
	"crypto/sha1"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"image"
	"image/png"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/egoist/mygo/ui/editor"

	"posse/internal/files"
	"posse/internal/gitx"
	"posse/internal/rpc"
)

func targetGitContentHandler(t *testing.T, f *hostControlFixture, id gitx.RepositoryIdentity, old, work []byte) {
	t.Helper()
	sum := sha1.Sum(append(fmt.Appendf(nil, "blob %d\x00", len(old)), old...))
	oid := hex.EncodeToString(sum[:])
	hash := sha256.Sum256(work)
	stamp := files.Stamp{Size: int64(len(work)), Mode: 0600, Ino: 42}
	f.mu.Lock()
	normal := f.desktopCall
	f.desktopCall = func(ctx context.Context, p rpc.DesktopCall) (any, error) {
		switch p.Method {
		case "git.file":
			var q rpc.GitFileParams
			if err := json.Unmarshal(p.Params, &q); err != nil {
				return nil, err
			}
			if q.Expected != id {
				return nil, rpc.Errorf(rpc.CodeConflict, "repository changed")
			}
			commit := strings.Repeat("a", 40)
			if q.Source == gitx.FileIndex {
				commit = ""
			}
			return rpc.GitFileResult{Identity: id, File: gitx.FileVersion{Source: q.Source, Revision: q.Revision, Commit: commit, Path: q.File, OID: oid, Size: int64(len(old)), Binary: files.IsBinary(old)}}, nil
		case "git.blobRange":
			var q rpc.GitBlobRangeParams
			if err := json.Unmarshal(p.Params, &q); err != nil {
				return nil, err
			}
			end := min(int64(len(old)), q.Offset+int64(q.Limit))
			return rpc.GitBlobRangeResult{Identity: id, OID: oid, Size: int64(len(old)), Offset: q.Offset, Data: old[q.Offset:end], EOF: end == int64(len(old))}, nil
		case "files.resourceRange":
			var q rpc.FileResourceRangeParams
			if err := json.Unmarshal(p.Params, &q); err != nil {
				return nil, err
			}
			end := min(int64(len(work)), q.Offset+int64(q.Limit))
			return rpc.FileRangeResult{Stamp: stamp, Data: work[q.Offset:end], EOF: end == int64(len(work))}, nil
		case "files.resourceVersion":
			var q rpc.FileResourceVersionParams
			if err := json.Unmarshal(p.Params, &q); err != nil {
				return nil, err
			}
			return files.Version{Path: filepath.Join(q.Root, q.Path), Stamp: stamp, Hash: hex.EncodeToString(hash[:])}, nil
		}
		return normal(ctx, p)
	}
	f.mu.Unlock()
}

func TestTargetGitContentDiff(t *testing.T) {
	for _, mode := range []string{"text", "index", "binary", "image", "empty"} {
		t.Run(mode, func(t *testing.T) {
			a, f, v, discovery := remoteGitFixture(t)
			old, work := []byte("before\n"), []byte("after\n")
			rel, rev, kind := "same.txt", "", docDiff
			switch mode {
			case "index":
				rev = ":"
			case "binary":
				old, work, kind = []byte{0, 1}, []byte{0, 2}, docBinary
			case "image":
				var b bytes.Buffer
				if err := png.Encode(&b, image.NewRGBA(image.Rect(0, 0, 2, 3))); err != nil {
					t.Fatal(err)
				}
				old, work, rel, kind = b.Bytes(), b.Bytes(), "same.png", docImageDiff
			case "empty":
				old, work = []byte{}, []byte{}
			}
			targetGitContentHandler(t, f, discovery.Repos[0].Identity, old, work)
			g := discoverTargetGit(t, a, v)
			a.openDiff(&a.workspaces[0], diffSpec{root: g.repo, rel: rel, oldRev: "HEAD", newRev: rev}, nil, true)
			d := v.cur
			if d == nil {
				t.Fatal("diff did not open")
			}
			settleTargetGit(t, a, func() bool { return !d.diff.loading })
			if d.kind != kind || !d.diff.loaded {
				t.Fatalf("kind=%v error=%s app=%s", d.kind, d.err, a.err)
			}
			if kind == docDiff {
				want := work
				if mode == "index" {
					want = old
				}
				if d.diff.state.Old.String() != string(old) || d.diff.state.New.String() != string(want) {
					t.Fatal("diff used wrong source")
				}
			}
			if kind == docImageDiff && (d.diff.images[0].w != 2 || d.diff.images[1].h != 3) {
				t.Fatal("image diff did not decode target bytes")
			}
		})
	}
}

func TestTargetGitContentHeadAndFailure(t *testing.T) {
	a, f, v, discovery := remoteGitFixture(t)
	targetGitContentHandler(t, f, discovery.Repos[0].Identity, []byte("committed\n"), []byte("disk\n"))
	g := discoverTargetGit(t, a, v)
	g.status = &gitx.Status{Commit: strings.Repeat("a", 40)}
	d := &fileDoc{path: filepath.Join(v.root, "same.txt"), kind: docText, ed: editor.State{Buffer: editor.NewBuffer("unsaved\n")}}
	v.insertDoc(d)
	a.loadHead(v, d)
	settleTargetGit(t, a, func() bool { return !d.headLoading })
	if d.ed.DiffBase == nil || d.ed.DiffBase.String() != "committed\n" || d.ed.Buffer.String() != "unsaved\n" {
		t.Fatalf("baseline failed or changed edits: %s", a.err)
	}
	f.mu.Lock()
	normal := f.desktopCall
	f.desktopCall = func(ctx context.Context, p rpc.DesktopCall) (any, error) {
		if p.Method == "git.file" {
			return nil, rpc.Errorf(rpc.CodePermissionDenied, "temporary failure")
		}
		return normal(ctx, p)
	}
	f.mu.Unlock()
	a.loadHead(v, d)
	settleTargetGit(t, a, func() bool { return !d.headLoading })
	if d.headFor != "" || d.headBuf.String() != "committed\n" || a.err == "" {
		t.Fatal("failed head read lost content or cannot retry")
	}
	g.status.Commit = ""
	a.loadHead(v, d)
	if d.ed.DiffBase != nil || d.ed.Buffer.String() != "unsaved\n" {
		t.Fatal("unborn HEAD kept old baseline or changed edits")
	}
}

func TestPreviewTargetGitContent(t *testing.T) {
	if os.Getenv("POSSE_PREVIEW_DIR") == "" {
		t.Skip("preview directory required")
	}
	for _, dark := range []bool{false, true} {
		a, f, v, discovery := remoteGitFixture(t)
		targetGitContentHandler(t, f, discovery.Repos[0].Identity, []byte("const host = 'local';\nconst connected = false;\n"), []byte("const host = 'remote';\nconst connected = true;\n"))
		p := previewApp(t)
		p.host, p.viewID = a.host, "remote"
		p.workspaces, p.projects, p.selected, p.files = a.workspaces, a.projects, a.selected, a.files
		p.indexWorkspaces()
		p.activeTerm = map[string]string{}
		a = p
		tt := newTester(a.build, 1200, 760)
		a.openFilesPanel(&a.workspaces[0], sideChanges)
		until(t, tt, "remote changes", func() bool { return v.git.status != nil })
		a.openDiff(&a.workspaces[0], diffSpec{root: v.git.repo, rel: "same.txt", oldRev: "HEAD"}, nil, true)
		until(t, tt, "remote diff", func() bool { return v.cur.kind == docDiff })
		suffix := map[bool]string{false: "light", true: "dark"}[dark]
		savePNG(t, newPreviewTester(a, dark), "remote-git-diff-"+suffix+".png")
	}
}

func TestTargetGitContentLate(t *testing.T) {
	for _, mode := range []string{"close", "switch", "replace", "reverse"} {
		t.Run(mode, func(t *testing.T) {
			a, f, v, discovery := remoteGitFixture(t)
			targetGitContentHandler(t, f, discovery.Repos[0].Identity, []byte("before\n"), []byte("after\n"))
			g := discoverTargetGit(t, a, v)
			entered, unblock := make(chan struct{}), make(chan struct{})
			var once sync.Once
			defer once.Do(func() { close(unblock) })
			f.mu.Lock()
			normal := f.desktopCall
			f.desktopCall = func(ctx context.Context, p rpc.DesktopCall) (any, error) {
				if p.Method == "git.file" {
					select {
					case <-entered:
					default:
						close(entered)
						<-unblock
					}
				}
				return normal(ctx, p)
			}
			f.mu.Unlock()
			a.openDiff(&a.workspaces[0], diffSpec{root: g.repo, rel: "same.txt", oldRev: "HEAD"}, nil, true)
			d := v.cur
			select {
			case <-entered:
			case <-time.After(5 * time.Second):
				t.Fatal("content read did not start")
			}
			switch mode {
			case "close":
				a.dropDoc(v, d)
			case "switch":
				views := newHostViews(a)
				views.add("other", &app{})
				if !views.activate("other") {
					t.Fatal("host switch failed")
				}
			case "replace":
				v.repos.list[0] = &gitState{v: v, repo: g.repo, identity: g.identity}
			case "reverse":
				a.openDiff(&a.workspaces[0], diffSpec{root: g.repo, rel: "newest.txt", oldRev: "HEAD"}, nil, true)
				settleTargetGit(t, a, func() bool { return d.diff.loaded })
			}
			once.Do(func() { close(unblock) })
			settleTargetGit(t, a, func() bool { return !d.diff.loading })
			if mode == "reverse" {
				if d.diff.rel != "newest.txt" || d.diff.state.New.String() != "after\n" {
					t.Fatal("old diff replaced new result")
				}
			} else if d.diff.loaded {
				t.Fatal("stale content applied")
			}
		})
	}
}
