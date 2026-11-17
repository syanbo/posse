package client

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"posse/internal/files"
	"posse/internal/rpc"
)

func TestRemoteFileListAndSearch(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "local.txt"), []byte("local needle"), 0600); err != nil {
		t.Fatal(err)
	}
	listParams := rpc.FileListParams{Root: root, SingleRepo: true, All: true}
	searchParams := rpc.FileSearchParams{Root: root, SingleRepo: true, Query: files.Query{Text: "needle", MatchCase: true, Dir: "src"}, Overlay: map[string]string{"src/target.txt": "needle"}}
	c := fileTestClient(t, func(p rpc.DesktopCall) (any, error) {
		switch p.Method {
		case "files.list":
			var got rpc.FileListParams
			if err := json.Unmarshal(p.Params, &got); err != nil || got != listParams {
				t.Errorf("list params: %+v %v", got, err)
			}
			return &files.List{Root: root, Files: []string{"src/target.txt"}, Git: true}, nil
		case "files.search":
			var got rpc.FileSearchParams
			if err := json.Unmarshal(p.Params, &got); err != nil || !reflect.DeepEqual(got, searchParams) {
				t.Errorf("search params: %+v %v", got, err)
			}
			return rpc.FileSearchResult{Hits: []files.Hit{{Path: "src/target.txt", Text: "needle", Spans: [][2]int{{0, 6}}}}, Truncated: true}, nil
		default:
			t.Errorf("unexpected method %s", p.Method)
			return nil, nil
		}
	})
	l, err := c.ListFiles(context.Background(), listParams)
	if err != nil || l == nil || !reflect.DeepEqual(l.Files, []string{"src/target.txt"}) || l.Ignored(filepath.Join(root, "src"), true) {
		t.Fatalf("target list: %+v %v", l, err)
	}
	r, err := c.SearchFiles(context.Background(), searchParams)
	if err != nil || len(r.Hits) != 1 || r.Hits[0].Path != "src/target.txt" || !r.Truncated {
		t.Fatalf("target search: %+v %v", r, err)
	}
}

func TestRemoteFileSearchFailureAndCancel(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "local.txt"), []byte("needle"), 0600); err != nil {
		t.Fatal(err)
	}
	c := fileTestClient(t, func(p rpc.DesktopCall) (any, error) {
		return nil, rpc.Errorf(rpc.CodePermissionDenied, "denied")
	})
	l, err := c.ListFiles(context.Background(), rpc.FileListParams{Root: root})
	var re *rpc.Error
	if l != nil || !errors.As(err, &re) || re.Code != rpc.CodePermissionDenied {
		t.Fatalf("list failure: %+v %v", l, err)
	}
	r, err := c.SearchFiles(context.Background(), rpc.FileSearchParams{Root: root, Query: files.Query{Text: "needle"}})
	if len(r.Hits) != 0 || !errors.As(err, &re) || re.Code != rpc.CodePermissionDenied {
		t.Fatalf("search fell back to local: %+v %v", r, err)
	}
	entered, release := make(chan struct{}), make(chan struct{})
	defer close(release)
	c = fileTestClient(t, func(p rpc.DesktopCall) (any, error) {
		close(entered)
		<-release
		return rpc.FileSearchResult{}, nil
	})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() {
		_, err := c.SearchFiles(ctx, rpc.FileSearchParams{Root: root, Query: files.Query{Text: "needle"}})
		done <- err
	}()
	select {
	case <-entered:
	case <-time.After(3 * time.Second):
		t.Fatal("request did not arrive")
	}
	cancel()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("cancel: %v", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("cancel did not stop request")
	}
}
