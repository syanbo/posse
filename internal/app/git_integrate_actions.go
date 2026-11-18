package app

import (
	"context"
	"posse/internal/gitx"
	"posse/internal/rpc"
	"posse/locales"
	"slices"
)

func (a *app) integrateParams(w *workspace, v *filesView, g *gitState, action string) (gitOperationTarget, rpc.GitIntegrateParams, bool) {
	if !a.pathsControls() || w == nil || g == nil || g.status == nil || !a.integrateWritable(v, g) {
		return gitOperationTarget{}, rpc.GitIntegrateParams{}, false
	}
	target := a.gitTarget(w, v, g)
	p := rpc.GitIntegrateParams{GitFetchParams: target.params, Action: action, Head: g.status.Commit, Current: g.status.Head}
	return target, p, target.writable()
}

func (a *app) integrateBranch(m locales.Messages, w *workspace, v *filesView, b, cur gitx.Branch, action string) {
	if !a.pathsControls() {
		return
	}
	what := m.GitMerge()
	if action == rpc.GitIntegrateRebase {
		what = m.GitOpRebase()
	}
	if action == rpc.GitIntegrateCheckoutRebase {
		what = m.BranchCheckoutRebase()
	}
	g := v.git
	var send func()
	if a.typedFetch() {
		target, p, ok := a.integrateParams(w, v, g, action)
		if !ok || cur.Name != p.Current || cur.OID != "" && cur.OID != p.Head {
			return
		}
		p.Branch, p.BranchOID, p.Remote = b.Name, b.OID, b.Remote
		if !rpc.ValidGitIntegrateParams(p) || !integrateInputsMatch(g, p) {
			return
		}
		send = func() { a.startTargetIntegrate(m, target, what, p, nil, nil) }
	} else {
		current := cur.Name
		if current == "" {
			current = "HEAD"
		}
		send = func() {
			a.gitDo(m, g, what, func(ctx context.Context, root string) error {
				switch action {
				case rpc.GitIntegrateRebase:
					return gitx.Rebase(ctx, root, b.Name)
				case rpc.GitIntegrateCheckoutRebase:
					return gitx.CheckoutAndRebase(ctx, root, b.Name, current)
				default:
					return gitx.MergeInto(ctx, root, b.Name)
				}
			})
		}
	}
	a.branchPop = nil
	a.guardAgent(m, w, what, send)
}

func (a *app) integrateCommits(m locales.Messages, w *workspace, v *filesView, g *gitState, action string, oids []string) {
	if !a.pathsControls() {
		return
	}
	what := m.GitCherryPick()
	if action == rpc.GitIntegrateRevert {
		what = m.GitRevert()
	}
	fixed := slices.Clone(oids)
	var send func()
	if a.typedFetch() {
		target, p, ok := a.integrateParams(w, v, g, action)
		if !ok {
			return
		}
		p.OIDs = fixed
		if !rpc.ValidGitIntegrateParams(p) {
			return
		}
		send = func() { a.startTargetIntegrate(m, target, what, p, nil, nil) }
	} else {
		if len(fixed) == 0 {
			return
		}
		send = func() {
			a.gitDo(m, g, what, func(ctx context.Context, root string) error {
				if action == rpc.GitIntegrateRevert {
					return gitx.RevertCommit(ctx, root, fixed[0])
				}
				return gitx.CherryPick(ctx, root, fixed...)
			})
		}
	}
	a.guardAgent(m, w, what, send)
}

func (a *app) integrateProgress(m locales.Messages, w *workspace, v *filesView, g *gitState, action, operation string) {
	if !a.pathsControls() {
		return
	}
	name := opName(m, operation)
	what := m.GitContinueOperation(name)
	if action == rpc.GitIntegrateSkip {
		what = m.GitSkip()
	}
	if action == rpc.GitIntegrateAbort {
		what = m.GitAbort()
	}
	var send func()
	if a.typedFetch() {
		target, p, ok := a.integrateParams(w, v, g, action)
		if !ok {
			return
		}
		p.Operation = operation
		if action == rpc.GitIntegrateContinue {
			p.IndexID = g.status.IndexID
		}
		if !rpc.ValidGitIntegrateParams(p) || !integrateInputsMatch(g, p) {
			return
		}
		send = func() { a.startTargetIntegrate(m, target, what, p, nil, nil) }
	} else {
		send = func() {
			a.gitDo(m, g, what, func(ctx context.Context, root string) error {
				switch action {
				case rpc.GitIntegrateSkip:
					return gitx.Skip(ctx, root, operation)
				case rpc.GitIntegrateAbort:
					return gitx.Abort(ctx, root, operation)
				default:
					return gitx.Continue(ctx, root, operation)
				}
			})
		}
	}
	if action == rpc.GitIntegrateAbort {
		a.confirm(m.GitAbortConfirm(name), m.GitAbortBody(name), m.GitAbort(), func() { a.update(send) })
	} else {
		send()
	}
}
