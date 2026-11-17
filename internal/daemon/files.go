package daemon

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"

	"posse/internal/files"
	"posse/internal/rpc"
)

func filePath(path string) error {
	if !filepath.IsAbs(path) || strings.ContainsRune(path, 0) || len(path) > 4096 {
		return rpc.Errorf(rpc.CodeBadRequest, "file path must be absolute and contain no NUL")
	}
	return nil
}

func filePaths(raw json.RawMessage) ([]string, error) {
	var p rpc.FilePathsParams
	if err := rpc.Decode(raw, &p); err != nil {
		return nil, err
	}
	if len(p.Paths) == 0 || len(p.Paths) > rpc.FileBatchLimit {
		return nil, rpc.Errorf(rpc.CodeBadRequest, "file request must contain between 1 and %d paths", rpc.FileBatchLimit)
	}
	for _, path := range p.Paths {
		if err := filePath(path); err != nil {
			return nil, err
		}
	}
	return p.Paths, nil
}

func fileRPCError(err error) *rpc.Error {
	if err == nil {
		return nil
	}
	var re *rpc.Error
	if errors.As(err, &re) {
		return re
	}
	code := rpc.CodeInternal
	switch {
	case errors.Is(err, files.ErrDirectoryLimit):
		code = rpc.CodeBadRequest
	case errors.Is(err, files.ErrChanged), errors.Is(err, files.ErrSaveConflict), errors.Is(err, files.ErrEntryChanged):
		code = rpc.CodeConflict
	case os.IsNotExist(err):
		code = rpc.CodeNotFound
	case os.IsPermission(err):
		code = rpc.CodePermissionDenied
	}
	return &rpc.Error{Code: code, Message: err.Error()}
}

func (d *Daemon) filesStat(ctx context.Context, raw json.RawMessage) (any, error) {
	paths, err := filePaths(raw)
	if err != nil {
		return nil, err
	}
	out := make([]rpc.FileStatResult, 0, len(paths))
	for _, path := range paths {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		st, err := files.Stat(path)
		out = append(out, rpc.FileStatResult{Path: path, Stamp: st, Error: fileRPCError(err)})
	}
	return out, nil
}

func (d *Daemon) filesReadDirs(ctx context.Context, raw json.RawMessage) (any, error) {
	paths, err := filePaths(raw)
	if err != nil {
		return nil, err
	}
	out, err := readDirectories(ctx, paths, 4<<20, files.ReadDirContext)
	if err != nil {
		return nil, err
	}
	return out, nil
}

func readDirectories(ctx context.Context, paths []string, budget int, read func(context.Context, string, int) ([]files.Entry, files.Stamp, int, error)) ([]rpc.FileDirectoryResult, error) {
	out := make([]rpc.FileDirectoryResult, 0, len(paths))
	for _, path := range paths {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		if budget <= 0 {
			out = append(out, rpc.FileDirectoryResult{Path: path, Error: fileRPCError(files.ErrDirectoryLimit)})
			continue
		}
		entries, st, used, err := read(ctx, path, budget)
		if ctxErr := ctx.Err(); ctxErr != nil {
			return nil, ctxErr
		}
		if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
			return nil, err
		}
		if err != nil {
			out = append(out, rpc.FileDirectoryResult{Path: path, Error: fileRPCError(err)})
			continue
		}
		budget -= used
		out = append(out, rpc.FileDirectoryResult{Path: path, Entries: entries, Stamp: st})
	}
	return out, nil
}

func (d *Daemon) filesReadRange(ctx context.Context, raw json.RawMessage) (any, error) {
	var p rpc.FileRangeParams
	if err := rpc.Decode(raw, &p); err != nil {
		return nil, err
	}
	if err := filePath(p.Path); err != nil {
		return nil, err
	}
	if p.Offset < 0 || p.Limit < 1 || p.Limit > files.RangeLimit {
		return nil, rpc.Errorf(rpc.CodeBadRequest, "invalid file range; maximum length is %d bytes", files.RangeLimit)
	}
	r, err := files.ReadRange(ctx, p.Path, p.Offset, p.Limit, p.Expected)
	if ctxErr := ctx.Err(); ctxErr != nil {
		return nil, ctxErr
	}
	if err != nil {
		return nil, fileRPCError(err)
	}
	return rpc.FileRangeResult{Data: r.Data, Stamp: r.Stamp, EOF: r.EOF}, nil
}
