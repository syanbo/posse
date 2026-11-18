package daemon

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"path/filepath"
	"slices"

	"posse/internal/files"
	"posse/internal/gitx"
	"posse/internal/rpc"
)

func (d *Daemon) gitCommit(ctx context.Context, raw json.RawMessage) (any, error) {
	request, ok := ctx.Value(desktopCallContext{}).(desktopRequestContext)
	if !ok || request.readOnly {
		return nil, rpc.Errorf(rpc.CodeReadOnly, "accepted desktop write required for Git commit")
	}
	if len(raw) > rpc.GitCommitParamsLimit {
		return nil, rpc.Errorf(rpc.CodeBadRequest, "invalid Git commit parameters")
	}
	var p rpc.GitCommitParams
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	if dec.Decode(&p) != nil || dec.Decode(new(any)) != io.EOF || !rpc.ValidGitCommitParams(p) {
		return nil, rpc.Errorf(rpc.CodeBadRequest, "invalid Git commit parameters")
	}
	out := rpc.GitFetchResult{Target: p.GitFetchParams, Logs: []gitx.Run{}}
	finish := func(err error) rpc.GitCommitResult {
		common := finishFetch(out, err)
		result := rpc.GitCommitResult{Target: p.Metadata(), Status: common.Status, Started: common.Started, Error: common.Error, Logs: common.Logs, LogsTruncated: common.LogsTruncated}
		if raw, _ := json.Marshal(result); len(raw) > rpc.GitCommitResultLimit {
			result.Logs, result.LogsTruncated = []gitx.Run{}, true
		}
		return result
	}
	conflict := func() rpc.GitCommitResult {
		return finish(rpc.Errorf(rpc.CodeConflict, "Git commit inputs changed; refresh the changes"))
	}
	ctx = gitx.WithTrace(ctx, nil)
	// 仓库、common-dir、项目和所有文件一次取齐，文件保存不能插进版本核验与 Git 读取之间。
	release, err := d.lockFetchTarget(ctx, p.GitFetchParams, p.WorktreePaths()...)
	if err != nil {
		return finish(err), nil
	}
	defer release()
	readCtx := gitx.WithReadLimit(ctx, rpc.GitReadLimit)
	status, err := gitx.ReadStatusChecked(readCtx, p.Expected.Root)
	if err != nil {
		return finish(err), nil
	}
	if status.Commit != p.Head || status.IndexID != p.IndexID {
		return conflict(), nil
	}
	whole := slices.Contains([]string{"merge", "cherry-pick", "revert"}, status.Operation)
	if p.Action == rpc.GitCommitWhole && !whole || p.Action == rpc.GitCommitPaths && whole {
		return conflict(), nil
	}
	if p.Action == rpc.GitCommitChange && status.Operation != "" {
		return finish(errors.New("cannot commit a change while a Git operation is in progress")), nil
	}
	for _, item := range p.Paths {
		e := item.Entry
		i := slices.IndexFunc(status.Entries, func(now gitx.Entry) bool { return now.Path == e.Path })
		if i < 0 || rpc.GitPathCategory(status.Entries[i]) != rpc.GitPathCategory(e) || status.Entries[i].Orig != e.Orig || status.Entries[i].Submodule != e.Submodule {
			return conflict(), nil
		}
		for path, expected := range map[string]*files.Version{e.Path: item.Version, e.Orig: item.OrigVersion} {
			if expected == nil {
				continue
			}
			absolute := filepath.Join(p.Expected.Root, filepath.FromSlash(path))
			stamp, err := files.Stat(absolute)
			if err != nil {
				return finish(err), nil
			}
			if !stamp.Same(expected.Stamp) || stamp.Mode != expected.Stamp.Mode {
				return conflict(), nil
			}
			current, err := files.ReadVersion(ctx, absolute)
			if errors.Is(err, files.ErrChanged) {
				return conflict(), nil
			}
			if err != nil {
				return finish(err), nil
			}
			if !current.Same(*expected) {
				return conflict(), nil
			}
		}
	}
	if err := gitx.CheckRepositoryIdentity(readCtx, p.Expected.Root, p.Expected); err != nil {
		return finish(err), nil
	}
	if err := ctx.Err(); err != nil {
		return finish(err), nil
	}
	traceCtx := gitWriteTrace(ctx, &out, "commit")
	if p.Action == rpc.GitCommitChange {
		truncated, err := gitx.CommitChangeBounded(traceCtx, p.Expected.Root, p.Path, p.Data, p.Message, func() { out.Started = true })
		out.LogsTruncated = out.LogsTruncated || truncated
		return finish(err), nil
	}
	for _, args := range rpc.GitCommitCommands(p) {
		if err := ctx.Err(); err != nil {
			return finish(err), nil
		}
		out.Started = true
		truncated, err := gitx.CommitBounded(traceCtx, p.Expected.Root, args, p.Message)
		out.LogsTruncated = out.LogsTruncated || truncated
		if err != nil {
			return finish(err), nil
		}
	}
	return finish(nil), nil
}
