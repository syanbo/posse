package client

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
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
