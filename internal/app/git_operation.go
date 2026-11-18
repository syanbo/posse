package app

import "posse/internal/rpc"

// 准备动作和确认回调使用同一份目标；储藏行可能来自未选中的另一个仓库。
type gitOperationTarget struct {
	syncBase             bool
	request              appRequest
	params               rpc.GitFetchParams
	view                 *filesView
	repository, selected *gitState
	project, workspace   string
}

func (a *app) gitTarget(w *workspace, v *filesView, g *gitState) gitOperationTarget {
	return gitOperationTarget{
		request: a.request(),
		params:  rpc.GitFetchParams{WorkspaceID: w.ID, WorkspacePath: w.Path, Expected: g.identity},
		view:    v, repository: g, selected: v.git,
		project: w.ProjectID, workspace: w.Name,
	}
}

func (t gitOperationTarget) current() bool {
	if !t.request.current() {
		return false
	}
	a, v, g := t.request.app, t.view, t.repository
	w := a.workspace(t.params.WorkspaceID)
	return w != nil && w.Path == t.params.WorkspacePath && w.ProjectID == t.project && a.files[w.ID] == v && v.git == t.selected && v.repoAt(g.repo) == g && g.identity == t.params.Expected
}

func (t gitOperationTarget) writable() bool {
	return t.current() && t.request.mayWrite()
}
