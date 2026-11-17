package daemon

import (
	"context"
	"encoding/json"

	"posse/internal/gitx"
	"posse/internal/rpc"
)

func (d *Daemon) gitPushPreview(ctx context.Context, raw json.RawMessage) (any, error) {
	var p rpc.GitPushPreviewParams
	if err := gitReadParams(ctx, raw, &p); err != nil {
		return nil, err
	}
	if !gitx.ValidPushPreviewQuery(p.Branch, p.Limit) {
		return nil, gitReadError(ctx, gitx.ErrPushQuery)
	}
	ctx = gitx.WithPushPreviewRead(gitx.WithReadLimit(ctx, rpc.GitReadLimit))
	id, err := gitFileIdentity(ctx, p.Path, p.Expected)
	if err != nil {
		return nil, err
	}
	result, readErr := gitx.ReadPushPreviewChecked(ctx, id.Root, p.Branch, p.Limit)
	if err := gitx.CheckRepositoryIdentity(ctx, p.Path, id); err != nil {
		return nil, gitReadError(ctx, err)
	}
	if readErr != nil {
		return nil, gitReadError(ctx, readErr)
	}
	return gitReadResult(ctx, rpc.GitPushPreviewResult{Identity: id, PushPreviewRead: result})
}
