package daemon

import (
	"context"
	"encoding/json"
	"time"

	"posse/internal/gitx"
	"posse/internal/rpc"
)

func (d *Daemon) gitDivergence(ctx context.Context, raw json.RawMessage) (any, error) {
	var p rpc.GitDivergenceParams
	if err := gitReadParams(ctx, raw, &p); err != nil {
		return nil, err
	}
	if !gitx.ValidDivergenceQuery(p.BaseRef, p.PreviousBaseline) {
		return nil, gitReadError(ctx, gitx.ErrDivergenceQuery)
	}
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	ctx = gitx.WithReadLimit(ctx, rpc.GitReadLimit)
	id, err := gitFileIdentity(ctx, p.Path, p.Expected)
	if err != nil {
		return nil, err
	}
	result, readErr := gitx.ReadDivergenceChecked(ctx, id, p.BaseRef, p.PreviousBaseline)
	if err := gitx.CheckRepositoryIdentity(ctx, p.Path, id); err != nil {
		return nil, gitReadError(ctx, err)
	}
	if readErr != nil {
		return nil, gitReadError(ctx, readErr)
	}
	return gitReadResult(ctx, rpc.GitDivergenceResult{Identity: id, BaseRef: p.BaseRef, DivergenceRead: result})
}
