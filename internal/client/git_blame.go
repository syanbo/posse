package client

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"path/filepath"
	"strings"

	"posse/internal/gitx"
	"posse/internal/rpc"
)

func (c *Client) GitBlame(ctx context.Context, p rpc.GitBlameParams) (rpc.GitBlameResult, error) {
	var out rpc.GitBlameResult
	if c.Desktop == nil {
		return out, rpc.Errorf(rpc.CodeReadOnly, "desktop identity required for Git queries")
	}
	if !validGitIdentity(p.Expected) || !gitx.ValidBlameFile(p.File) {
		return out, gitx.ErrBlameQuery
	}
	if err := c.gitRead(ctx, "git.blame", p.Path, p, &out); err != nil {
		return rpc.GitBlameResult{}, err
	}
	version := out.Version
	if out.Identity != p.Expected || out.File != p.File || !gitx.ValidBlobOID(out.Head) || strings.Trim(out.Head, "0") == "" || out.Lines == nil || len(out.Lines) > gitx.BlameReadLineLimit || !gitReadPath(version.Path) || filepath.Clean(version.Path) != version.Path || version.Stamp.Missing || !version.Stamp.Mode.IsRegular() || version.Stamp.Ino == 0 || version.Stamp.Size < 0 || version.Stamp.Size > gitx.BlameReadSizeLimit || len(version.Hash) != 64 || !gitx.ValidBlobOID(version.Hash) {
		return rpc.GitBlameResult{}, gitx.ErrBlameOutput
	}
	rel, err := filepath.Rel(out.Identity.Root, version.Path)
	if err != nil || rel == "." || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return rpc.GitBlameResult{}, gitx.ErrBlameOutput
	}
	var data bytes.Buffer
	for i, line := range out.Lines {
		if !gitx.ValidBlameLine(line, len(out.Head)) {
			return rpc.GitBlameResult{}, gitx.ErrBlameOutput
		}
		if i > 0 {
			data.WriteByte('\n')
		}
		if data.Len()+len(line.Text) > gitx.BlameReadSizeLimit {
			return rpc.GitBlameResult{}, gitx.ErrBlameOutput
		}
		data.WriteString(line.Text)
	}
	// 行结构不额外携带末行换行标记，原始版本的长度和摘要共同核对它。
	if len(out.Lines) > 0 && int64(data.Len())+1 == version.Stamp.Size {
		data.WriteByte('\n')
	}
	lineCount := bytes.Count(data.Bytes(), []byte{'\n'})
	if data.Len() > 0 && data.Bytes()[data.Len()-1] != '\n' {
		lineCount++
	}
	if lineCount != len(out.Lines) {
		return rpc.GitBlameResult{}, gitx.ErrBlameOutput
	}
	sum := sha256.Sum256(data.Bytes())
	if int64(data.Len()) != version.Stamp.Size || hex.EncodeToString(sum[:]) != version.Hash {
		return rpc.GitBlameResult{}, gitx.ErrBlameOutput
	}
	return out, ctx.Err()
}
