package daemon

import (
	"context"
	"encoding/json"
	"fmt"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"posse/internal/files"
	"posse/internal/rpc"
)

func fileSymbolsViewer(t *testing.T) (*Daemon, func(string, any, any) error) {
	t.Helper()
	d, _ := newTestDaemon(t)
	socket := desktopServer(t, d)
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	t.Cleanup(cancel)
	return d, func(method string, p, out any) error {
		raw, err := json.Marshal(p)
		if err != nil {
			return err
		}
		return rpc.Call(ctx, socket, "desktop.call", rpc.DesktopCall{
			Desktop: rpc.DesktopIdentity{InstanceID: d.instanceID, ClientID: "viewer"}, Method: method, Params: raw,
		}, out)
	}
}

func TestDesktopFileSymbols(t *testing.T) {
	d, call := fileSymbolsViewer(t)
	root := t.TempDir()
	gitInit(t, root)
	for path, text := range map[string]string{
		".gitignore":        "ignored/\n",
		"conf.go":           "package conf\ntype Config struct{}\nfunc loadConfig() {}\n",
		"web/app.ts":        "export class ConfigStore {}\n",
		"ignored/hidden.go": "package hidden\ntype HiddenConfig struct{}\n",
	} {
		fileSearchWrite(t, filepath.Join(root, path), text)
	}
	for _, tc := range []struct {
		name string
		q    files.SymbolQuery
		ov   map[string]string
		want []string
	}{
		{"all", files.SymbolQuery{Text: "config"}, nil, []string{"Config", "ConfigStore", "loadConfig"}},
		{"types", files.SymbolQuery{Text: "config", TypesOnly: true}, nil, []string{"Config", "ConfigStore"}},
		{"scope", files.SymbolQuery{Text: "config", Dir: "web/../ignored/"}, nil, []string{"HiddenConfig"}},
		{"overlay", files.SymbolQuery{Text: "config", TypesOnly: true}, map[string]string{"./conf.go": "package conf\ntype OverlayConfig struct{}\n"}, []string{"ConfigStore", "OverlayConfig"}},
		{"dot", files.SymbolQuery{Text: "config", Dir: "."}, nil, []string{"Config", "ConfigStore", "loadConfig"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var out rpc.FileSymbolsResult
			if err := call("files.symbols", rpc.FileSymbolsParams{Root: root, Query: tc.q, Overlay: tc.ov}, &out); err != nil {
				t.Fatal(err)
			}
			var names []string
			for _, sym := range out.Symbols {
				names = append(names, sym.Name)
			}
			slices.Sort(names)
			if !slices.Equal(names, tc.want) || out.Truncated {
				t.Fatalf("symbols=%v truncated=%v", names, out.Truncated)
			}
		})
	}
	gitInit(t, filepath.Join(root, "nested"))
	fileSearchWrite(t, filepath.Join(root, "nested", "nested.go"), "package nested\ntype NestedConfig struct{}\n")
	for _, single := range []bool{false, true} {
		var out rpc.FileSymbolsResult
		if err := call("files.symbols", rpc.FileSymbolsParams{Root: root, SingleRepo: single, Query: files.SymbolQuery{Text: "NestedConfig"}}, &out); err != nil || len(out.Symbols) != map[bool]int{false: 1, true: 0}[single] {
			t.Fatalf("single=%v symbols=%+v err=%v", single, out, err)
		}
	}
	var body strings.Builder
	for i := range files.MaxSymbols + 1 {
		fmt.Fprintf(&body, "type ManyConfig%d struct{}\n", i)
	}
	fileSearchWrite(t, filepath.Join(root, "many.go"), body.String())
	var out rpc.FileSymbolsResult
	if err := call("files.symbols", rpc.FileSymbolsParams{Root: root, Query: files.SymbolQuery{Text: "ManyConfig"}}, &out); err != nil || len(out.Symbols) != files.MaxSymbols || !out.Truncated {
		t.Fatalf("symbol cap: %d %v %v", len(out.Symbols), out.Truncated, err)
	}
	if state := desktopTestCall(t, d.desktopStatus, nil); state.Owner != "" || len(d.terms) != 0 {
		t.Fatal("symbols acquired control or launched a terminal")
	}
}

func TestDesktopFileDefinition(t *testing.T) {
	d, call := fileSymbolsViewer(t)
	base := t.TempDir()
	root := filepath.Join(base, "workspace")
	for path, text := range map[string]string{
		"src/main.ts":      "import * as api from '../a/wrong';\napi.run();\n",
		"a/wrong.ts":       "export function run() {}\n",
		"z/expected.ts":    "export function run() {}\n",
		"a/OtherPicker.ts": "export class Wrong {\n  openAlbum() {}\n}\n",
		"z/MediaPicker.ts": "export class MediaPicker {\n  openAlbum() {}\n}\n",
		"self.ts":          "export function Own() {}\n",
		"tsconfig.json":    `{"compilerOptions":{"baseUrl":".","paths":{"@lib/*":["z/*"]}}}`,
	} {
		fileSearchWrite(t, filepath.Join(root, path), text)
	}
	query := func(path, text, needle string, overlay map[string]string) rpc.FileDefinitionResult {
		t.Helper()
		offset := strings.LastIndex(text, needle)
		if offset < 0 {
			t.Fatalf("missing test needle %q", needle)
		}
		var out rpc.FileDefinitionResult
		if err := call("files.definition", rpc.FileDefinitionParams{Root: root, Path: path, Text: text, Offset: offset + 1, Overlay: overlay}, &out); err != nil {
			t.Fatal(err)
		}
		return out
	}
	path := filepath.Join(root, "src/main.ts")
	for _, text := range []string{
		"import * as api from '../z/expected';\napi.run();\n",
		"import { run } from '../z/expected';\nrun();\n",
		"import { run } from '../z/expected';",
	} {
		out := query(path, text, "run", nil)
		if out.Path != "" || len(out.Definitions) < 2 || out.Definitions[0].Path != "z/expected.ts" || out.Definitions[0].Line != 0 {
			t.Fatalf("snapshot imports/prefer lost: %+v", out)
		}
	}
	out := query(path, "const picker = new MediaPicker();\npicker.openAlbum();\n", "openAlbum", nil)
	if len(out.Definitions) < 2 || out.Definitions[0].Path != "z/MediaPicker.ts" {
		t.Fatalf("type inference lost: %+v", out)
	}
	out = query(path, "run();", "run", map[string]string{"z/expected.ts": "export function renamed() {}\n"})
	if len(out.Definitions) != 1 || out.Definitions[0].Path != "a/wrong.ts" {
		t.Fatalf("overlay ignored: %+v", out)
	}
	out = query(filepath.Join(root, "self.ts"), "export function Own() {}\n", "Own", nil)
	if len(out.Definitions) != 1 || out.Definitions[0].Path != "self.ts" || out.Definitions[0].Line != 0 {
		t.Fatalf("service filtered current definition: %+v", out)
	}
	for _, tc := range []struct {
		text string
		line int
	}{
		{`import '@lib/expected';`, 1},
		{`// ../z/expected.ts:7`, 7},
	} {
		out := query(path, tc.text, "expected", nil)
		if out.Path != filepath.Join(root, "z/expected.ts") || out.Line != tc.line || len(out.Definitions) != 0 {
			t.Fatalf("path resolution: %+v", out)
		}
	}
	outside := filepath.Join(base, "outside.ts")
	out = query(outside, "import * as api from './workspace/z/expected';\napi.run();\n", "run", nil)
	if len(out.Definitions) < 2 || out.Definitions[0].Path != "z/expected.ts" {
		t.Fatalf("outside source rejected or import lost: %+v", out)
	}
	out = query(outside, "import './workspace/z/expected';", "expected", nil)
	if out.Path != filepath.Join(root, "z/expected.ts") || out.Line != 1 {
		t.Fatalf("outside relative path: %+v", out)
	}
	for _, tc := range []struct{ path, text, needle string }{
		{path, `"https://example.invalid/never-open"`, "example"},
		{path, `"./missing-file.ts"`, "missing"},
		{path, "// ordinary comment", "ordinary"},
		{filepath.Join(root, "notes.txt"), "run()", "run"},
	} {
		out := query(tc.path, tc.text, tc.needle, nil)
		if out.Path != "" || len(out.Definitions) != 0 {
			t.Fatalf("non-actionable source: %+v", out)
		}
	}
	if state := desktopTestCall(t, d.desktopStatus, nil); state.Owner != "" || len(d.terms) != 0 {
		t.Fatal("definition acquired control or launched a terminal")
	}
}

func TestFileSymbolsDefinitionValidation(t *testing.T) {
	d := &Daemon{}
	root := t.TempDir()
	path := filepath.Join(root, "source.ts")
	call := func(ctx context.Context, h rpc.Handler, p any) error {
		raw, err := json.Marshal(p)
		if err != nil {
			t.Fatal(err)
		}
		_, err = h(ctx, raw)
		return err
	}
	for _, invalid := range []string{"relative", "/nul\x00path"} {
		desktopErrorCode(t, call(context.Background(), d.filesSymbols, rpc.FileSymbolsParams{Root: invalid}), rpc.CodeBadRequest)
		desktopErrorCode(t, call(context.Background(), d.filesDefinition, rpc.FileDefinitionParams{Root: invalid, Path: path}), rpc.CodeBadRequest)
		desktopErrorCode(t, call(context.Background(), d.filesDefinition, rpc.FileDefinitionParams{Root: root, Path: invalid}), rpc.CodeBadRequest)
	}
	for _, dir := range []string{"/absolute", "../escape", "a/../../escape", "nul\x00dir"} {
		desktopErrorCode(t, call(context.Background(), d.filesSymbols, rpc.FileSymbolsParams{Root: root, Query: files.SymbolQuery{Dir: dir}}), rpc.CodeBadRequest)
	}
	oversized := make(map[string]string)
	for i := range fileSearchOverlayLimit + 1 {
		oversized[fmt.Sprint(i)] = ""
	}
	for _, overlay := range []map[string]string{{"../escape.ts": ""}, {"a": "", "./a": ""}, oversized} {
		desktopErrorCode(t, call(context.Background(), d.filesSymbols, rpc.FileSymbolsParams{Root: root, Overlay: overlay}), rpc.CodeBadRequest)
		desktopErrorCode(t, call(context.Background(), d.filesDefinition, rpc.FileDefinitionParams{Root: root, Path: path, Overlay: overlay}), rpc.CodeBadRequest)
	}
	for _, offset := range []int{-1, 2} {
		desktopErrorCode(t, call(context.Background(), d.filesDefinition, rpc.FileDefinitionParams{Root: root, Path: path, Text: "a", Offset: offset}), rpc.CodeBadRequest)
	}
	if err := call(context.Background(), d.filesDefinition, rpc.FileDefinitionParams{Root: root, Path: path, Text: " ", Offset: 1}); err != nil {
		t.Fatalf("end offset rejected: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	for _, h := range []rpc.Handler{d.filesSymbols, d.filesDefinition} {
		if err := call(ctx, h, rpc.FileDefinitionParams{Root: root, Path: path}); err != context.Canceled {
			t.Fatalf("cancel: %v", err)
		}
	}
	for _, value := range []any{
		rpc.FileSymbolsResult{Symbols: []files.Symbol{{Text: strings.Repeat("\x00", fileSearchReplyBytes/6)}}},
		rpc.FileDefinitionResult{Definitions: []files.Definition{{Text: strings.Repeat("x", fileSearchReplyBytes)}}},
	} {
		_, err := fileSearchReply(context.Background(), value)
		desktopErrorCode(t, err, rpc.CodeBadRequest)
	}
}

func TestFileDefinitionSourceLimit(t *testing.T) {
	root := t.TempDir()
	for _, size := range []int{files.MaxSize, files.MaxSize + 1} {
		text := strings.Repeat(" ", size-1) + "\n"
		raw, err := json.Marshal(rpc.FileDefinitionParams{Root: root, Path: filepath.Join(root, "large.ts"), Text: text, Offset: len(text)})
		if err != nil {
			t.Fatal(err)
		}
		_, err = (&Daemon{}).filesDefinition(context.Background(), raw)
		if size > files.MaxSize {
			desktopErrorCode(t, err, rpc.CodeBadRequest)
		} else if err != nil {
			t.Fatalf("read-only document rejected: %v", err)
		}
	}
}
