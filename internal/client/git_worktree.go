package client

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"strings"
	"unicode/utf8"

	"posse/internal/gitx"
	"posse/internal/rpc"
)

func (c *Client) GitWorktreeCompare(ctx context.Context, p rpc.GitWorktreeCompareParams) (rpc.GitWorktreeCompareResult, error) {
	var out rpc.GitWorktreeCompareResult
	if c.Desktop == nil {
		return out, rpc.Errorf(rpc.CodeReadOnly, "desktop identity required for Git queries")
	}
	if !validGitIdentity(p.Expected) || !gitx.ValidWorktreeQuery(p.Revision, p.Baseline) {
		return out, gitx.ErrWorktreeQuery
	}
	if err := c.gitRead(ctx, "git.worktreeCompare", p.Path, p, &out); err != nil {
		return rpc.GitWorktreeCompareResult{}, err
	}
	if out.Identity != p.Expected || out.Revision != p.Revision || !gitx.ValidBlobOID(out.Commit) || strings.Trim(out.Commit, "0") == "" || !gitx.ValidLogBaseline(out.Baseline) || p.Baseline != "" && p.Baseline != out.Baseline || out.Files == nil {
		return rpc.GitWorktreeCompareResult{}, gitx.ErrWorktreeOutput
	}
	seen := map[string]bool{}
	for _, f := range out.Files {
		if !gitx.ValidReadFileChange(f.FileChange) || seen[f.Path] || !validWorktreeFile(p.Expected.Root, len(out.Commit), f) {
			return rpc.GitWorktreeCompareResult{}, gitx.ErrWorktreeOutput
		}
		seen[f.Path] = true
	}
	return out, ctx.Err()
}

func validWorktreeFile(root string, hashLen int, f gitx.WorktreeFile) bool {
	v := f.Version
	if v.Path != filepath.Join(root, filepath.FromSlash(f.Path)) || v.Stamp.Size < 0 {
		return false
	}
	if f.BaseMode == "" {
		if f.BaseOID != "" || f.Status != 'A' {
			return false
		}
	} else {
		if !gitx.ValidBlobOID(f.BaseOID) || len(f.BaseOID) != hashLen || strings.Trim(f.BaseOID, "0") == "" || !strings.Contains("|100644|100755|120000|160000|", "|"+f.BaseMode+"|") {
			return false
		}
	}
	if f.Kind != "gitlink" && (f.OID != "" || f.Dirty) {
		return false
	}
	if f.Kind != "symlink" && f.Link != "" {
		return false
	}
	if f.Kind == "missing" {
		return v.Stamp.Missing && v.Hash == ""
	}
	if v.Stamp.Missing || v.Stamp.Ino == 0 || !gitx.ValidLogBaseline(v.Hash) {
		return false
	}
	switch f.Kind {
	case "file":
		return v.Stamp.Mode.IsRegular()
	case "symlink":
		sum := sha256.Sum256([]byte(f.Link))
		return v.Stamp.Mode.Type() == os.ModeSymlink && utf8.ValidString(f.Link) && !strings.ContainsRune(f.Link, 0) && int64(len(f.Link)) == v.Stamp.Size && hex.EncodeToString(sum[:]) == v.Hash
	case "directory":
		return v.Stamp.Mode.IsDir()
	case "gitlink":
		return v.Stamp.Mode.IsDir() && gitx.ValidBlobOID(f.OID) && len(f.OID) == hashLen && strings.Trim(f.OID, "0") != ""
	}
	return false
}
