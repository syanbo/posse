package app

import (
	"context"
	"posse/internal/errtext"
	"posse/internal/gitx"
	"posse/internal/rpc"
	"posse/locales"
	"time"
)

func (a *app) cachedBase(w *workspace, g *gitState) *trackInfo {
	if !a.remoteHost() {
		if tr := a.track[trackKey(w, g.repo)]; tr != nil {
			return tr
		}
	}
	if g.baseTrack != nil && g.baseTrack.ref == w.BaseRef {
		return &g.baseTrack.track
	}
	return nil
}

func (a *app) syncInputsMatch(target gitOperationTarget, p rpc.GitIntegrateParams) bool {
	w, g := a.workspace(p.WorkspaceID), target.repository
	if w == nil || w.BaseRef != p.Branch || g.status == nil || g.status.Commit != p.Head || g.status.Head != p.Current || g.status.Operation != "" {
		return false
	}
	tr := a.cachedBase(w, g)
	return tr != nil && tr.baseOK && tr.base.Base == p.BranchOID && tr.base.Head == p.Head
}

func (a *app) syncTargetBase(m locales.Messages, w *workspace, rebase bool) {
	v := a.files[w.ID]
	if v == nil || !baseRepository(w, v) {
		return
	}
	g := v.git
	action, what := rpc.GitIntegrateMerge, m.BranchMergeFrom(w.BaseRef)
	if rebase {
		action, what = rpc.GitIntegrateRebase, m.BranchRebaseToOp(w.BaseRef)
	}
	target, p, ok := a.integrateParams(w, v, g, action)
	if !ok {
		return
	}
	tr := a.cachedBase(w, g)
	if tr == nil || !tr.baseOK || tr.base.Base == "" {
		return
	}
	p.Branch, p.BranchOID = w.BaseRef, tr.base.Base
	target.syncBase = true
	if !rpc.ValidGitIntegrateParams(p) || !a.syncInputsMatch(target, p) {
		return
	}
	a.guardAgent(m, w, what, func() { a.startTargetIntegrate(m, target, what, p, nil, nil) })
}

// 同步停在冲突上属于正常结果；先从原目标读操作状态，再决定是否显示失败。
func (a *app) finishSyncIntegrate(m locales.Messages, s *targetIntegrateOperation, out rpc.GitIntegrateResult, g *gitState) {
	r := s.request
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		status, err := r.client.GitStatus(ctx, rpc.GitStatusParams{Path: s.params.Expected.Root})
		a.update(func() {
			if !a.pathsControls() || !r.current() || g.identity != s.params.Expected || a.files[s.params.WorkspaceID] != g.v || g.v.repoAt(g.repo) != g {
				return
			}
			if err == nil && status.Identity != s.params.Expected {
				err = gitx.ErrRepositoryChanged
			}
			stopped := err == nil && status.Status != nil && (status.Status.Operation == "merge" || status.Status.Operation == "rebase")
			if stopped {
				s.err = ""
				s.stopped = true
			} else if s.err != "" {
				a.err = m.GitOperationFailed(s.what, s.err)
			} else if err != nil {
				a.err = errtext.Text(err)
			}
			if err == nil {
				g.status = status.Status
			}
			a.refreshRepo(g)
			a.refsChanged(s.project)
			if !stopped && out.Status == rpc.GitFetchSucceeded {
				done := m.BranchMerged(s.params.Branch)
				if s.params.Action == rpc.GitIntegrateRebase {
					done = m.BranchRebased(s.params.Branch)
				}
				a.notify(done, "", nil)
			}
		})
	}()
}
