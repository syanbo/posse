package app

import (
	"context"
	"time"

	"posse/internal/errtext"
	"posse/internal/gitx"
	"posse/internal/rpc"
	"posse/locales"
)

func (a *app) historyAction(m locales.Messages, w *workspace, v *filesView, g *gitState, ci gitx.CommitInfo, action, mode string) {
	if !a.pathsControls() || w == nil || g == nil {
		return
	}
	what := m.LogReset()
	switch action {
	case rpc.GitHistoryReword:
		what = m.LogReword()
	case rpc.GitHistorySquash:
		what = m.LogSquash()
	case rpc.GitHistoryDrop:
		what = m.LogDrop()
	}
	typed := a.typedFetch()
	var target gitOperationTarget
	var p rpc.GitHistoryParams
	if typed {
		if g.status == nil || !a.historyWritable(v, g) {
			return
		}
		target = a.gitTarget(w, v, g)
		p = rpc.GitHistoryParams{GitFetchParams: target.params, Action: action, Mode: mode, OID: ci.Hash, Head: g.status.Commit, Current: g.status.Head}
	}
	current := func() bool { return !typed || target.writable() && a.historyWritable(v, g) && historyInputsMatch(g, p) }
	if !current() {
		return
	}
	send := func(message string) {
		if !current() {
			return
		}
		if typed {
			if action == rpc.GitHistoryReword || action == rpc.GitHistorySquash {
				p.Message, p.MessageHash = message, rpc.GitCommitHash([]byte(message))
			}
			fixed := p
			a.guardAgent(m, w, what, func() { a.startTargetHistory(m, target, what, fixed, nil, nil) })
		} else {
			a.guardAgent(m, w, what, func() {
				a.gitDo(m, g, what, func(ctx context.Context, root string) error {
					switch action {
					case rpc.GitHistoryReset:
						return gitx.Reset(ctx, root, ci.Hash, mode)
					case rpc.GitHistoryReword:
						return gitx.Reword(ctx, root, ci.Hash, message)
					case rpc.GitHistorySquash:
						return gitx.SquashIntoParent(ctx, root, ci.Hash, message)
					default:
						return gitx.Drop(ctx, root, ci.Hash)
					}
				})
			})
		}
	}
	short := ci.Hash[:min(7, len(ci.Hash))]
	afterPushed := func() {
		if !current() {
			return
		}
		switch action {
		case rpc.GitHistoryReset:
			if mode == "hard" {
				a.confirm(m.LogHardResetConfirm(short), m.LogHardResetBody(), m.LogHardReset(), func() { a.update(func() { send("") }) })
			} else {
				send("")
			}
		case rpc.GitHistoryDrop:
			a.confirm(m.LogDropConfirm(short), m.LogDropBody(ci.Subject), m.LogDrop(), func() { a.update(func() { send("") }) })
		default:
			first, second, title := ci.Hash, "", m.LogRewordTitle(short)
			if action == rpc.GitHistorySquash {
				if len(ci.Parents) != 1 {
					return
				}
				first, second, title = ci.Parents[0], ci.Hash, m.LogSquashTitle()
			}
			if !typed {
				a.askMessage(m, g.repo, title, first, second, send)
				return
			}
			go func() {
				ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
				defer cancel()
				read := func(oid string) (string, error) {
					out, err := target.request.client.GitCommitFiles(ctx, rpc.GitCommitFilesParams{Path: g.repo, Expected: p.Expected, Hash: oid, FullMessage: true})
					return out.FullMessage, err
				}
				msg, err := read(first)
				if err == nil && second != "" {
					var other string
					other, err = read(second)
					msg += "\n\n" + other
				}
				a.update(func() {
					if !current() {
						return
					}
					if err != nil {
						a.err = errtext.Text(err)
						return
					}
					a.prompt(promptReq{title: title, initial: msg, verb: m.LogMessageConfirm(), multiline: true, do: func(s string, _ bool) { send(s) }})
				})
			}()
		}
	}
	oldest := ci.Hash
	if action == rpc.GitHistorySquash {
		if len(ci.Parents) != 1 {
			return
		}
		oldest = ci.Parents[0]
	}
	if !typed {
		if action == rpc.GitHistoryReset {
			afterPushed()
		} else {
			a.guardPushed(m, g.repo, oldest, afterPushed)
		}
		return
	}
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		out, err := target.request.client.GitRemotesContaining(ctx, rpc.GitRemotesContainingParams{Path: g.repo, Expected: p.Expected, OID: oldest})
		a.update(func() {
			if !current() {
				return
			}
			if err != nil {
				a.err = errtext.Text(err)
				return
			}
			p.Remotes = out.Remotes
			if len(p.Remotes) == 0 {
				afterPushed()
				return
			}
			where := p.Remotes[0]
			if len(p.Remotes) > 1 {
				where = m.LogRemotesMore(where, len(p.Remotes))
			}
			a.confirm(m.LogPushedTitle(), m.LogPushedBody(where), m.LogRewriteAnyway(), func() {
				a.update(func() {
					if current() {
						p.Confirmed = true
						afterPushed()
					}
				})
			})
		})
	}()
}
