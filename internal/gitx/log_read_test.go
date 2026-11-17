package gitx

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func historyContext(t *testing.T) context.Context { return WithReadLimit(t.Context(), 4<<20) }

func TestHistoryControlCharactersAndMalformedObjects(t *testing.T) {
	root := t.TempDir()
	initRepo(t, root)
	tree := gitIn(t, root, "rev-parse", "HEAD^{tree}")
	parent := gitIn(t, root, "rev-parse", "HEAD")
	raw := fmt.Sprintf("tree %s\nparent %s\nauthor A\x1e\x1fB <a@example.invalid> 1700000000 +0000\ncommitter C <c@example.invalid> 1700000000 +0000\n\nsubject\x1e\x1ftrail\ncontinued\n\nbody\n", tree, parent)
	save := func(raw string) string {
		t.Helper()
		out, err := runRaw(t.Context(), root, []byte(raw), nil, "hash-object", "-w", "-t", "commit", "--stdin", "--literally")
		if err != nil {
			t.Fatal(err)
		}
		return strings.TrimSpace(string(out))
	}
	hash := save(raw)
	gitIn(t, root, "update-ref", "refs/heads/main", hash)
	page, err := ReadLogChecked(historyContext(t), root, LogFilter{Branches: []string{"HEAD"}}, 0, 10, "")
	if err != nil || len(page.Commits) != 2 {
		t.Fatalf("log %+v %v", page, err)
	}
	got := page.Commits[0]
	if got.Author != "A\x1e\x1fB" || got.Subject != "subject\x1e\x1ftrail continued" {
		t.Fatalf("control bytes lost %+v", got)
	}
	info, changes, err := ReadCommitFilesChecked(historyContext(t), root, hash)
	if err != nil || info.Subject != got.Subject || len(changes) != 0 {
		t.Fatalf("detail %+v %+v %v", info, changes, err)
	}
	for _, bad := range []string{strings.Replace(raw, "trail", "tr\x00ail", 1), strings.Replace(raw, "trail", "tr\xffail", 1)} {
		hash := save(bad)
		gitIn(t, root, "update-ref", "refs/heads/main", hash)
		if _, err := ReadLogChecked(historyContext(t), root, LogFilter{}, 0, 10, ""); !errors.Is(err, ErrLogOutput) {
			t.Fatalf("malformed object hidden %v", err)
		}
	}
	for _, bad := range [][]byte{[]byte("bad"), []byte(parent + "\x00refs\x00extra\x00"), []byte(parent + "\x00refs\x00" + parent + "\x00refs\x00")} {
		if _, _, err := parseHistoryIDs(bad); !errors.Is(err, ErrLogOutput) {
			t.Fatalf("malformed log accepted %q", bad)
		}
	}
	for _, bad := range []string{"M\x00", "R100\x00old\x00", "R999\x00old\x00new\x00", "M\x00../escape\x00", "Q\x00file\x00", "M\x00file", "M\x00file\x00M\x00file\x00"} {
		if _, err := parseHistoryChanges([]byte(bad)); !errors.Is(err, ErrLogOutput) {
			t.Fatalf("malformed changes accepted %q", bad)
		}
	}
}

func TestHistorySpecialRevisionAndFileRegression(t *testing.T) {
	root := t.TempDir()
	initRepo(t, root)
	for _, name := range []string{"topic+fix", "quote'\"$;]雪", "+plus", "refs/heads/slash/name"} {
		ref := name
		if !strings.HasPrefix(ref, "refs/") {
			ref = "refs/heads/" + ref
		}
		gitIn(t, root, "update-ref", ref, "HEAD")
		for _, rev := range []string{ref, ref + "~0", ref + "^0"} {
			if !ValidReadRevision(rev) {
				t.Fatalf("valid ref rejected %q", rev)
			}
			v, err := ResolveFile(historyContext(t), root, FileCommit, rev, "README")
			if err != nil || v.Missing || v.Size != 2 {
				t.Fatalf("file ref %q: %+v %v", rev, v, err)
			}
			page, err := ReadLogChecked(historyContext(t), root, LogFilter{Branches: []string{rev}}, 0, 10, "")
			if err != nil || len(page.Commits) != 1 {
				t.Fatalf("log ref %q: %+v %v", rev, page, err)
			}
		}
	}
	for _, bad := range []string{"--all", "HEAD:README", "HEAD^{tree}", "HEAD..main", "@{0}", "main.lock", "a/.b", "a//b", "a?b", "a[b", "a\\b", "a\n", "HEAD^@", "HEAD^!"} {
		if ValidReadRevision(bad) {
			t.Fatalf("unsafe rev accepted %q", bad)
		}
	}
	// 真正损坏的引用不能被 for-each-ref 的成功退出码吞掉。
	if err := os.WriteFile(filepath.Join(root, ".git", "refs", "heads", "broken"), []byte("invalid\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := ReadLogChecked(historyContext(t), root, LogFilter{}, 0, 10, ""); err == nil {
		t.Fatal("broken reference hidden")
	}
}
