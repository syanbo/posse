package app

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"github.com/egoist/mygo/ui/editor"
	"posse/internal/files"
	"posse/internal/rpc"
)

func targetSearchFixture(t *testing.T) (*app, *hostControlFixture, *filesView) {
	t.Helper()
	a, f := requestTestApp(t, "remote")
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "local-only.txt"), []byte("local secret"), 0600); err != nil {
		t.Fatal(err)
	}
	v := &filesView{wsID: "remote-w", root: root, remote: true, tree: files.NewTree(root)}
	a.files = map[string]*filesView{v.wsID: v}
	return a, f, v
}

func finishTargetSearch(t *testing.T, a *app) {
	t.Helper()
	waitFileUpdate(t, a)
	a.runPending()
	waitFileUpdate(t, a)
	a.runPending()
}

func TestTargetListAndQuickAll(t *testing.T) {
	a, f, v := targetSearchFixture(t)
	var all atomic.Bool
	f.mu.Lock()
	f.desktopCall = func(_ context.Context, p rpc.DesktopCall) (any, error) {
		if p.Method != "files.list" || p.Sequence != 0 {
			return nil, rpc.Errorf(rpc.CodeBadRequest, "unexpected request")
		}
		var args rpc.FileListParams
		if err := json.Unmarshal(p.Params, &args); err != nil {
			return nil, err
		}
		all.Store(args.All)
		return &files.List{Root: args.Root, Files: []string{"target-only.txt"}}, nil
	}
	f.mu.Unlock()
	a.openQuick(&workspace{ID: v.wsID, Path: v.root}, quickFiles)
	waitFileUpdate(t, a)
	a.runPending()
	if v.list == nil || len(v.list.Files) != 1 || v.list.Files[0] != "target-only.txt" || all.Load() {
		t.Fatalf("target list: %+v", v.list)
	}
	a.toggleQuickAll(v)
	waitFileUpdate(t, a)
	a.runPending()
	if !all.Load() || a.quick.allList == nil || a.quick.allList.Files[0] != "target-only.txt" {
		t.Fatal("all-files did not use target")
	}
	a.closeQuick()
}

func TestTargetListStaleAndFailure(t *testing.T) {
	a, f, v := targetSearchFixture(t)
	var calls atomic.Int32
	f.mu.Lock()
	f.desktopCall = func(_ context.Context, p rpc.DesktopCall) (any, error) {
		n := calls.Add(1)
		if n == 3 {
			return nil, rpc.Errorf(rpc.CodePermissionDenied, "denied")
		}
		return &files.List{Root: v.root, Files: []string{"target.txt"}}, nil
	}
	f.mu.Unlock()
	a.refreshList(v, false, nil)
	waitFileUpdate(t, a)
	takeRequestTestControl(t, a)
	a.runPending()
	if v.list != nil || v.listing || v.listErr != nil {
		t.Fatal("old listing applied")
	}
	a.refreshList(v, false, nil)
	waitFileUpdate(t, a)
	a.runPending()
	if v.list == nil {
		t.Fatal("listing did not restart after identity change")
	}
	a.refreshList(v, true, nil)
	waitFileUpdate(t, a)
	a.runPending()
	if v.list == nil || v.list.Files[0] != "target.txt" || v.listErr == nil {
		t.Fatal("failed listing erased cache or hid error")
	}
}

func TestTargetSearchQueryAndOverlay(t *testing.T) {
	a, f, v := targetSearchFixture(t)
	d := &fileDoc{path: filepath.Join(v.root, "target.txt"), kind: docText, ed: editor.State{Buffer: editor.NewBuffer("original")}}
	d.ed.Insert("draft")
	v.docs = []*fileDoc{d}
	v.search.query = files.Query{Text: "draft", Dir: "ignored", Regex: true, MatchCase: true, Include: "*.txt", Exclude: "old*"}
	requests := make(chan rpc.FileSearchParams, 1)
	f.mu.Lock()
	f.desktopCall = func(_ context.Context, p rpc.DesktopCall) (any, error) {
		if p.Method != "files.search" || p.Sequence != 0 {
			return nil, rpc.Errorf(rpc.CodeBadRequest, "unexpected request")
		}
		var args rpc.FileSearchParams
		if err := json.Unmarshal(p.Params, &args); err != nil {
			return nil, err
		}
		requests <- args
		return rpc.FileSearchResult{Hits: []files.Hit{{Path: "target.txt", Text: "target draft", Line: 2}}, Truncated: true}, nil
	}
	f.mu.Unlock()
	a.startSearch(v, 0)
	finishTargetSearch(t, a)
	args := <-requests
	if args.Root != v.root || args.Query != v.search.query || args.Overlay["target.txt"] != d.ed.Buffer.String() {
		t.Fatalf("query changed: %+v", args)
	}
	if v.search.running || v.search.err != "" || len(v.search.hits) != 1 || v.search.hits[0].Text != "target draft" || !v.search.truncated {
		t.Fatalf("target search result: %+v", v.search)
	}
}

func TestTargetSearchStaleAndErrors(t *testing.T) {
	for _, mode := range []string{"before-send", "identity", "query", "error"} {
		t.Run(mode, func(t *testing.T) {
			a, f, v := targetSearchFixture(t)
			var calls atomic.Int32
			f.mu.Lock()
			f.desktopCall = func(_ context.Context, p rpc.DesktopCall) (any, error) {
				calls.Add(1)
				if mode == "error" {
					return nil, rpc.Errorf(rpc.CodePermissionDenied, "denied")
				}
				return rpc.FileSearchResult{Hits: []files.Hit{{Path: "stale.txt"}}}, nil
			}
			f.mu.Unlock()
			v.search.query.Text = "query"
			v.search.hits = []files.Hit{{Path: "keep.txt"}}
			a.startSearch(v, 0)
			waitFileUpdate(t, a)
			if mode == "before-send" {
				takeRequestTestControl(t, a)
				a.runPending()
			} else {
				a.runPending()
				waitFileUpdate(t, a)
				switch mode {
				case "identity":
					takeRequestTestControl(t, a)
				case "query":
					v.search.gen++
				}
				a.runPending()
			}
			if len(v.search.hits) != 1 || v.search.hits[0].Path != "keep.txt" {
				t.Fatal("stale or failed query replaced previous hits")
			}
			if mode == "before-send" && calls.Load() != 0 {
				t.Fatal("delayed query borrowed new identity")
			}
			if mode == "error" && v.search.err == "" {
				t.Fatal("search error hidden")
			}
		})
	}
}

func TestTargetQuickTextAndClose(t *testing.T) {
	for _, closeBeforeReply := range []bool{false, true} {
		t.Run(map[bool]string{false: "results", true: "closed"}[closeBeforeReply], func(t *testing.T) {
			a, f, v := targetSearchFixture(t)
			f.mu.Lock()
			f.desktopCall = func(_ context.Context, p rpc.DesktopCall) (any, error) {
				hits := make([]files.Hit, quickTextMax+1)
				for i := range hits {
					hits[i] = files.Hit{Path: "target.txt", Text: "remote hit", Line: i}
				}
				return rpc.FileSearchResult{Hits: hits}, nil
			}
			f.mu.Unlock()
			a.quick = quickOpen{open: true, ws: v.wsID, tab: quickText}
			a.syncQuickText(v, "remote")
			waitFileUpdate(t, a)
			a.runPending()
			waitFileUpdate(t, a)
			if closeBeforeReply {
				a.closeQuick()
			}
			a.runPending()
			if closeBeforeReply {
				if len(a.quick.text.hits) != 0 || a.quick.open {
					t.Fatal("closed quick search received old hits")
				}
			} else {
				if a.quick.text.running || len(a.quick.text.hits) != quickTextMax || !a.quick.text.more {
					t.Fatal("quick search did not limit target hits")
				}
				a.workspaces = []workspace{{ID: v.wsID, Path: v.root}}
				a.indexWorkspaces()
				a.quick.query = "remote"
				v.listing = true
				tt := newTester(a.quickDialog, 900, 700)
				tt.Frame()
				if !tt.HasText("remote hit") {
					t.Fatal("target text results waited for an unrelated file list")
				}
			}
			a.closeQuick()
		})
	}
}

func TestPreviewTargetSearch(t *testing.T) {
	if os.Getenv("POSSE_PREVIEW_DIR") == "" {
		t.Skip("preview directory required")
	}
	for _, dark := range []bool{false, true} {
		a := previewApp(t)
		v := previewFiles(t, a, "main.go")
		a.viewID, v.remote = "remote", true
		a.quick = quickOpen{open: true, ws: v.wsID, tab: quickText, query: "config",
			text: quickTextSearch{key: "\x00config", hits: []files.Hit{
				{Path: "main.go", Line: 17, Text: "func loadConfig(home string) (*config, error) {", Spans: [][2]int{{9, 15}, {30, 36}}},
				{Path: "main.go", Line: 21, Text: "return &config{Home: home}, nil", Spans: [][2]int{{8, 14}}},
			}}}
		suffix := map[bool]string{false: "light", true: "dark"}[dark]
		savePNG(t, newPreviewTester(a, dark), "target-search-"+suffix+".png")
	}
}

func TestTargetSearchBadRegex(t *testing.T) {
	a, _, v := targetSearchFixture(t)
	v.search.query = files.Query{Text: "[", Regex: true}
	a.startSearch(v, time.Second)
	if !v.search.badRegex || v.search.running || v.search.err == "" {
		t.Fatal("invalid regex started remote request")
	}
}
