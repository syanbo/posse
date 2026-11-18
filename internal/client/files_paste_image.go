package client

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"os"
	"path/filepath"
	"strings"

	"posse/internal/files"
	"posse/internal/rpc"
)

func (c *Client) PasteImage(ctx context.Context, data []byte) (string, error) {
	if c.Desktop == nil {
		return "", rpc.Errorf(rpc.CodeReadOnly, "desktop identity required for image paste")
	}
	if err := ctx.Err(); err != nil {
		return "", err
	}
	if len(data) > files.MaxSize {
		return "", rpc.Errorf(rpc.CodeBadRequest, "file exceeds the maximum size of %d bytes", files.MaxSize)
	}
	sum := sha256.Sum256(data)
	var out rpc.FilePasteImageResult
	if err := c.Call(ctx, "files.pasteImage", rpc.FilePasteImageParams{Data: data, Hash: hex.EncodeToString(sum[:])}, &out); err != nil {
		return "", err
	}
	if !filepath.IsAbs(out.Path) || strings.ContainsRune(out.Path, 0) {
		return "", errors.New("invalid image paste response")
	}
	return out.Path, nil
}

// PasteFile 上传本地普通文件到目标临时目录并返回绝对路径；读取前后核对完整版本。
func (c *Client) PasteFile(ctx context.Context, localPath string) (string, error) {
	if c.Desktop == nil {
		return "", rpc.Errorf(rpc.CodeReadOnly, "desktop identity required for file saves")
	}
	if err := ctx.Err(); err != nil {
		return "", err
	}
	before, err := files.ReadVersion(ctx, localPath)
	if err != nil {
		return "", err
	}
	if before.Stamp.Missing {
		return "", &os.PathError{Op: "open", Path: localPath, Err: os.ErrNotExist}
	}
	data, err := readUploadSource(ctx, localPath, before)
	if err != nil {
		return "", err
	}
	var out rpc.FilePasteImageResult
	if err := c.Call(ctx, "files.pasteFile", rpc.FilePasteFileParams{
		Name: filepath.Base(localPath),
		Data: data,
		Hash: before.Hash,
	}, &out); err != nil {
		return "", err
	}
	if !filepath.IsAbs(out.Path) || strings.ContainsRune(out.Path, 0) {
		return "", errors.New("invalid file paste response")
	}
	return out.Path, nil
}
