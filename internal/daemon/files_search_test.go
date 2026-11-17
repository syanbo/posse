package daemon

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"posse/internal/files"
	"posse/internal/rpc"
)

func fileSearchWrite(t *testing.T, path, text string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(text), 0600); err != nil {
		t.Fatal(err)
	}
}

func TestDesktopFileListAndSearch(t *testing.T) {
	d, _ := newTestDaemon(t)
	base := t.TempDir()
	root, ext := filepath.Join(base, "workspace"), filepath.Join(base, "external")
	repo := filepath.Join(root, "repo")
	for _, dir := range []string{repo, ext} {
		gitInit(t, dir)
		fileSearchWrite(t, filepath.Join(dir, ".gitignore"), "ignored/\n*.log\n")
		fileSearchWrite(t, filepath.Join(dir, "src", "main.go"), "Alpha a.b\nalpha axb\n")
		fileSearchWrite(t, filepath.Join(dir, "ignored", "hidden.txt"), "secret\n")
		fileSearchWrite(t, filepath.Join(dir, "debug.log"), "ignored log\n")
	}
	gitInit(t, filepath.Join(repo, "nested"))
	fileSearchWrite(t, filepath.Join(repo, "nested", "child.txt"), "nested\n")
	fileSearchWrite(t, filepath.Join(root, "notes.txt"), "notes\n")
	for _, alias := range []string{"link", "alias"} {
		if err := os.Symlink(ext, filepath.Join(root, alias)); err != nil {
			t.Fatal(err)
		}
	}
	socket := desktopServer(t, d)
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	query := func(method string, p, out any) error {
		raw, err := json.Marshal(p)
		if err != nil {
			t.Fatal(err)
		}
		return rpc.Call(ctx, socket, "desktop.call", rpc.DesktopCall{
			Desktop: rpc.DesktopIdentity{InstanceID: d.instanceID, ClientID: "viewer"}, Method: method, Params: raw,
		}, out)
	}
	var list files.List
	if err := query("files.list", rpc.FileListParams{Root: root}, &list); err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{"repo/src/main.go", "repo/nested/child.txt", "alias/src/main.go", "link/src/main.go", "notes.txt"} {
		if !slices.Contains(list.Files, path) {
			t.Errorf("missing %s in %v", path, list.Files)
		}
	}
	if !list.Git || list.Truncated || !list.Ignored(filepath.Join(root, "alias/debug.log"), false) || list.Ignored(filepath.Join(root, "repo/src"), true) || list.Ignored(filepath.Join(root, "unlisted.txt"), false) {
		t.Fatalf("lost ignore metadata: %+v", list)
	}
	if err := query("files.list", rpc.FileListParams{Root: repo, SingleRepo: true}, &list); err != nil {
		t.Fatal(err)
	}
	if slices.Contains(list.Files, "nested/child.txt") || !list.Ignored(filepath.Join(repo, "debug.log"), false) {
		t.Fatalf("single repo: %v", list.Files)
	}
	if err := query("files.list", rpc.FileListParams{Root: repo}, &list); err != nil || !slices.Contains(list.Files, "nested/child.txt") || !list.Ignored(filepath.Join(repo, "debug.log"), false) {
		t.Fatalf("nested repo lost outer ignore rules: %+v %v", list, err)
	}
	if err := query("files.list", rpc.FileListParams{Root: repo, All: true}, &list); err != nil || !slices.Contains(list.Files, "ignored/hidden.txt") || list.Git {
		t.Fatalf("all: %+v %v", list, err)
	}

	for _, tc := range []struct {
		name string
		q    files.Query
		ov   map[string]string
		want int
	}{
		{"literal", files.Query{Text: "a.b", Dir: "src"}, nil, 1},
		{"regex", files.Query{Text: "a.b", Regex: true, Dir: "src"}, nil, 2},
		{"case", files.Query{Text: "Alpha", MatchCase: true, Dir: "src"}, nil, 1},
		{"fold", files.Query{Text: "Alpha", Dir: "src"}, nil, 2},
		{"scope", files.Query{Text: "secret", Dir: "src/../ignored/"}, nil, 1},
		{"ignored", files.Query{Text: "secret"}, nil, 0},
		{"overlay", files.Query{Text: "unsaved", Dir: "src"}, map[string]string{"./src/main.go": "unsaved"}, 1},
		{"overlay-replaces", files.Query{Text: "Alpha", Dir: "src"}, map[string]string{"src/main.go": "unsaved"}, 0},
		{"overlay-not-list", files.Query{Text: "unsaved"}, map[string]string{"forged.txt": "unsaved"}, 0},
		{"glob", files.Query{Text: "Alpha", Include: "*.txt", Exclude: "nested"}, nil, 0},
		{"dot-scope", files.Query{Text: "Alpha", Dir: "."}, nil, 2},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var result rpc.FileSearchResult
			err := query("files.search", rpc.FileSearchParams{Root: repo, Query: tc.q, Overlay: tc.ov}, &result)
			if err != nil || len(result.Hits) != tc.want || result.Truncated {
				t.Fatalf("hits=%+v truncated=%v err=%v", result.Hits, result.Truncated, err)
			}
		})
	}
	var result rpc.FileSearchResult
	if err := query("files.search", rpc.FileSearchParams{Root: root, Query: files.Query{Text: "Alpha", Dir: "alias/src"}}, &result); err != nil || len(result.Hits) != 2 || result.Hits[0].Path != "alias/src/main.go" {
		t.Fatalf("symlink scope: %+v %v", result, err)
	}
	fileSearchWrite(t, filepath.Join(repo, "many.txt"), strings.Repeat("needle\n", files.MaxHits+1))
	if err := query("files.search", rpc.FileSearchParams{Root: repo, Query: files.Query{Text: "needle"}}, &result); err != nil || len(result.Hits) != files.MaxHits || !result.Truncated {
		t.Fatalf("max hits: %d %v %v", len(result.Hits), result.Truncated, err)
	}
	if state := desktopTestCall(t, d.desktopStatus, nil); state.Owner != "" || len(d.terms) != 0 {
		t.Fatal("file reads acquired control or launched a terminal")
	}
	for _, method := range []string{"files.list", "files.search"} {
		desktopErrorCode(t, query(method, map[string]any{"root": filepath.Join(base, "missing")}, nil), rpc.CodeNotFound)
	}
	t.Run("unreadable-root", func(t *testing.T) {
		if os.Geteuid() == 0 {
			t.Skip("root bypasses directory permissions")
		}
		denied := filepath.Join(base, "denied")
		if err := os.Mkdir(denied, 0000); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { os.Chmod(denied, 0700) })
		for _, method := range []string{"files.list", "files.search"} {
			desktopErrorCode(t, query(method, map[string]any{"root": denied}, nil), rpc.CodePermissionDenied)
		}
	})
}

func TestFileSearchValidationAndCancellation(t *testing.T) {
	d := &Daemon{}
	root := t.TempDir()
	call := func(ctx context.Context, h rpc.Handler, p any) error {
		raw, err := json.Marshal(p)
		if err != nil {
			t.Fatal(err)
		}
		_, err = h(ctx, raw)
		return err
	}
	for _, path := range []string{"relative", "/bad\x00root"} {
		desktopErrorCode(t, call(context.Background(), d.filesList, rpc.FileListParams{Root: path}), rpc.CodeBadRequest)
		desktopErrorCode(t, call(context.Background(), d.filesSearch, rpc.FileSearchParams{Root: path}), rpc.CodeBadRequest)
	}
	for _, path := range []string{"/absolute", "../escape", "a/../../escape", "..", "nul\x00path"} {
		desktopErrorCode(t, call(context.Background(), d.filesSearch, rpc.FileSearchParams{Root: root, Query: files.Query{Dir: path}}), rpc.CodeBadRequest)
		desktopErrorCode(t, call(context.Background(), d.filesSearch, rpc.FileSearchParams{Root: root, Overlay: map[string]string{path: ""}}), rpc.CodeBadRequest)
	}
	overCount := make(map[string]string)
	for i := range fileSearchOverlayLimit + 1 {
		overCount[fmt.Sprint(i)] = ""
	}
	for _, overlay := range []map[string]string{
		{".": ""}, {"a": "", "./a": ""}, overCount,
		{"a": strings.Repeat("x", files.ReadOnlySize+1)},
		{"a": strings.Repeat("x", 4<<20), "b": strings.Repeat("x", (4<<20)+1)},
	} {
		desktopErrorCode(t, call(context.Background(), d.filesSearch, rpc.FileSearchParams{Root: root, Overlay: overlay}), rpc.CodeBadRequest)
	}
	desktopErrorCode(t, call(context.Background(), d.filesSearch, rpc.FileSearchParams{Root: root, Query: files.Query{Text: "[", Regex: true}}), rpc.CodeBadRequest)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	for _, h := range []rpc.Handler{d.filesList, d.filesSearch} {
		if err := call(ctx, h, rpc.FileListParams{Root: root}); err != context.Canceled {
			t.Fatalf("cancel: %v", err)
		}
	}
}

func TestFileSearchReplyBudget(t *testing.T) {
	ctx := context.Background()
	// JSON 转义后的长度才是线上的预算；原文不到上限也可能超限。
	for _, value := range []any{
		&files.List{Root: "/target", Files: []string{strings.Repeat("\x00", fileSearchReplyBytes/6)}},
		rpc.FileSearchResult{Hits: []files.Hit{{Path: "x", Text: strings.Repeat("x", fileSearchReplyBytes)}}},
	} {
		_, err := fileSearchReply(ctx, value)
		desktopErrorCode(t, err, rpc.CodeBadRequest)
	}
	data, err := fileSearchReply(ctx, &files.List{Root: "/target", Files: []string{"a"}})
	if err != nil || !json.Valid(data) {
		t.Fatalf("small reply: %s %v", data, err)
	}
	// 一条很长的命中行也能超限，确认处理器没有把它回成空结果或悄悄截断。
	root := t.TempDir()
	fileSearchWrite(t, filepath.Join(root, "large.txt"), strings.Repeat("x", (4<<20)+1))
	raw, _ := json.Marshal(rpc.FileSearchParams{Root: root, Query: files.Query{Text: "x+", Regex: true}})
	_, err = (&Daemon{}).filesSearch(ctx, raw)
	desktopErrorCode(t, err, rpc.CodeBadRequest)
}
