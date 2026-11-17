package gitx

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestCompareReadBranches(t *testing.T) {
	for _, format := range []string{"sha1", "sha256"} {
		t.Run(format, func(t *testing.T) {
			root := t.TempDir()
			gitIn(t, root, "init", "-b", "main", "--object-format="+format)
			gitIn(t, root, "config", "user.name", "Test")
			gitIn(t, root, "config", "user.email", "test@example.invalid")
			write(t, filepath.Join(root, "a"), "base")
			gitIn(t, root, "add", "a")
			gitIn(t, root, "commit", "-qm", "base")
			base := gitIn(t, root, "rev-parse", "HEAD")
			other := "quote'\"$;]雪+other"
			gitIn(t, root, "checkout", "-qb", other)
			for i := 0; i < 2; i++ {
				gitIn(t, root, "commit", "--allow-empty", "-qm", fmt.Sprintf("other %d", i))
			}
			otherOID := gitIn(t, root, "rev-parse", "HEAD")
			gitIn(t, root, "tag", "-am", "tag", "tagged")
			gitIn(t, root, "checkout", "-q", "main")
			gitIn(t, root, "commit", "--allow-empty", "-qm", "current")
			current := gitIn(t, root, "rev-parse", "HEAD")
			got, err := ReadCompareChecked(historyContext(t), root, "HEAD", "tagged", 1)
			if err != nil || got.CurrentOID != current || got.OtherOID != otherOID || len(got.OnlyCurrent) != 1 || len(got.OnlyOther) != 1 || got.MoreCurrent || !got.MoreOther || !ValidLogBaseline(got.Baseline) {
				t.Fatalf("compare %+v %v", got, err)
			}
			full, err := ReadCompareChecked(historyContext(t), root, "main", other, 1000)
			if err != nil || len(full.OnlyOther) != 2 || len(full.OnlyCurrent) != 1 || full.MoreOther {
				t.Fatalf("full %+v %v", full, err)
			}
			same, err := ReadCompareChecked(historyContext(t), root, "HEAD", "HEAD", 1000)
			if err != nil || same.OnlyCurrent == nil || same.OnlyOther == nil || len(same.OnlyCurrent)+len(same.OnlyOther) != 0 {
				t.Fatalf("same %+v %v", same, err)
			}
			ancestor, err := ReadCompareChecked(historyContext(t), root, base, current, 1000)
			if err != nil || len(ancestor.OnlyCurrent) != 0 || len(ancestor.OnlyOther) != 1 {
				t.Fatalf("ancestor %+v %v", ancestor, err)
			}
			gitIn(t, root, "checkout", "--detach", "-q", current)
			detached, err := ReadCompareChecked(historyContext(t), root, "HEAD", other, 1000)
			if err != nil || len(detached.OnlyOther) != 2 || len(detached.OnlyCurrent) != 1 {
				t.Fatalf("detached %+v %v", detached, err)
			}
		})
	}
}

func TestCompareReadLimitAndEncoding(t *testing.T) {
	root := t.TempDir()
	initRepo(t, root)
	base := gitIn(t, root, "rev-parse", "HEAD")
	var input strings.Builder
	for i := 1; i <= 1001; i++ {
		parent := base
		if i > 1 {
			parent = fmt.Sprintf(":%d", i-1)
		}
		fmt.Fprintf(&input, "commit refs/heads/long\nmark :%d\ncommitter Test <test@example.invalid> %d +0000\ndata 1\nx\nfrom %s\n\n", i, time.Now().Unix()+int64(i), parent)
	}
	if _, err := runRaw(t.Context(), root, []byte(input.String()), nil, "fast-import", "--quiet"); err != nil {
		t.Fatal(err)
	}
	got, err := ReadCompareChecked(historyContext(t), root, base, "long", 1000)
	if err != nil || len(got.OnlyOther) != 1000 || !got.MoreOther || len(got.OnlyCurrent) != 0 {
		t.Fatalf("limit len=%d more=%v current=%+v err=%v", len(got.OnlyOther), got.MoreOther, got.OnlyCurrent, err)
	}
	tree := gitIn(t, root, "rev-parse", base+"^{tree}")
	raw := fmt.Sprintf("tree %s\nparent %s\nauthor Andr\xe9 <a@example.invalid> 1700000000 +0000\ncommitter Test <a@example.invalid> 1700000000 +0000\nencoding ISO-8859-1\n\ncaf\xe9\n", tree, base)
	oid, err := runRaw(t.Context(), root, []byte(raw), nil, "hash-object", "-w", "-t", "commit", "--stdin")
	if err != nil {
		t.Fatal(err)
	}
	got, err = ReadCompareChecked(historyContext(t), root, base, strings.TrimSpace(string(oid)), 1000)
	if err != nil || len(got.OnlyOther) != 1 || got.OnlyOther[0].Author != "André" || got.OnlyOther[0].Subject != "café" {
		t.Fatalf("encoding %+v %v", got, err)
	}
	for _, rev := range []string{"--all", "main..long", "HEAD:a", "missing"} {
		if _, err := ReadCompareChecked(historyContext(t), root, base, rev, 1000); err == nil {
			t.Fatalf("accepted %q", rev)
		}
	}
	if _, err := ReadCompareChecked(WithReadLimit(t.Context(), 100), root, base, "long", 1000); !errors.Is(err, ErrReadLimit) {
		t.Fatalf("budget %v", err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if _, err := ReadCompareChecked(WithReadLimit(ctx, 4<<20), root, base, "long", 1000); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancel %v", err)
	}
}
