package app

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/egoist/mygo/ui/editor"
	"posse/internal/files"
	"posse/internal/rpc"
)

func TestTargetSymbolsQuery(t *testing.T) {
	a, f, v := targetSearchFixture(t)
	v.search.query.Dir = "ignored"
	d := &fileDoc{path: filepath.Join(v.root, "draft.go"), kind: docText, ed: editor.State{Buffer: editor.NewBuffer("package main")}}
	d.ed.Insert("type Target struct{}\n")
	v.docs = []*fileDoc{d}
	a.quick = quickOpen{open: true, ws: v.wsID, tab: quickSymbols, query: "Target"}
	requests := make(chan rpc.FileSymbolsParams, 1)
	f.mu.Lock()
	f.desktopCall = func(_ context.Context, p rpc.DesktopCall) (any, error) {
		if p.Method != "files.symbols" || p.Sequence != 0 {
			return nil, rpc.Errorf(rpc.CodeBadRequest, "unexpected request")
		}
		var args rpc.FileSymbolsParams
		if err := json.Unmarshal(p.Params, &args); err != nil {
			return nil, err
		}
		requests <- args
		return rpc.FileSymbolsResult{Symbols: []files.Symbol{{Name: "Target", Path: "remote.go", Kind: "type"}}}, nil
	}
	f.mu.Unlock()
	a.syncQuickSymbols(v, "Target", true)
	finishTargetSearch(t, a)
	args := <-requests
	if args.Root != v.root || !args.Query.TypesOnly || args.Query.Dir != "ignored" || args.Overlay["draft.go"] != d.ed.Buffer.String() {
		t.Fatalf("symbol request changed: %+v", args)
	}
	if a.quick.sym.running || len(a.quick.sym.syms) != 1 || a.quick.sym.syms[0].Path != "remote.go" {
		t.Fatal("target symbols missing")
	}
	a.closeQuick()
}

func TestTargetSymbolsStale(t *testing.T) {
	for _, mode := range []string{"identity", "query", "closed"} {
		t.Run(mode, func(t *testing.T) {
			a, f, v := targetSearchFixture(t)
			a.quick = quickOpen{open: true, ws: v.wsID, tab: quickSymbols}
			f.mu.Lock()
			f.desktopCall = func(context.Context, rpc.DesktopCall) (any, error) {
				return rpc.FileSymbolsResult{Symbols: []files.Symbol{{Name: "stale"}}}, nil
			}
			f.mu.Unlock()
			a.syncQuickSymbols(v, "stale", false)
			waitFileUpdate(t, a)
			a.runPending()
			waitFileUpdate(t, a)
			switch mode {
			case "identity":
				takeRequestTestControl(t, a)
			case "query":
				a.quick.sym.gen++
			case "closed":
				a.closeQuick()
			}
			a.runPending()
			if len(a.quick.sym.syms) != 0 {
				t.Fatal("stale symbols applied")
			}
			a.closeQuick()
		})
	}
}

func targetDefinitionFixture(t *testing.T, text string) (*app, *hostControlFixture, *workspace, *filesView, *fileDoc) {
	t.Helper()
	remote, f, v := targetSearchFixture(t)
	a := previewApp(t)
	a.host, a.viewID = remote.host, "remote"
	w := &a.workspaces[0]
	w.Path = v.root
	a.indexWorkspaces()
	v.wsID = w.ID
	a.files = map[string]*filesView{w.ID: v}
	d := &fileDoc{path: filepath.Join(v.root, "main.go"), kind: docText, ed: editor.State{Buffer: editor.NewBuffer(text)}}
	v.docs, v.cur = []*fileDoc{d}, d
	v.tree.Set(v.root, []files.Entry{{Name: "main.go"}, {Name: "target.go"}, {Name: "other.go"}}, files.Stamp{Ino: 1}, nil)
	if err := os.WriteFile(filepath.Join(v.root, "target.go"), []byte("package main\nfunc localOnly() {}"), 0600); err != nil {
		t.Fatal(err)
	}
	return a, f, w, v, d
}

func TestTargetDefinitionJump(t *testing.T) {
	for _, pathLink := range []bool{false, true} {
		t.Run(map[bool]string{false: "word", true: "path"}[pathLink], func(t *testing.T) {
			text := "package main\nfunc main() { target() }"
			if pathLink {
				text = "package main\nimport \"./target.go\""
			}
			a, f, w, v, d := targetDefinitionFixture(t, text)
			requests := make(chan rpc.FileDefinitionParams, 1)
			f.mu.Lock()
			f.desktopCall = func(_ context.Context, p rpc.DesktopCall) (any, error) {
				if p.Method != "files.definition" || p.Sequence != 0 {
					return nil, rpc.Errorf(rpc.CodeBadRequest, "unexpected request")
				}
				var args rpc.FileDefinitionParams
				if err := json.Unmarshal(p.Params, &args); err != nil {
					return nil, err
				}
				requests <- args
				if pathLink {
					return rpc.FileDefinitionResult{Path: filepath.Join(v.root, "target.go"), Line: 7}, nil
				}
				return rpc.FileDefinitionResult{Definitions: []files.Definition{{Path: "target.go", Line: 6, Text: "func target() {}", Span: [2]int{5, 11}, Decl: true}}}, nil
			}
			f.mu.Unlock()
			off := strings.Index(text, "target")
			a.goToDefinition(appL(), w, v, d, off)
			waitFileUpdate(t, a)
			a.runPending()
			args := <-requests
			if args.Text != text || args.Offset != off || args.Path != d.path || args.Root != v.root {
				t.Fatalf("definition source changed: %+v", args)
			}
			if v.cur == d || v.cur.path != filepath.Join(v.root, "target.go") || v.cur.gotoLine != 7 {
				t.Fatal("did not jump to target definition")
			}
		})
	}
}

func TestTargetDefinitionStale(t *testing.T) {
	for _, mode := range []string{"edit", "close", "identity", "new-query", "path", "new-tab"} {
		t.Run(mode, func(t *testing.T) {
			text := "package main\nfunc main() { target() }"
			a, f, w, v, d := targetDefinitionFixture(t, text)
			f.mu.Lock()
			f.desktopCall = func(context.Context, rpc.DesktopCall) (any, error) {
				return rpc.FileDefinitionResult{Definitions: []files.Definition{{Path: "target.go", Line: 6, Decl: true}}}, nil
			}
			f.mu.Unlock()
			a.goToDefinition(appL(), w, v, d, strings.Index(text, "target"))
			waitFileUpdate(t, a)
			switch mode {
			case "edit":
				d.ed.Insert("draft")
			case "close":
				v.docs = nil
				v.cur = nil
			case "identity":
				takeRequestTestControl(t, a)
			case "new-query":
				v.def.gen++
			case "path":
				d.path += ".moved"
			case "new-tab":
				v.cur = &fileDoc{path: filepath.Join(v.root, "new.go")}
			}
			before := v.cur
			a.runPending()
			if v.cur != before || a.defPick.open || a.err != "" {
				t.Fatal("stale definition changed selection or error")
			}
		})
	}
}

func TestTargetDefinitionPickerExpires(t *testing.T) {
	for _, mode := range []string{"edit", "identity"} {
		t.Run(mode, func(t *testing.T) {
			text := "package main\nfunc main() { target() }"
			a, f, w, v, d := targetDefinitionFixture(t, text)
			f.mu.Lock()
			f.desktopCall = func(context.Context, rpc.DesktopCall) (any, error) {
				return rpc.FileDefinitionResult{Definitions: []files.Definition{
					{Path: "target.go", Line: 6, Text: "func target() {}", Decl: true},
					{Path: "other.go", Line: 2, Text: "func target() {}", Decl: true},
				}}, nil
			}
			f.mu.Unlock()
			a.goToDefinition(appL(), w, v, d, strings.Index(text, "target"))
			waitFileUpdate(t, a)
			a.runPending()
			if !a.defPick.open || !a.defPick.current() {
				t.Fatal("multiple definitions did not open picker")
			}
			if mode == "edit" {
				d.ed.Insert("draft")
			} else {
				takeRequestTestControl(t, a)
			}
			tt := newTester(a.defDialog, 900, 700)
			tt.Frame()
			if a.defPick.open || v.cur != d {
				t.Fatal("expired picker remained usable")
			}
		})
	}
}

func TestTargetDefinitionAtDeclarationSearchesUsages(t *testing.T) {
	text := "package main\nfunc target() {}"
	a, f, w, v, d := targetDefinitionFixture(t, text)
	requests := make(chan rpc.FileSearchParams, 1)
	f.mu.Lock()
	f.desktopCall = func(_ context.Context, p rpc.DesktopCall) (any, error) {
		if p.Method == "files.definition" {
			return rpc.FileDefinitionResult{Definitions: []files.Definition{{Path: "main.go", Line: 1, Text: "func target() {}", Decl: true}}}, nil
		}
		if p.Method == "files.search" {
			var args rpc.FileSearchParams
			if err := json.Unmarshal(p.Params, &args); err != nil {
				return nil, err
			}
			requests <- args
			return rpc.FileSearchResult{}, nil
		}
		return nil, rpc.Errorf(rpc.CodeUnknown, "unexpected request")
	}
	f.mu.Unlock()
	a.goToDefinition(appL(), w, v, d, strings.Index(text, "target"))
	waitFileUpdate(t, a)
	a.runPending()
	finishTargetSearch(t, a)
	select {
	case q := <-requests:
		if q.Query.Text != `\btarget\b` || !q.Query.Regex || !q.Query.MatchCase {
			t.Fatalf("usage query: %+v", q.Query)
		}
	default:
		t.Fatal("current declaration did not search target usages")
	}
}

func TestPreviewTargetDefinition(t *testing.T) {
	if os.Getenv("POSSE_PREVIEW_DIR") == "" {
		t.Skip("preview directory required")
	}
	for _, dark := range []bool{false, true} {
		text := "package main\n\nfunc main() {\n    target()\n}\n"
		a, f, w, v, d := targetDefinitionFixture(t, text)
		newGitStates(v)
		a.showFiles(w)
		v.poll.at, v.listAt = time.Now().Add(time.Hour), time.Now().Add(time.Hour)
		v.list = &files.List{Root: v.root, Files: []string{"main.go", "target.go", "other.go"}}
		v.listRequest = a.request()
		f.mu.Lock()
		f.desktopCall = func(context.Context, rpc.DesktopCall) (any, error) {
			return rpc.FileDefinitionResult{Definitions: []files.Definition{
				{Path: "target.go", Line: 6, Text: "func target() {}", Span: [2]int{5, 11}, Kind: "func", Decl: true},
				{Path: "other.go", Line: 2, Text: "func target() {}", Span: [2]int{5, 11}, Kind: "func", Decl: true},
			}}, nil
		}
		f.mu.Unlock()
		a.goToDefinition(appL(), w, v, d, strings.Index(text, "target"))
		waitFileUpdate(t, a)
		a.runPending()
		suffix := map[bool]string{false: "light", true: "dark"}[dark]
		savePNG(t, newPreviewTester(a, dark), "target-definition-"+suffix+".png")
	}
}

func TestTargetDefinitionDoesNotResolveLocalImport(t *testing.T) {
	text := "package main\nimport \"./target.go\""
	a, f, w, v, d := targetDefinitionFixture(t, text)
	f.mu.Lock()
	f.desktopCall = func(context.Context, rpc.DesktopCall) (any, error) { return rpc.FileDefinitionResult{}, nil }
	f.mu.Unlock()
	a.goToDefinition(appL(), w, v, d, strings.Index(text, "target"))
	waitFileUpdate(t, a)
	a.runPending()
	if v.cur != d || d.hint == "" {
		t.Fatal("missing target import fell back to local path")
	}
}
