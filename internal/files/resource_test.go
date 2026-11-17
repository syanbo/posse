package files

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"os"
	"path/filepath"
	"syscall"
	"testing"
)

func TestReadResourceVersionRawBytes(t *testing.T) {
	for _, data := range [][]byte{nil, []byte("first\r\nsecond\r\n"), bytes.Repeat([]byte{0, 0xff, '\r', '\n', 0xfe}, RangeLimit/5+10)} {
		root := t.TempDir()
		path := filepath.Join(root, "file")
		write(t, path, string(data))
		if err := os.Symlink(path, filepath.Join(root, "absolute")); err != nil {
			t.Fatal(err)
		}
		v, err := ReadResourceVersion(context.Background(), root, "absolute")
		real, pathErr := filepath.EvalSymlinks(path)
		sum := sha256.Sum256(data)
		if err != nil || pathErr != nil || v.Path != real || !v.Stamp.Same(StatFile(path)) || v.Hash != hex.EncodeToString(sum[:]) {
			t.Fatalf("raw bytes=%d: %+v err=%v", len(data), v, err)
		}
	}
}

func TestReadResourceVersionCanceled(t *testing.T) {
	root := t.TempDir()
	write(t, filepath.Join(root, "file"), string(make([]byte, 64<<10)))
	for _, at := range []int{1, 2, 3, 4, 5, 6, 7} {
		base, cancel := context.WithCancel(context.Background())
		ctx := &rangeStepContext{Context: base, at: at, do: cancel}
		v, err := ReadResourceVersion(ctx, root, "file")
		cancel()
		if !errors.Is(err, context.Canceled) || v != (Version{}) {
			t.Fatalf("step=%d version=%+v err=%v", at, v, err)
		}
	}
}

func TestReadResourceVersionRootBoundary(t *testing.T) {
	for _, at := range []int{0, 2, 4} {
		for _, change := range []string{"link", "root"} {
			root, outside := t.TempDir(), t.TempDir()
			write(t, filepath.Join(root, "file"), string(make([]byte, 64<<10)))
			write(t, filepath.Join(outside, "file"), "outside")
			link := filepath.Join(root, "link")
			if err := os.Symlink("file", link); err != nil {
				t.Fatal(err)
			}
			changePath := func() {
				if change == "link" {
					if err := os.Remove(link); err != nil {
						t.Fatal(err)
					}
					if err := os.Symlink(filepath.Join(outside, "file"), link); err != nil {
						t.Fatal(err)
					}
				} else {
					if err := os.Rename(root, root+"-moved"); err != nil {
						t.Fatal(err)
					}
					t.Cleanup(func() { os.RemoveAll(root + "-moved") })
					if err := os.Symlink(outside, root); err != nil {
						t.Fatal(err)
					}
				}
			}
			if at == 0 && change == "root" {
				continue
			}
			if at == 0 {
				changePath()
			}
			ctx := &rangeStepContext{Context: context.Background(), at: at, do: changePath}
			v, err := ReadResourceVersion(ctx, root, "link")
			if err == nil || v != (Version{}) {
				t.Fatalf("step=%d change=%s version=%+v err=%v", at, change, v, err)
			}
		}
	}
}

func TestReadResourceVersionSizeLimit(t *testing.T) {
	for _, growDuringRead := range []bool{false, true} {
		root := t.TempDir()
		path := filepath.Join(root, "file")
		write(t, path, "small")
		grow := func() {
			if err := os.Truncate(path, MaxSize+1); err != nil {
				t.Fatal(err)
			}
		}
		var ctx context.Context = context.Background()
		if growDuringRead {
			ctx = &rangeStepContext{Context: ctx, at: 3, do: grow}
		} else {
			grow()
		}
		v, err := ReadResourceVersion(ctx, root, "file")
		if err == nil || v != (Version{}) {
			t.Fatalf("oversized: version=%+v err=%v", v, err)
		}
	}
}

func TestReadResourceRangeBytes(t *testing.T) {
	root := t.TempDir()
	data := bytes.Repeat([]byte{0, 0xff, '\r', '\n', 0xfe}, RangeLimit/5+10)
	write(t, filepath.Join(root, "binary"), string(data))
	first, err := ReadResourceRange(context.Background(), root, "binary", 0, RangeLimit, nil)
	if err != nil || first.EOF || !bytes.Equal(first.Data, data[:RangeLimit]) {
		t.Fatalf("first: bytes=%d eof=%v error=%v", len(first.Data), first.EOF, err)
	}
	last, err := ReadResourceRange(context.Background(), root, "binary", RangeLimit, RangeLimit, &first.Stamp)
	if err != nil || !last.EOF || !bytes.Equal(last.Data, data[RangeLimit:]) {
		t.Fatalf("last: bytes=%d eof=%v error=%v", len(last.Data), last.EOF, err)
	}
	write(t, filepath.Join(root, "empty"), "")
	for _, offset := range []int64{0, 5, 1<<63 - 1} {
		r, err := ReadResourceRange(context.Background(), root, "empty", offset, 1, nil)
		if err != nil || !r.EOF || len(r.Data) != 0 || r.Stamp.Size != 0 || r.Stamp.Missing {
			t.Fatalf("empty: %+v, %v", r, err)
		}
	}
}

func TestReadResourceRangePaths(t *testing.T) {
	root, outside := t.TempDir(), t.TempDir()
	write(t, filepath.Join(root, "dir", "nested", "file"), "nested")
	write(t, filepath.Join(root, "dir", "file"), "inside")
	write(t, filepath.Join(outside, "file"), "outside")
	for name, target := range map[string]string{
		"relative": "dir/file", "absolute": filepath.Join(root, "dir", "file"),
		"relative-dir": "dir/nested", "absolute-dir": filepath.Join(root, "dir", "nested"),
		"outside": outside, "outside-file": filepath.Join(outside, "file"),
		"outside-relative": "../" + filepath.Base(outside),
	} {
		if err := os.Symlink(target, filepath.Join(root, name)); err != nil {
			t.Fatal(err)
		}
	}
	for _, path := range []string{"dir/file", "relative", "absolute", "dir/nested/../file", "relative-dir/../file", "absolute-dir/../file"} {
		r, err := ReadResourceRange(context.Background(), root, path, 0, 20, nil)
		if err != nil || string(r.Data) != "inside" || !r.EOF {
			t.Errorf("%q: %+v, %v", path, r, err)
		}
	}
	for _, path := range []string{"", "/absolute", "dir/\x00file", "../escape", "dir/../../escape", "outside/file", "outside-file", "outside-relative/file", "outside/../file"} {
		r, err := ReadResourceRange(context.Background(), root, path, 0, 20, nil)
		assertRangeError(t, r, err, ErrResourcePath)
	}
	linkRoot := filepath.Join(t.TempDir(), "root")
	if err := os.Symlink(root, linkRoot); err != nil {
		t.Fatal(err)
	}
	r, err := ReadResourceRange(context.Background(), linkRoot, "absolute", 0, 20, nil)
	if err != nil || string(r.Data) != "inside" {
		t.Fatalf("linked root: %+v, %v", r, err)
	}
	// 显式选择的图片可用 / 作为根，仍走同一份目标读取。
	r, err = ReadResourceRange(context.Background(), "/", filepath.Join(root, "absolute")[1:], 0, 20, nil)
	if err != nil || string(r.Data) != "inside" {
		t.Fatalf("filesystem root: %+v, %v", r, err)
	}
}

func TestReadResourceRangeInvalidAndTypes(t *testing.T) {
	root := t.TempDir()
	write(t, filepath.Join(root, "file"), "data")
	for _, tc := range []struct {
		offset int64
		limit  int
	}{{-1, 1}, {0, 0}, {0, -1}, {0, RangeLimit + 1}} {
		r, err := ReadResourceRange(context.Background(), root, "file", tc.offset, tc.limit, nil)
		assertRangeError(t, r, err, nil)
	}
	for _, root := range []string{"relative", "/nul\x00root"} {
		r, err := ReadResourceRange(context.Background(), root, "file", 0, 1, nil)
		assertRangeError(t, r, err, nil)
	}
	if err := syscall.Mkfifo(filepath.Join(root, "fifo"), 0600); err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{".", "fifo", "missing", "file/child"} {
		r, err := ReadResourceRange(context.Background(), root, path, 0, 1, nil)
		assertRangeError(t, r, err, nil)
	}
	st := StatFile(filepath.Join(root, "file"))
	st.Mode ^= 0100
	r, err := ReadResourceRange(context.Background(), root, "file", 0, 1, &st)
	assertRangeError(t, r, err, ErrChanged)
}

func TestReadResourceRangeChanged(t *testing.T) {
	for _, change := range []string{"write", "replace", "remove", "chmod", "link", "root", "fifo-before-open", "outside-before-open"} {
		t.Run(change, func(t *testing.T) {
			parent := t.TempDir()
			root := filepath.Join(parent, "root")
			path := filepath.Join(root, "file")
			write(t, path, "old")
			write(t, filepath.Join(root, "other"), "new")
			if err := os.Symlink("file", filepath.Join(root, "link")); err != nil {
				t.Fatal(err)
			}
			st := StatFile(path)
			at := 3
			if change == "fifo-before-open" || change == "outside-before-open" {
				at = 2
			}
			ctx := &rangeStepContext{Context: context.Background(), at: at, do: func() {
				var err error
				switch change {
				case "write":
					write(t, path, "longer contents")
				case "replace":
					err = os.Rename(filepath.Join(root, "other"), path)
				case "remove":
					err = os.Remove(path)
				case "chmod":
					err = os.Chmod(path, 0400)
				case "link":
					if err = os.Remove(filepath.Join(root, "link")); err == nil {
						err = os.Symlink("other", filepath.Join(root, "link"))
					}
				case "root":
					if err = os.Rename(root, root+"-old"); err == nil {
						write(t, path, "replacement")
					}
				case "fifo-before-open":
					if err = os.Remove(path); err == nil {
						err = syscall.Mkfifo(path, 0600)
					}
				case "outside-before-open":
					outside := filepath.Join(parent, "outside")
					write(t, outside, "secret")
					if err = os.Remove(path); err == nil {
						err = os.Symlink(outside, path)
					}
				}
				if err != nil {
					t.Fatal(err)
				}
			}}
			r, err := ReadResourceRange(ctx, root, "link", 0, 2, &st)
			assertRangeError(t, r, err, nil)
		})
	}
}

func TestReadResourceRangeCanceled(t *testing.T) {
	root := t.TempDir()
	write(t, filepath.Join(root, "file"), "data")
	for _, at := range []int{1, 2, 3, 4} {
		base, cancel := context.WithCancel(context.Background())
		ctx := &rangeStepContext{Context: base, at: at, do: cancel}
		r, err := ReadResourceRange(ctx, root, "file", 0, 2, nil)
		cancel()
		assertRangeError(t, r, err, context.Canceled)
	}
}
