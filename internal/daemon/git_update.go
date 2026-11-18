package daemon

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"slices"

	"posse/internal/gitx"
	"posse/internal/rpc"
)

func (d *Daemon) gitUpdate(ctx context.Context, raw json.RawMessage) (any, error) {
	request, ok := ctx.Value(desktopCallContext{}).(desktopRequestContext)
	if !ok || request.readOnly {
		return nil, rpc.Errorf(rpc.CodeReadOnly, "accepted desktop write required for Git update")
	}
	if len(raw) > rpc.GitUpdateParamsLimit {
		return nil, rpc.Errorf(rpc.CodeBadRequest, "invalid Git update parameters")
	}
	var p rpc.GitUpdateParams
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	if dec.Decode(&p) != nil || dec.Decode(new(any)) != io.EOF || !rpc.ValidGitUpdateParams(p) {
		return nil, rpc.Errorf(rpc.CodeBadRequest, "invalid Git update parameters")
	}
	out := rpc.GitFetchResult{Target: p.GitFetchParams, Logs: []gitx.Run{}}
	result := rpc.GitUpdateResult{Target: p}
	finish := func(err error) rpc.GitUpdateResult {
		common := finishFetch(out, err)
		result.Status, result.Started, result.Error = common.Status, common.Started, common.Error
		result.Logs, result.LogsTruncated = common.Logs, common.LogsTruncated
		if raw, _ := json.Marshal(result); len(raw) > rpc.GitUpdateResultLimit {
			result.Logs, result.LogsTruncated = []gitx.Run{}, true
		}
		return result
	}
	ctx = gitx.WithTrace(ctx, nil)
	release, err := d.lockFetchTarget(ctx, p.GitFetchParams)
	if err != nil {
		return finish(err), nil
	}
	defer release()
	readCtx := gitx.WithReadLimit(ctx, rpc.GitReadLimit)
	branches, err := gitx.Branches(readCtx, p.Expected.Root)
	if err == nil {
		err = gitx.CheckRepositoryIdentity(readCtx, p.Expected.Root, p.Expected)
	}
	if err != nil {
		return finish(err), nil
	}
	i := slices.IndexFunc(branches, func(b gitx.Branch) bool { return !b.Remote && b.Name == p.Branch })
	if i < 0 || branches[i].OID != p.BranchOID || branches[i].Upstream != p.Upstream || branches[i].Current != p.Current || !p.Current && branches[i].Worktree != "" {
		return finish(rpc.Errorf(rpc.CodeConflict, "%s", gitx.ErrUpdateChanged)), nil
	}
	if p.Upstream == "" {
		return finish(gitx.ErrNoUpstream), nil
	}
	if err := ctx.Err(); err != nil {
		return finish(err), nil
	}
	out.Started = true
	writeCtx := gitWriteTrace(ctx, &out, "update")
	truncated, err := gitx.UpdateBounded(writeCtx, p.Expected.Root, branches[i])
	out.LogsTruncated = out.LogsTruncated || truncated
	var left *gitx.AutostashLeft
	if errors.As(err, &left) {
		result.Autostash, err = left, nil
	}
	if err == nil {
		result.Incoming = gitx.Incoming(readCtx, p.Expected.Root, p.BranchOID, "refs/remotes/"+p.Upstream)
	}
	return finish(err), nil
}
