package daemon

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"sync"
	"time"

	"posse/internal/gitx"
	"posse/internal/rpc"
	"posse/internal/store"
	"posse/internal/writecoord"
)

func (d *Daemon) gitFetch(ctx context.Context, raw json.RawMessage) (any, error) {
	request, ok := ctx.Value(desktopCallContext{}).(desktopRequestContext)
	if !ok || request.readOnly {
		return nil, rpc.Errorf(rpc.CodeReadOnly, "accepted desktop write required for Git fetch")
	}
	if len(raw) > rpc.GitFetchParamsLimit {
		return nil, rpc.Errorf(rpc.CodeBadRequest, "invalid Git fetch parameters")
	}
	var p rpc.GitFetchParams
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	if dec.Decode(&p) != nil || dec.Decode(new(any)) != io.EOF || !rpc.ValidGitFetchParams(p) {
		return nil, rpc.Errorf(rpc.CodeBadRequest, "invalid Git fetch parameters")
	}
	out := rpc.GitFetchResult{Target: p, Logs: []gitx.Run{}}
	ctx = gitx.WithTrace(ctx, nil)
	release, err := d.lockFetchTarget(ctx, p)
	if err != nil {
		return finishFetch(out, err), nil
	}
	defer release()
	if err := ctx.Err(); err != nil {
		return finishFetch(out, err), nil
	}
	ctx = gitWriteTrace(ctx, &out, "fetch")
	out.Started = true
	truncated, err := gitx.FetchBounded(ctx, p.Expected.Root)
	out.LogsTruncated = out.LogsTruncated || truncated
	return finishFetch(out, err), nil
}

func gitWriteTrace(ctx context.Context, out *rpc.GitFetchResult, operation string) context.Context {
	var mu sync.Mutex
	return gitx.WithOp(gitx.WithTrace(ctx, func(r gitx.Run) {
		if !r.Done {
			return
		}
		mu.Lock()
		defer mu.Unlock()
		// 储藏说明可能含凭据，不把 -m 的原文留进操作记录。
		if operation == "stash" && len(r.Args) == 5 && r.Args[0] == "stash" && r.Args[1] == "push" && r.Args[3] == "-m" {
			r.Args = slices.Clone(r.Args)
			r.Args[4] = "[message omitted]"
		}
		cleanOut, cleanErr := gitx.SanitizeFetchText(r.Out), gitx.SanitizeFetchText(r.Err)
		out.LogsTruncated = out.LogsTruncated || cleanOut != r.Out || cleanErr != r.Err
		r.Out, r.Err = cleanOut, cleanErr
		if len(out.Logs) == rpc.GitFetchLogLimit {
			out.Logs = out.Logs[1:]
			out.LogsTruncated = true
		}
		out.Logs = append(out.Logs, r)
	}), operation)
}

func finishFetch(out rpc.GitFetchResult, err error) rpc.GitFetchResult {
	if err == nil {
		out.Status = rpc.GitFetchSucceeded
	} else {
		code := rpc.CodeInternal
		switch {
		case errors.Is(err, context.Canceled):
			out.Status, code = rpc.GitFetchCanceled, "canceled"
		case errors.Is(err, context.DeadlineExceeded):
			out.Status, code = rpc.GitFetchTimedOut, rpc.CodeTimeout
		default:
			var re *rpc.Error
			if errors.Is(err, gitx.ErrRepositoryChanged) || errors.As(err, &re) && re.Code == rpc.CodeConflict {
				out.Status, code = rpc.GitFetchConflict, rpc.CodeConflict
			} else {
				out.Status = rpc.GitFetchFailed
			}
		}
		out.Error = rpc.Errorf(code, "%s", gitx.SanitizeFetchText(err.Error()))
	}
	// 核心结果必须始终留在操作缓存；日志再大也不能把成功的 fetch 变成 outcome_unknown。
	encoded, _ := json.Marshal(out)
	if len(encoded) > rpc.GitFetchResultLimit {
		out.LogsTruncated = true
		for i := range out.Logs {
			out.Logs[i].Out = ""
			out.Logs[i].Err = ""
		}
		encoded, _ = json.Marshal(out)
		if len(encoded) > rpc.GitFetchResultLimit {
			out.Logs = []gitx.Run{}
		}
	}
	return out
}

type fetchTargetSnapshot struct {
	workspace     store.Workspace
	project       store.Project
	workspaceReal string
	workspaceInfo os.FileInfo
	scope         writecoord.Scope
}

func (d *Daemon) fetchTargetSnapshot(ctx context.Context, p rpc.GitFetchParams, relative ...string) (fetchTargetSnapshot, error) {
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	ctx = gitx.WithReadLimit(gitx.WithTrace(ctx, nil), rpc.GitReadLimit)
	var s fetchTargetSnapshot
	var err error
	s.workspace, err = d.store.Workspace(p.WorkspaceID)
	if err != nil || s.workspace.Path != p.WorkspacePath {
		return s, workspaceWriteChanged()
	}
	s.project, err = d.store.FindProject(s.workspace.ProjectID)
	if err != nil {
		return s, workspaceWriteChanged()
	}
	s.workspaceReal, err = filepath.EvalSymlinks(s.workspace.Path)
	if err != nil {
		return s, workspaceWriteChanged()
	}
	s.workspaceInfo, err = os.Stat(s.workspaceReal)
	if err != nil || !s.workspaceInfo.IsDir() {
		return s, workspaceWriteChanged()
	}
	// 与窗口读取共用发现规则，包含父仓、多仓与软链别名，不能仅凭目录前缀认定归属。
	found, identities, err := gitx.DiscoverChecked(ctx, s.workspace.Path, !s.project.SingleRepo)
	if err != nil {
		return s, err
	}
	var repository string
	for _, repo := range found.Repos {
		if identities[repo.Real] == p.Expected {
			repository = repo.Root
			break
		}
	}
	if repository == "" {
		return s, workspaceWriteChanged()
	}
	if err := gitx.CheckRepositoryIdentity(ctx, p.Expected.Root, p.Expected); err != nil {
		return s, err
	}
	paths := []string{repository, p.Expected.Root}
	// 这里只收集锁范围；执行时的路径在拿齐锁并复查身份后才拼接。
	for _, rel := range relative {
		paths = append(paths, p.Expected.Root+"/"+rel)
	}
	s.scope, err = writeScope(ctx, paths, nil, false)
	if err != nil {
		return s, err
	}
	links, err := resourceLinks(ctx, s.workspace.Path)
	if err != nil {
		return s, err
	}
	s.scope.SharedPaths = append(s.scope.SharedPaths, links...)
	slices.Sort(s.scope.SharedPaths)
	s.scope.SharedPaths = slices.Compact(s.scope.SharedPaths)
	s.scope.SharedKeys = []string{"project:" + s.project.ID}
	return s, nil
}

func (d *Daemon) lockFetchTarget(ctx context.Context, p rpc.GitFetchParams, relative ...string) (func(), error) {
	before, err := d.fetchTargetSnapshot(ctx, p, relative...)
	if err != nil {
		return nil, err
	}
	release, err := d.writes.Acquire(ctx, before.scope)
	if err != nil {
		return nil, err
	}
	after, err := d.fetchTargetSnapshot(ctx, p, relative...)
	if err != nil {
		release()
		return nil, err
	}
	if before.workspace.ID != after.workspace.ID || before.workspace.ProjectID != after.workspace.ProjectID || before.workspace.Path != after.workspace.Path || before.workspace.Kind != after.workspace.Kind || !reflect.DeepEqual(before.workspace.Repos, after.workspace.Repos) || before.project.ID != after.project.ID || before.project.Path != after.project.Path || before.project.Kind != after.project.Kind || before.project.SingleRepo != after.project.SingleRepo || before.workspaceReal != after.workspaceReal || !os.SameFile(before.workspaceInfo, after.workspaceInfo) || !reflect.DeepEqual(before.scope, after.scope) {
		release()
		return nil, workspaceWriteChanged()
	}
	return release, nil
}
