package client

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"

	"posse/internal/files"
	"posse/internal/rpc"
)

// UploadFile 的 expected 来自目标 FileVersion；未知结果由 SaveFile 保留操作引用，不重发。
func (c *Client) UploadFile(ctx context.Context, localPath, targetPath string, expected files.Version) (rpc.FileSaveResult, error) {
	if err := ctx.Err(); err != nil {
		return rpc.FileSaveResult{}, err
	}
	if c.Desktop == nil {
		return rpc.FileSaveResult{}, rpc.Errorf(rpc.CodeReadOnly, "desktop identity required for file saves")
	}
	before, err := files.ReadVersion(ctx, localPath)
	if err != nil {
		return rpc.FileSaveResult{}, err
	}
	if before.Stamp.Missing {
		return rpc.FileSaveResult{}, &os.PathError{Op: "open", Path: localPath, Err: os.ErrNotExist}
	}
	data, err := readUploadSource(ctx, localPath, before)
	if err != nil {
		return rpc.FileSaveResult{}, err
	}
	return c.SaveFile(ctx, rpc.FileSaveParams{Path: targetPath, Expected: expected, Data: data, Hash: before.Hash})
}

func readUploadSource(ctx context.Context, path string, before files.Version) ([]byte, error) {
	data := make([]byte, 0, int(before.Stamp.Size))
	for {
		r, err := files.ReadResourceRange(ctx, filepath.Dir(before.Path), filepath.Base(before.Path), int64(len(data)), files.RangeLimit, &before.Stamp)
		if err != nil {
			return nil, err
		}
		data = append(data, r.Data...)
		if r.EOF {
			break
		}
	}
	// 原地改写并恢复 mtime 可以绕过磁盘戳；前后完整版本和实际读到的摘要都必须一致。
	after, err := files.ReadVersion(ctx, path)
	if err != nil {
		return nil, err
	}
	sum := sha256.Sum256(data)
	if !before.Same(after) || int64(len(data)) != before.Stamp.Size || hex.EncodeToString(sum[:]) != before.Hash {
		return nil, files.ErrChanged
	}
	return data, ctx.Err()
}

// DownloadFile 的 expected 来自本机 ReadVersion；目标完整校验成功后才开始本机条件原子写入。
func (c *Client) DownloadFile(ctx context.Context, targetPath, localPath string, expected files.Version) error {
	if !filepath.IsAbs(targetPath) || strings.ContainsRune(targetPath, 0) {
		return errors.New("file path must be absolute and contain no NUL")
	}
	// 不清理 link/..，让目标按真实软链语义解析路径。
	data, _, err := c.ReadResource(ctx, "/", strings.TrimPrefix(targetPath, "/"), files.MaxSize)
	if err != nil {
		return err
	}
	_, err = files.WriteFileCoordinated(ctx, localPath, expected, func(w io.Writer) error {
		_, err := w.Write(data)
		return err
	})
	return err
}
