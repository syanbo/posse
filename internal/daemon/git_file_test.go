package daemon

import (
	"bytes"
	"context"
	"crypto/sha1"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"posse/internal/files"
	"posse/internal/gitx"
	"posse/internal/rpc"
)

func gitFileTestParams(t *testing.T, c *gitReadTestClient, root string) rpc.GitFileParams {
	t.Helper()
	r, err := c.GitDiscover(t.Context(), rpc.GitDiscoverParams{Path: root, SingleRepo: true})
	if err != nil || len(r.Repos) != 1 {
		t.Fatalf("discover: %+v %v", r, err)
	}
	return rpc.GitFileParams{Path: root, Expected: r.Repos[0].Identity, File: "a.txt", Source: gitx.FileCommit, Revision: "HEAD"}
}
func gitFileTestCall(t *testing.T, c *gitReadTestClient, p rpc.GitFileParams) (rpc.GitFileResult, error) {
	t.Helper()
	var out rpc.GitFileResult
	err := mediaTestCall(t.Context(), c.Socket, *c.Desktop, "git.file", p, &out)
	return out, err
}
func gitBlobTestRead(t *testing.T, c *gitReadTestClient, p rpc.GitFileParams, v gitx.FileVersion) []byte {
	t.Helper()
	var data []byte
	for {
		var r rpc.GitBlobRangeResult
		q := rpc.GitBlobRangeParams{Path: p.Path, Expected: p.Expected, OID: v.OID, Offset: int64(len(data)), Limit: files.RangeLimit}
		if err := mediaTestCall(t.Context(), c.Socket, *c.Desktop, "git.blobRange", q, &r); err != nil {
			t.Fatal(err)
		}
		if r.Identity != p.Expected || r.OID != v.OID || r.Offset != int64(len(data)) || r.Size != v.Size || len(r.Data) > files.RangeLimit {
			t.Fatalf("range: %+v", r)
		}
		data = append(data, r.Data...)
		if r.EOF {
			break
		}
		if len(r.Data) == 0 {
			t.Fatal("no progress")
		}
	}
	h := sha1.New()
	fmt.Fprintf(h, "blob %d\x00", len(data))
	h.Write(data)
	if hex.EncodeToString(h.Sum(nil)) != v.OID {
		t.Fatal("cross-range hash mismatch")
	}
	return data
}
func TestDesktopGitFileVersions(t *testing.T) {
	d, c := gitReadClient(t)
	root := readGitRepo(t, filepath.Join(t.TempDir(), "repo"), true)
	content := map[string][]byte{"empty": {}, "binary": {'x', 0, 255}, "-option": []byte("option"), ":(glob)*": []byte("literal"), "space tab\tline\n": []byte("special"), "dir/file": []byte("nested"), "large": bytes.Repeat([]byte("abcdefgh"), (files.RangeLimit*2+32)/8)}
	for name, b := range content {
		if err := os.MkdirAll(filepath.Dir(filepath.Join(root, name)), 0700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(root, name), b, 0600); err != nil {
			t.Fatal(err)
		}
	}
	readGit(t, root, "add", "--all")
	readGit(t, root, "commit", "-qm", "files")
	p := gitFileTestParams(t, c, root)
	for _, source := range []gitx.FileSource{gitx.FileCommit, gitx.FileIndex} {
		p.Source = source
		p.Revision = ""
		if source == gitx.FileCommit {
			p.Revision = "HEAD"
		}
		for name, want := range content {
			p.File = name
			r, err := gitFileTestCall(t, c, p)
			if err != nil || r.File.Missing || r.File.Size != int64(len(want)) || r.File.Binary != files.IsBinary(want) {
				t.Fatalf("%s %q: %+v %v", source, name, r, err)
			}
			if got := gitBlobTestRead(t, c, p, r.File); !bytes.Equal(got, want) {
				t.Fatalf("%s %q bytes differ", source, name)
			}
		}
		p.File = "absent"
		r, err := gitFileTestCall(t, c, p)
		if err != nil || !r.File.Missing || r.File.Unborn {
			t.Fatalf("missing %+v %v", r, err)
		}
		p.File = "dir"
		_, err = gitFileTestCall(t, c, p)
		desktopErrorCode(t, err, rpc.CodeBadRequest)
	}
	// 解析完成后改动 HEAD 和 index，原 OID 的分段仍必须是原版本。
	p.File = "a.txt"
	p.Source = gitx.FileCommit
	p.Revision = "HEAD"
	before, err := gitFileTestCall(t, c, p)
	if err != nil {
		t.Fatal(err)
	}
	os.WriteFile(filepath.Join(root, "a.txt"), []byte("changed\n"), 0600)
	readGit(t, root, "add", "a.txt")
	readGit(t, root, "commit", "-qm", "change")
	if got := gitBlobTestRead(t, c, p, before.File); string(got) != "base\n" {
		t.Fatalf("mixed revision: %q", got)
	}
	p.Source = gitx.FileIndex
	p.Revision = ""
	staged, err := gitFileTestCall(t, c, p)
	if err != nil {
		t.Fatal(err)
	}
	os.WriteFile(filepath.Join(root, "a.txt"), []byte("again\n"), 0600)
	readGit(t, root, "add", "a.txt")
	if got := gitBlobTestRead(t, c, p, staged.File); string(got) != "changed\n" {
		t.Fatalf("mixed index: %q", got)
	}
	for _, method := range []string{"git.file", "git.blobRange"} {
		desktopErrorCode(t, rpc.Call(t.Context(), c.Socket, method, p, nil), rpc.CodeReadOnly)
		stale := *c.Desktop
		stale.InstanceID = "stale"
		desktopErrorCode(t, mediaTestCall(t.Context(), c.Socket, stale, method, p, nil), rpc.CodeConflict)
	}
	d.desktop.mu.Lock()
	owner := d.desktop.owner
	d.desktop.mu.Unlock()
	if owner != "" {
		t.Fatal("read acquired control")
	}
}

func TestDesktopGitFileErrors(t *testing.T) {
	_, c := gitReadClient(t)
	root := readGitRepo(t, filepath.Join(t.TempDir(), "repo"), false)
	p := gitFileTestParams(t, c, root)
	r, err := gitFileTestCall(t, c, p)
	if err != nil || !r.File.Missing || !r.File.Unborn {
		t.Fatalf("unborn %+v %v", r, err)
	}
	p.Source = gitx.FileIndex
	p.Revision = ""
	r, err = gitFileTestCall(t, c, p)
	if err != nil || r.File.Missing {
		t.Fatalf("unborn index %+v %v", r, err)
	}
	readGit(t, root, "commit", "-qm", "base")
	p.Source = gitx.FileCommit
	p.Revision = "HEAD"
	for _, rev := range []string{"--help", "HEAD:a.txt", "HEAD^{tree}", "HEAD@{0}", "HEAD..main", "HEAD\n", ""} {
		q := p
		q.Revision = rev
		_, err := gitFileTestCall(t, c, q)
		desktopErrorCode(t, err, rpc.CodeBadRequest)
	}
	for _, name := range []string{"../a", "/a", "a/../b", "a\x00", ".", ""} {
		q := p
		q.File = name
		_, err := gitFileTestCall(t, c, q)
		desktopErrorCode(t, err, rpc.CodeBadRequest)
	}
	p.Revision = "unknown-revision"
	if r, err := gitFileTestCall(t, c, p); err == nil {
		t.Fatalf("invalid revision hidden: %+v", r)
	}
	p.Revision = "HEAD"
	commit := readGit(t, root, "rev-parse", "HEAD")
	readGit(t, root, "update-index", "--add", "--cacheinfo", "160000,"+commit+",sub")
	p.Source = gitx.FileIndex
	p.Revision = ""
	p.File = "sub"
	_, err = gitFileTestCall(t, c, p)
	desktopErrorCode(t, err, rpc.CodeBadRequest)
	readGit(t, root, "commit", "-qm", "submodule")
	p.Source = gitx.FileCommit
	p.Revision = "HEAD"
	_, err = gitFileTestCall(t, c, p)
	desktopErrorCode(t, err, rpc.CodeBadRequest)
	p.File = "a.txt"
	p.Expected.RootInode++
	_, err = gitFileTestCall(t, c, p)
	desktopErrorCode(t, err, rpc.CodeConflict)
}

func TestDesktopGitFileFiltersAndFailures(t *testing.T) {
	_, c := gitReadClient(t)
	root := readGitRepo(t, filepath.Join(t.TempDir(), "repo"), true)
	p := gitFileTestParams(t, c, root)
	marker := filepath.Join(t.TempDir(), "executed")
	readGit(t, root, "config", "filter.danger.clean", "touch '"+marker+"'; cat")
	readGit(t, root, "config", "filter.danger.smudge", "touch '"+marker+"'; cat")
	readGit(t, root, "config", "diff.danger.textconv", "touch '"+marker+"'; cat")
	os.WriteFile(filepath.Join(root, ".gitattributes"), []byte("* filter=danger diff=danger\n"), 0600)
	for _, source := range []gitx.FileSource{gitx.FileCommit, gitx.FileIndex} {
		p.Source = source
		p.Revision = ""
		if source == gitx.FileCommit {
			p.Revision = "HEAD"
		}
		r, err := gitFileTestCall(t, c, p)
		if err != nil {
			t.Fatal(err)
		}
		gitBlobTestRead(t, c, p, r.File)
	}
	if _, err := os.Stat(marker); !os.IsNotExist(err) {
		t.Fatalf("filter executed: %v", err)
	}
	t.Run("git-error", func(t *testing.T) {
		gitReadWrapper(t, "case \" $* \" in *\" ls-files \"*) printf 'permission denied in query\\n' >&2; exit 128;; esac")
		_, err := gitFileTestCall(t, c, p)
		if err == nil || !strings.Contains(err.Error(), "permission denied") {
			t.Fatalf("error hidden: %v", err)
		}
	})
	t.Run("replace", func(t *testing.T) {
		next := readGitRepo(t, filepath.Join(t.TempDir(), "next"), true)
		t.Setenv("READ_ROOT", root)
		t.Setenv("READ_NEXT", next)
		t.Setenv("READ_OLD", root+"-old")
		t.Cleanup(func() { os.RemoveAll(root + "-old") })
		gitReadWrapper(t, "case \" $* \" in *\" cat-file blob \"*) /usr/bin/git \"$@\" || exit $?; /bin/mv \"$READ_ROOT\" \"$READ_OLD\"; /bin/mv \"$READ_NEXT\" \"$READ_ROOT\"; exit 0;; esac")
		_, err := gitFileTestCall(t, c, p)
		desktopErrorCode(t, err, rpc.CodeConflict)
	})
}

func TestDesktopGitBlobSizeAndCancel(t *testing.T) {
	d, c := gitReadClient(t)
	root := readGitRepo(t, filepath.Join(t.TempDir(), "repo"), true)
	p := gitFileTestParams(t, c, root)
	for _, size := range []int64{files.MaxSize, files.MaxSize + 1} {
		name := filepath.Join(root, "boundary")
		f, err := os.Create(name)
		if err != nil {
			t.Fatal(err)
		}
		if err = f.Truncate(size); err != nil {
			t.Fatal(err)
		}
		f.Close()
		oid := readGit(t, root, "hash-object", "-w", "--no-filters", name)
		q := rpc.GitBlobRangeParams{Path: root, Expected: p.Expected, OID: oid, Offset: size - 8, Limit: 16}
		var r rpc.GitBlobRangeResult
		err = mediaTestCall(t.Context(), c.Socket, *c.Desktop, "git.blobRange", q, &r)
		if size == files.MaxSize {
			if err != nil || !r.EOF || len(r.Data) != 8 {
				t.Fatalf("boundary: %+v %v", r, err)
			}
		} else if err == nil || !strings.Contains(err.Error(), "maximum size") {
			t.Fatalf("oversize accepted: %v", err)
		}
	}
	r, err := gitFileTestCall(t, c, p)
	if err != nil {
		t.Fatal(err)
	}
	gitReadWrapper(t, "case \" $* \" in *\" cat-file blob \"*) test \"$GIT_NO_LAZY_FETCH\" = 1 || exit 99; exec /bin/sleep 20;; esac")
	ctx, cancel := context.WithTimeout(t.Context(), 150*time.Millisecond)
	defer cancel()
	ctx = context.WithValue(ctx, desktopCallContext{}, desktopRequestContext{identity: *c.Desktop, readOnly: true})
	raw, _ := json.Marshal(rpc.GitBlobRangeParams{Path: root, Expected: p.Expected, OID: r.File.OID, Limit: 1})
	_, err = d.gitBlobRange(ctx, raw)
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("cancel: %v", err)
	}
}
