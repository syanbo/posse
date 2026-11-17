package client

import (
	"context"
	"crypto/sha1"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"hash"

	"posse/internal/files"
	"posse/internal/gitx"
	"posse/internal/rpc"
)

func (c *Client) GitFile(ctx context.Context, p rpc.GitFileParams) (rpc.GitFileResult, error) {
	var out rpc.GitFileResult
	if c.Desktop == nil {
		return out, rpc.Errorf(rpc.CodeReadOnly, "desktop identity required for Git queries")
	}
	if !validGitIdentity(p.Expected) || !gitx.ValidFileQuery(p.Source, p.Revision, p.File) {
		return out, gitx.ErrFileQuery
	}
	if err := c.gitRead(ctx, "git.file", p.Path, p, &out); err != nil {
		return rpc.GitFileResult{}, err
	}
	v := out.File
	valid := out.Identity == p.Expected && v.Source == p.Source && v.Revision == p.Revision && v.Path == p.File && v.Size >= 0 && v.Size <= files.MaxSize
	if v.Source == gitx.FileIndex {
		valid = valid && v.Commit == "" && !v.Unborn
	} else if v.Unborn {
		valid = valid && v.Missing && v.Revision == "HEAD" && v.Commit == ""
	} else {
		valid = valid && gitx.ValidBlobOID(v.Commit)
	}
	if v.Missing {
		valid = valid && v.OID == "" && v.Size == 0 && !v.Binary
	} else {
		valid = valid && gitx.ValidBlobOID(v.OID) && !v.Unborn && (!v.Binary || v.Size > 0)
	}
	if !valid {
		return rpc.GitFileResult{}, errors.New("invalid Git file response")
	}
	return out, ctx.Err()
}

func (c *Client) GitBlobRange(ctx context.Context, p rpc.GitBlobRangeParams) (rpc.GitBlobRangeResult, error) {
	var out rpc.GitBlobRangeResult
	if c.Desktop == nil {
		return out, rpc.Errorf(rpc.CodeReadOnly, "desktop identity required for Git queries")
	}
	if !validGitIdentity(p.Expected) || !gitx.ValidBlobOID(p.OID) || p.Offset < 0 || p.Offset > files.MaxSize || p.Limit < 1 || p.Limit > files.RangeLimit {
		return out, gitx.ErrFileQuery
	}
	if err := c.gitRead(ctx, "git.blobRange", p.Path, p, &out); err != nil {
		return rpc.GitBlobRangeResult{}, err
	}
	if out.Identity != p.Expected || out.OID != p.OID || out.Offset != p.Offset || out.Size < 0 || out.Size > files.MaxSize || out.Offset > out.Size || int64(len(out.Data)) != min(int64(p.Limit), out.Size-out.Offset) || out.EOF != (out.Offset+int64(len(out.Data)) == out.Size) {
		return rpc.GitBlobRangeResult{}, gitx.ErrBlobResponse
	}
	return out, ctx.Err()
}

// ReadGitFile 的完整摘要覆盖所有分段，不能只凭各段的 OID 标签相信内容。
func (c *Client) ReadGitFile(ctx context.Context, p rpc.GitFileParams) (rpc.GitFileResult, []byte, error) {
	fail := func(err error) (rpc.GitFileResult, []byte, error) { return rpc.GitFileResult{}, nil, err }
	out, err := c.GitFile(ctx, p)
	if err != nil {
		return fail(err)
	}
	if out.File.Missing {
		return out, nil, nil
	}
	v := out.File
	var h hash.Hash = sha1.New()
	if len(v.OID) == 64 {
		h = sha256.New()
	}
	fmt.Fprintf(h, "blob %d\x00", v.Size)
	data := make([]byte, 0, int(v.Size))
	for {
		if err := ctx.Err(); err != nil {
			return fail(err)
		}
		r, err := c.GitBlobRange(ctx, rpc.GitBlobRangeParams{Path: p.Path, Expected: out.Identity, OID: v.OID, Offset: int64(len(data)), Limit: files.RangeLimit})
		if err != nil {
			return fail(err)
		}
		if r.Size != v.Size {
			return fail(gitx.ErrBlobResponse)
		}
		h.Write(r.Data)
		data = append(data, r.Data...)
		if r.EOF {
			break
		}
	}
	if hex.EncodeToString(h.Sum(nil)) != v.OID || files.IsBinary(data) != v.Binary {
		return fail(errors.New("Git blob content checksum does not match"))
	}
	if err := ctx.Err(); err != nil {
		return fail(err)
	}
	return out, data, nil
}
