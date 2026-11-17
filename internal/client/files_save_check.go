package client

import (
	"context"
	"errors"

	"posse/internal/files"
	"posse/internal/rpc"
)

type FileSaveCheck struct {
	Version files.Version
	Pending bool
	Matches bool
}

// CheckFileSave 只核对当前内容；unknown 不代表从未执行，也不能据此重发保存。
func (c *Client) CheckFileSave(ctx context.Context, ref rpc.DesktopOperationRef, path, hash string) (FileSaveCheck, error) {
	var operation rpc.DesktopOperation
	if err := c.Call(ctx, "desktop.operation", ref, &operation); err != nil {
		return FileSaveCheck{}, err
	}
	if operation.DesktopOperationRef != ref ||
		(operation.Method != "files.save" && (operation.State != "unknown" || operation.Method != "")) {
		return FileSaveCheck{}, errors.New("invalid file save operation response")
	}
	switch operation.State {
	case "running":
		return FileSaveCheck{Pending: true}, nil
	case "done", "unknown":
	default:
		return FileSaveCheck{}, errors.New("invalid file save operation response")
	}

	version, err := c.FileVersion(ctx, path)
	if err != nil {
		return FileSaveCheck{}, err
	}
	out := FileSaveCheck{Version: version, Matches: !version.Stamp.Missing && version.Hash == hash}
	// 保存报错后磁盘仍可能已有目标正文，以当前完整指纹为准。
	if !out.Matches && operation.State == "done" && operation.Error != nil {
		return out, operation.Error
	}
	return out, nil
}
