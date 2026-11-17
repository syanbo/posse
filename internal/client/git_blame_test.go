package client

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"posse/internal/files"
	"posse/internal/gitx"
	"posse/internal/rpc"
)

func clientBlameResult(p rpc.GitBlameParams, content string) rpc.GitBlameResult {
	sum := sha256.Sum256([]byte(content))
	out := rpc.GitBlameResult{Identity: p.Expected, File: p.File, Head: strings.Repeat("a", 40), Version: files.Version{Path: filepath.Join(p.Expected.Root, p.File), Stamp: files.Stamp{Ino: 1, Size: int64(len(content)), Mode: 0600}, Hash: hex.EncodeToString(sum[:])}, Lines: []gitx.BlameLine{}}
	if content != "" {
		for _, text := range strings.Split(strings.TrimSuffix(content, "\n"), "\n") {
			out.Lines = append(out.Lines, gitx.BlameLine{Hash: strings.Repeat("0", 40), Text: text, Time: time.Unix(1700000000, 0)})
		}
	}
	return out
}

func TestGitBlameClient(t *testing.T) {
	p := rpc.GitBlameParams{Path: "/target/repo", Expected: testGitIdentity(), File: "a"}
	for _, content := range []string{"", "\n", "one\n", "one\r\ntwo", "one\r\ntwo\r\n", "last\r"} {
		t.Run(content, func(t *testing.T) {
			want := clientBlameResult(p, content)
			calls := 0
			c := fileTestClient(t, func(call rpc.DesktopCall) (any, error) {
				calls++
				if call.Method != "git.blame" || call.Sequence != 0 {
					t.Fatalf("call %+v", call)
				}
				var got rpc.GitBlameParams
				if err := rpc.Decode(call.Params, &got); err != nil || got != p {
					t.Fatalf("params %+v %v", got, err)
				}
				return want, nil
			})
			got, err := c.GitBlame(t.Context(), p)
			if err != nil || got.Version != want.Version || calls != 1 {
				t.Fatalf("result %+v calls %d err %v", got, calls, err)
			}
		})
	}
}

func TestGitBlameClientRejects(t *testing.T) {
	p := rpc.GitBlameParams{Path: "/target/repo", Expected: testGitIdentity(), File: "a"}
	for name, change := range map[string]func(*rpc.GitBlameResult){
		"identity":  func(r *rpc.GitBlameResult) { r.Identity.RootInode++ },
		"file":      func(r *rpc.GitBlameResult) { r.File = "other" },
		"head":      func(r *rpc.GitBlameResult) { r.Head = strings.Repeat("0", 40) },
		"path":      func(r *rpc.GitBlameResult) { r.Version.Path = "/outside/a" },
		"hash":      func(r *rpc.GitBlameResult) { r.Version.Hash = strings.Repeat("b", 64) },
		"size":      func(r *rpc.GitBlameResult) { r.Version.Stamp.Size++ },
		"lines":     func(r *rpc.GitBlameResult) { r.Lines = nil },
		"line-hash": func(r *rpc.GitBlameResult) { r.Lines[0].Hash = strings.Repeat("0", 64) },
		"nul":       func(r *rpc.GitBlameResult) { r.Lines[0].Text = "\x00" },
		"utf8":      func(r *rpc.GitBlameResult) { r.Lines[0].Text = "\xff" },
		"lf":        func(r *rpc.GitBlameResult) { r.Lines[0].Text = "one\n" },
	} {
		t.Run(name, func(t *testing.T) {
			out := clientBlameResult(p, "one\n")
			change(&out)
			calls := 0
			c := fileTestClient(t, func(rpc.DesktopCall) (any, error) { calls++; return out, nil })
			_, err := c.GitBlame(t.Context(), p)
			if !errors.Is(err, gitx.ErrBlameOutput) || calls != 1 {
				t.Fatalf("calls %d err %v", calls, err)
			}
		})
	}
	calls := 0
	c := fileTestClient(t, func(rpc.DesktopCall) (any, error) {
		calls++
		return nil, rpc.Errorf(rpc.CodeNotFound, "unknown method")
	})
	if _, err := c.GitBlame(t.Context(), p); err == nil || calls != 1 {
		t.Fatalf("unknown calls=%d err=%v", calls, err)
	}
	c.Desktop = nil
	if _, err := c.GitBlame(t.Context(), p); err == nil || calls != 1 {
		t.Fatalf("identity calls=%d err=%v", calls, err)
	}
}
