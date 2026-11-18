package app

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"time"

	"github.com/egoist/mygo/ui"
	"posse/internal/errtext"
	"posse/internal/gitx"
	"posse/internal/rpc"
	"posse/locales"
)

// 已发送操作归主机所有，关闭弹窗和连接换代都不取消它。
type targetUpdateOperation struct {
	ref                                  rpc.DesktopOperationRef
	params                               rpc.GitUpdateParams
	project, workspace                   string
	request                              appRequest
	at                                   time.Time
	what                                 string
	repos                                []*gitState
	result                               *rpc.GitUpdateResult
	err                                  string
	unknown, done, acknowledged          bool
	checking, canceling, cancelRequested bool
}

func (s *targetUpdateOperation) pending() bool { return !s.done && !s.acknowledged }
func (s *targetUpdateOperation) key() string {
	return fmt.Sprintf("update:%s:%s:%d:%d", s.ref.Desktop.ClientID, s.ref.Desktop.InstanceID, s.ref.Desktop.Epoch, s.ref.Sequence)
}
func (a *app) updateFor(g *gitState) *targetUpdateOperation {
	if g == nil {
		return nil
	}
	for i := len(a.targetUpdates) - 1; i >= 0; i-- {
		s := a.targetUpdates[i]
		if s.matchesRepository(g) {
			return s
		}
	}
	return nil
}

func (s *targetUpdateOperation) matchesRepository(g *gitState) bool {
	return s.params.Expected == g.identity || s.params.Expected.CommonDir != "" && s.params.Expected.CommonDir == g.identity.CommonDir
}

func (a *app) holdUpdate(s *targetUpdateOperation, g *gitState) {
	if s.pending() && g.busy == "" && !slices.Contains(s.repos, g) {
		g.busy, g.busyKind, g.busyUpdate = s.what, gitUpdate, s
		g.cancel = func() { a.cancelUpdate(appL(), s) }
		s.repos = append(s.repos, g)
	}
}

func (a *app) releaseUpdate(s *targetUpdateOperation) {
	for _, g := range s.repos {
		if g.busyUpdate == s {
			g.busy, g.busyKind, g.cancel, g.busyUpdate = "", "", nil, nil
			a.gitNext(g)
		}
	}
	s.repos = nil
}
func (a *app) reserveUpdate() bool {
	for {
		used := 0
		for _, s := range a.targetUpdates {
			b, _ := json.Marshal(s.params)
			used += len(b) + len(s.err) + len(s.project) + len(s.workspace) + 1024
			if !s.done {
				used += rpc.GitUpdateResultLimit
			} else {
				b, _ = json.Marshal(s.result)
				used += len(b)
			}
		}
		if len(a.targetUpdates) < gitConsoleMax && used+rpc.GitUpdateResultLimit+rpc.GitUpdateParamsLimit+1024 <= targetFetchBytes {
			return true
		}
		i := slices.IndexFunc(a.targetUpdates, func(s *targetUpdateOperation) bool { return !s.pending() })
		if i < 0 {
			return false
		}
		a.targetUpdates = slices.Delete(a.targetUpdates, i, i+1)
	}
}
func (a *app) updateWritable(v *filesView) bool {
	if !a.fetchWritable(v) || !a.request().mayWrite() {
		return false
	}
	g, s := v.git, a.updateFor(v.git)
	for _, view := range a.files {
		for _, candidate := range view.repos.list {
			if candidate == g || candidate.identity.CommonDir != "" && candidate.identity.CommonDir == g.identity.CommonDir {
				if candidate.busy != "" && (s == nil || candidate.busyUpdate != s) {
					return false
				}
			}
		}
	}
	return s == nil || !s.pending() || s.unknown && !s.checking && !s.canceling
}

func (a *app) updateInputsCurrent(target gitOperationTarget, p rpc.GitUpdateParams) bool {
	if !target.writable() || !a.updateWritable(target.view) {
		return false
	}
	g := target.repository
	for _, b := range g.branches.list {
		if b.Remote || b.Name != p.Branch {
			continue
		}
		if b.Upstream != p.Upstream || b.Current != p.Current {
			return false
		}
		if p.Current {
			return g.status != nil && g.status.Head == p.Branch && g.status.Commit == p.BranchOID
		}
		return b.OID == p.BranchOID && b.Worktree == ""
	}
	return false
}

func (a *app) startTargetUpdate(m locales.Messages, w *workspace, v *filesView, what string, b gitx.Branch) {
	if !a.updateWritable(v) || b.Remote {
		return
	}
	target := a.gitTarget(w, v, v.git)
	p := rpc.GitUpdateParams{GitFetchParams: target.params, Branch: b.Name, BranchOID: b.OID, Upstream: b.Upstream, Current: b.Current}
	if b.Current {
		if v.git.status == nil || v.git.status.Head != b.Name {
			return
		}
		p.BranchOID = v.git.status.Commit
	}
	if !rpc.ValidGitUpdateParams(p) || !a.updateInputsCurrent(target, p) {
		return
	}
	send := func() {
		if !a.updateInputsCurrent(target, p) {
			return
		}
		if previous := a.updateFor(v.git); previous != nil && previous.pending() {
			if !previous.unknown {
				return
			}
			a.confirm(what, m.FetchNewBody(), m.BranchUpdate(), func() {
				a.update(func() {
					if !a.updateInputsCurrent(target, p) || !previous.pending() || !previous.unknown || previous.checking || previous.canceling {
						return
					}
					previous.acknowledged = true
					a.releaseUpdate(previous)
					a.executeUpdate(m, target, what, p)
				})
			})
			return
		}
		a.executeUpdate(m, target, what, p)
	}
	if p.Current {
		a.guardAgent(m, w, what, send)
	} else {
		send()
	}
}
func (a *app) executeUpdate(m locales.Messages, target gitOperationTarget, what string, p rpc.GitUpdateParams) {
	if !a.updateInputsCurrent(target, p) {
		return
	}
	if !a.reserveUpdate() {
		a.err = errtext.Text(errors.New("too many desktop operations are running; retry after they finish"))
		return
	}
	r := target.request
	op, err := r.client.PrepareGitUpdate(p)
	if err != nil {
		a.err = errtext.Text(err)
		return
	}
	s := &targetUpdateOperation{ref: op.Ref(), params: p, project: target.project, workspace: target.workspace, request: r, at: time.Now(), what: what}
	a.targetUpdates = append(a.targetUpdates, s)
	for _, v := range a.files {
		for _, g := range v.repos.list {
			if s.matchesRepository(g) {
				a.holdUpdate(s, g)
			}
		}
	}
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
		defer cancel()
		if !r.current() {
			a.update(func() {
				s.done = true
				a.releaseUpdate(s)
			})
			return
		}
		out, err := op.Execute(ctx)
		a.update(func() {
			if s.done || !slices.Contains(a.targetUpdates, s) {
				return
			}
			if err != nil {
				var re *rpc.Error
				s.unknown = !errors.As(err, &re) || re.Code == rpc.CodeOutcomeUnknown
				s.done, s.err = !s.unknown, errtext.Text(err)
				if r.current() {
					a.err = s.err
				}
				if s.done {
					a.releaseUpdate(s)
				}
				return
			}
			a.finishUpdate(s, out, r, m)
		})
	}()
}
func (a *app) finishUpdate(s *targetUpdateOperation, out rpc.GitUpdateResult, r appRequest, m locales.Messages) {
	if s.done || !slices.Contains(a.targetUpdates, s) {
		return
	}
	s.result, s.done, s.unknown = &out, true, false
	defer a.releaseUpdate(s)
	s.err = ""
	if out.Error != nil {
		s.err = errtext.Text(out.Error)
	}
	if s.acknowledged {
		return
	}
	v := a.files[s.params.WorkspaceID]
	if v == nil {
		return
	}
	var g *gitState
	for _, candidate := range v.repos.list {
		if candidate.identity == s.params.Expected {
			g = candidate
			break
		}
	}
	if g == nil || a.updateFor(g) != s {
		return
	}
	// 断线只抑制界面跳转；已完成操作留下的储藏仍属于原仓库，重连后继续显示。
	if left := out.Autostash; left != nil {
		g.autostash, g.autostashConflicts, g.autostashSeen = left.Stash, left.Conflicts, false
	}
	if !r.current() {
		return
	}
	a.refreshGit(v)
	a.refsChanged(s.project)
	if s.err != "" {
		a.err = m.GitOperationFailed(s.what, s.err)
	}
	if out.Status == rpc.GitFetchSucceeded {
		shown := len(a.toasts)
		if s.request.current() {
			if out.Incoming == 0 {
				a.notify(m.BranchUpToDate(s.params.Branch), "", nil)
			} else {
				a.notify(m.BranchPulled(s.params.Branch, out.Incoming), "", nil)
			}
		}
		if out.Autostash != nil {
			a.autostashLeft(m, g, out.Autostash, shown)
		}
	}
}
func (a *app) queryUpdate(m locales.Messages, s *targetUpdateOperation) {
	r := a.request()
	if !r.current() || r.client == nil || r.client.Desktop == nil || s.done || s.checking || s.canceling || !slices.Contains(a.targetUpdates, s) {
		return
	}
	s.checking = true
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		out, err := r.client.GitUpdateStatus(ctx, s.ref, s.params)
		a.update(func() {
			s.checking = false
			if s.done || !slices.Contains(a.targetUpdates, s) {
				return
			}
			if err != nil {
				s.err = errtext.Text(err)
				return
			}
			if out.Result != nil {
				a.finishUpdate(s, *out.Result, r, m)
				return
			}
			s.err = ""
			s.unknown = out.Operation.State == "unknown"
			if out.Operation.State == "done" && out.Operation.Error != nil {
				s.done, s.err = true, errtext.Text(out.Operation.Error)
				a.releaseUpdate(s)
			}
		})
	}()
}
func (a *app) cancelUpdate(m locales.Messages, s *targetUpdateOperation) {
	r := a.request()
	if !r.mayWrite() || r.client == nil || r.client.Desktop == nil || !s.pending() || s.checking || s.canceling || !slices.Contains(a.targetUpdates, s) {
		return
	}
	s.canceling = true
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		out, err := r.client.CancelDesktopOperation(ctx, s.ref)
		a.update(func() {
			s.canceling = false
			if s.done || !slices.Contains(a.targetUpdates, s) {
				return
			}
			if err != nil {
				s.err = errtext.Text(err)
				return
			}
			s.unknown, s.cancelRequested = out.State == "unknown", out.State != "unknown"
			if out.State == "done" && r.current() {
				a.queryUpdate(m, s)
			}
		})
	}()
}
func (a *app) updateOperationState(c *ui.Context, s *targetUpdateOperation) {
	m, t := L(c), c.Theme()
	text := s.what
	switch {
	case s.result != nil:
		if s.result.Status == rpc.GitFetchSucceeded {
			text = m.CommonDone()
		} else {
			text = m.CommonFailed()
		}
	case s.done:
		text = m.CommonFailed()
	case s.unknown:
		text = errtext.Text(errors.New("desktop operation result is no longer available; inspect the current state before trying again"))
	case s.cancelRequested:
		text = m.FetchCancelRequested()
	}
	ui.Text(c, text).FontSize(fsSmall).TextColor(t.TextMuted)
	if s.result != nil && s.result.Started && s.result.Status != rpc.GitFetchSucceeded {
		ui.Text(c, m.FetchPartial()).FontSize(fsSmall).TextColor(t.TextMuted)
	}
	if s.err != "" {
		ui.Text(c, s.err).FontSize(fsSmall).TextColor(t.Danger)
	}
	if !s.done {
		ui.Row(c).Gap(4).Children(func() {
			if ui.Button(c, m.FetchQuery()).Disabled(!a.targetGitReady() || s.checking || s.canceling).Clicked() {
				a.queryUpdate(m, s)
			}
			r := a.request()
			if ui.Button(c, m.CommonCancel()).Disabled(!a.targetGitReady() || r.host != nil && !r.snapshot.Controlled || s.checking || s.canceling || !s.pending()).Clicked() {
				a.cancelUpdate(m, s)
			}
		})
	}
}
func (a *app) updateConsole(project string) []gitOpRecord {
	var ops []gitOpRecord
	for _, s := range a.targetUpdates {
		if s.project != project || !s.pending() && s.at.Before(a.state.GitConsoleClearedAt[project]) {
			continue
		}
		var runs []gitx.Run
		if s.result != nil {
			runs = s.result.Logs
		}
		ops = append(ops, gitOpRecord{name: s.what, runs: runs, repeat: 1, since: s.at, update: s})
	}
	return ops
}
func (a *app) updateConsoleRow(c *ui.Context, s *targetUpdateOperation, day string) {
	t, m := c.Theme(), L(c)
	ui.Column(c.Key(s.key())).Shrink(0).Children(func() {
		if day != "" {
			ui.Text(c, day).FontSize(fsSmall).FontWeight(fwMedium).TextColor(t.TextMuted).Padding(12, 16, 4, 16)
		}
		ui.Column(c).Margin(2, 6).Padding(6, 8).Gap(4).Children(func() {
			ui.Row(c).Gap(8).Children(func() {
				ui.Text(c, s.what).FontSize(fsBody).FontWeight(fwMedium)
				ui.Text(c, s.workspace).FontSize(fsSmall).TextColor(t.TextMuted)
				ui.Spacer(c)
				ui.Text(c, m.AppTimeSeconds(s.at.Local())).FontSize(fsSmall).Font(monoFont).TextColor(t.TextMuted)
			})
			ui.Text(c, s.params.Expected.Root).FontSize(fsSmall).Font(monoFont).TextColor(t.TextMuted)
			a.updateOperationState(c, s)
			if s.result != nil {
				if s.result.LogsTruncated {
					ui.Text(c, m.FetchLogsTruncated()).FontSize(fsSmall).TextColor(t.TextMuted)
				}
				for _, run := range s.result.Logs {
					gitRunRow(c, run)
				}
			}
		})
	})
}
