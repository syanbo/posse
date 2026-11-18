package app

import (
	"context"
	"slices"

	"posse/internal/gitx"
	"posse/internal/rpc"
	"posse/locales"
)

func (a *app) stashParams(v *filesView, g *gitState, action, oid string) (gitOperationTarget, rpc.GitStashParams, bool) {
	w := a.workspace(v.wsID)
	if !a.pathsControls() || w == nil || action != rpc.GitStashDrop && g.status == nil || !a.stashWritable(v, g) {
		return gitOperationTarget{}, rpc.GitStashParams{}, false
	}
	target := a.gitTarget(w, v, g)
	p := rpc.GitStashParams{GitFetchParams: target.params, Action: action, OID: oid}
	if action != rpc.GitStashDrop {
		p.Head, p.IndexID = g.status.Commit, g.status.IndexID
	}
	if action == rpc.GitStashPush {
		p.Entries = slices.Clone(g.status.Entries)
		p.MessageHash = rpc.GitCommitHash(nil)
	}
	return target, p, target.writable() && stashInputsMatch(g, p)
}

func (a *app) stashPush(m locales.Messages, v *filesView, g *gitState) {
	if !a.pathsControls() {
		return
	}
	var target gitOperationTarget
	var p rpc.GitStashParams
	typed := a.typedFetch()
	if typed {
		var ok bool
		target, p, ok = a.stashParams(v, g, rpc.GitStashPush, "")
		if !ok {
			return
		}
	}
	a.prompt(promptReq{title: m.GitStashTitle(), body: m.GitStashBody(), placeholder: m.GitStashMessage(), verb: m.GitStash(), optional: true, do: func(msg string, _ bool) {
		if typed {
			p.Message, p.MessageHash = msg, rpc.GitCommitHash([]byte(msg))
			a.startTargetStash(m, target, m.GitStash(), p, nil, nil)
			return
		}
		a.gitDo(m, g, m.GitStash(), func(ctx context.Context, root string) error { return gitx.StashPush(ctx, root, msg) })
	}})
}

func (a *app) stashTake(m locales.Messages, v *filesView, g *gitState, s gitx.Stash, action, what, other string, owner *workspace) {
	if !a.pathsControls() {
		return
	}
	var send func()
	if a.typedFetch() {
		target, p, ok := a.stashParams(v, g, action, s.Hash)
		if !ok {
			return
		}
		send = func() { a.startTargetStash(m, target, what, p, nil, nil) }
	} else {
		send = func() {
			a.gitDo(m, g, what, func(ctx context.Context, root string) error {
				if action == rpc.GitStashPop {
					return gitx.StashPop(ctx, root, s.Ref)
				}
				return gitx.StashApply(ctx, root, s.Ref)
			})
		}
	}
	if other == "" {
		send()
		return
	}
	where := m.GitStashWhereBranch(other)
	if owner != nil {
		where = m.GitStashWhereWorkspace(displayName(owner), other)
	}
	a.confirm(m.GitStashOtherConfirm(what), m.GitStashOtherBody(where, what), m.GitOperationAnyway(what), func() { a.update(send) })
}

func (a *app) stashDrop(m locales.Messages, v *filesView, g *gitState, s gitx.Stash, autostash bool) {
	if !a.pathsControls() {
		return
	}
	var send func()
	if a.typedFetch() {
		target, p, ok := a.stashParams(v, g, rpc.GitStashDrop, s.Hash)
		if !ok {
			return
		}
		send = func() { a.startTargetStash(m, target, m.GitStashDelete(), p, nil, nil) }
	} else {
		send = func() {
			a.gitDo(m, g, m.GitStashDelete(), func(ctx context.Context, root string) error {
				if autostash {
					return gitx.StashDropHash(ctx, root, s.Hash)
				}
				return gitx.StashDrop(ctx, root, s.Ref)
			})
		}
	}
	body := m.GitStashDeleteBody(s.Message)
	if autostash {
		body = m.GitAutostashDeleteBody()
	}
	a.confirm(m.GitStashDeleteConfirm(), body, m.CommonDelete(), func() { a.update(send) })
}
