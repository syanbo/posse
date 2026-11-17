package client

import (
	"context"
	"errors"
	"path/filepath"
	"strings"

	"posse/internal/files"
	"posse/internal/rpc"
)

func (c *Client) FileEntry(ctx context.Context, path string) (files.EntryVersion, error) {
	var out files.EntryVersion
	if err := c.Call(ctx, "files.entry", rpc.FileVersionParams{Path: path}, &out); err != nil {
		return out, fileReadError(path, err)
	}
	if !filepath.IsAbs(out.Path) || strings.ContainsRune(out.Path, 0) || out.Stamp.Missing {
		return files.EntryVersion{}, errors.New("invalid file entry response")
	}
	return out, nil
}

func (c *Client) TrashFile(ctx context.Context, p rpc.FileTrashParams) (rpc.FileTrashResult, error) {
	var out rpc.FileTrashResult
	if c.Desktop == nil {
		return out, rpc.Errorf(rpc.CodeReadOnly, "desktop identity required for file trash")
	}
	err := c.Call(ctx, "files.trash", p, &out)
	return out, err
}
