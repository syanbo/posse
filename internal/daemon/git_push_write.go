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

func (d *Daemon) gitPush(ctx context.Context, raw json.RawMessage) (any, error) {
	request, ok := ctx.Value(desktopCallContext{}).(desktopRequestContext)
	if !ok || request.readOnly {
		return nil, rpc.Errorf(rpc.CodeReadOnly, "accepted desktop write required for Git push")
	}
	if len(raw) > rpc.GitPushParamsLimit {
		return nil, rpc.Errorf(rpc.CodeBadRequest, "invalid Git push parameters")
	}
	var p rpc.GitPushParams
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	if dec.Decode(&p) != nil || dec.Decode(new(any)) != io.EOF || !rpc.ValidGitPushParams(p) {
		return nil, rpc.Errorf(rpc.CodeBadRequest, "invalid Git push parameters")
	}
	out := rpc.GitFetchResult{Target: p.GitFetchParams, Logs: []gitx.Run{}}
	ctx = gitx.WithTrace(ctx, nil)
	release, err := d.lockFetchTarget(ctx, p.GitFetchParams)
	if err != nil {
		return finishPush(p, out, err), nil
	}
	defer release()
	readCtx := gitx.WithPushPreviewRead(gitx.WithReadLimit(ctx, rpc.GitReadLimit))
	preview, err := gitx.ReadPushPreviewChecked(readCtx, p.Expected.Root, p.Branch, gitx.PushPreviewReadLimit)
	if err == nil {
		err = gitx.CheckRepositoryIdentity(readCtx, p.Expected.Root, p.Expected)
	}
	if err != nil {
		// 预览已无法重建也不能沿用旧计划；取消和预算错误保留各自含义。
		if !errors.Is(err, context.Canceled) && !errors.Is(err, context.DeadlineExceeded) && !errors.Is(err, gitx.ErrReadLimit) {
			err = rpc.Errorf(rpc.CodeConflict, "%s", gitx.ErrPushChanged)
		}
		return finishPush(p, out, err), nil
	}
	if !p.Matches(preview) {
		return finishPush(p, out, rpc.Errorf(rpc.CodeConflict, "%s", gitx.ErrPushChanged)), nil
	}
	if err := ctx.Err(); err != nil {
		return finishPush(p, out, err), nil
	}
	ctx = gitWriteTrace(ctx, &out, "push")
	out.Started = true
	truncated, err := gitx.PushBounded(ctx, p.Expected.Root, preview.Plan, p.Force, p.Tags)
	out.LogsTruncated = out.LogsTruncated || truncated
	return finishPush(p, out, err), nil
}

func finishPush(p rpc.GitPushParams, out rpc.GitFetchResult, err error) rpc.GitPushResult {
	rejected := out.Started && gitx.Rejected(err) && !errors.Is(err, context.Canceled) && !errors.Is(err, context.DeadlineExceeded)
	out = finishFetch(out, err)
	result := rpc.GitPushResult{Target: p, Status: out.Status, Started: out.Started, Rejected: rejected, Error: out.Error, Logs: out.Logs, LogsTruncated: out.LogsTruncated}
	if raw, _ := json.Marshal(result); len(raw) > rpc.GitPushResultLimit {
		result.Logs, result.LogsTruncated = []gitx.Run{}, true
	}
	return result
}
