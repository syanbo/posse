package gitx

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"posse/internal/files"
)

func TestGitFileUnmergedAndObjectFailures(t *testing.T) {
	root := t.TempDir()
	initRepo(t, root)
	oid := gitIn(t, root, "rev-parse", "HEAD:README")
	input := fmt.Sprintf("0 %s\tREADME\n100644 %s 1\tREADME\n100644 %s 2\tREADME\n100644 %s 3\tREADME\n", strings.Repeat("0", 40), oid, oid, oid)
	if _, err := runRaw(t.Context(), root, []byte(input), nil, "update-index", "--index-info"); err != nil {
		t.Fatal(err)
	}
	ctx := WithReadLimit(t.Context(), 4<<20)
	if _, err := ResolveFile(ctx, root, FileIndex, "", "README"); !errors.Is(err, ErrFileUnmerged) {
		t.Fatalf("unmerged: %v", err)
	}
	commit := gitIn(t, root, "rev-parse", "HEAD")
	if _, err := BlobSize(ctx, root, commit); !errors.Is(err, ErrBlobResponse) {
		t.Fatalf("commit as blob: %v", err)
	}
	if _, err := BlobSize(ctx, root, strings.Repeat("a", 40)); !errors.Is(err, ErrBlobResponse) {
		t.Fatalf("missing object: %v", err)
	}
	// replace 引用不应改变按对象摘要读取的内容。
	other, err := runRaw(t.Context(), root, []byte("different\n"), nil, "hash-object", "-w", "--stdin")
	if err != nil {
		t.Fatal(err)
	}
	gitIn(t, root, "replace", oid, strings.TrimSpace(string(other)))
	v, err := ResolveFile(ctx, root, FileCommit, "HEAD", "README")
	if err != nil || v.OID != oid || v.Size != 2 {
		t.Fatalf("replacement followed: %+v %v", v, err)
	}
	b, err := ReadBlobRange(ctx, root, oid, v.Size, 0, 10)
	if err != nil || string(b) != "x\n" {
		t.Fatalf("replacement bytes %q %v", b, err)
	}
	if err := os.Remove(filepath.Join(root, ".git", "objects", oid[:2], oid[2:])); err != nil {
		t.Fatal(err)
	}
	if _, err := ResolveFile(ctx, root, FileCommit, "HEAD", "README"); err == nil {
		t.Fatal("missing object became missing path")
	}
}

func TestGitBlobStreamBudget(t *testing.T) {
	root := t.TempDir()
	initRepo(t, root)
	data := bytes.Repeat([]byte("abcd"), files.RangeLimit)
	oid, err := runRaw(t.Context(), root, data, nil, "hash-object", "-w", "--stdin")
	if err != nil {
		t.Fatal(err)
	}
	id := strings.TrimSpace(string(oid))
	// 元数据预算比正文小，正文仍可流式扫描；只返回末尾小段。
	ctx := WithReadLimit(t.Context(), 1024)
	b, err := ReadBlobRange(ctx, root, id, int64(len(data)), int64(len(data)-7), 7)
	if err != nil || !bytes.Equal(b, data[len(data)-7:]) {
		t.Fatalf("stream: %q %v", b, err)
	}
	_, err = ReadBlobRange(ctx, root, id, 1, 0, 1)
	if !errors.Is(err, ErrReadLimit) {
		t.Fatalf("body exceeded declared size: %v", err)
	}
	_, err = runReadOutput(WithReadLimit(t.Context(), 4), root, nil, nil, &bytes.Buffer{}, 10, "-c", "alias.emit=!printf 12345 >&2", "emit")
	if !errors.Is(err, ErrReadLimit) {
		t.Fatalf("stderr budget: %v", err)
	}
	cancelCtx, cancel := context.WithCancel(t.Context())
	cancel()
	_, err = ReadBlobRange(WithReadLimit(cancelCtx, 1024), root, id, int64(len(data)), 0, 1)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled: %v", err)
	}
}
