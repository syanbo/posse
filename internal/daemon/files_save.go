package daemon

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"

	"posse/internal/files"
	"posse/internal/rpc"
)

func (d *Daemon) filesVersion(ctx context.Context, raw json.RawMessage) (any, error) {
	var p rpc.FileVersionParams
	if err := rpc.Decode(raw, &p); err != nil {
		return nil, err
	}
	if err := filePath(p.Path); err != nil {
		return nil, err
	}
	v, err := files.ReadVersion(ctx, p.Path)
	if err != nil {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		return nil, fileRPCError(err)
	}
	return v, nil
}

func (d *Daemon) filesSave(ctx context.Context, raw json.RawMessage) (any, error) {
	// 保存只通过桌面执行器接受，确保断线后的结果可查询，且不绕过控制权。
	request, ok := ctx.Value(desktopCallContext{}).(desktopRequestContext)
	if !ok || request.readOnly {
		return nil, rpc.Errorf(rpc.CodeReadOnly, "desktop identity required for file saves")
	}
	if len(raw) > rpc.FileSaveRequestLimit {
		return nil, rpc.Errorf(rpc.CodeBadRequest, "file save request exceeds the maximum size")
	}
	var p rpc.FileSaveParams
	if err := rpc.Decode(raw, &p); err != nil {
		return nil, err
	}
	if err := filePath(p.Path); err != nil {
		return nil, err
	}
	if err := filePath(p.Expected.Path); err != nil {
		return nil, err
	}
	if len(p.Data) > files.MaxSize {
		return nil, rpc.Errorf(rpc.CodeBadRequest, "file exceeds the maximum size of %d bytes", files.MaxSize)
	}
	sum := sha256.Sum256(p.Data)
	if p.Hash != hex.EncodeToString(sum[:]) {
		return nil, rpc.Errorf(rpc.CodeBadRequest, "file save content checksum does not match")
	}
	if p.Expected.Stamp.Missing {
		if p.Expected.Hash != "" {
			return nil, rpc.Errorf(rpc.CodeBadRequest, "invalid file save baseline")
		}
	} else if hash, err := hex.DecodeString(p.Expected.Hash); err != nil || len(hash) != sha256.Size || !p.Expected.Stamp.Mode.IsRegular() || p.Expected.Stamp.Size < 0 || p.Expected.Stamp.Size > files.MaxSize {
		return nil, rpc.Errorf(rpc.CodeBadRequest, "invalid file save baseline")
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	release, err := d.lockWritePaths(ctx, []string{p.Path})
	if err != nil {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		return nil, fileRPCError(err)
	}
	defer release()
	version, err := files.WriteFileChecked(ctx, p.Path, p.Expected, func(w io.Writer) error {
		for offset := 0; offset < len(p.Data); {
			end := min(offset+(64<<10), len(p.Data))
			if _, err := w.Write(p.Data[offset:end]); err != nil {
				return err
			}
			offset = end
		}
		return nil
	})
	if err != nil {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		return nil, fileRPCError(err)
	}
	return rpc.FileSaveResult{Version: version, Revision: p.Revision}, nil
}
