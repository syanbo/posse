package daemon

import (
	"context"
	"encoding/json"
	"posse/internal/gitx"
	"posse/internal/rpc"
)

func (d *Daemon) gitWorktreeCompare(ctx context.Context, raw json.RawMessage) (any, error) {
	var p rpc.GitWorktreeCompareParams
	if err := gitReadParams(ctx, raw, &p); err != nil {
		return nil, err
	}
	if !gitx.ValidWorktreeQuery(p.Revision, p.Baseline) {
		return nil, gitReadError(ctx, gitx.ErrWorktreeQuery)
	}
	ctx = gitx.WithReadLimit(ctx, rpc.GitReadLimit)
	id, err := gitFileIdentity(ctx, p.Path, p.Expected)
	if err != nil {
		return nil, err
	}
	result, readErr := gitx.ReadWorktreeComparison(ctx, id, p.Revision, p.Baseline)
	if err := gitx.CheckRepositoryIdentity(ctx, p.Path, id); err != nil {
		return nil, gitReadError(ctx, err)
	}
	if readErr != nil {
		return nil, gitReadError(ctx, readErr)
	}
	return gitReadResult(ctx, rpc.GitWorktreeCompareResult{Identity: id, Revision: p.Revision, WorktreeComparison: result})
}
