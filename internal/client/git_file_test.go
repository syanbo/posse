package client

import (
	"bytes"
	"context"
	"crypto/sha1"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"hash"
	"strings"
	"testing"

	"posse/internal/files"
	"posse/internal/gitx"
	"posse/internal/rpc"
)

func blobTestOID(data []byte, sha256Object bool) string {
	var h hash.Hash = sha1.New()
	if sha256Object {
		h = sha256.New()
	}
	fmt.Fprintf(h, "blob %d\x00", len(data))
	h.Write(data)
	return hex.EncodeToString(h.Sum(nil))
}
func clientGitFileParams() rpc.GitFileParams {
	return rpc.GitFileParams{Path: "/target/repo", Expected: testGitIdentity(), File: "a", Source: gitx.FileCommit, Revision: "HEAD"}
}
func clientGitVersion(p rpc.GitFileParams, data []byte, sha256Object bool) rpc.GitFileResult {
	return rpc.GitFileResult{Identity: p.Expected, File: gitx.FileVersion{Source: p.Source, Revision: p.Revision, Commit: strings.Repeat("a", 40), Path: p.File, OID: blobTestOID(data, sha256Object), Size: int64(len(data)), Binary: files.IsBinary(data)}}
}
func TestReadGitFileClient(t *testing.T) {
	for _, kind := range []string{"missing", "empty", "binary", "multi", "sha256", "corrupt"} {
		t.Run(kind, func(t *testing.T) {
			p := clientGitFileParams()
			data := []byte{}
			if kind == "binary" {
				data = []byte{0, 255, 'a'}
			}
			if kind == "multi" || kind == "sha256" || kind == "corrupt" {
				data = bytes.Repeat([]byte("abcdefgh"), (files.RangeLimit*2+16)/8)
			}
			meta := clientGitVersion(p, data, kind == "sha256")
			if kind == "missing" {
				meta.File.Missing = true
				meta.File.OID = ""
			}
			calls := 0
			c := fileTestClient(t, func(call rpc.DesktopCall) (any, error) {
				calls++
				if call.Sequence != 0 {
					t.Fatal("write sequence")
				}
				switch call.Method {
				case "git.file":
					var got rpc.GitFileParams
					if err := rpc.Decode(call.Params, &got); err != nil || got != p {
						t.Fatalf("params: %+v %v", got, err)
					}
					return meta, nil
				case "git.blobRange":
					var q rpc.GitBlobRangeParams
					if err := rpc.Decode(call.Params, &q); err != nil {
						t.Fatal(err)
					}
					if q.Expected != p.Expected || q.OID != meta.File.OID || q.Path != p.Path || q.Limit != files.RangeLimit {
						t.Fatalf("range params %+v", q)
					}
					end := min(int64(len(data)), q.Offset+int64(q.Limit))
					b := append([]byte{}, data[q.Offset:end]...)
					if kind == "corrupt" && q.Offset > 0 {
						b[0] ^= 1
					}
					return rpc.GitBlobRangeResult{Identity: p.Expected, OID: q.OID, Size: int64(len(data)), Offset: q.Offset, Data: b, EOF: end == int64(len(data))}, nil
				}
				t.Fatalf("unexpected %s", call.Method)
				return nil, nil
			})
			got, b, err := c.ReadGitFile(t.Context(), p)
			if kind == "corrupt" {
				if err == nil || !strings.Contains(err.Error(), "checksum") {
					t.Fatalf("cross-range corruption: %v", err)
				}
				if b != nil || got.File.OID != "" {
					t.Fatal("partial result leaked")
				}
				return
			}
			if err != nil || got != meta || !bytes.Equal(b, data) {
				t.Fatalf("result %+v len=%d err=%v", got, len(b), err)
			}
			if kind == "missing" && calls != 1 {
				t.Fatal("missing requested blob")
			}
			if (kind == "multi" || kind == "sha256") && calls != 4 {
				t.Fatalf("calls=%d", calls)
			}
		})
	}
}

func TestGitFileClientRejects(t *testing.T) {
	p := clientGitFileParams()
	for _, remote := range []bool{false, true} {
		c := &Client{Remote: remote, Socket: "/missing"}
		_, _, err := c.ReadGitFile(t.Context(), p)
		var re *rpc.Error
		if !errors.As(err, &re) || re.Code != rpc.CodeReadOnly {
			t.Fatalf("identity: %v", err)
		}
	}
	calls := 0
	c := fileTestClient(t, func(rpc.DesktopCall) (any, error) { calls++; return nil, rpc.Errorf(rpc.CodeUnknown, "unsupported") })
	for _, change := range []func(*rpc.GitFileParams){func(p *rpc.GitFileParams) { p.Path = "relative" }, func(p *rpc.GitFileParams) { p.Expected.RootInode = 0 }, func(p *rpc.GitFileParams) { p.Revision = "HEAD:a" }, func(p *rpc.GitFileParams) { p.File = "../a" }} {
		q := p
		change(&q)
		if _, _, err := c.ReadGitFile(t.Context(), q); err == nil {
			t.Fatal("bad request accepted")
		}
	}
	if calls != 0 {
		t.Fatal("invalid request dispatched")
	}
	if _, _, err := c.ReadGitFile(t.Context(), p); err == nil || calls != 1 {
		t.Fatalf("fallback/replay %d %v", calls, err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if _, _, err := c.ReadGitFile(ctx, p); err == nil {
		t.Fatal("cancelled request accepted")
	}
}

func TestGitFileClientInvalidResponses(t *testing.T) {
	p := clientGitFileParams()
	for _, change := range []func(*rpc.GitFileResult){
		func(r *rpc.GitFileResult) { r.Identity.RootInode++ }, func(r *rpc.GitFileResult) { r.File.Path = "other" }, func(r *rpc.GitFileResult) { r.File.Source = gitx.FileIndex }, func(r *rpc.GitFileResult) { r.File.Revision = "main" }, func(r *rpc.GitFileResult) { r.File.Commit = "" }, func(r *rpc.GitFileResult) { r.File.OID = "bad" }, func(r *rpc.GitFileResult) { r.File.Size = files.MaxSize + 1 }, func(r *rpc.GitFileResult) { r.File.Missing = true }, func(r *rpc.GitFileResult) { r.File.Unborn = true },
		func(r *rpc.GitFileResult) {
			r.File.Unborn = true
			r.File.Missing = true
			r.File.OID = ""
			r.File.Size = 0
		},
	} {
		meta := clientGitVersion(p, []byte("a"), false)
		change(&meta)
		c := fileTestClient(t, func(rpc.DesktopCall) (any, error) { return meta, nil })
		if _, err := c.GitFile(t.Context(), p); err == nil {
			t.Fatalf("bad metadata %+v", meta)
		}
	}
	for _, change := range []func(*rpc.GitBlobRangeResult){
		func(r *rpc.GitBlobRangeResult) { r.Identity.RootInode++ }, func(r *rpc.GitBlobRangeResult) { r.OID = strings.Repeat("b", 40) }, func(r *rpc.GitBlobRangeResult) { r.Offset++ }, func(r *rpc.GitBlobRangeResult) { r.Size++ }, func(r *rpc.GitBlobRangeResult) { r.Data = nil }, func(r *rpc.GitBlobRangeResult) { r.EOF = false },
	} {
		q := rpc.GitBlobRangeParams{Path: p.Path, Expected: p.Expected, OID: strings.Repeat("a", 40), Limit: files.RangeLimit}
		out := rpc.GitBlobRangeResult{Identity: p.Expected, OID: q.OID, Size: 1, Data: []byte("a"), EOF: true}
		change(&out)
		c := fileTestClient(t, func(rpc.DesktopCall) (any, error) { return out, nil })
		if _, err := c.GitBlobRange(t.Context(), q); err == nil {
			t.Fatalf("bad range %+v", out)
		}
	}
}
