package client

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"path/filepath"
	"strings"

	"posse/internal/files"
	"posse/internal/rpc"
)

// ReadResource 只交付完整一致的目标字节；限额由调用方按资源用途指定，不回退到本机读取。
func (c *Client) ReadResource(ctx context.Context, root, path string, maxBytes int64) ([]byte, files.Stamp, error) {
	if err := ctx.Err(); err != nil {
		return nil, files.Stamp{}, err
	}
	if c.Desktop == nil {
		return nil, files.Stamp{}, rpc.Errorf(rpc.CodeReadOnly, "desktop identity required for resource reads")
	}
	if maxBytes < 0 {
		return nil, files.Stamp{}, errors.New("resource size limit must not be negative")
	}
	var data []byte
	var expected *files.Stamp
	for {
		if err := ctx.Err(); err != nil {
			return nil, files.Stamp{}, err
		}
		var r rpc.FileRangeResult
		p := rpc.FileResourceRangeParams{Root: root, Path: path, Offset: int64(len(data)), Limit: files.RangeLimit, Expected: expected}
		if err := c.Call(ctx, "files.resourceRange", p, &r); err != nil {
			return nil, files.Stamp{}, fileReadError(path, err)
		}
		if err := ctx.Err(); err != nil {
			return nil, files.Stamp{}, err
		}
		if r.Stamp.Missing || r.Stamp.Size < 0 || !r.Stamp.Mode.IsRegular() {
			return nil, files.Stamp{}, errors.New("invalid resource range response")
		}
		if r.Stamp.Size > maxBytes {
			return nil, files.Stamp{}, fmt.Errorf("file exceeds the maximum size of %d bytes", maxBytes)
		}
		if expected == nil {
			st := r.Stamp
			expected = &st
		} else if !r.Stamp.Same(*expected) || r.Stamp.Mode != expected.Mode {
			return nil, files.Stamp{}, files.ErrChanged
		}
		if len(r.Data) > files.RangeLimit || int64(len(data)) > expected.Size || int64(len(r.Data)) > expected.Size-int64(len(data)) {
			return nil, files.Stamp{}, errors.New("invalid resource range response")
		}
		end := int64(len(data)) + int64(len(r.Data))
		if r.EOF != (end == expected.Size) {
			return nil, files.Stamp{}, errors.New("invalid resource range response")
		}
		if !r.EOF && len(r.Data) == 0 {
			return nil, files.Stamp{}, errors.New("file range response made no progress")
		}
		data = append(data, r.Data...)
		if r.EOF {
			break
		}
	}
	// 同长度原地改写并恢复 mtime 不会改变 Stamp；完整摘要不能由分段戳代替。
	var version files.Version
	p := rpc.FileResourceVersionParams{Root: root, Path: path}
	if err := c.Call(ctx, "files.resourceVersion", p, &version); err != nil {
		return nil, files.Stamp{}, fileReadError(path, err)
	}
	if err := ctx.Err(); err != nil {
		return nil, files.Stamp{}, err
	}
	if !filepath.IsAbs(version.Path) || strings.ContainsRune(version.Path, 0) {
		return nil, files.Stamp{}, errors.New("invalid file version response")
	}
	sum := sha256.Sum256(data)
	if !expected.Same(version.Stamp) || expected.Mode != version.Stamp.Mode || hex.EncodeToString(sum[:]) != version.Hash {
		return nil, files.Stamp{}, files.ErrChanged
	}
	if err := ctx.Err(); err != nil {
		return nil, files.Stamp{}, err
	}
	return data, *expected, nil
}
