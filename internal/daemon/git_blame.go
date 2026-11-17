package daemon

import (
	"context"
	"encoding/json"

	"posse/internal/gitx"
	"posse/internal/rpc"
)

func (d *Daemon) gitBlame(ctx context.Context, raw json.RawMessage) (any, error) {
	var p rpc.GitBlameParams
	if err := gitReadParams(ctx, raw, &p); err != nil {
		return nil, err
	}
	if !gitx.ValidBlameFile(p.File) {
		return nil, gitReadError(ctx, gitx.ErrBlameQuery)
	}
	ctx = gitx.WithReadLimit(ctx, rpc.GitReadLimit)
	id, err := gitFileIdentity(ctx, p.Path, p.Expected)
	if err != nil {
		return nil, err
	}
	result, readErr := gitx.ReadBlameChecked(ctx, id, p.File)
	if err := gitx.CheckRepositoryIdentity(ctx, p.Path, id); err != nil {
		return nil, gitReadError(ctx, err)
	}
	if readErr != nil {
		return nil, gitReadError(ctx, readErr)
	}
	return gitReadResult(ctx, rpc.GitBlameResult{Identity: id, File: p.File, Head: result.Head, Version: result.Version, Lines: result.Lines})
}
