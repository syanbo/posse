package client

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"os"

	"posse/internal/files"
	"posse/internal/rpc"
)

func (c *Client) FileStats(ctx context.Context, paths []string) ([]rpc.FileStatResult, error) {
	var out []rpc.FileStatResult
	err := c.Call(ctx, "files.stat", rpc.FilePathsParams{Paths: paths}, &out)
	return out, err
}

func (c *Client) ReadDirectories(ctx context.Context, paths []string) ([]rpc.FileDirectoryResult, error) {
	var out []rpc.FileDirectoryResult
	err := c.Call(ctx, "files.readDirs", rpc.FilePathsParams{Paths: paths}, &out)
	return out, err
}

func (c *Client) ReadFileRange(ctx context.Context, p rpc.FileRangeParams) (rpc.FileRangeResult, error) {
	var out rpc.FileRangeResult
	err := c.Call(ctx, "files.readRange", p, &out)
	return out, fileReadError(p.Path, err)
}

func fileReadError(path string, err error) error {
	if err == nil {
		return nil
	}
	var re *rpc.Error
	if errors.As(err, &re) {
		switch re.Code {
		case rpc.CodeNotFound:
			return &os.PathError{Op: "read", Path: path, Err: os.ErrNotExist}
		case rpc.CodePermissionDenied:
			return &os.PathError{Op: "read", Path: path, Err: os.ErrPermission}
		}
	}
	return err
}

// ReadFile 保留桌面文件类型与大小限制；分段在目标核对同一磁盘版本，原始字节经 base64 传输。
// 指纹对应返回的文本，供后续条件保存核对，不把读到一半的内容交给编辑器。
func (c *Client) ReadFile(ctx context.Context, path string) (files.Content, string, error) {
	stats, err := c.FileStats(ctx, []string{path})
	if err != nil {
		return files.Content{}, "", err
	}
	if len(stats) != 1 || stats[0].Path != path {
		return files.Content{}, "", errors.New("invalid file status response")
	}
	stat := stats[0]
	if stat.Error != nil {
		return files.Content{}, "", fileReadError(path, stat.Error)
	}
	st := stat.Stamp
	out := files.Content{Stamp: st}
	if st.Missing {
		return out, "", &os.PathError{Op: "read", Path: path, Err: os.ErrNotExist}
	}
	if st.Mode.IsDir() {
		out.Kind = files.Directory
		return out, "", nil
	}
	if !st.Mode.IsRegular() {
		return out, "", errors.New("not a regular file")
	}
	if st.Size < 0 {
		return out, "", errors.New("invalid file status response")
	}
	if st.Size > files.MaxSize {
		out.Kind = files.TooLarge
		return out, "", nil
	}
	var buf bytes.Buffer
	for {
		r, err := c.ReadFileRange(ctx, rpc.FileRangeParams{Path: path, Offset: int64(buf.Len()), Limit: files.RangeLimit, Expected: &st})
		if err != nil {
			return out, "", err
		}
		if !r.Stamp.Same(st) || len(r.Data) > files.RangeLimit || int64(buf.Len()+len(r.Data)) > st.Size {
			return out, "", files.ErrChanged
		}
		if buf.Len() == 0 && files.IsBinary(r.Data) {
			out.Kind = files.Binary
			return out, "", nil
		}
		buf.Write(r.Data)
		if r.EOF {
			if int64(buf.Len()) != st.Size {
				return out, "", files.ErrChanged
			}
			break
		}
		if len(r.Data) == 0 {
			return out, "", errors.New("file range response made no progress")
		}
	}
	sum := sha256.Sum256(buf.Bytes())
	out.Text, out.ReadOnly = buf.String(), buf.Len() > files.ReadOnlySize
	return out, hex.EncodeToString(sum[:]), nil
}
