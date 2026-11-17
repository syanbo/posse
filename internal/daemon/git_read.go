package daemon

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"

	"posse/internal/gitx"
	"posse/internal/rpc"
)

func gitReadParams(ctx context.Context, raw json.RawMessage, out any) error {
	request, ok := ctx.Value(desktopCallContext{}).(desktopRequestContext)
	if !ok || !request.readOnly {
		return rpc.Errorf(rpc.CodeReadOnly, "desktop identity required for Git queries")
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if len(raw) > 16<<10 {
		return rpc.Errorf(rpc.CodeBadRequest, "invalid Git query parameters")
	}
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	if err := dec.Decode(out); err != nil {
		return rpc.Errorf(rpc.CodeBadRequest, "invalid Git query parameters")
	}
	if dec.Decode(new(any)) != io.EOF {
		return rpc.Errorf(rpc.CodeBadRequest, "invalid Git query parameters")
	}
	return nil
}

func gitReadError(ctx context.Context, err error) error {
	if ctx.Err() != nil {
		return ctx.Err()
	}
	if errors.Is(err, gitx.ErrRepositoryChanged) || errors.Is(err, gitx.ErrFileUnmerged) || errors.Is(err, gitx.ErrLogChanged) || errors.Is(err, gitx.ErrBlameChanged) || errors.Is(err, gitx.ErrCompareChanged) || errors.Is(err, gitx.ErrWorktreeChanged) || errors.Is(err, gitx.ErrPushChanged) || errors.Is(err, gitx.ErrDivergenceChanged) {
		return rpc.Errorf(rpc.CodeConflict, "%s", err.Error())
	}
	if errors.Is(err, gitx.ErrReadLimit) || errors.Is(err, gitx.ErrFileQuery) || errors.Is(err, gitx.ErrFileNotBlob) || errors.Is(err, gitx.ErrLogQuery) || errors.Is(err, gitx.ErrBlameQuery) || errors.Is(err, gitx.ErrBlameNotText) || errors.Is(err, gitx.ErrBlameNoHead) || errors.Is(err, gitx.ErrCompareQuery) || errors.Is(err, gitx.ErrWorktreeQuery) || errors.Is(err, gitx.ErrWorktreeLimit) || errors.Is(err, gitx.ErrWorktreeUnsupported) || errors.Is(err, gitx.ErrPushQuery) || errors.Is(err, gitx.ErrPushConfig) || errors.Is(err, gitx.ErrDivergenceQuery) {
		return rpc.Errorf(rpc.CodeBadRequest, "%s", err.Error())
	}
	return fileRPCError(err)
}

func gitReadResult(ctx context.Context, out any) (any, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	encoded, err := json.Marshal(out)
	if err != nil {
		return nil, err
	}
	if len(encoded) > rpc.GitReadLimit {
		return nil, rpc.Errorf(rpc.CodeBadRequest, "Git query exceeds the response limit")
	}
	return out, nil
}

func (d *Daemon) gitDiscover(ctx context.Context, raw json.RawMessage) (any, error) {
	var p rpc.GitDiscoverParams
	if err := gitReadParams(ctx, raw, &p); err != nil {
		return nil, err
	}
	if err := filePath(p.Path); err != nil {
		return nil, err
	}
	ctx = gitx.WithReadLimit(ctx, rpc.GitReadLimit)
	found, identities, err := gitx.DiscoverChecked(ctx, p.Path, !p.SingleRepo)
	if err != nil {
		return nil, gitReadError(ctx, err)
	}
	out := rpc.GitDiscoverResult{RealPath: found.RealPath, Repos: []rpc.GitRepository{}, Partial: found.Partial}
	for _, repo := range found.Repos {
		out.Repos = append(out.Repos, rpc.GitRepository{Root: repo.Root, Real: repo.Real, Aliases: repo.Aliases, Identity: identities[repo.Real]})
	}
	return gitReadResult(ctx, out)
}

func (d *Daemon) gitStatus(ctx context.Context, raw json.RawMessage) (any, error) {
	var p rpc.GitStatusParams
	if err := gitReadParams(ctx, raw, &p); err != nil {
		return nil, err
	}
	if err := filePath(p.Path); err != nil {
		return nil, err
	}
	ctx = gitx.WithReadLimit(ctx, rpc.GitReadLimit)
	id, err := gitx.RepositoryIdentityChecked(ctx, p.Path)
	if err != nil {
		return nil, gitReadError(ctx, err)
	}
	out := rpc.GitStatusResult{Identity: id}
	out.Status, err = gitx.ReadStatusChecked(ctx, id.Root)
	if err != nil {
		return nil, gitReadError(ctx, err)
	}
	if p.Full {
		if out.Status.Commit == "" {
			// porcelain 明确报告 unborn 才能按空树统计，其他 HEAD 错误不能降级。
			out.Head, err = gitx.NumStat(ctx, id.Root, true)
		} else {
			out.Head, err = gitx.NumStatHEADChecked(ctx, id.Root)
		}
		if err != nil {
			return nil, gitReadError(ctx, err)
		}
		out.Stashes, err = gitx.Stashes(ctx, id.Root)
		if err != nil {
			return nil, gitReadError(ctx, err)
		}
	}
	if err := gitx.CheckRepositoryIdentity(ctx, p.Path, id); err != nil {
		return nil, gitReadError(ctx, err)
	}
	return gitReadResult(ctx, out)
}

func (d *Daemon) gitBranches(ctx context.Context, raw json.RawMessage) (any, error) {
	var p rpc.GitBranchesParams
	if err := gitReadParams(ctx, raw, &p); err != nil {
		return nil, err
	}
	if err := filePath(p.Path); err != nil {
		return nil, err
	}
	ctx = gitx.WithReadLimit(ctx, rpc.GitReadLimit)
	id, err := gitx.RepositoryIdentityChecked(ctx, p.Path)
	if err != nil {
		return nil, gitReadError(ctx, err)
	}
	out := rpc.GitBranchesResult{Identity: id}
	out.Branches, err = gitx.Branches(ctx, id.Root)
	if err != nil {
		return nil, gitReadError(ctx, err)
	}
	out.Tags, err = gitx.Tags(ctx, id.Root)
	if err != nil {
		return nil, gitReadError(ctx, err)
	}
	hasHead, err := gitx.HeadExistsChecked(ctx, id.Root)
	if err != nil {
		return nil, gitReadError(ctx, err)
	}
	if hasHead {
		out.Recent, err = gitx.RecentBranchesChecked(ctx, id.Root, 6)
		if err != nil {
			return nil, gitReadError(ctx, err)
		}
	}
	if out.Branches == nil {
		out.Branches = []gitx.Branch{}
	}
	if out.Tags == nil {
		out.Tags = []string{}
	}
	if out.Recent == nil {
		out.Recent = []string{}
	}
	if err := gitx.CheckRepositoryIdentity(ctx, p.Path, id); err != nil {
		return nil, gitReadError(ctx, err)
	}
	return gitReadResult(ctx, out)
}
