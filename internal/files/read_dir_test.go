package files

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

type listingDirEntry struct {
	name   string
	onType func()
}

func (e listingDirEntry) Name() string { return e.name }
func (e listingDirEntry) IsDir() bool  { return false }
func (e listingDirEntry) Type() os.FileMode {
	if e.onType != nil {
		e.onType()
	}
	return 0
}
func (e listingDirEntry) Info() (os.FileInfo, error) { panic("unexpected Info") }

func TestReadDirBudgetStopsDuringEnumeration(t *testing.T) {
	calls, visited := 0, 0
	read := func(n int) ([]os.DirEntry, error) {
		calls++
		if n <= 0 || n > 128 {
			t.Fatalf("unbounded read: %d", n)
		}
		batch := make([]os.DirEntry, n)
		for i := range batch {
			batch[i] = listingDirEntry{name: "x", onType: func() { visited++ }}
		}
		return batch, nil
	}
	entries, used, err := readDirEntries(context.Background(), "", 128*70, read)
	if !errors.Is(err, ErrDirectoryLimit) || entries != nil || used != 0 {
		t.Fatalf("partial result: %v, %d, %v", entries, used, err)
	}
	if calls != 2 || visited != 128 {
		t.Fatalf("read %d batches, visited %d entries", calls, visited)
	}
}

func TestReadDirCancellationStopsEnumeration(t *testing.T) {
	for _, stage := range []string{"before", "batch", "entry", "read-error"} {
		t.Run(stage, func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			if stage == "before" {
				cancel()
			}
			calls, visited := 0, 0
			read := func(n int) ([]os.DirEntry, error) {
				calls++
				if calls > 2 {
					t.Fatal("read continued after cancellation")
				}
				if calls == 2 && stage != "entry" {
					cancel()
					if stage == "read-error" {
						return nil, os.ErrNotExist
					}
					return nil, io.EOF
				}
				batch := make([]os.DirEntry, n)
				for i := range batch {
					batch[i] = listingDirEntry{name: "x", onType: func() {
						visited++
						if stage == "entry" && visited == 3 {
							cancel()
						}
					}}
				}
				return batch, nil
			}
			entries, used, err := readDirEntries(ctx, "", 4<<20, read)
			if !errors.Is(err, context.Canceled) || entries != nil || used != 0 {
				t.Fatalf("cancellation lost: %v, %d, %v", entries, used, err)
			}
			if stage == "before" && calls != 0 {
				t.Fatal("read with canceled context")
			}
			if stage == "entry" && (calls != 1 || visited != 3) {
				t.Fatalf("continued: %d, %d", calls, visited)
			}
		})
	}
}

func TestReadDirBudgetBoundaries(t *testing.T) {
	for _, tc := range []struct {
		name    string
		budget  int
		wantErr error
	}{
		{"exact", 70, nil}, {"short", 69, ErrDirectoryLimit}, {"zero", 0, ErrDirectoryLimit}, {"unlimited", -1, nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			calls := 0
			entries, used, err := readDirEntries(context.Background(), "", tc.budget, func(int) ([]os.DirEntry, error) {
				calls++
				return []os.DirEntry{listingDirEntry{name: ".git"}, listingDirEntry{name: ".DS_Store"}, listingDirEntry{name: "x"}}, io.EOF
			})
			if !errors.Is(err, tc.wantErr) {
				t.Fatalf("error: %v", err)
			}
			if err == nil && (len(entries) != 1 || used != 70) {
				t.Fatalf("entries=%v used=%d", entries, used)
			}
			if err != nil && (entries != nil || used != 0) {
				t.Fatal("partial result")
			}
			if tc.budget == 0 && calls != 0 {
				t.Fatal("read with no budget")
			}
		})
	}
}

func TestReadDirContextMatchesLocal(t *testing.T) {
	root := t.TempDir()
	for _, name := range []string{".git", "z-dir"} {
		if err := os.Mkdir(filepath.Join(root, name), 0700); err != nil {
			t.Fatal(err)
		}
	}
	want := []Entry{{Name: "z-dir", Dir: true}}
	for i := range 260 {
		name := fmt.Sprintf("file-%03d", i)
		if err := os.WriteFile(filepath.Join(root, name), nil, 0600); err != nil {
			t.Fatal(err)
		}
		want = append(want, Entry{Name: name})
	}
	for _, name := range []string{".DS_Store", ".visible", "Upper"} {
		if err := os.WriteFile(filepath.Join(root, name), nil, 0600); err != nil {
			t.Fatal(err)
		}
		if name != ".DS_Store" {
			want = append(want, Entry{Name: name})
		}
	}
	for _, entry := range []Entry{{Name: "dir-link", Dir: true, Link: "z-dir"}, {Name: "file-link", Link: "file-000"}, {Name: "broken", Link: "missing"}} {
		if err := os.Symlink(entry.Link, filepath.Join(root, entry.Name)); err != nil {
			t.Fatal(err)
		}
		want = append(want, entry)
	}
	SortEntries(want)
	local, stamp, err := ReadDir(root)
	if err != nil {
		t.Fatal(err)
	}
	entries, gotStamp, used, err := ReadDirContext(context.Background(), root, 4<<20)
	if err != nil || !reflect.DeepEqual(entries, local) || !reflect.DeepEqual(entries, want) || gotStamp != stamp {
		t.Fatalf("directory semantics changed: err=%v entries=%v stamp=%+v", err, entries, gotStamp)
	}
	wantUsed := 0
	for _, e := range want {
		wantUsed += 6*(len(e.Name)+len(e.Link)) + 64
	}
	if used != wantUsed {
		t.Fatalf("used=%d, want=%d", used, wantUsed)
	}
	if _, _, exact, err := ReadDirContext(context.Background(), root, used); err != nil || exact != used {
		t.Fatalf("exact budget: %d %v", exact, err)
	}
	if es, _, n, err := ReadDirContext(context.Background(), root, used-1); es != nil || n != 0 || !errors.Is(err, ErrDirectoryLimit) {
		t.Fatalf("short budget: %v %d %v", es, n, err)
	}
}

func TestReadDirContextLinkBudget(t *testing.T) {
	root := t.TempDir()
	if err := os.Symlink("a-long-missing-target", filepath.Join(root, "x")); err != nil {
		t.Fatal(err)
	}
	entries, _, used, err := ReadDirContext(context.Background(), root, 70)
	if entries != nil || used != 0 || !errors.Is(err, ErrDirectoryLimit) {
		t.Fatalf("link omitted from budget: %v %d %v", entries, used, err)
	}
}
