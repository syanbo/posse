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
	ctx = gitx.WithReadLimit(ctx, rpc.GitDivergenceReadLimit)
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
	out := rpc.GitDivergenceResult{Identity: id, BaseRef: p.BaseRef, DivergenceRead: result}
	// 读取份额比回复上限大，冲突列表放不进回复时按超出预测范围处理。
	if encoded, err := json.Marshal(out); err == nil && len(encoded) > rpc.GitReadLimit {
		out.Prediction, out.Reason, out.Conflicts = gitx.DivergenceUnavailable, gitx.DivergenceLimit, []string{}
	}
	return gitReadResult(ctx, out)
}
