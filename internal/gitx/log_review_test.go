package gitx

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func TestHistorySelectedStartsBaseline(t *testing.T) {
	root := t.TempDir()
	initRepo(t, root)
	gitIn(t, root, "commit", "--allow-empty", "-qm", "second")
	gitIn(t, root, "commit", "--allow-empty", "-qm", "third")
	for _, ref := range []string{"ORIG_HEAD", "FETCH_HEAD"} {
		setRef := func(revision string) {
			if ref == "FETCH_HEAD" {
				hash := gitIn(t, root, "rev-parse", revision)
				if err := os.WriteFile(filepath.Join(root, ".git", "FETCH_HEAD"), []byte(hash+"\t\tbranch 'main'\n"), 0600); err != nil {
					t.Fatal(err)
				}
			} else {
				gitIn(t, root, "update-ref", ref, revision)
			}
		}
		setRef("HEAD")
		filter := LogFilter{Branches: []string{ref}}
		page, err := ReadLogChecked(historyContext(t), root, filter, 0, 1, "")
		if err != nil {
			t.Fatal(err)
		}
		setRef("HEAD~1")
		if _, err := ReadLogChecked(historyContext(t), root, filter, page.NextSkip, 1, page.Baseline); !errors.Is(err, ErrLogChanged) {
			t.Fatalf("moved %s accepted old page: %v", ref, err)
		}
	}
}

func TestHistoryShallowDetailsAndDeepen(t *testing.T) {
	source := t.TempDir()
	initRepo(t, source)
	write(t, filepath.Join(source, "second"), "next\n")
	gitIn(t, source, "add", "second")
	gitIn(t, source, "commit", "-qm", "second")
	clone := filepath.Join(t.TempDir(), "clone")
	gitIn(t, source, "clone", "--depth=1", "file://"+source, clone)
	hash := gitIn(t, clone, "rev-parse", "HEAD")
	reference, err := CommitFiles(t.Context(), clone, hash)
	if err != nil {
		t.Fatal(err)
	}
	detail, err := ReadCommitDetailsChecked(historyContext(t), clone, hash)
	if err != nil || !detail.Shallow || detail.DiffBase != "" || len(detail.Commit.Parents) != 1 || !reflect.DeepEqual(detail.Files, reference) {
		t.Fatalf("shallow %+v %v; reference %+v", detail, err, reference)
	}
	page, err := ReadLogChecked(historyContext(t), clone, LogFilter{}, 0, 1, "")
	if err != nil {
		t.Fatal(err)
	}
	gitIn(t, clone, "fetch", "--deepen=1", "origin")
	if _, err := ReadLogChecked(historyContext(t), clone, LogFilter{}, page.NextSkip, 1, page.Baseline); !errors.Is(err, ErrLogChanged) {
		t.Fatalf("deepened history accepted old page: %v", err)
	}
	detail, err = ReadCommitDetailsChecked(historyContext(t), clone, hash)
	if err != nil || detail.Shallow || detail.DiffBase != detail.Commit.Parents[0] || len(detail.Files) != 1 || detail.Files[0].Path != "second" {
		t.Fatalf("deepened detail %+v %v", detail, err)
	}
	// 丢失的普通父对象不能冒充浅边界。
	parent := detail.Commit.Parents[0]
	loose := filepath.Join(source, ".git", "objects", parent[:2], parent[2:])
	if err := os.Remove(loose); err != nil {
		t.Fatal(err)
	}
	if _, err := ReadCommitDetailsChecked(historyContext(t), source, hash); err == nil {
		t.Fatal("missing parent object hidden as shallow")
	}
}

func TestHistoryDeclaredCommitEncoding(t *testing.T) {
	root := t.TempDir()
	initRepo(t, root)
	gitIn(t, root, "config", "i18n.commitEncoding", "ISO-8859-1")
	if _, err := runRaw(t.Context(), root, []byte("caf\xe9\x1e\x1f end\n"), nil, "commit", "--allow-empty", "-F", "-"); err != nil {
		t.Fatal(err)
	}
	gitIn(t, root, "fsck", "--no-reflogs")
	gitIn(t, root, "config", "i18n.logOutputEncoding", "ISO-8859-1")
	hash := gitIn(t, root, "rev-parse", "HEAD")
	for _, f := range []LogFilter{{}, {Text: "café"}} {
		page, err := ReadLogChecked(historyContext(t), root, f, 0, 1, "")
		if err != nil || len(page.Commits) != 1 || page.Commits[0].Subject != "café\x1e\x1f end" {
			t.Fatalf("encoded log %+v %v", page, err)
		}
	}
	detail, err := ReadCommitDetailsChecked(historyContext(t), root, hash)
	if err != nil || detail.Commit.Subject != "café\x1e\x1f end" {
		t.Fatalf("encoded detail %+v %v", detail, err)
	}
	raw := []byte("encoding ISO-8859-1\n\ncaf\xe9\x1e\x1f\n")
	decoded, err := decodeCommitObject(historyContext(t), raw)
	if err != nil || !strings.Contains(string(decoded), "café\x1e\x1f") {
		t.Fatalf("decode %q %v", decoded, err)
	}
	if _, err := decodeCommitObject(WithReadLimit(t.Context(), 3), raw); !errors.Is(err, ErrReadLimit) {
		t.Fatalf("decoded budget bypass: %v", err)
	}
	for _, bad := range [][]byte{[]byte("encoding unknown-charset\n\nascii\n"), []byte("encoding ISO-8859-1\n\nNUL\x00\n"), []byte("encoding Shift_JIS\n\ninvalid\x81\n")} {
		if _, err := decodeCommitObject(historyContext(t), bad); err == nil {
			t.Fatalf("invalid encoding accepted %q", bad)
		}
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if _, err := decodeCommitObject(WithReadLimit(ctx, 4096), raw); !errors.Is(err, context.Canceled) {
		t.Fatalf("decode cancel %v", err)
	}
}
