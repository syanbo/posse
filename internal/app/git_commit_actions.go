package app

import (
	"context"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"posse/internal/errtext"
	"posse/internal/rpc"
	"posse/locales"
)

func (a *app) targetCommit(m locales.Messages, v *filesView) {
	if !a.pathsControls() {
		return
	}
	msg, amend := strings.TrimSpace(v.ch.message), v.ch.amend
	if msg == "" {
		a.err = m.GitMessageRequired()
		return
	}
	targets, _ := v.commitTargets()
	if len(targets) == 0 {
		a.err = m.GitSelectFiles()
		return
	}
	if amend && len(targets) > 1 {
		a.err = m.GitAmendOneRepo()
		return
	}
	w := a.workspace(v.wsID)
	if w == nil {
		return
	}
	original := v.ch.message
	left, failed := len(targets), false
	finish := func(t commitTarget, out rpc.GitCommitResult) {
		if out.Status != rpc.GitFetchSucceeded {
			failed = true
		} else {
			for _, path := range t.paths {
				delete(t.g.picks, path)
			}
		}
		left--
		if left != 0 || failed {
			return
		}
		// 用户在执行期间写了下一条说明，或切了 amend，不替他清掉新的输入。
		if v.ch.message == original && v.ch.amend == amend {
			v.ch.message, v.ch.amend, v.ch.amendMsg = "", false, ""
		}
		subject, _, _ := strings.Cut(msg, "\n")
		if len(targets) > 1 {
			a.notify(m.GitCommittedRepos(subject, len(targets)), "", nil)
		} else {
			a.notify(m.GitCommitted(subject), "", nil)
		}
	}
	for _, t := range targets {
		if t.g.status == nil {
			failed = true
			left--
			continue
		}
		target := a.gitTarget(w, v, t.g)
		p := rpc.GitCommitParams{GitFetchParams: target.params, Action: rpc.GitCommitPaths, Head: t.g.status.Commit, IndexID: t.g.status.IndexID, Message: msg, MessageHash: rpc.GitCommitHash([]byte(msg)), Amend: amend, Add: slices.Clone(t.add)}
		if t.whole {
			p.Action, p.Add = rpc.GitCommitWhole, nil
		}
		for _, e := range t.g.status.Entries {
			if slices.Contains(t.paths, e.Path) {
				p.Paths = append(p.Paths, rpc.GitPath{Entry: e})
			}
		}
		what := m.GitCommitButton()
		if len(targets) > 1 {
			what = m.GitCommitInRepo(v.repoName(t.g))
		}
		a.prepareCommit(m, target, what, p, nil, func(out rpc.GitCommitResult) { finish(t, out) })
	}
}

// 只读取一次点击时的文件版本；确认另开和排队重试都沿用这份前提。
func (a *app) prepareCommit(m locales.Messages, target gitOperationTarget, what string, p rpc.GitCommitParams, check func() bool, completed func(rpc.GitCommitResult)) {
	if !a.pathsControls() || !target.writable() {
		return
	}
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		var err error
		for i := range p.Paths {
			item := &p.Paths[i]
			version, readErr := target.request.saveVersion(ctx, filepath.Join(p.Expected.Root, filepath.FromSlash(item.Entry.Path)))
			if readErr != nil {
				err = readErr
				break
			}
			item.Version = &version
			if item.Entry.Orig != "" {
				version, readErr := target.request.saveVersion(ctx, filepath.Join(p.Expected.Root, filepath.FromSlash(item.Entry.Orig)))
				if readErr != nil {
					err = readErr
					break
				}
				item.OrigVersion = &version
			}
		}
		a.update(func() {
			if !target.writable() || !a.pathsControls() {
				return
			}
			if err != nil {
				a.err = errtext.Text(err)
				if completed != nil {
					completed(rpc.GitCommitResult{Target: p.Metadata(), Status: rpc.GitFetchFailed})
				}
				return
			}
			if !rpc.ValidGitCommitParams(p) {
				a.err = errtext.Text(rpc.Errorf(rpc.CodeBadRequest, "invalid Git commit parameters"))
				return
			}
			if previous := a.commitFor(target.repository); previous != nil && previous.pending() && (previous.params.Expected == p.Expected || previous.unknown) {
				a.startTargetCommit(m, target, what, p, check, completed)
			} else {
				a.commitAfterQueue(m, target, what, p, check, completed)
			}
		})
	}()
}

func (a *app) targetCommitChange(m locales.Messages, v *filesView, d *fileDoc, cp *changePop, rel string, data []byte, message string) {
	if !a.pathsControls() || d.isModified() || d.headBuf == nil {
		return
	}
	g, w := v.repoFor(d.path), a.workspace(v.wsID)
	if g == nil || w == nil || g.status == nil || g.status.Operation != "" || d.headFor != g.status.Commit {
		return
	}
	target := a.gitTarget(w, v, g)
	p := rpc.GitCommitParams{GitFetchParams: target.params, Action: rpc.GitCommitChange, Head: g.status.Commit, IndexID: g.status.IndexID, Path: rel, Data: slices.Clone(data), Hash: rpc.GitCommitHash(data), Message: message, MessageHash: rpc.GitCommitHash([]byte(message))}
	buffer, head := d.ed.Buffer.String(), d.headBuf.String()
	current := func() bool {
		return d.change == cp && !d.isModified() && d.ed.Buffer != nil && d.ed.Buffer.String() == buffer && d.headBuf != nil && d.headBuf.String() == head && d.headFor == p.Head && v.repoFor(d.path) == g
	}
	a.prepareCommit(m, target, m.GitCommitButton(), p, current, func(out rpc.GitCommitResult) {
		if out.Status != rpc.GitFetchSucceeded {
			return
		}
		if d.change == cp {
			d.change = nil
		}
		a.notify(m.GitChangeCommitted(message), "", nil)
	})
}

func (a *app) targetLastMessage(v *filesView, g *gitState) {
	if !a.pathsControls() || !a.targetGitReady() {
		return
	}
	r, identity, root := a.request(), g.identity, g.repo
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		out, err := r.client.GitStatus(ctx, rpc.GitStatusParams{Path: root, LastMessage: true})
		a.update(func() {
			if !r.current() || !a.pathsControls() || a.files[v.wsID] != v || v.amendRepo() != g || g.identity != identity || !v.ch.amend || strings.TrimSpace(v.ch.message) != "" {
				return
			}
			if err != nil {
				a.err = errtext.Text(err)
				return
			}
			if !a.targetGitIdentity(g, out.Identity) {
				return
			}
			if g.status != nil && out.Status.Commit != g.status.Commit {
				return
			}
			v.ch.message, v.ch.amendMsg = out.LastMessage, out.LastMessage
		})
	}()
}
