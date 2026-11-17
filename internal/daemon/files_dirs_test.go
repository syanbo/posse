package daemon

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"posse/internal/files"
	"posse/internal/rpc"
)

func TestReadDirectoriesSharedBudget(t *testing.T) {
	calls := 0
	out, err := readDirectories(context.Background(), []string{"first", "large", "last", "no-budget"}, 100,
		func(ctx context.Context, path string, budget int) ([]files.Entry, files.Stamp, int, error) {
			calls++
			want := 100
			if calls > 1 {
				want = 40
			}
			if budget != want {
				t.Fatalf("%s budget=%d, want=%d", path, budget, want)
			}
			if path == "large" {
				return nil, files.Stamp{}, 0, files.ErrDirectoryLimit
			}
			used := 60
			if path == "last" {
				used = 40
			}
			return []files.Entry{{Name: path}}, files.Stamp{}, used, nil
		})
	if err != nil || len(out) != 4 || calls != 3 {
		t.Fatalf("out=%v calls=%d err=%v", out, calls, err)
	}
	for _, i := range []int{1, 3} {
		if out[i].Error == nil || out[i].Error.Code != rpc.CodeBadRequest || out[i].Error.Message != files.ErrDirectoryLimit.Error() || out[i].Entries != nil {
			t.Fatalf("limit result: %+v", out[i])
		}
	}
	if out[0].Error != nil || out[2].Error != nil {
		t.Fatalf("successful reads lost: %v", out)
	}
}

func TestReadDirectoriesCancellationDiscardsBatch(t *testing.T) {
	for _, stage := range []string{"success", "missing", "canceled", "deadline"} {
		t.Run(stage, func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			calls := 0
			wantErr := error(context.Canceled)
			if stage == "deadline" {
				wantErr = context.DeadlineExceeded
			}
			out, err := readDirectories(ctx, []string{"first", "second", "third"}, 1000,
				func(context.Context, string, int) ([]files.Entry, files.Stamp, int, error) {
					calls++
					if calls == 1 {
						return []files.Entry{{Name: "done"}}, files.Stamp{}, 88, nil
					}
					if stage == "canceled" || stage == "deadline" {
						return nil, files.Stamp{}, 0, fmt.Errorf("read: %w", wantErr)
					}
					cancel()
					if stage == "missing" {
						return nil, files.Stamp{}, 0, os.ErrNotExist
					}
					return []files.Entry{{Name: "ignored"}}, files.Stamp{}, 106, nil
				})
			if !errors.Is(err, wantErr) || out != nil || calls != 2 {
				t.Fatalf("out=%v calls=%d err=%v", out, calls, err)
			}
		})
	}
}

func TestFilesReadDirsBudgetAndCancellation(t *testing.T) {
	root := t.TempDir()
	for i := range 80 {
		name := fmt.Sprintf("%03d-%s", i, strings.Repeat("x", 200))
		if err := os.WriteFile(filepath.Join(root, name), nil, 0600); err != nil {
			t.Fatal(err)
		}
	}
	_, _, cost, err := files.ReadDirContext(context.Background(), root, 4<<20)
	if err != nil {
		t.Fatal(err)
	}
	count := (4<<20)/cost + 2
	paths := make([]string, count)
	for i := range paths {
		paths[i] = root
	}
	raw, err := json.Marshal(rpc.FilePathsParams{Paths: paths})
	if err != nil {
		t.Fatal(err)
	}
	d := &Daemon{}
	value, err := d.filesReadDirs(context.Background(), raw)
	if err != nil {
		t.Fatal(err)
	}
	out := value.([]rpc.FileDirectoryResult)
	if len(out) != count {
		t.Fatalf("results=%d", len(out))
	}
	for i, result := range out {
		if i < count-2 {
			if result.Error != nil || len(result.Entries) != 80 {
				t.Fatalf("directory %d: %+v", i, result)
			}
		} else if result.Error == nil || result.Error.Code != rpc.CodeBadRequest || result.Entries != nil {
			t.Fatalf("directory %d escaped shared limit: %+v", i, result)
		}
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if result, err := d.filesReadDirs(ctx, raw); result != nil || !errors.Is(err, context.Canceled) {
		t.Fatalf("cancel: %v %v", result, err)
	}
}
