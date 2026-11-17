package client

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"posse/internal/files"
	"posse/internal/gitx"
	"posse/internal/rpc"
)

func clientWorktreeResult(p rpc.GitWorktreeCompareParams, kind string) rpc.GitWorktreeCompareResult {
	sum := sha256.Sum256([]byte("data"))
	f := gitx.WorktreeFile{FileChange: gitx.FileChange{Path: "a", Status: 'M'}, Kind: kind, BaseMode: "100644", BaseOID: strings.Repeat("b", 40), Version: files.Version{Path: filepath.Join(p.Expected.Root, "a"), Stamp: files.Stamp{Ino: 1, Mode: 0600, Size: 4}, Hash: hex.EncodeToString(sum[:])}}
	switch kind {
	case "symlink":
		f.Version.Stamp.Mode = os.ModeSymlink | 0777
		f.Link = "data"
	case "gitlink":
		f.Version.Stamp.Mode = os.ModeDir | 0700
		f.BaseMode = "160000"
		f.OID = strings.Repeat("c", 40)
		f.Dirty = true
	case "directory":
		f.Version.Stamp.Mode = os.ModeDir | 0700
		f.Status = 'D'
	case "missing":
		f.Version.Stamp = files.Stamp{Missing: true}
		f.Version.Hash = ""
		f.Status = 'D'
	}
	return rpc.GitWorktreeCompareResult{Identity: p.Expected, Revision: p.Revision, WorktreeComparison: gitx.WorktreeComparison{Commit: strings.Repeat("a", 40), Baseline: strings.Repeat("e", 64), Files: []gitx.WorktreeFile{f}}}
}
func TestGitWorktreeCompareClient(t *testing.T) {
	p := rpc.GitWorktreeCompareParams{Path: "/target/repo", Expected: testGitIdentity(), Revision: "HEAD"}
	for _, kind := range []string{"file", "symlink", "gitlink", "directory", "missing"} {
		t.Run(kind, func(t *testing.T) {
			calls := 0
			c := fileTestClient(t, func(call rpc.DesktopCall) (any, error) {
				calls++
				if call.Method != "git.worktreeCompare" || call.Sequence != 0 {
					t.Fatalf("call %+v", call)
				}
				var got rpc.GitWorktreeCompareParams
				if err := rpc.Decode(call.Params, &got); err != nil || got != p {
					t.Fatalf("params %+v %v", got, err)
				}
				return clientWorktreeResult(p, kind), nil
			})
			out, err := c.GitWorktreeCompare(t.Context(), p)
			if err != nil || len(out.Files) != 1 || calls != 1 {
				t.Fatalf("out %+v calls=%d err=%v", out, calls, err)
			}
		})
	}
}
func TestGitWorktreeCompareClientRejects(t *testing.T) {
	p := rpc.GitWorktreeCompareParams{Path: "/target/repo", Expected: testGitIdentity(), Revision: "HEAD"}
	for name, mutate := range map[string]func(*rpc.GitWorktreeCompareResult){
		"identity":  func(r *rpc.GitWorktreeCompareResult) { r.Identity.RootInode++ },
		"revision":  func(r *rpc.GitWorktreeCompareResult) { r.Revision = "other" },
		"commit":    func(r *rpc.GitWorktreeCompareResult) { r.Commit = strings.Repeat("0", 40) },
		"baseline":  func(r *rpc.GitWorktreeCompareResult) { r.Baseline = "bad" },
		"nil":       func(r *rpc.GitWorktreeCompareResult) { r.Files = nil },
		"duplicate": func(r *rpc.GitWorktreeCompareResult) { r.Files = append(r.Files, r.Files[0]) },
		"path":      func(r *rpc.GitWorktreeCompareResult) { r.Files[0].Version.Path = "/outside/a" },
		"kind":      func(r *rpc.GitWorktreeCompareResult) { r.Files[0].Kind = "unknown" },
		"hash":      func(r *rpc.GitWorktreeCompareResult) { r.Files[0].Version.Hash = "bad" },
		"mode":      func(r *rpc.GitWorktreeCompareResult) { r.Files[0].Version.Stamp.Mode = os.ModeSymlink },
		"old":       func(r *rpc.GitWorktreeCompareResult) { r.Files[0].BaseMode = "160001" },
		"oid":       func(r *rpc.GitWorktreeCompareResult) { r.Files[0].OID = strings.Repeat("c", 40) },
	} {
		t.Run(name, func(t *testing.T) {
			calls := 0
			out := clientWorktreeResult(p, "file")
			mutate(&out)
			c := fileTestClient(t, func(rpc.DesktopCall) (any, error) { calls++; return out, nil })
			_, err := c.GitWorktreeCompare(t.Context(), p)
			if !errors.Is(err, gitx.ErrWorktreeOutput) || calls != 1 {
				t.Fatalf("calls=%d err=%v", calls, err)
			}
		})
	}
	calls := 0
	c := fileTestClient(t, func(rpc.DesktopCall) (any, error) {
		calls++
		return nil, rpc.Errorf(rpc.CodeNotFound, "unknown method")
	})
	if _, err := c.GitWorktreeCompare(t.Context(), p); err == nil || calls != 1 {
		t.Fatalf("calls=%d err=%v", calls, err)
	}
	c.Desktop = nil
	if _, err := c.GitWorktreeCompare(t.Context(), p); err == nil || calls != 1 {
		t.Fatalf("identity calls=%d err=%v", calls, err)
	}
}
