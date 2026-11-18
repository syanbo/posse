package daemon

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"posse/internal/gitx"
	"posse/internal/rpc"
	"slices"
	"unicode/utf8"
)

func (d *Daemon) gitIntegrate(ctx context.Context, raw json.RawMessage) (any, error) {
	request, ok := ctx.Value(desktopCallContext{}).(desktopRequestContext)
	if !ok || request.readOnly {
		return nil, rpc.Errorf(rpc.CodeReadOnly, "accepted desktop write required for Git integration")
	}
	if len(raw) > rpc.GitIntegrateParamsLimit || !utf8.Valid(raw) {
		return nil, rpc.Errorf(rpc.CodeBadRequest, "invalid Git integration parameters")
	}
	var p rpc.GitIntegrateParams
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	if dec.Decode(&p) != nil || dec.Decode(new(any)) != io.EOF || !rpc.ValidGitIntegrateParams(p) {
		return nil, rpc.Errorf(rpc.CodeBadRequest, "invalid Git integration parameters")
	}
	out := rpc.GitFetchResult{Target: p.GitFetchParams, Logs: []gitx.Run{}}
	finish := func(err error) rpc.GitIntegrateResult {
		common := finishFetch(out, err)
		result := rpc.GitIntegrateResult{Target: p, Status: common.Status, Started: common.Started, Error: common.Error, Logs: common.Logs, LogsTruncated: common.LogsTruncated}
		if raw, _ := json.Marshal(result); len(raw) > rpc.GitIntegrateResultLimit {
			result.Logs, result.LogsTruncated = []gitx.Run{}, true
		}
		return result
	}
	conflict := func() rpc.GitIntegrateResult {
		return finish(rpc.Errorf(rpc.CodeConflict, "Git integration inputs changed; refresh the branches and changes"))
	}
	ctx = gitx.WithTrace(ctx, nil)
	// 所有动作一次取齐仓库路径、common-dir 和项目共享键，锁内再核对工作区登记和身份。
	release, err := d.lockFetchTarget(ctx, p.GitFetchParams)
	if err != nil {
		return finish(err), nil
	}
	defer release()
	readCtx := gitx.WithReadLimit(ctx, rpc.GitReadLimit)
	st, err := gitx.ReadStatusChecked(readCtx, p.Expected.Root)
	if err != nil {
		return finish(err), nil
	}
	if st.Commit != p.Head || st.Head != p.Current {
		return conflict(), nil
	}
	message := ""
	switch p.Action {
	case rpc.GitIntegrateMerge, rpc.GitIntegrateRebase, rpc.GitIntegrateCheckoutRebase:
		if st.Operation != "" || !p.Remote && p.Branch == p.Current {
			return conflict(), nil
		}
		ref := "refs/heads/" + p.Branch
		if p.Remote {
			ref = "refs/remotes/" + p.Branch
		}
		oid, err := gitx.ResolveBranchCommit(readCtx, p.Expected.Root, ref)
		if err != nil || oid != p.BranchOID {
			if ctx.Err() != nil {
				return finish(ctx.Err()), nil
			}
			return conflict(), nil
		}
		// 裸名字若被同名标签遮住，就不能执行已经核对过另一引用的命令。
		resolved, resolveErr := gitx.ResolveBranchCommit(readCtx, p.Expected.Root, p.Branch)
		if resolveErr != nil || resolved != p.BranchOID {
			if ctx.Err() != nil {
				return finish(ctx.Err()), nil
			}
			return conflict(), nil
		}
		if p.Action == rpc.GitIntegrateCheckoutRebase {
			if p.Current != "" {
				current, err := gitx.ResolveBranchCommit(readCtx, p.Expected.Root, p.Current)
				if err != nil || current != p.Head {
					if ctx.Err() != nil {
						return finish(ctx.Err()), nil
					}
					return conflict(), nil
				}
			}
			occupied, err := gitx.BranchOccupied(readCtx, p.Expected.Root, p.Branch, p.Expected.Root)
			if err != nil {
				return finish(err), nil
			}
			if occupied {
				return conflict(), nil
			}
		}
	case rpc.GitIntegrateCherryPick, rpc.GitIntegrateRevert:
		if st.Operation != "" {
			return conflict(), nil
		}
		for _, original := range p.OIDs {
			oid, err := gitx.ResolveBranchCommit(readCtx, p.Expected.Root, original)
			if err != nil || oid != original {
				if ctx.Err() != nil {
					return finish(ctx.Err()), nil
				}
				return conflict(), nil
			}
		}
	case rpc.GitIntegrateContinue, rpc.GitIntegrateSkip, rpc.GitIntegrateAbort:
		if st.Operation != p.Operation {
			return conflict(), nil
		}
		if p.Action == rpc.GitIntegrateSkip && p.Operation != "rebase" && p.Operation != "cherry-pick" {
			return conflict(), nil
		}
		if p.Action == rpc.GitIntegrateContinue {
			if st.IndexID != p.IndexID || slices.ContainsFunc(st.Entries, func(e gitx.Entry) bool { return e.Conflict }) {
				return conflict(), nil
			}
			if p.Operation == "merge" {
				message, err = gitx.MergeMessage(p.Expected.GitDir, rpc.GitCommitMessageLimit)
				if err != nil {
					return conflict(), nil
				}
			}
		}
	}
	if err := gitx.CheckRepositoryIdentity(readCtx, p.Expected.Root, p.Expected); err != nil {
		return finish(err), nil
	}
	if err := ctx.Err(); err != nil {
		return finish(err), nil
	}
	out.Started = true
	truncated, err := gitx.IntegrateBounded(gitWriteTrace(ctx, &out, "integrate"), p.Expected.Root, rpc.GitIntegrateArgs(p), message, p.Action == rpc.GitIntegrateContinue || p.Action == rpc.GitIntegrateSkip)
	out.LogsTruncated = out.LogsTruncated || truncated
	return finish(err), nil
}
