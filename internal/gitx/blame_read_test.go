package gitx

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func blameReadTest(t *testing.T, root, name string) (BlameRead, error) {
	t.Helper()
	ctx := historyContext(t)
	id, err := RepositoryIdentityChecked(ctx, root)
	if err != nil {
		t.Fatal(err)
	}
	return ReadBlameChecked(ctx, id, name)
}

func TestBlameReadText(t *testing.T) {
	for _, format := range []string{"sha1", "sha256"} {
		t.Run(format, func(t *testing.T) {
			root := t.TempDir()
			gitIn(t, root, "init", "-b", "main", "--object-format="+format)
			gitIn(t, root, "config", "user.name", "Test")
			gitIn(t, root, "config", "user.email", "test@example.invalid")
			names := []string{"a", "empty", "-option", ":(glob)*", "space tab\tline\n雪", "nested/file"}
			for _, name := range names {
				os.MkdirAll(filepath.Dir(filepath.Join(root, name)), 0700)
				write(t, filepath.Join(root, name), "first\nsecond\n")
			}
			gitIn(t, root, "add", "--all")
			gitIn(t, root, "commit", "-qm", "initial")
			head := gitIn(t, root, "rev-parse", "HEAD")
			for _, name := range names {
				t.Run(name, func(t *testing.T) {
					content := "first\nchanged\nlast"
					if name == "empty" {
						content = ""
					}
					write(t, filepath.Join(root, name), content)
					got, err := blameReadTest(t, root, name)
					if err != nil {
						t.Fatal(err)
					}
					if got.Head != head || len(got.Lines) != len(blameTextLines([]byte(content))) {
						t.Fatalf("result %+v", got)
					}
					for i, line := range got.Lines {
						if line.Text != blameTextLines([]byte(content))[i] {
							t.Fatal("text changed")
						}
						if i == 0 && line.Hash != head {
							t.Fatalf("first hash %s", line.Hash)
						}
						if i > 0 && line.Hash != strings.Repeat("0", len(head)) {
							t.Fatalf("uncommitted hash %s", line.Hash)
						}
					}
				})
			}
			write(t, filepath.Join(root, "new"), "staged\n")
			gitIn(t, root, "add", "new")
			got, err := blameReadTest(t, root, "new")
			if err != nil || len(got.Lines) != 1 || got.Lines[0].Hash != strings.Repeat("0", len(head)) {
				t.Fatalf("staged %+v %v", got, err)
			}
			write(t, filepath.Join(root, "a"), "first\r\nsecond\r\n")
			write(t, filepath.Join(root, ".gitattributes"), "* text eol=lf\n")
			got, err = blameReadTest(t, root, "a")
			if err != nil || len(got.Lines) != 2 || got.Lines[0].Text != "first\r" || got.Lines[0].Hash != head {
				t.Fatalf("CRLF %+v %v", got, err)
			}
		})
	}
}

func TestBlameReadRejectsAndBudget(t *testing.T) {
	root := t.TempDir()
	initRepo(t, root)
	for _, tc := range []struct {
		name, content string
		want          error
	}{
		{"nul", "a\x00b", ErrBlameNotText}, {"invalid", "\xff", ErrBlameNotText},
		{"size", strings.Repeat("x", BlameReadSizeLimit+1), nil}, {"lines", strings.Repeat("\n", BlameReadLineLimit+1), nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			write(t, filepath.Join(root, "a"), tc.content)
			_, err := blameReadTest(t, root, "a")
			if err == nil || tc.want != nil && !errors.Is(err, tc.want) {
				t.Fatalf("err %v", err)
			}
		})
	}
	write(t, filepath.Join(root, "a"), "hello\n")
	gitIn(t, root, "add", "a")
	gitIn(t, root, "commit", "-qm", "a")
	id, err := RepositoryIdentityChecked(historyContext(t), root)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := ReadBlameChecked(WithReadLimit(t.Context(), 1), id, "a"); !errors.Is(err, ErrReadLimit) {
		t.Fatalf("budget %v", err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if _, err := ReadBlameChecked(WithReadLimit(ctx, 4<<20), id, "a"); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancel %v", err)
	}
}

func TestBlameReadUnavailableFiles(t *testing.T) {
	root := t.TempDir()
	gitIn(t, root, "init", "-b", "main")
	write(t, filepath.Join(root, "a"), "file\n")
	if _, err := blameReadTest(t, root, "a"); !errors.Is(err, ErrBlameNoHead) {
		t.Fatalf("unborn: %v", err)
	}
	gitIn(t, root, "config", "user.name", "Test")
	gitIn(t, root, "config", "user.email", "test@example.invalid")
	gitIn(t, root, "add", "a")
	gitIn(t, root, "commit", "-qm", "initial")
	write(t, filepath.Join(root, "untracked"), "file\n")
	if _, err := blameReadTest(t, root, "untracked"); err == nil {
		t.Fatal("untracked accepted")
	}
	if err := os.Symlink("a", filepath.Join(root, "link")); err != nil {
		t.Fatal(err)
	}
	if _, err := blameReadTest(t, root, "link"); !errors.Is(err, ErrBlameNotText) {
		t.Fatalf("symlink: %v", err)
	}
	oid := gitIn(t, root, "rev-parse", "HEAD:a")
	gitIn(t, root, "update-index", "--force-remove", "a")
	if _, err := runRaw(t.Context(), root, []byte("100644 "+oid+" 1\ta\n100644 "+oid+" 2\ta\n"), nil, "update-index", "--index-info"); err != nil {
		t.Fatal(err)
	}
	if _, err := blameReadTest(t, root, "a"); !errors.Is(err, ErrFileUnmerged) {
		t.Fatalf("unmerged: %v", err)
	}
	gitIn(t, root, "reset", "-q", "HEAD", "--", "a")
	if err := os.Remove(filepath.Join(root, "a")); err != nil {
		t.Fatal(err)
	}
	if _, err := blameReadTest(t, root, "a"); !os.IsNotExist(err) {
		t.Fatalf("deleted: %v", err)
	}
}

func TestBlameParseStrict(t *testing.T) {
	root := t.TempDir()
	initRepo(t, root)
	write(t, filepath.Join(root, "a"), "one\ntwo\n")
	gitIn(t, root, "add", "a")
	gitIn(t, root, "commit", "-qm", "a")
	raw, err := historyRead(historyContext(t), root, nil, "blame", "--line-porcelain", "--", "a")
	if err != nil {
		t.Fatal(err)
	}
	lines, err := parseBlameChecked(raw, 40)
	if err != nil || len(lines) != 2 {
		t.Fatalf("parse %+v %v\n%s", lines, err, raw)
	}
	bad := [][]byte{raw[:len(raw)-1], bytes.Replace(raw, []byte("author-time "), []byte("author-time x"), 1), bytes.Replace(raw, []byte("filename a\n"), []byte("unknown a\n"), 1), bytes.Replace(raw, []byte("author Test"), []byte("author Test\nauthor duplicate"), 1), []byte("\tno header\n"), []byte("bad\x00\n")}
	for i, b := range bad {
		if reflect.DeepEqual(b, raw) {
			continue
		}
		if _, err := parseBlameChecked(b, 40); !errors.Is(err, ErrBlameOutput) {
			t.Fatalf("bad %d: %v", i, err)
		}
	}
}
