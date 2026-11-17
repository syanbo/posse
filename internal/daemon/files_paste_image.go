package daemon

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"

	"posse/internal/files"
	"posse/internal/rpc"
)

func (d *Daemon) filesPasteImage(ctx context.Context, raw json.RawMessage) (any, error) {
	request, ok := ctx.Value(desktopCallContext{}).(desktopRequestContext)
	if !ok || request.readOnly {
		return nil, rpc.Errorf(rpc.CodeReadOnly, "desktop identity required for image paste")
	}
	if len(raw) > rpc.FileSaveRequestLimit {
		return nil, rpc.Errorf(rpc.CodeBadRequest, "file write request exceeds the maximum size")
	}
	var p rpc.FilePasteImageParams
	if err := rpc.Decode(raw, &p); err != nil {
		return nil, err
	}
	if len(p.Data) > files.MaxSize {
		return nil, rpc.Errorf(rpc.CodeBadRequest, "file exceeds the maximum size of %d bytes", files.MaxSize)
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	sum := sha256.Sum256(p.Data)
	if p.Hash != hex.EncodeToString(sum[:]) {
		return nil, rpc.Errorf(rpc.CodeBadRequest, "image paste content checksum does not match")
	}
	path, err := files.PasteImage(ctx, p.Data)
	if err != nil {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		if errors.Is(err, files.ErrInvalidPasteImage) {
			return nil, rpc.Errorf(rpc.CodeBadRequest, "%s", err)
		}
		return nil, fileRPCError(err)
	}
	return rpc.FilePasteImageResult{Path: path}, nil
}
