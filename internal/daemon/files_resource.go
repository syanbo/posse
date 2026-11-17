package daemon

import (
	"context"
	"encoding/json"
	"errors"

	"posse/internal/files"
	"posse/internal/rpc"
)

func (d *Daemon) filesResourceVersion(ctx context.Context, raw json.RawMessage) (any, error) {
	request, ok := ctx.Value(desktopCallContext{}).(desktopRequestContext)
	if !ok || !request.readOnly {
		return nil, rpc.Errorf(rpc.CodeReadOnly, "desktop identity required for resource reads")
	}
	var p rpc.FileResourceVersionParams
	if err := rpc.Decode(raw, &p); err != nil {
		return nil, err
	}
	if err := filePath(p.Root); err != nil {
		return nil, err
	}
	version, err := files.ReadResourceVersion(ctx, p.Root, p.Path)
	if ctxErr := ctx.Err(); ctxErr != nil {
		return nil, ctxErr
	}
	if errors.Is(err, files.ErrResourcePath) {
		return nil, rpc.Errorf(rpc.CodeBadRequest, "%s", err)
	}
	if err != nil {
		return nil, fileRPCError(err)
	}
	return version, nil
}

func (d *Daemon) filesResourceRange(ctx context.Context, raw json.RawMessage) (any, error) {
	request, ok := ctx.Value(desktopCallContext{}).(desktopRequestContext)
	if !ok || !request.readOnly {
		return nil, rpc.Errorf(rpc.CodeReadOnly, "desktop identity required for resource reads")
	}
	var p rpc.FileResourceRangeParams
	if err := rpc.Decode(raw, &p); err != nil {
		return nil, err
	}
	if err := filePath(p.Root); err != nil {
		return nil, err
	}
	if p.Offset < 0 || p.Limit < 1 || p.Limit > files.RangeLimit {
		return nil, rpc.Errorf(rpc.CodeBadRequest, "invalid file range; maximum length is %d bytes", files.RangeLimit)
	}
	r, err := files.ReadResourceRange(ctx, p.Root, p.Path, p.Offset, p.Limit, p.Expected)
	if ctxErr := ctx.Err(); ctxErr != nil {
		return nil, ctxErr
	}
	if errors.Is(err, files.ErrResourcePath) {
		return nil, rpc.Errorf(rpc.CodeBadRequest, "%s", err)
	}
	if err != nil {
		return nil, fileRPCError(err)
	}
	return rpc.FileRangeResult{Data: r.Data, Stamp: r.Stamp, EOF: r.EOF}, nil
}
