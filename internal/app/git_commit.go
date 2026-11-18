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
type targetCommitOperation struct {
	target                               gitOperationTarget
	ref                                  rpc.DesktopOperationRef
	params                               rpc.GitCommitParams
	project, workspace                   string
	request                              appRequest
	at                                   time.Time
	what                                 string
	retainedBytes                        int
	completed                            func(rpc.GitCommitResult)
	repos                                []*gitState
	result                               *rpc.GitCommitResult
	err                                  string
	unknown, done, acknowledged          bool
	checking, canceling, cancelRequested bool
}

func (s *targetCommitOperation) pending() bool { return !s.done && !s.acknowledged }
func (s *targetCommitOperation) key() string {
	return fmt.Sprintf("commit:%s:%s:%d:%d", s.ref.Desktop.ClientID, s.ref.Desktop.InstanceID, s.ref.Desktop.Epoch, s.ref.Sequence)
}
func (a *app) commitFor(g *gitState) *targetCommitOperation {
	if g == nil {
		return nil
	}
	for i := len(a.targetCommits) - 1; i >= 0; i-- {
		s := a.targetCommits[i]
		if s.matchesRepository(g) {
			return s
		}
	}
	return nil
}

func (s *targetCommitOperation) matchesRepository(g *gitState) bool {
	return s.params.Expected == g.identity || s.params.Expected.CommonDir != "" && s.params.Expected.CommonDir == g.identity.CommonDir
}

func (a *app) holdCommit(s *targetCommitOperation, g *gitState) {
	if s.pending() && g.busy == "" && !slices.Contains(s.repos, g) {
		g.busy, g.busyKind, g.busyCommit = s.what, gitCommit, s
		g.cancel = func() { a.cancelCommit(appL(), s) }
		s.repos = append(s.repos, g)
	}
}

func (a *app) releaseCommit(s *targetCommitOperation) {
	for _, g := range s.repos {
		if g.busyCommit == s {
			g.busy, g.busyKind, g.cancel, g.busyCommit = "", "", nil, nil
			a.gitNext(g)
		}
	}
	s.repos = nil
}
func (a *app) reserveCommit(retained int) bool {
	for {
		used := 0
		for _, s := range a.targetCommits {
			b, _ := json.Marshal(s.params)
			used += s.retainedBytes + len(b) + len(s.err) + len(s.project) + len(s.workspace) + 1024
			if !s.done {
				used += rpc.GitCommitResultLimit
			} else {
				b, _ = json.Marshal(s.result)
				used += len(b)
			}
		}
		if len(a.targetCommits) < gitConsoleMax && used+retained+rpc.GitCommitResultLimit+rpc.GitFetchParamsLimit+1024 <= targetFetchBytes {
			return true
		}
		i := slices.IndexFunc(a.targetCommits, func(s *targetCommitOperation) bool { return !s.pending() })
		if i < 0 {
			return false
		}
		a.targetCommits = slices.Delete(a.targetCommits, i, i+1)
	}
}
func (a *app) commitWritable(v *filesView, g *gitState) bool {
	if !a.fetchWritable(v) || !a.request().mayWrite() {
		return false
	}
	s := a.commitFor(g)
	for _, view := range a.files {
		for _, candidate := range view.repos.list {
			if candidate == g || candidate.identity.CommonDir != "" && candidate.identity.CommonDir == g.identity.CommonDir {
				if candidate.busy != "" && (s == nil || candidate.busyCommit != s) {
					return false
				}
			}
		}
	}
	return s == nil || !s.pending() || s.unknown && !s.checking && !s.canceling
}

func (a *app) commitInputsCurrent(target gitOperationTarget, p rpc.GitCommitParams) bool {
	if !a.commitWritable(target.view, target.repository) || !target.writable() || !rpc.ValidGitCommitParams(p) {
		return false
	}
	return commitInputsMatch(target.repository, p)
}

func commitInputsMatch(g *gitState, p rpc.GitCommitParams) bool {
	if g.status == nil || g.status.Commit != p.Head || g.status.IndexID != p.IndexID {
		return false
	}
	for _, item := range p.Paths {
		i := slices.IndexFunc(g.status.Entries, func(e gitx.Entry) bool { return e.Path == item.Entry.Path })
		if i < 0 || rpc.GitPathCategory(g.status.Entries[i]) != rpc.GitPathCategory(item.Entry) || g.status.Entries[i].Orig != item.Entry.Orig {
			return false
		}
	}
	return true
}

// 确认后仍核对点击时的状态和 diff，不用新缓存替换原前提。
func (a *app) startTargetCommit(m locales.Messages, target gitOperationTarget, what string, p rpc.GitCommitParams, check func() bool, completed func(rpc.GitCommitResult)) {
	current := func() bool { return a.commitInputsCurrent(target, p) && (check == nil || check()) }
	if !current() {
		return
	}
	send := func() {
		if !current() {
			return
		}
		if previous := a.commitFor(target.repository); previous != nil && previous.pending() {
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
					a.releaseCommit(previous)
					a.commitAfterQueue(m, target, what, p, check, completed)
				})
			})
			return
		}
		a.executeCommit(m, target, what, p, completed)
	}
	send()
}

// 只在用户确认解除未知结果的阻塞后排队；旧闭包的执行路径保持原样。
func (a *app) commitAfterQueue(m locales.Messages, target gitOperationTarget, what string, p rpc.GitCommitParams, check func() bool, completed func(rpc.GitCommitResult)) {
	if !a.fetchWritable(target.view) || !target.writable() || !commitInputsMatch(target.repository, p) || check != nil && !check() {
		return
	}
	if s := a.commitFor(target.repository); s != nil && s.pending() {
		if s.params.Expected == p.Expected || s.unknown {
			return
		}
		// 同一批次里的另一个 worktree 也共用 common-dir；等它完成后提交自己的固定前提。
		a.holdCommit(s, target.repository)
	}
	for _, view := range a.files {
		for _, g := range view.repos.list {
			if (g == target.repository || g.identity.CommonDir != "" && g.identity.CommonDir == target.repository.identity.CommonDir) && g.busy != "" {
				g.queue = append(g.queue, gitOp{m: m, what: what, ready: func() { a.commitAfterQueue(m, target, what, p, check, completed) }})
				return
			}
		}
	}
	a.executeCommit(m, target, what, p, completed)
}

func (a *app) executeCommit(m locales.Messages, target gitOperationTarget, what string, p rpc.GitCommitParams, completed func(rpc.GitCommitResult)) {
	if !a.commitInputsCurrent(target, p) {
		return
	}
	if !a.reserveCommit(len(p.Message)) {
		a.err = errtext.Text(errors.New("too many desktop operations are running; retry after they finish"))
		return
	}
	r := target.request
	op, err := r.client.PrepareGitCommit(p)
	if err != nil {
		a.err = errtext.Text(err)
		return
	}
	s := &targetCommitOperation{target: target, ref: op.Ref(), params: p.Metadata(), completed: completed, project: target.project, workspace: target.workspace, request: r, at: time.Now(), what: what, retainedBytes: len(p.Message)}
	a.targetCommits = append(a.targetCommits, s)
	for _, v := range a.files {
		for _, g := range v.repos.list {
			if s.matchesRepository(g) {
				a.holdCommit(s, g)
			}
		}
	}
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
		defer cancel()
		if !r.current() {
			a.update(func() {
				s.done = true
				a.releaseCommit(s)
			})
			return
		}
		out, err := op.Execute(ctx)
		a.update(func() {
			if s.done || !slices.Contains(a.targetCommits, s) {
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
					a.commitRejected(s)
					a.releaseCommit(s)
				}
				return
			}
			a.finishCommit(s, out, r, m)
		})
	}()
}

// 已知拒绝交还批次收尾，未知结果保留说明和勾选直到原操作有终态。
func (a *app) commitRejected(s *targetCommitOperation) {
	if !s.acknowledged && s.request.current() && a.pathsControls() && s.completed != nil {
		s.completed(rpc.GitCommitResult{Target: s.params, Status: rpc.GitFetchFailed})
	}
	s.completed = nil
	s.retainedBytes = 0
}

func (a *app) finishCommit(s *targetCommitOperation, out rpc.GitCommitResult, r appRequest, m locales.Messages) {
	if s.done || !slices.Contains(a.targetCommits, s) {
		return
	}
	s.result, s.done, s.unknown = &out, true, false
	defer a.releaseCommit(s)
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
	if g == nil || a.commitFor(g) != s {
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

func (a *app) queryCommit(m locales.Messages, s *targetCommitOperation) {
	r := a.request()
	if !r.current() || r.client == nil || r.client.Desktop == nil || s.done || s.checking || s.canceling || !slices.Contains(a.targetCommits, s) {
		return
	}
	s.checking = true
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		out, err := r.client.GitCommitStatus(ctx, s.ref, s.params)
		a.update(func() {
			s.checking = false
			if s.done || !slices.Contains(a.targetCommits, s) {
				return
			}
			if err != nil {
				s.err = errtext.Text(err)
				return
			}
			if out.Result != nil {
				a.finishCommit(s, *out.Result, r, m)
				return
			}
			s.err = ""
			s.unknown = out.Operation.State == "unknown"
			if out.Operation.State == "done" && out.Operation.Error != nil {
				s.done, s.err = true, errtext.Text(out.Operation.Error)
				a.commitRejected(s)
				a.releaseCommit(s)
			}
		})
	}()
}
func (a *app) cancelCommit(m locales.Messages, s *targetCommitOperation) {
	r := a.request()
	if !a.pathsControls() || r.client == nil || r.client.Desktop == nil || !s.pending() || s.checking || s.canceling || !slices.Contains(a.targetCommits, s) {
		return
	}
	s.canceling = true
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		out, err := r.client.CancelDesktopOperation(ctx, s.ref)
		a.update(func() {
			s.canceling = false
			if s.done || !slices.Contains(a.targetCommits, s) {
				return
			}
			if err != nil {
				s.err = errtext.Text(err)
				return
			}
			s.unknown, s.cancelRequested = out.State == "unknown", out.State != "unknown"
			if out.State == "done" && r.current() {
				a.queryCommit(m, s)
			}
		})
	}()
}
func (a *app) commitOperationState(c *ui.Context, s *targetCommitOperation) {
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
				a.queryCommit(m, s)
			}
			r := a.request()
			if ui.Button(c, m.CommonCancel()).Disabled(!a.targetGitReady() || r.host != nil && !r.snapshot.Controlled || s.checking || s.canceling || !s.pending()).Clicked() {
				a.cancelCommit(m, s)
			}
		})
	}
}
func (a *app) commitConsole(project string) []gitOpRecord {
	var ops []gitOpRecord
	for _, s := range a.targetCommits {
		if s.project != project || !s.pending() && s.at.Before(a.state.GitConsoleClearedAt[project]) {
			continue
		}
		var runs []gitx.Run
		if s.result != nil {
			runs = s.result.Logs
		}
		ops = append(ops, gitOpRecord{name: s.what, runs: runs, repeat: 1, since: s.at, commit: s})
	}
	return ops
}
func (a *app) commitConsoleRow(c *ui.Context, s *targetCommitOperation, day string) {
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
			a.commitOperationState(c, s)
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
