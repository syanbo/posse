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
type targetPathsOperation struct {
	target                               gitOperationTarget
	ref                                  rpc.DesktopOperationRef
	params                               rpc.GitPathsParams
	project, workspace                   string
	request                              appRequest
	at                                   time.Time
	what                                 string
	completed                            func(rpc.GitPathsResult)
	repos                                []*gitState
	result                               *rpc.GitPathsResult
	err                                  string
	unknown, done, acknowledged          bool
	checking, canceling, cancelRequested bool
}

func (s *targetPathsOperation) pending() bool { return !s.done && !s.acknowledged }
func (s *targetPathsOperation) key() string {
	return fmt.Sprintf("paths:%s:%s:%d:%d", s.ref.Desktop.ClientID, s.ref.Desktop.InstanceID, s.ref.Desktop.Epoch, s.ref.Sequence)
}
func (a *app) pathsFor(g *gitState) *targetPathsOperation {
	if g == nil {
		return nil
	}
	for i := len(a.targetPaths) - 1; i >= 0; i-- {
		s := a.targetPaths[i]
		if s.matchesRepository(g) {
			return s
		}
	}
	return nil
}

func (s *targetPathsOperation) matchesRepository(g *gitState) bool {
	return s.params.Expected == g.identity || s.params.Expected.CommonDir != "" && s.params.Expected.CommonDir == g.identity.CommonDir
}

func (a *app) holdPaths(s *targetPathsOperation, g *gitState) {
	if s.pending() && g.busy == "" && !slices.Contains(s.repos, g) {
		g.busy, g.busyKind, g.busyPaths = s.what, gitPaths, s
		g.cancel = func() { a.cancelPaths(appL(), s) }
		s.repos = append(s.repos, g)
	}
}

func (a *app) releasePaths(s *targetPathsOperation) {
	for _, g := range s.repos {
		if g.busyPaths == s {
			g.busy, g.busyKind, g.cancel, g.busyPaths = "", "", nil, nil
			a.gitNext(g)
		}
	}
	s.repos = nil
}
func (a *app) reservePaths() bool {
	for {
		used := 0
		for _, s := range a.targetPaths {
			b, _ := json.Marshal(s.params)
			used += len(b) + len(s.err) + len(s.project) + len(s.workspace) + 1024
			if !s.done {
				used += rpc.GitPathsResultLimit
			} else {
				b, _ = json.Marshal(s.result)
				used += len(b)
			}
		}
		if len(a.targetPaths) < gitConsoleMax && used+rpc.GitPathsResultLimit+rpc.GitFetchParamsLimit+1024 <= targetFetchBytes {
			return true
		}
		i := slices.IndexFunc(a.targetPaths, func(s *targetPathsOperation) bool { return !s.pending() })
		if i < 0 {
			return false
		}
		a.targetPaths = slices.Delete(a.targetPaths, i, i+1)
	}
}
func (a *app) pathsWritable(v *filesView, g *gitState) bool {
	if !a.fetchWritable(v) || !a.request().mayWrite() {
		return false
	}
	s := a.pathsFor(g)
	for _, view := range a.files {
		for _, candidate := range view.repos.list {
			if candidate == g || candidate.identity.CommonDir != "" && candidate.identity.CommonDir == g.identity.CommonDir {
				if candidate.busy != "" && (s == nil || candidate.busyPaths != s) {
					return false
				}
			}
		}
	}
	return s == nil || !s.pending() || s.unknown && !s.checking && !s.canceling
}

func (a *app) pathsInputsCurrent(target gitOperationTarget, p rpc.GitPathsParams) bool {
	if !a.pathsWritable(target.view, target.repository) || !target.writable() || !rpc.ValidGitPathsParams(p) {
		return false
	}
	return pathsInputsMatch(target.repository, p)
}

func pathsInputsMatch(g *gitState, p rpc.GitPathsParams) bool {
	if g.status == nil || g.status.Commit != p.Head {
		return false
	}
	for _, item := range p.Items() {
		i := slices.IndexFunc(g.status.Entries, func(e gitx.Entry) bool { return e.Path == item.Entry.Path })
		if i < 0 || rpc.GitPathCategory(g.status.Entries[i]) != rpc.GitPathCategory(item.Entry) || g.status.Entries[i].Orig != item.Entry.Orig {
			return false
		}
	}
	return true
}

// 确认后仍核对点击时的状态和 diff，不用新缓存替换原前提。
func (a *app) startTargetPaths(m locales.Messages, target gitOperationTarget, what string, p rpc.GitPathsParams, check func() bool, completed func(rpc.GitPathsResult)) {
	current := func() bool { return a.pathsInputsCurrent(target, p) && (check == nil || check()) }
	if !current() {
		return
	}
	send := func() {
		if !current() {
			return
		}
		if previous := a.pathsFor(target.repository); previous != nil && previous.pending() {
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
					a.releasePaths(previous)
					a.pathsAfterQueue(m, target, what, p, check, completed)
				})
			})
			return
		}
		a.executePaths(m, target, what, p, completed)
	}
	send()
}

// 只在用户确认解除未知结果的阻塞后排队；旧闭包的执行路径保持原样。
func (a *app) pathsAfterQueue(m locales.Messages, target gitOperationTarget, what string, p rpc.GitPathsParams, check func() bool, completed func(rpc.GitPathsResult)) {
	if !a.fetchWritable(target.view) || !target.writable() || !pathsInputsMatch(target.repository, p) || check != nil && !check() {
		return
	}
	if s := a.pathsFor(target.repository); s != nil && s.pending() {
		return
	}
	for _, view := range a.files {
		for _, g := range view.repos.list {
			if (g == target.repository || g.identity.CommonDir != "" && g.identity.CommonDir == target.repository.identity.CommonDir) && g.busy != "" {
				g.queue = append(g.queue, gitOp{m: m, what: what, ready: func() { a.pathsAfterQueue(m, target, what, p, check, completed) }})
				return
			}
		}
	}
	a.executePaths(m, target, what, p, completed)
}

func (a *app) executePaths(m locales.Messages, target gitOperationTarget, what string, p rpc.GitPathsParams, completed func(rpc.GitPathsResult)) {
	if !a.pathsInputsCurrent(target, p) {
		return
	}
	if !a.reservePaths() {
		a.err = errtext.Text(errors.New("too many desktop operations are running; retry after they finish"))
		return
	}
	r := target.request
	op, err := r.client.PrepareGitPaths(p)
	if err != nil {
		a.err = errtext.Text(err)
		return
	}
	s := &targetPathsOperation{target: target, ref: op.Ref(), params: p.Metadata(), completed: completed, project: target.project, workspace: target.workspace, request: r, at: time.Now(), what: what}
	a.targetPaths = append(a.targetPaths, s)
	for _, v := range a.files {
		for _, g := range v.repos.list {
			if s.matchesRepository(g) {
				a.holdPaths(s, g)
			}
		}
	}
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
		defer cancel()
		if !r.current() {
			a.update(func() {
				s.done = true
				a.releasePaths(s)
			})
			return
		}
		out, err := op.Execute(ctx)
		a.update(func() {
			if s.done || !slices.Contains(a.targetPaths, s) {
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
					a.pathsRejected(s)
					a.releasePaths(s)
				}
				return
			}
			a.finishPaths(s, out, r, m)
		})
	}()
}

// 拒绝请求也让逐段回滚重读；整体回滚的后续动作只接受成功结果。
func (a *app) pathsRejected(s *targetPathsOperation) {
	if !s.acknowledged && s.request.current() && a.pathsControls() && s.completed != nil {
		s.completed(rpc.GitPathsResult{Target: s.params, Status: rpc.GitFetchFailed})
	}
	s.completed = nil
}

func (a *app) finishPaths(s *targetPathsOperation, out rpc.GitPathsResult, r appRequest, m locales.Messages) {
	if s.done || !slices.Contains(a.targetPaths, s) {
		return
	}
	s.result, s.done, s.unknown = &out, true, false
	defer a.releasePaths(s)
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
	if g == nil || a.pathsFor(g) != s {
		return
	}
	if !a.pathsControls() || !s.request.current() {
		return
	}
	if s.err != "" {
		a.err = m.GitOperationFailed(s.what, s.err)
	}
	if s.completed != nil {
		s.completed(out)
		s.completed = nil
	}
	a.refreshRepo(g)
}

func (a *app) queryPaths(m locales.Messages, s *targetPathsOperation) {
	r := a.request()
	if !r.current() || r.client == nil || r.client.Desktop == nil || s.done || s.checking || s.canceling || !slices.Contains(a.targetPaths, s) {
		return
	}
	s.checking = true
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		out, err := r.client.GitPathsStatus(ctx, s.ref, s.params)
		a.update(func() {
			s.checking = false
			if s.done || !slices.Contains(a.targetPaths, s) {
				return
			}
			if err != nil {
				s.err = errtext.Text(err)
				return
			}
			if out.Result != nil {
				a.finishPaths(s, *out.Result, r, m)
				return
			}
			s.err = ""
			s.unknown = out.Operation.State == "unknown"
			if out.Operation.State == "done" && out.Operation.Error != nil {
				s.done, s.err = true, errtext.Text(out.Operation.Error)
				a.pathsRejected(s)
				a.releasePaths(s)
			}
		})
	}()
}
func (a *app) cancelPaths(m locales.Messages, s *targetPathsOperation) {
	r := a.request()
	if !a.pathsControls() || r.client == nil || r.client.Desktop == nil || !s.pending() || s.checking || s.canceling || !slices.Contains(a.targetPaths, s) {
		return
	}
	s.canceling = true
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		out, err := r.client.CancelDesktopOperation(ctx, s.ref)
		a.update(func() {
			s.canceling = false
			if s.done || !slices.Contains(a.targetPaths, s) {
				return
			}
			if err != nil {
				s.err = errtext.Text(err)
				return
			}
			s.unknown, s.cancelRequested = out.State == "unknown", out.State != "unknown"
			if out.State == "done" && r.current() {
				a.queryPaths(m, s)
			}
		})
	}()
}
func (a *app) pathsOperationState(c *ui.Context, s *targetPathsOperation) {
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
				a.queryPaths(m, s)
			}
			r := a.request()
			if ui.Button(c, m.CommonCancel()).Disabled(!a.targetGitReady() || r.host != nil && !r.snapshot.Controlled || s.checking || s.canceling || !s.pending()).Clicked() {
				a.cancelPaths(m, s)
			}
		})
	}
}
func (a *app) pathsConsole(project string) []gitOpRecord {
	var ops []gitOpRecord
	for _, s := range a.targetPaths {
		if s.project != project || !s.pending() && s.at.Before(a.state.GitConsoleClearedAt[project]) {
			continue
		}
		var runs []gitx.Run
		if s.result != nil {
			runs = s.result.Logs
		}
		ops = append(ops, gitOpRecord{name: s.what, runs: runs, repeat: 1, since: s.at, paths: s})
	}
	return ops
}
func (a *app) pathsConsoleRow(c *ui.Context, s *targetPathsOperation, day string) {
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
			a.pathsOperationState(c, s)
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

// 查看端的动作入口保持安静，不借 git.write 报错或准备确认回调。
func (a *app) pathsControls() bool {
	if !a.typedFetch() {
		return true
	}
	r := a.request()
	return r.current() && r.client != nil && r.client.Desktop != nil && (r.host == nil || r.snapshot.Controlled)
}
