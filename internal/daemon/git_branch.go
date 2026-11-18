package daemon

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"slices"

	"posse/internal/gitx"
	"posse/internal/rpc"
)

func (d *Daemon) gitBranch(ctx context.Context, raw json.RawMessage) (any, error) {
	request, ok := ctx.Value(desktopCallContext{}).(desktopRequestContext)
	if !ok || request.readOnly {
		return nil, rpc.Errorf(rpc.CodeReadOnly, "accepted desktop write required for Git branch")
	}
	if len(raw) > rpc.GitBranchParamsLimit {
		return nil, rpc.Errorf(rpc.CodeBadRequest, "invalid Git branch parameters")
	}
	var p rpc.GitBranchParams
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	valid := dec.Decode(&p) == nil && dec.Decode(new(any)) == io.EOF && rpc.ValidGitBranchParams(p)
	out := rpc.GitFetchResult{Target: p.GitFetchParams, Logs: []gitx.Run{}}
	result := rpc.GitBranchResult{Target: p}
	finish := func(err error) rpc.GitBranchResult {
		common := finishFetch(out, err)
		result.Status, result.Started, result.Error = common.Status, common.Started, common.Error
		result.Logs, result.LogsTruncated = common.Logs, common.LogsTruncated
		if raw, _ := json.Marshal(result); len(raw) > rpc.GitBranchResultLimit {
			result.Logs, result.LogsTruncated = []gitx.Run{}, true
		}
		return result
	}
	conflict := func() rpc.GitBranchResult { return finish(rpc.Errorf(rpc.CodeConflict, "%s", gitx.ErrBranchChanged)) }
	if !valid {
		return conflict(), nil
	}
	ctx = gitx.WithTrace(ctx, nil)
	release, err := d.lockFetchTarget(ctx, p.GitFetchParams)
	if err != nil {
		return finish(err), nil
	}
	defer release()
	readCtx := gitx.WithReadLimit(ctx, rpc.GitReadLimit)
	branches, err := gitx.Branches(readCtx, p.Expected.Root)
	if err != nil {
		return finish(err), nil
	}
	find := func(name string, remote bool) (gitx.Branch, bool) {
		i := slices.IndexFunc(branches, func(b gitx.Branch) bool { return b.Name == name && b.Remote == remote })
		if i < 0 {
			return gitx.Branch{}, false
		}
		return branches[i], true
	}
	oid, noop := "", false
	switch p.Action {
	case rpc.GitBranchCheckout, rpc.GitBranchDelete:
		b, exists := find(p.Branch, p.Remote)
		if !exists || b.OID != p.BranchOID {
			return conflict(), nil
		}
		oid = b.OID
		if p.Action == rpc.GitBranchCheckout {
			if p.Remote {
				local, exists := find(gitx.LocalOf(p.Branch), false)
				if exists != (p.LocalOID != "") || exists && local.OID != p.LocalOID {
					return conflict(), nil
				}
				if exists {
					b, oid = local, local.OID
				}
			}
			if !b.Remote {
				occupied, err := gitx.BranchOccupied(readCtx, p.Expected.Root, b.Name, p.Expected.Root)
				if err != nil {
					return finish(err), nil
				}
				if occupied || !b.Current && b.Worktree != "" {
					return conflict(), nil
				}
				noop = b.Current
			}
		} else if !p.Remote {
			occupied, err := gitx.BranchOccupied(readCtx, p.Expected.Root, b.Name, "")
			if err != nil {
				return finish(err), nil
			}
			if b.Current || b.Worktree != "" || occupied {
				return conflict(), nil
			}
		}
	case rpc.GitBranchCreate:
		if _, exists := find(p.Branch, false); exists {
			return conflict(), nil
		}
		oid, err = gitx.ResolveBranchCommit(readCtx, p.Expected.Root, p.FromOID)
		if err != nil || oid != p.FromOID {
			return conflict(), nil
		}
	case rpc.GitBranchRename:
		b, exists := find(p.Branch, false)
		if !exists || b.OID != p.BranchOID {
			return conflict(), nil
		}
		if _, exists := find(p.NewBranch, false); exists {
			return conflict(), nil
		}
		occupied, err := gitx.BranchOccupied(readCtx, p.Expected.Root, b.Name, p.Expected.Root)
		if err != nil {
			return finish(err), nil
		}
		if occupied || !b.Current && b.Worktree != "" {
			return conflict(), nil
		}
		oid = b.OID
	case rpc.GitBranchRevision:
		oid, err = gitx.ResolveBranchCommit(readCtx, p.Expected.Root, p.Revision)
		if err != nil || p.RevisionOID != "" && oid != p.RevisionOID {
			return conflict(), nil
		}
	}
	if err := gitx.CheckRepositoryIdentity(readCtx, p.Expected.Root, p.Expected); err != nil {
		return finish(err), nil
	}
	if err := ctx.Err(); err != nil {
		return finish(err), nil
	}
	if noop {
		result.OID = oid
		return finish(nil), nil
	}
	out.Started = true
	truncated, err := gitx.BranchBounded(gitWriteTrace(ctx, &out, "branch"), p.Expected.Root, rpc.GitBranchArgs(p, oid))
	out.LogsTruncated = out.LogsTruncated || truncated
	if p.Action == rpc.GitBranchDelete && !p.Remote && !p.Force && gitx.NotMerged(err) {
		current, readErr := gitx.ResolveBranchCommit(readCtx, p.Expected.Root, "refs/heads/"+p.Branch)
		if readErr == nil && current == p.BranchOID {
			// -d 没有改变引用：保留命令记录，但界面继续走原来的未合并确认。
			result.Unmerged, out.Started = true, false
			return finish(nil), nil
		}
	}
	if err == nil {
		result.OID = oid
	}
	return finish(err), nil
}
