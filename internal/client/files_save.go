package client

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"

	"posse/internal/files"
	"posse/internal/rpc"
)

func (c *Client) FileVersion(ctx context.Context, path string) (files.Version, error) {
	var out files.Version
	err := c.Call(ctx, "files.version", rpc.FileVersionParams{Path: path}, &out)
	return out, fileReadError(path, err)
}

// ReadFileVersion 在分段读取后核对完整原始字节指纹，避免相同磁盘戳的中途改写形成混合正文。
func (c *Client) ReadFileVersion(ctx context.Context, path string) (files.Content, files.Version, error) {
	content, hash, err := c.ReadFile(ctx, path)
	if err != nil && !os.IsNotExist(err) {
		return files.Content{}, files.Version{}, err
	}
	if err == nil && content.Kind != files.Text {
		return content, files.Version{}, nil
	}
	version, versionErr := c.FileVersion(ctx, path)
	if versionErr != nil {
		return files.Content{}, files.Version{}, versionErr
	}
	if !filepath.IsAbs(version.Path) || strings.ContainsRune(version.Path, 0) {
		return files.Content{}, files.Version{}, errors.New("invalid file version response")
	}
	if err != nil {
		if !version.Stamp.Missing {
			return files.Content{}, files.Version{}, files.ErrChanged
		}
		return files.Content{Stamp: version.Stamp}, version, err
	}
	if !content.Stamp.Same(version.Stamp) || content.Stamp.Mode != version.Stamp.Mode || hash != version.Hash {
		return files.Content{}, files.Version{}, files.ErrChanged
	}
	return content, version, nil
}

// SaveFile 的未知结果保留原操作身份；调用方只能查询及重读，不能自动重发正文。
func (c *Client) SaveFile(ctx context.Context, p rpc.FileSaveParams) (rpc.FileSaveResult, error) {
	var out rpc.FileSaveResult
	if c.Desktop == nil {
		return out, rpc.Errorf(rpc.CodeReadOnly, "desktop identity required for file saves")
	}
	err := c.Call(ctx, "files.save", p, &out)
	return out, err
}
