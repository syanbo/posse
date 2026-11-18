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
type targetIntegrateOperation struct {
	target                               gitOperationTarget
	ref                                  rpc.DesktopOperationRef
	params                               rpc.GitIntegrateParams
	project, workspace                   string
	request                              appRequest
	at                                   time.Time
	what                                 string
	completed                            func(rpc.GitIntegrateResult)
	repos                                []*gitState
	result                               *rpc.GitIntegrateResult
	err                                  string
	unknown, done, acknowledged          bool
	stopped                              bool
	checking, canceling, cancelRequested bool
}

func (s *targetIntegrateOperation) pending() bool { return !s.done && !s.acknowledged }
func (s *targetIntegrateOperation) key() string {
	return fmt.Sprintf("integrate:%s:%s:%d:%d", s.ref.Desktop.ClientID, s.ref.Desktop.InstanceID, s.ref.Desktop.Epoch, s.ref.Sequence)
}
func (a *app) integrateFor(g *gitState) *targetIntegrateOperation {
	if g == nil {
		return nil
	}
	for i := len(a.targetIntegrations) - 1; i >= 0; i-- {
		s := a.targetIntegrations[i]
		if s.matchesRepository(g) {
			return s
		}
	}
	return nil
}

func (s *targetIntegrateOperation) matchesRepository(g *gitState) bool {
	return s.params.Expected == g.identity || s.params.Expected.CommonDir != "" && s.params.Expected.CommonDir == g.identity.CommonDir
}

func (a *app) holdIntegrate(s *targetIntegrateOperation, g *gitState) {
	if s.pending() && g.busy == "" && !slices.Contains(s.repos, g) {
		g.busy, g.busyKind, g.busyIntegrate = s.what, gitIntegrate, s
		g.cancel = func() { a.cancelIntegrate(appL(), s) }
		s.repos = append(s.repos, g)
	}
}

func (a *app) releaseIntegrate(s *targetIntegrateOperation) {
	for _, g := range s.repos {
		if g.busyIntegrate == s {
			g.busy, g.busyKind, g.cancel, g.busyIntegrate = "", "", nil, nil
			a.gitNext(g)
		}
	}
	s.repos = nil
}
func (a *app) reserveIntegrate() bool {
	for {
		used := 0
		for _, s := range a.targetIntegrations {
			b, _ := json.Marshal(s.params)
			used += len(b) + len(s.err) + len(s.project) + len(s.workspace) + 1024
			if !s.done {
				used += rpc.GitIntegrateResultLimit
			} else {
				b, _ = json.Marshal(s.result)
				used += len(b)
			}
		}
		if len(a.targetIntegrations) < gitConsoleMax && used+rpc.GitIntegrateResultLimit+rpc.GitIntegrateParamsLimit+1024 <= targetFetchBytes {
			return true
		}
		i := slices.IndexFunc(a.targetIntegrations, func(s *targetIntegrateOperation) bool { return !s.pending() })
		if i < 0 {
			return false
		}
		a.targetIntegrations = slices.Delete(a.targetIntegrations, i, i+1)
	}
}
func (a *app) integrateWritable(v *filesView, g *gitState) bool {
	if !a.fetchWritable(v) || !a.request().mayWrite() {
		return false
	}
	s := a.integrateFor(g)
	for _, view := range a.files {
		for _, candidate := range view.repos.list {
			if candidate == g || candidate.identity.CommonDir != "" && candidate.identity.CommonDir == g.identity.CommonDir {
				if candidate.busy != "" && (s == nil || candidate.busyIntegrate != s) {
					return false
				}
			}
		}
	}
	return s == nil || !s.pending() || s.unknown && !s.checking && !s.canceling
}

func (a *app) integrateInputsCurrent(target gitOperationTarget, p rpc.GitIntegrateParams) bool {
	if !a.integrateWritable(target.view, target.repository) || !target.writable() || !rpc.ValidGitIntegrateParams(p) {
		return false
	}
	return a.integrateTargetInputsMatch(target, p)
}

func (a *app) integrateTargetInputsMatch(target gitOperationTarget, p rpc.GitIntegrateParams) bool {
	if target.syncBase {
		return a.syncInputsMatch(target, p)
	}
	return integrateInputsMatch(target.repository, p)
}

func integrateInputsMatch(g *gitState, p rpc.GitIntegrateParams) bool {
	if g.status == nil || g.status.Commit != p.Head || g.status.Head != p.Current {
		return false
	}
	switch p.Action {
	case rpc.GitIntegrateMerge, rpc.GitIntegrateRebase, rpc.GitIntegrateCheckoutRebase:
		if g.status.Operation != "" {
			return false
		}
		return slices.ContainsFunc(g.branches.list, func(b gitx.Branch) bool { return b.Name == p.Branch && b.Remote == p.Remote && b.OID == p.BranchOID })
	case rpc.GitIntegrateContinue, rpc.GitIntegrateSkip, rpc.GitIntegrateAbort:
		return g.status.Operation == p.Operation && (p.Action != rpc.GitIntegrateContinue || g.status.IndexID == p.IndexID && !slices.ContainsFunc(g.status.Entries, func(e gitx.Entry) bool { return e.Conflict }))
	default:
		return g.status.Operation == ""
	}
}

// 确认后仍核对点击时的 OID 和状态，不用新缓存替换原前提。
func (a *app) startTargetIntegrate(m locales.Messages, target gitOperationTarget, what string, p rpc.GitIntegrateParams, check func() bool, completed func(rpc.GitIntegrateResult)) {
	p.OIDs = slices.Clone(p.OIDs)
	current := func() bool { return a.integrateInputsCurrent(target, p) && (check == nil || check()) }
	if !current() {
		return
	}
	send := func() {
		if !current() {
			return
		}
		if previous := a.integrateFor(target.repository); previous != nil && previous.pending() {
			if !previous.unknown {
				return
			}
			a.confirm(what, m.FetchNewBody(), m.FetchNewAction(), func() {
				a.update(func() {
					if !current() || !previous.pending() || !previous.unknown || previous.checking || previous.canceling {
						return
					}
					previous.acknowledged = true
					// 释放先运行本机队列，新动作等已有队列结束后再复查输入。
					a.releaseIntegrate(previous)
					a.integrateAfterQueue(m, target, what, p, check, completed)
				})
			})
			return
		}
		a.executeIntegrate(m, target, what, p, completed)
	}
	send()
}

// 只在用户确认解除未知结果的阻塞后排队；旧闭包的执行路径保持原样。
func (a *app) integrateAfterQueue(m locales.Messages, target gitOperationTarget, what string, p rpc.GitIntegrateParams, check func() bool, completed func(rpc.GitIntegrateResult)) {
	if !a.fetchWritable(target.view) || !target.writable() || !a.integrateTargetInputsMatch(target, p) || check != nil && !check() {
		return
	}
	if s := a.integrateFor(target.repository); s != nil && s.pending() {
		if s.params.Expected == p.Expected || s.unknown {
			return
		}
		// 另一个 worktree 也共用 common-dir；等它完成后复查固定前提。
		a.holdIntegrate(s, target.repository)
	}
	for _, view := range a.files {
		for _, g := range view.repos.list {
			if (g == target.repository || g.identity.CommonDir != "" && g.identity.CommonDir == target.repository.identity.CommonDir) && g.busy != "" {
				g.queue = append(g.queue, gitOp{m: m, what: what, ready: func() { a.integrateAfterQueue(m, target, what, p, check, completed) }})
				return
			}
		}
	}
	a.executeIntegrate(m, target, what, p, completed)
}

func (a *app) executeIntegrate(m locales.Messages, target gitOperationTarget, what string, p rpc.GitIntegrateParams, completed func(rpc.GitIntegrateResult)) {
	if !a.integrateInputsCurrent(target, p) {
		return
	}
	if !a.reserveIntegrate() {
		a.err = errtext.Text(errors.New("too many desktop operations are running; retry after they finish"))
		return
	}
	r := target.request
	op, err := r.client.PrepareGitIntegrate(p)
	if err != nil {
		a.err = errtext.Text(err)
		return
	}
	s := &targetIntegrateOperation{target: target, ref: op.Ref(), params: p, completed: completed, project: target.project, workspace: target.workspace, request: r, at: time.Now(), what: what}
	a.targetIntegrations = append(a.targetIntegrations, s)
	for _, v := range a.files {
		for _, g := range v.repos.list {
			if s.matchesRepository(g) {
				a.holdIntegrate(s, g)
			}
		}
	}
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
		defer cancel()
		if !r.current() {
			a.update(func() {
				s.done = true
				a.releaseIntegrate(s)
			})
			return
		}
		out, err := op.Execute(ctx)
		a.update(func() {
			if s.done || !slices.Contains(a.targetIntegrations, s) {
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
					a.integrateRejected(s)
					a.releaseIntegrate(s)
				}
				return
			}
			a.finishIntegrate(s, out, r, m)
		})
	}()
}

func (a *app) integrateRejected(s *targetIntegrateOperation) {
	s.completed = nil
}

func (a *app) finishIntegrate(s *targetIntegrateOperation, out rpc.GitIntegrateResult, r appRequest, m locales.Messages) {
	if s.done || !slices.Contains(a.targetIntegrations, s) {
		return
	}
	s.result, s.done, s.unknown = &out, true, false
	defer a.releaseIntegrate(s)
	defer func() { s.completed = nil }()
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
	if g == nil || a.integrateFor(g) != s {
		return
	}
	if !a.pathsControls() || !s.request.current() {
		return
	}
	if s.target.syncBase {
		a.finishSyncIntegrate(m, s, out, g)
		return
	}
	if s.err != "" {
		a.err = m.GitOperationFailed(s.what, s.err)
	}
	if out.Status != rpc.GitFetchSucceeded {
		return
	}
	if s.completed != nil {
		s.completed(out)
		s.completed = nil
	}
	a.refreshRepo(g)
	a.refsChanged(s.project)
}

func (a *app) queryIntegrate(m locales.Messages, s *targetIntegrateOperation) {
	r := a.request()
	if !r.current() || r.client == nil || r.client.Desktop == nil || s.done || s.checking || s.canceling || !slices.Contains(a.targetIntegrations, s) {
		return
	}
	s.checking = true
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		out, err := r.client.GitIntegrateStatus(ctx, s.ref, s.params)
		a.update(func() {
			s.checking = false
			if s.done || !slices.Contains(a.targetIntegrations, s) {
				return
			}
			if err != nil {
				s.err = errtext.Text(err)
				return
			}
			if out.Result != nil {
				a.finishIntegrate(s, *out.Result, r, m)
				return
			}
			s.err = ""
			s.unknown = out.Operation.State == "unknown"
			if out.Operation.State == "done" && out.Operation.Error != nil {
				s.done, s.err = true, errtext.Text(out.Operation.Error)
				a.integrateRejected(s)
				a.releaseIntegrate(s)
			}
		})
	}()
}
func (a *app) cancelIntegrate(m locales.Messages, s *targetIntegrateOperation) {
	r := a.request()
	if !a.pathsControls() || r.client == nil || r.client.Desktop == nil || !s.pending() || s.checking || s.canceling || !slices.Contains(a.targetIntegrations, s) {
		return
	}
	s.canceling = true
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		out, err := r.client.CancelDesktopOperation(ctx, s.ref)
		a.update(func() {
			s.canceling = false
			if s.done || !slices.Contains(a.targetIntegrations, s) {
				return
			}
			if err != nil {
				s.err = errtext.Text(err)
				return
			}
			s.unknown, s.cancelRequested = out.State == "unknown", out.State != "unknown"
			if out.State == "done" && r.current() {
				a.queryIntegrate(m, s)
			}
		})
	}()
}
func (a *app) integrateOperationState(c *ui.Context, s *targetIntegrateOperation) {
	m, t := L(c), c.Theme()
	text := s.what
	switch {
	case s.stopped:
		text = s.what
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
				a.queryIntegrate(m, s)
			}
			r := a.request()
			if ui.Button(c, m.CommonCancel()).Disabled(!a.targetGitReady() || r.host != nil && !r.snapshot.Controlled || s.checking || s.canceling || !s.pending()).Clicked() {
				a.cancelIntegrate(m, s)
			}
		})
	}
}
func (a *app) integrateConsole(project string) []gitOpRecord {
	var ops []gitOpRecord
	for _, s := range a.targetIntegrations {
		if s.project != project || !s.pending() && s.at.Before(a.state.GitConsoleClearedAt[project]) {
			continue
		}
		var runs []gitx.Run
		if s.result != nil {
			runs = s.result.Logs
		}
		ops = append(ops, gitOpRecord{name: s.what, runs: runs, repeat: 1, since: s.at, integrate: s})
	}
	return ops
}
func (a *app) integrateConsoleRow(c *ui.Context, s *targetIntegrateOperation, day string) {
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
			a.integrateOperationState(c, s)
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
