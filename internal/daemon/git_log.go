package daemon

import (
	"context"
	"encoding/json"

	"posse/internal/gitx"
	"posse/internal/rpc"
)

func (d *Daemon) gitLog(ctx context.Context, raw json.RawMessage) (any, error) {
	var p rpc.GitLogParams
	if err := gitReadParams(ctx, raw, &p); err != nil {
		return nil, err
	}
	if !gitx.ValidLogQuery(p.Filter, p.Skip, p.Limit, p.Baseline) {
		return nil, gitReadError(ctx, gitx.ErrLogQuery)
	}
	ctx = gitx.WithReadLimit(ctx, rpc.GitReadLimit)
	id, err := gitFileIdentity(ctx, p.Path, p.Expected)
	if err != nil {
		return nil, err
	}
	page, err := gitx.ReadLogChecked(ctx, id.Root, p.Filter, p.Skip, p.Limit, p.Baseline)
	if err != nil {
		return nil, gitReadError(ctx, err)
	}
	if err := gitx.CheckRepositoryIdentity(ctx, p.Path, id); err != nil {
		return nil, gitReadError(ctx, err)
	}
	return gitReadResult(ctx, rpc.GitLogResult{Identity: id, Commits: page.Commits, Baseline: page.Baseline, HasMore: page.HasMore, NextSkip: page.NextSkip})
}

func (d *Daemon) gitCommitFiles(ctx context.Context, raw json.RawMessage) (any, error) {
	var p rpc.GitCommitFilesParams
	if err := gitReadParams(ctx, raw, &p); err != nil {
		return nil, err
	}
	if !gitx.ValidBlobOID(p.Hash) {
		return nil, gitReadError(ctx, gitx.ErrLogQuery)
	}
	ctx = gitx.WithReadLimit(ctx, rpc.GitReadLimit)
	id, err := gitFileIdentity(ctx, p.Path, p.Expected)
	if err != nil {
		return nil, err
	}
	detail, readErr := gitx.ReadCommitDetailsChecked(ctx, id.Root, p.Hash)
	// 替换仓库也可能先表现为父对象读不到，先核对身份再报告读取错误。
	if err := gitx.CheckRepositoryIdentity(ctx, p.Path, id); err != nil {
		return nil, gitReadError(ctx, err)
	}
	if readErr != nil {
		return nil, gitReadError(ctx, readErr)
	}
	return gitReadResult(ctx, rpc.GitCommitFilesResult{Identity: id, Commit: detail.Commit, Files: detail.Files, DiffBase: detail.DiffBase, Shallow: detail.Shallow})
}
