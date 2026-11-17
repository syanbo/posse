package files

import (
	"bytes"
	"context"
	"errors"
	"math"
	"os"
	"path/filepath"
	"syscall"
	"testing"
)

func TestStat(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "file")
	write(t, path, "bytes")
	stamp, err := Stat(path)
	if err != nil || stamp.Missing || !stamp.Same(StatFile(path)) {
		t.Fatalf("regular file: %+v, %v", stamp, err)
	}
	missing, err := Stat(filepath.Join(dir, "missing"))
	if err != nil || missing != (Stamp{Missing: true}) {
		t.Fatalf("missing file: %+v, %v", missing, err)
	}
	bad, err := Stat(filepath.Join(path, "child"))
	if !errors.Is(err, syscall.ENOTDIR) || bad.Missing {
		t.Fatalf("not a directory: %+v, %v", bad, err)
	}
	link := filepath.Join(dir, "link")
	if err := os.Symlink(path, link); err != nil {
		t.Fatal(err)
	}
	linked, err := Stat(link)
	if err != nil || !linked.Same(stamp) {
		t.Fatalf("symlink: %+v, %v", linked, err)
	}
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	missing, err = Stat(link)
	if err != nil || !missing.Missing {
		t.Fatalf("dangling symlink: %+v, %v", missing, err)
	}
}

func TestStatPermissionError(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root bypasses directory permissions")
	}
	dir := filepath.Join(t.TempDir(), "private")
	path := filepath.Join(dir, "file")
	write(t, path, "private")
	if err := os.Chmod(dir, 0); err != nil {
		t.Fatal(err)
	}
	defer os.Chmod(dir, 0o700)
	stamp, err := Stat(path)
	if !os.IsPermission(err) || stamp.Missing {
		t.Fatalf("permission error: %+v, %v", stamp, err)
	}
}

func TestReadRangeBytesAndBoundaries(t *testing.T) {
	path := filepath.Join(t.TempDir(), "bytes")
	data := []byte{'a', 0, 0xff, 0xfe, '\r', '\n', 0xe4, 0xb8, 0xad}
	write(t, path, string(data))
	stamp := StatFile(path)
	for _, tc := range []struct {
		name   string
		offset int64
		limit  int
		want   []byte
		eof    bool
	}{
		{"first", 0, 3, data[:3], false},
		{"middle", 3, 3, data[3:6], false},
		{"exact end", 6, 3, data[6:], true},
		{"short end", 7, 5, data[7:], true},
		{"at end", int64(len(data)), 1, nil, true},
		{"past end", int64(len(data) + 1), 1, nil, true},
		{"maximum offset", math.MaxInt64, 1, nil, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := ReadRange(context.Background(), path, tc.offset, tc.limit, &stamp)
			if err != nil || !bytes.Equal(got.Data, tc.want) || got.EOF != tc.eof || !got.Stamp.Same(stamp) {
				t.Fatalf("range: %+v, %v; want %x, EOF=%v", got, err, tc.want, tc.eof)
			}
		})
	}
	link := filepath.Join(filepath.Dir(path), "link")
	if err := os.Symlink(path, link); err != nil {
		t.Fatal(err)
	}
	got, err := ReadRange(context.Background(), link, 0, len(data), &stamp)
	if err != nil || !bytes.Equal(got.Data, data) || !got.EOF || !got.Stamp.Same(stamp) {
		t.Fatalf("symlink range: %+v, %v", got, err)
	}
	write(t, path, "")
	got, err = ReadRange(context.Background(), path, 0, 1, nil)
	if err != nil || len(got.Data) != 0 || !got.EOF || got.Stamp.Size != 0 || got.Stamp.Missing {
		t.Fatalf("empty file: %+v, %v", got, err)
	}
}

func TestReadRangeLimit(t *testing.T) {
	path := filepath.Join(t.TempDir(), "large")
	data := bytes.Repeat([]byte{0xff, 0, 1}, RangeLimit/3+1)
	write(t, path, string(data))
	first, err := ReadRange(context.Background(), path, 0, RangeLimit, nil)
	if err != nil || len(first.Data) != RangeLimit || first.EOF || !bytes.Equal(first.Data, data[:RangeLimit]) {
		t.Fatalf("first chunk: len=%d, EOF=%v, err=%v", len(first.Data), first.EOF, err)
	}
	last, err := ReadRange(context.Background(), path, RangeLimit, RangeLimit, &first.Stamp)
	if err != nil || !last.EOF || !bytes.Equal(last.Data, data[RangeLimit:]) {
		t.Fatalf("last chunk: %+v, %v", last, err)
	}
}

func TestReadRangeInvalid(t *testing.T) {
	path := filepath.Join(t.TempDir(), "file")
	write(t, path, "data")
	for _, tc := range []struct {
		offset int64
		limit  int
	}{{-1, 1}, {0, 0}, {0, -1}, {0, RangeLimit + 1}} {
		got, err := ReadRange(context.Background(), path, tc.offset, tc.limit, nil)
		assertRangeError(t, got, err, nil)
	}
	got, err := ReadRange(context.Background(), filepath.Join(filepath.Dir(path), "missing"), 0, 1, nil)
	assertRangeError(t, got, err, os.ErrNotExist)
	got, err = ReadRange(context.Background(), filepath.Join(path, "child"), 0, 1, nil)
	assertRangeError(t, got, err, syscall.ENOTDIR)
}

func TestReadRangeExpectedStamp(t *testing.T) {
	path := filepath.Join(t.TempDir(), "file")
	write(t, path, "old")
	before := StatFile(path)
	write(t, path, "new version")
	for _, expected := range []Stamp{before, {Missing: true}} {
		got, err := ReadRange(context.Background(), path, 0, 1, &expected)
		assertRangeError(t, got, err, ErrChanged)
	}
}

// 在上下文检查处改变磁盘，固定读前、读后的时序，避免靠 sleep 争抢文件系统。
type rangeStepContext struct {
	context.Context
	step int
	at   int
	do   func()
}

func (c *rangeStepContext) Err() error {
	c.step++
	if c.step == c.at {
		c.do()
	}
	return c.Context.Err()
}

func TestReadRangeChangedDuringRead(t *testing.T) {
	for _, change := range []string{"write", "rename", "remove"} {
		t.Run(change, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "file")
			write(t, path, "old")
			before := StatFile(path)
			ctx := &rangeStepContext{Context: context.Background(), at: 3, do: func() {
				switch change {
				case "write":
					write(t, path, "new version")
				case "rename":
					replacement := path + ".new"
					write(t, replacement, "new")
					if err := os.Chtimes(replacement, before.ModTime, before.ModTime); err != nil {
						t.Fatal(err)
					}
					if err := os.Rename(replacement, path); err != nil {
						t.Fatal(err)
					}
				case "remove":
					if err := os.Remove(path); err != nil {
						t.Fatal(err)
					}
				}
			}}
			got, err := ReadRange(ctx, path, 0, 2, &before)
			assertRangeError(t, got, err, ErrChanged)
		})
	}
}

func TestReadRangeCanceled(t *testing.T) {
	path := filepath.Join(t.TempDir(), "file")
	write(t, path, "data")
	for _, at := range []int{1, 2, 3, 4} {
		base, cancel := context.WithCancel(context.Background())
		ctx := &rangeStepContext{Context: base, at: at, do: cancel}
		got, err := ReadRange(ctx, path, 0, 2, nil)
		cancel()
		assertRangeError(t, got, err, context.Canceled)
	}
}

func TestReadRangeRejectsNonRegular(t *testing.T) {
	dir := t.TempDir()
	fifo := filepath.Join(dir, "fifo")
	if err := syscall.Mkfifo(fifo, 0o600); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(dir, "link")
	if err := os.Symlink(fifo, link); err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{dir, fifo, link} {
		got, err := ReadRange(context.Background(), path, 0, 1, nil)
		assertRangeError(t, got, err, nil)
	}
	path := filepath.Join(dir, "file")
	write(t, path, "data")
	ctx := &rangeStepContext{Context: context.Background(), at: 2, do: func() {
		if err := os.Rename(fifo, path); err != nil {
			t.Fatal(err)
		}
	}}
	got, err := ReadRange(ctx, path, 0, 1, nil)
	assertRangeError(t, got, err, nil)
}

func assertRangeError(t *testing.T, got Range, err, want error) {
	t.Helper()
	if err == nil || (want != nil && !errors.Is(err, want)) {
		t.Fatalf("error = %v, want %v", err, want)
	}
	if got.Data != nil || got.Stamp != (Stamp{}) || got.EOF {
		t.Fatalf("error returned partial result: %+v", got)
	}
}
