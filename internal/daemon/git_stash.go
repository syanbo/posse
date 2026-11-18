package daemon

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"slices"
	"unicode/utf8"

	"posse/internal/gitx"
	"posse/internal/rpc"
)

func (d *Daemon) gitStash(ctx context.Context, raw json.RawMessage) (any, error) {
	request, ok := ctx.Value(desktopCallContext{}).(desktopRequestContext)
	if !ok || request.readOnly {
		return nil, rpc.Errorf(rpc.CodeReadOnly, "accepted desktop write required for Git stash")
	}
	if len(raw) > rpc.GitStashParamsLimit || !utf8.Valid(raw) {
		return nil, rpc.Errorf(rpc.CodeBadRequest, "invalid Git stash parameters")
	}
	var p rpc.GitStashParams
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	if dec.Decode(&p) != nil || dec.Decode(new(any)) != io.EOF || !rpc.ValidGitStashParams(p) {
		return nil, rpc.Errorf(rpc.CodeBadRequest, "invalid Git stash parameters")
	}
	out := rpc.GitFetchResult{Target: p.GitFetchParams, Logs: []gitx.Run{}}
	oid := ""
	finish := func(err error) rpc.GitStashResult {
		common := finishFetch(out, err)
		result := rpc.GitStashResult{Target: p.Metadata(), Status: common.Status, Started: common.Started, OID: oid, Error: common.Error, Logs: common.Logs, LogsTruncated: common.LogsTruncated}
		if raw, _ := json.Marshal(result); len(raw) > rpc.GitStashResultLimit {
			result.Logs, result.LogsTruncated = []gitx.Run{}, true
		}
		return result
	}
	conflict := func() rpc.GitStashResult {
		return finish(rpc.Errorf(rpc.CodeConflict, "Git stash inputs changed; refresh the stashes and changes"))
	}
	ctx = gitx.WithTrace(ctx, nil)
	// 储藏 reflog 属于 common-dir；一次持有仓库路径和项目共享键，不逐文件取锁。
	release, err := d.lockFetchTarget(ctx, p.GitFetchParams)
	if err != nil {
		return finish(err), nil
	}
	defer release()
	readCtx := gitx.WithReadLimit(ctx, rpc.GitReadLimit)
	if p.Action != rpc.GitStashDrop {
		status, err := gitx.ReadStatusChecked(readCtx, p.Expected.Root)
		if err != nil {
			return finish(err), nil
		}
		if status.Commit != p.Head || status.IndexID != p.IndexID || p.Action == rpc.GitStashPush && !slices.Equal(status.Entries, p.Entries) {
			return conflict(), nil
		}
	}
	if err := gitx.CheckRepositoryIdentity(readCtx, p.Expected.Root, p.Expected); err != nil {
		return finish(err), nil
	}
	before, err := gitx.Stashes(readCtx, p.Expected.Root)
	if err != nil {
		return finish(err), nil
	}
	value := p.Message
	if p.Action != rpc.GitStashPush {
		ref, count := "", 0
		for _, s := range before {
			if s.Hash == p.OID {
				ref, count = s.Ref, count+1
			}
		}
		if count != 1 || !rpc.ValidGitStashRef(ref) {
			return conflict(), nil
		}
		resolved, err := gitx.StashRefOID(readCtx, p.Expected.Root, ref)
		if err != nil {
			if ctx.Err() != nil {
				return finish(ctx.Err()), nil
			}
			return conflict(), nil
		}
		if resolved != p.OID {
			return conflict(), nil
		}
		value = ref
	}
	if err := ctx.Err(); err != nil {
		return finish(err), nil
	}
	traceCtx := gitWriteTrace(ctx, &out, "stash")
	out.Started = true
	truncated, err := gitx.StashBounded(traceCtx, p.Expected.Root, p.Action, value)
	out.LogsTruncated = out.LogsTruncated || truncated
	if err != nil {
		return finish(err), nil
	}
	if p.Action == rpc.GitStashPush {
		after, err := gitx.Stashes(readCtx, p.Expected.Root)
		if err != nil {
			return finish(err), nil
		}
		counts := map[string]int{}
		for _, s := range before {
			counts[s.Hash]++
		}
		added := []string{}
		for _, s := range after {
			if counts[s.Hash] > 0 {
				counts[s.Hash]--
			} else {
				added = append(added, s.Hash)
			}
		}
		if len(added) == 1 && rpc.ValidGitBranchOID(added[0]) {
			oid = added[0]
		}
	}
	return finish(nil), nil
}
