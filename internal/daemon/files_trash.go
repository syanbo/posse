package daemon

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"

	"posse/internal/files"
	"posse/internal/rpc"
	"posse/internal/trash"
)

func fileOperationError(ctx context.Context, err error) error {
	if err == nil {
		return nil
	}
	if ctx.Err() != nil {
		return ctx.Err()
	}
	return fileRPCError(err)
}

func (d *Daemon) filesEntry(ctx context.Context, raw json.RawMessage) (any, error) {
	var p rpc.FileVersionParams
	if err := rpc.Decode(raw, &p); err != nil {
		return nil, err
	}
	if err := filePath(p.Path); err != nil {
		return nil, err
	}
	version, err := files.ReadEntryVersion(ctx, p.Path)
	return version, fileOperationError(ctx, err)
}

func (d *Daemon) filesTrash(ctx context.Context, raw json.RawMessage) (any, error) {
	return d.trashFileEntry(ctx, raw, trash.Move)
}

func (d *Daemon) trashFileEntry(ctx context.Context, raw json.RawMessage, move func(string) (string, error)) (any, error) {
	request, ok := ctx.Value(desktopCallContext{}).(desktopRequestContext)
	if !ok || request.readOnly {
		return nil, rpc.Errorf(rpc.CodeReadOnly, "desktop identity required for file trash")
	}
	var p rpc.FileTrashParams
	if err := rpc.Decode(raw, &p); err != nil {
		return nil, err
	}
	if err := filePath(p.Path); err != nil {
		return nil, err
	}
	if err := filePath(p.Expected.Path); err != nil {
		return nil, err
	}
	if p.Expected.Stamp.Missing {
		return nil, rpc.Errorf(rpc.CodeBadRequest, "invalid file trash baseline")
	}
	entry, err := files.ReadEntryVersion(ctx, p.Path)
	if err != nil {
		return nil, fileOperationError(ctx, err)
	}
	if !entry.Same(p.Expected) {
		return nil, fileRPCError(files.ErrEntryChanged)
	}
	// 最后一级链接只锁所在目录；保留原始父路径，才能同时保护途经的目录链接。
	parent, _ := filepath.Split(strings.TrimRight(p.Path, string(filepath.Separator)))
	paths := []string{parent}
	if entry.Stamp.Mode&os.ModeSymlink == 0 {
		paths = append(paths, entry.Path)
	}
	release, err := d.lockWritePaths(ctx, paths)
	if err != nil {
		return nil, fileOperationError(ctx, err)
	}
	defer release()
	destination, err := files.TrashEntryChecked(ctx, p.Path, p.Expected, move)
	if err != nil {
		return nil, fileOperationError(ctx, err)
	}
	return rpc.FileTrashResult{Destination: destination}, nil
}
