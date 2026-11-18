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
type targetHistoryOperation struct {
	target                               gitOperationTarget
	ref                                  rpc.DesktopOperationRef
	params                               rpc.GitHistoryParams
	project, workspace                   string
	request                              appRequest
	at                                   time.Time
	what                                 string
	retainedBytes                        int
	completed                            func(rpc.GitHistoryResult)
	repos                                []*gitState
	result                               *rpc.GitHistoryResult
	err                                  string
	unknown, done, acknowledged          bool
	checking, canceling, cancelRequested bool
}

func (s *targetHistoryOperation) pending() bool { return !s.done && !s.acknowledged }
func (s *targetHistoryOperation) key() string {
	return fmt.Sprintf("history:%s:%s:%d:%d", s.ref.Desktop.ClientID, s.ref.Desktop.InstanceID, s.ref.Desktop.Epoch, s.ref.Sequence)
}
func (a *app) historyFor(g *gitState) *targetHistoryOperation {
	if g == nil {
		return nil
	}
	for i := len(a.targetHistories) - 1; i >= 0; i-- {
		s := a.targetHistories[i]
		if s.matchesRepository(g) {
			return s
		}
	}
	return nil
}

func (s *targetHistoryOperation) matchesRepository(g *gitState) bool {
	return s.params.Expected == g.identity || s.params.Expected.CommonDir != "" && s.params.Expected.CommonDir == g.identity.CommonDir
}

func (a *app) holdHistory(s *targetHistoryOperation, g *gitState) {
	if s.pending() && g.busy == "" && !slices.Contains(s.repos, g) {
		g.busy, g.busyKind, g.busyHistory = s.what, gitHistory, s
		g.cancel = func() { a.cancelHistory(appL(), s) }
		s.repos = append(s.repos, g)
	}
}

func (a *app) releaseGitHistory(s *targetHistoryOperation) {
	for _, g := range s.repos {
		if g.busyHistory == s {
			g.busy, g.busyKind, g.cancel, g.busyHistory = "", "", nil, nil
			a.gitNext(g)
		}
	}
	s.repos = nil
}
func (a *app) reserveHistory(retained int) bool {
	for {
		used := 0
		for _, s := range a.targetHistories {
			b, _ := json.Marshal(s.params)
			used += s.retainedBytes + len(b) + len(s.err) + len(s.project) + len(s.workspace) + 1024
			if !s.done {
				used += rpc.GitHistoryResultLimit
			} else {
				b, _ = json.Marshal(s.result)
				used += len(b)
			}
		}
		if len(a.targetHistories) < gitConsoleMax && used+retained+rpc.GitHistoryResultLimit+rpc.GitFetchParamsLimit+1024 <= targetFetchBytes {
			return true
		}
		i := slices.IndexFunc(a.targetHistories, func(s *targetHistoryOperation) bool { return !s.pending() })
		if i < 0 {
			return false
		}
		a.targetHistories = slices.Delete(a.targetHistories, i, i+1)
	}
}
func (a *app) historyWritable(v *filesView, g *gitState) bool {
	if !a.fetchWritable(v) || !a.request().mayWrite() {
		return false
	}
	s := a.historyFor(g)
	for _, view := range a.files {
		for _, candidate := range view.repos.list {
			if candidate == g || candidate.identity.CommonDir != "" && candidate.identity.CommonDir == g.identity.CommonDir {
				if candidate.busy != "" && (s == nil || candidate.busyHistory != s) {
					return false
				}
			}
		}
	}
	return s == nil || !s.pending() || s.unknown && !s.checking && !s.canceling
}

func (a *app) historyInputsCurrent(target gitOperationTarget, p rpc.GitHistoryParams) bool {
	if !a.historyWritable(target.view, target.repository) || !target.writable() || !rpc.ValidGitHistoryParams(p) {
		return false
	}
	return historyInputsMatch(target.repository, p)
}

func historyInputsMatch(g *gitState, p rpc.GitHistoryParams) bool {
	return g.status != nil && g.status.Commit == p.Head && g.status.Head == p.Current && g.status.Operation == ""
}

// 确认后仍核对点击时的 OID 和状态，不用新缓存替换原前提。
func (a *app) startTargetHistory(m locales.Messages, target gitOperationTarget, what string, p rpc.GitHistoryParams, check func() bool, completed func(rpc.GitHistoryResult)) {
	current := func() bool { return a.historyInputsCurrent(target, p) && (check == nil || check()) }
	if !current() {
		return
	}
	send := func() {
		if !current() {
			return
		}
		if previous := a.historyFor(target.repository); previous != nil && previous.pending() {
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
					a.releaseGitHistory(previous)
					a.historyAfterQueue(m, target, what, p, check, completed)
				})
			})
			return
		}
		a.executeHistory(m, target, what, p, completed)
	}
	send()
}

// 只在用户确认解除未知结果的阻塞后排队；旧闭包的执行路径保持原样。
func (a *app) historyAfterQueue(m locales.Messages, target gitOperationTarget, what string, p rpc.GitHistoryParams, check func() bool, completed func(rpc.GitHistoryResult)) {
	if !a.fetchWritable(target.view) || !target.writable() || !historyInputsMatch(target.repository, p) || check != nil && !check() {
		return
	}
	if s := a.historyFor(target.repository); s != nil && s.pending() {
		if s.params.Expected == p.Expected || s.unknown {
			return
		}
		// 另一个 worktree 也共用 common-dir；等它完成后复查固定前提。
		a.holdHistory(s, target.repository)
	}
	for _, view := range a.files {
		for _, g := range view.repos.list {
			if (g == target.repository || g.identity.CommonDir != "" && g.identity.CommonDir == target.repository.identity.CommonDir) && g.busy != "" {
				g.queue = append(g.queue, gitOp{m: m, what: what, ready: func() { a.historyAfterQueue(m, target, what, p, check, completed) }})
				return
			}
		}
	}
	a.executeHistory(m, target, what, p, completed)
}

func (a *app) executeHistory(m locales.Messages, target gitOperationTarget, what string, p rpc.GitHistoryParams, completed func(rpc.GitHistoryResult)) {
	if !a.historyInputsCurrent(target, p) {
		return
	}
	if !a.reserveHistory(len(p.Message)) {
		a.err = errtext.Text(errors.New("too many desktop operations are running; retry after they finish"))
		return
	}
	r := target.request
	op, err := r.client.PrepareGitHistory(p)
	if err != nil {
		a.err = errtext.Text(err)
		return
	}
	s := &targetHistoryOperation{target: target, ref: op.Ref(), params: p.Metadata(), completed: completed, project: target.project, workspace: target.workspace, request: r, at: time.Now(), what: what, retainedBytes: len(p.Message)}
	a.targetHistories = append(a.targetHistories, s)
	for _, v := range a.files {
		for _, g := range v.repos.list {
			if s.matchesRepository(g) {
				a.holdHistory(s, g)
			}
		}
	}
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
		defer cancel()
		if !r.current() {
			a.update(func() {
				s.done = true
				a.releaseGitHistory(s)
			})
			return
		}
		out, err := op.Execute(ctx)
		a.update(func() {
			if s.done || !slices.Contains(a.targetHistories, s) {
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
					a.historyRejected(s)
					a.releaseGitHistory(s)
				}
				return
			}
			a.finishHistory(s, out, r, m)
		})
	}()
}

func (a *app) historyRejected(s *targetHistoryOperation) {
	s.completed = nil
	s.retainedBytes = 0
}

func (a *app) finishHistory(s *targetHistoryOperation, out rpc.GitHistoryResult, r appRequest, m locales.Messages) {
	if s.done || !slices.Contains(a.targetHistories, s) {
		return
	}
	s.result, s.done, s.unknown = &out, true, false
	defer a.releaseGitHistory(s)
	defer func() { s.completed = nil; s.retainedBytes = 0 }()
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
	if g == nil || a.historyFor(g) != s {
		return
	}
	if !a.pathsControls() || !s.request.current() {
		return
	}
	if s.err != "" {
		a.err = m.GitOperationFailed(s.what, s.err)
	}
	if out.Status != rpc.GitFetchSucceeded {
		return
	}
	if out.Autostash != nil {
		a.autostashLeft(m, g, out.Autostash, len(a.toasts))
	}
	if s.completed != nil {
		s.completed(out)
		s.completed = nil
	}
	a.refreshRepo(g)
	a.refsChanged(s.project)
}

func (a *app) queryHistory(m locales.Messages, s *targetHistoryOperation) {
	r := a.request()
	if !r.current() || r.client == nil || r.client.Desktop == nil || s.done || s.checking || s.canceling || !slices.Contains(a.targetHistories, s) {
		return
	}
	s.checking = true
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		out, err := r.client.GitHistoryStatus(ctx, s.ref, s.params)
		a.update(func() {
			s.checking = false
			if s.done || !slices.Contains(a.targetHistories, s) {
				return
			}
			if err != nil {
				s.err = errtext.Text(err)
				return
			}
			if out.Result != nil {
				a.finishHistory(s, *out.Result, r, m)
				return
			}
			s.err = ""
			s.unknown = out.Operation.State == "unknown"
			if out.Operation.State == "done" && out.Operation.Error != nil {
				s.done, s.err = true, errtext.Text(out.Operation.Error)
				a.historyRejected(s)
				a.releaseGitHistory(s)
			}
		})
	}()
}
func (a *app) cancelHistory(m locales.Messages, s *targetHistoryOperation) {
	r := a.request()
	if !a.pathsControls() || r.client == nil || r.client.Desktop == nil || !s.pending() || s.checking || s.canceling || !slices.Contains(a.targetHistories, s) {
		return
	}
	s.canceling = true
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		out, err := r.client.CancelDesktopOperation(ctx, s.ref)
		a.update(func() {
			s.canceling = false
			if s.done || !slices.Contains(a.targetHistories, s) {
				return
			}
			if err != nil {
				s.err = errtext.Text(err)
				return
			}
			s.unknown, s.cancelRequested = out.State == "unknown", out.State != "unknown"
			if out.State == "done" && r.current() {
				a.queryHistory(m, s)
			}
		})
	}()
}
func (a *app) historyOperationState(c *ui.Context, s *targetHistoryOperation) {
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
				a.queryHistory(m, s)
			}
			r := a.request()
			if ui.Button(c, m.CommonCancel()).Disabled(!a.targetGitReady() || r.host != nil && !r.snapshot.Controlled || s.checking || s.canceling || !s.pending()).Clicked() {
				a.cancelHistory(m, s)
			}
		})
	}
}
func (a *app) historyConsole(project string) []gitOpRecord {
	var ops []gitOpRecord
	for _, s := range a.targetHistories {
		if s.project != project || !s.pending() && s.at.Before(a.state.GitConsoleClearedAt[project]) {
			continue
		}
		var runs []gitx.Run
		if s.result != nil {
			runs = s.result.Logs
		}
		ops = append(ops, gitOpRecord{name: s.what, runs: runs, repeat: 1, since: s.at, history: s})
	}
	return ops
}
func (a *app) historyConsoleRow(c *ui.Context, s *targetHistoryOperation, day string) {
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
			a.historyOperationState(c, s)
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
