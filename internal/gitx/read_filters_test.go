package gitx

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"path/filepath"
	"strings"
	"testing"
)

func TestStatusReusesIndexScanUnderSmallBudget(t *testing.T) {
	dir := t.TempDir()
	initRepo(t, dir)
	gitIn(t, dir, "config", "filter.unused.clean", "cat")
	for i := 0; i < 3000; i++ {
		write(t, filepath.Join(dir, "p", "file-"+hexShort(i)+".txt"), "x\n")
	}
	write(t, filepath.Join(dir, "p", "a b.txt"), "x\n")
	gitIn(t, dir, "add", "-A")
	gitIn(t, dir, "commit", "-q", "-m", "files")
	listing, err := runRaw(context.Background(), dir, nil, nil, "ls-files", "-s", "-z")
	if err != nil {
		t.Fatal(err)
	}
	cfg, err := runRaw(context.Background(), dir, nil, nil, "config", "--null", "--list", "--includes")
	if err != nil {
		t.Fatal(err)
	}
	if len(listing) <= len(cfg)+8192 {
		t.Fatalf("listing %d is not larger than the content budget around config %d", len(listing), len(cfg))
	}
	st, err := ReadStatusChecked(WithReadLimit(t.Context(), len(cfg)+8192), dir)
	if err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(listing)
	if st.IndexID != hex.EncodeToString(sum[:]) || st.Head != "main" {
		t.Fatalf("status %+v", st)
	}
}

func TestStatusRejectsTrackedFilter(t *testing.T) {
	dir := t.TempDir()
	initRepo(t, dir)
	write(t, filepath.Join(dir, ".gitattributes"), "*.txt filter=marker\n")
	write(t, filepath.Join(dir, "a.txt"), "a\n")
	gitIn(t, dir, "add", "-A")
	gitIn(t, dir, "config", "filter.marker.clean", "cat")
	_, err := ReadStatusChecked(WithReadLimit(t.Context(), 1<<20), dir)
	if err == nil || !strings.Contains(err.Error(), "Git content query would execute filter marker") {
		t.Fatal(err)
	}
}

func hexShort(i int) string {
	const digits = "0123456789abcdef"
	var b [4]byte
	for n := 3; n >= 0; n-- {
		b[n] = digits[i&15]
		i >>= 4
	}
	return string(b[:])
}
