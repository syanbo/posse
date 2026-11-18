package daemon

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"slices"

	"posse/internal/files"
	"posse/internal/gitx"
	"posse/internal/rpc"
)

func (d *Daemon) gitPaths(ctx context.Context, raw json.RawMessage) (any, error) {
	request, ok := ctx.Value(desktopCallContext{}).(desktopRequestContext)
	if !ok || request.readOnly {
		return nil, rpc.Errorf(rpc.CodeReadOnly, "accepted desktop write required for Git paths")
	}
	if len(raw) > rpc.GitPathsParamsLimit {
		return nil, rpc.Errorf(rpc.CodeBadRequest, "invalid Git paths parameters")
	}
	var p rpc.GitPathsParams
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	if dec.Decode(&p) != nil || dec.Decode(new(any)) != io.EOF || !rpc.ValidGitPathsParams(p) {
		return nil, rpc.Errorf(rpc.CodeBadRequest, "invalid Git paths parameters")
	}
	out := rpc.GitFetchResult{Target: p.GitFetchParams, Logs: []gitx.Run{}}
	result := rpc.GitPathsResult{Target: p.Metadata()}
	finish := func(err error) rpc.GitPathsResult {
		if errors.Is(err, files.ErrSaveConflict) || errors.Is(err, files.ErrChanged) {
			err = rpc.Errorf(rpc.CodeConflict, "%s", files.ErrSaveConflict)
		}
		common := finishFetch(out, err)
		result.Status, result.Started, result.Error = common.Status, common.Started, common.Error
		result.Logs, result.LogsTruncated = common.Logs, common.LogsTruncated
		if raw, _ := json.Marshal(result); len(raw) > rpc.GitPathsResultLimit {
			result.Logs, result.LogsTruncated = []gitx.Run{}, true
		}
		return result
	}
	conflict := func() rpc.GitPathsResult {
		return finish(rpc.Errorf(rpc.CodeConflict, "Git path inputs changed; refresh the changes"))
	}
	var relative []string
	for _, item := range p.Items() {
		relative = append(relative, item.Entry.Path)
		if item.Entry.Orig != "" {
			relative = append(relative, item.Entry.Orig)
		}
	}
	ctx = gitx.WithTrace(ctx, nil)
	release, err := d.lockFetchTarget(ctx, p.GitFetchParams, relative...)
	if err != nil {
		return finish(err), nil
	}
	defer release()
	readCtx := gitx.WithReadLimit(ctx, rpc.GitReadLimit)
	status, err := gitx.ReadStatus(readCtx, p.Expected.Root)
	if err != nil {
		return finish(err), nil
	}
	if status.Commit != p.Head {
		return conflict(), nil
	}
	for _, item := range p.Items() {
		e := item.Entry
		idx := slices.IndexFunc(status.Entries, func(now gitx.Entry) bool { return now.Path == e.Path })
		if idx < 0 {
			return conflict(), nil
		}
		now := status.Entries[idx]
		if rpc.GitPathCategory(now) != rpc.GitPathCategory(e) || now.Orig != e.Orig || now.Submodule != e.Submodule || p.Action == rpc.GitPathsUnstage && now.X == '.' {
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
				return finish(files.ErrSaveConflict), nil
			}
			current, err := files.ReadVersion(ctx, absolute)
			if errors.Is(err, files.ErrChanged) {
				return finish(files.ErrSaveConflict), nil
			}
			if err != nil {
				return finish(err), nil
			}
			if !current.Same(*expected) {
				return finish(files.ErrSaveConflict), nil
			}
		}
	}
	if err := gitx.CheckRepositoryIdentity(readCtx, p.Expected.Root, p.Expected); err != nil {
		return finish(err), nil
	}
	if err := ctx.Err(); err != nil {
		return finish(err), nil
	}
	if p.Action == rpc.GitPathsWrite {
		item := p.Paths[0]
		out.Started = true
		version, err := files.WriteFileChecked(ctx, filepath.Join(p.Expected.Root, filepath.FromSlash(item.Entry.Path)), *item.Version, func(w io.Writer) error { _, err := w.Write(p.Data); return err })
		if err == nil {
			result.Version = &version
		}
		return finish(err), nil
	}
	commands := rpc.GitPathsCommands(p)
	traceCtx := gitWriteTrace(ctx, &out, "paths")
	for i, args := range commands {
		if err := ctx.Err(); err != nil {
			return finish(err), nil
		}
		out.Started = true
		truncated, err := gitx.PathsBounded(traceCtx, p.Expected.Root, args)
		out.LogsTruncated = out.LogsTruncated || truncated
		if err != nil {
			return finish(err), nil
		}
		if p.Action == rpc.GitPathsRollback && i >= len(commands)-len(p.Remove) {
			item := p.Remove[i-(len(commands)-len(p.Remove))]
			path := filepath.Join(p.Expected.Root, filepath.FromSlash(item.Entry.Path))
			// Git 成功后才删文件；不递归删除，命令期间外部程序新写的内容也不能删。
			current, err := files.ReadVersion(ctx, path)
			if err != nil {
				return finish(err), nil
			}
			if !current.Same(*item.Version) {
				return finish(files.ErrSaveConflict), nil
			}
			if !current.Stamp.Missing {
				if err := os.Remove(path); err != nil {
					return finish(err), nil
				}
			}
		}
	}
	return finish(nil), nil
}
