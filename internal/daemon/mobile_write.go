package daemon

import (
	"context"
	"reflect"

	"posse/internal/mobile"
)

func (h mobileHost) BeginGitWrite(ctx context.Context, target mobile.GitWriteTarget) (context.Context, func(), error) {
	ctx, finish, err := h.d.beginWorkspaceOperation(ctx)
	if err != nil {
		return nil, nil, err
	}
	w, err := h.d.store.Workspace(target.WorkspaceID)
	if err != nil {
		finish()
		return nil, nil, err
	}
	if w.Path != target.WorkspacePath {
		finish()
		return nil, nil, workspaceWriteChanged()
	}
	scope, err := writeScope(ctx, []string{target.Root}, nil, false)
	if err != nil {
		finish()
		return nil, nil, err
	}
	// 同项目的不同仓库可并行，只对项目登记持共享占用；删除、重定位与设置修改持独占键。
	scope.SharedKeys = []string{"project:" + w.ProjectID}
	release, err := h.d.writes.Acquire(ctx, scope)
	if err != nil {
		finish()
		return nil, nil, err
	}
	fail := func(err error) (context.Context, func(), error) {
		release()
		finish()
		return nil, nil, err
	}
	current, err := h.d.store.Workspace(w.ID)
	if err != nil || current.ProjectID != w.ProjectID || current.Path != w.Path {
		return fail(workspaceWriteChanged())
	}
	if _, err := h.d.store.FindProject(w.ProjectID); err != nil {
		return fail(workspaceWriteChanged())
	}
	checked, err := writeScope(ctx, []string{target.Root}, nil, false)
	if err != nil {
		return fail(err)
	}
	checked.SharedKeys = scope.SharedKeys
	if !reflect.DeepEqual(scope, checked) {
		return fail(workspaceWriteChanged())
	}
	return ctx, func() { release(); finish() }, nil
}
