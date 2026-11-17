package daemon

import (
	"context"
	"encoding/json"

	"posse/internal/gitx"
	"posse/internal/rpc"
)

func (d *Daemon) gitCompare(ctx context.Context, raw json.RawMessage) (any, error) {
	var p rpc.GitCompareParams
	if err := gitReadParams(ctx, raw, &p); err != nil {
		return nil, err
	}
	if !gitx.ValidCompareQuery(p.Current, p.Other, p.Limit) {
		return nil, gitReadError(ctx, gitx.ErrCompareQuery)
	}
	ctx = gitx.WithReadLimit(ctx, rpc.GitReadLimit)
	id, err := gitFileIdentity(ctx, p.Path, p.Expected)
	if err != nil {
		return nil, err
	}
	result, readErr := gitx.ReadCompareChecked(ctx, id.Root, p.Current, p.Other, p.Limit)
	if err := gitx.CheckRepositoryIdentity(ctx, p.Path, id); err != nil {
		return nil, gitReadError(ctx, err)
	}
	if readErr != nil {
		return nil, gitReadError(ctx, readErr)
	}
	return gitReadResult(ctx, rpc.GitCompareResult{Identity: id, Current: p.Current, Other: p.Other, CompareRead: result})
}
