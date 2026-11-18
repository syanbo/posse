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
type targetStashOperation struct {
	target                               gitOperationTarget
	ref                                  rpc.DesktopOperationRef
	params                               rpc.GitStashParams
	project, workspace                   string
	request                              appRequest
	at                                   time.Time
	what                                 string
	retainedBytes                        int
	completed                            func(rpc.GitStashResult)
	repos                                []*gitState
	result                               *rpc.GitStashResult
	err                                  string
	unknown, done, acknowledged          bool
	checking, canceling, cancelRequested bool
}

func (s *targetStashOperation) pending() bool { return !s.done && !s.acknowledged }
func (s *targetStashOperation) key() string {
	return fmt.Sprintf("stash:%s:%s:%d:%d", s.ref.Desktop.ClientID, s.ref.Desktop.InstanceID, s.ref.Desktop.Epoch, s.ref.Sequence)
}
func (a *app) stashFor(g *gitState) *targetStashOperation {
	if g == nil {
		return nil
	}
	for i := len(a.targetStashes) - 1; i >= 0; i-- {
		s := a.targetStashes[i]
		if s.matchesRepository(g) {
			return s
		}
	}
	return nil
}

func (s *targetStashOperation) matchesRepository(g *gitState) bool {
	return s.params.Expected == g.identity || s.params.Expected.CommonDir != "" && s.params.Expected.CommonDir == g.identity.CommonDir
}

func (a *app) holdStash(s *targetStashOperation, g *gitState) {
	if s.pending() && g.busy == "" && !slices.Contains(s.repos, g) {
		g.busy, g.busyKind, g.busyStash = s.what, gitStash, s
		g.cancel = func() { a.cancelStash(appL(), s) }
		s.repos = append(s.repos, g)
	}
}

func (a *app) releaseStash(s *targetStashOperation) {
	for _, g := range s.repos {
		if g.busyStash == s {
			g.busy, g.busyKind, g.cancel, g.busyStash = "", "", nil, nil
			a.gitNext(g)
		}
	}
	s.repos = nil
}
func (a *app) reserveStash(retained int) bool {
	for {
		used := 0
		for _, s := range a.targetStashes {
			b, _ := json.Marshal(s.params)
			used += s.retainedBytes + len(b) + len(s.err) + len(s.project) + len(s.workspace) + 1024
			if !s.done {
				used += rpc.GitStashResultLimit
			} else {
				b, _ = json.Marshal(s.result)
				used += len(b)
			}
		}
		if len(a.targetStashes) < gitConsoleMax && used+retained+rpc.GitStashResultLimit+rpc.GitFetchParamsLimit+1024 <= targetFetchBytes {
			return true
		}
		i := slices.IndexFunc(a.targetStashes, func(s *targetStashOperation) bool { return !s.pending() })
		if i < 0 {
			return false
		}
		a.targetStashes = slices.Delete(a.targetStashes, i, i+1)
	}
}
func (a *app) stashWritable(v *filesView, g *gitState) bool {
	if !a.fetchWritable(v) || !a.request().mayWrite() {
		return false
	}
	s := a.stashFor(g)
	for _, view := range a.files {
		for _, candidate := range view.repos.list {
			if candidate == g || candidate.identity.CommonDir != "" && candidate.identity.CommonDir == g.identity.CommonDir {
				if candidate.busy != "" && (s == nil || candidate.busyStash != s) {
					return false
				}
			}
		}
	}
	return s == nil || !s.pending() || s.unknown && !s.checking && !s.canceling
}

func (a *app) stashInputsCurrent(target gitOperationTarget, p rpc.GitStashParams) bool {
	if !a.stashWritable(target.view, target.repository) || !target.writable() || !rpc.ValidGitStashParams(p) {
		return false
	}
	return stashInputsMatch(target.repository, p)
}

func stashInputsMatch(g *gitState, p rpc.GitStashParams) bool {
	if p.Action != rpc.GitStashDrop && (g.status == nil || g.status.Commit != p.Head || g.status.IndexID != p.IndexID) {
		return false
	}
	if p.Action == rpc.GitStashPush {
		return slices.Equal(g.status.Entries, p.Entries)
	}
	count := 0
	for _, s := range g.stashes {
		if s.Hash == p.OID {
			count++
		}
	}
	return count == 1
}

// 确认后仍核对点击时的 OID 和状态，不用新缓存替换原前提。
func (a *app) startTargetStash(m locales.Messages, target gitOperationTarget, what string, p rpc.GitStashParams, check func() bool, completed func(rpc.GitStashResult)) {
	current := func() bool { return a.stashInputsCurrent(target, p) && (check == nil || check()) }
	if !current() {
		return
	}
	send := func() {
		if !current() {
			return
		}
		if previous := a.stashFor(target.repository); previous != nil && previous.pending() {
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
					a.releaseStash(previous)
					a.stashAfterQueue(m, target, what, p, check, completed)
				})
			})
			return
		}
		a.executeStash(m, target, what, p, completed)
	}
	send()
}

// 只在用户确认解除未知结果的阻塞后排队；旧闭包的执行路径保持原样。
func (a *app) stashAfterQueue(m locales.Messages, target gitOperationTarget, what string, p rpc.GitStashParams, check func() bool, completed func(rpc.GitStashResult)) {
	if !a.fetchWritable(target.view) || !target.writable() || !stashInputsMatch(target.repository, p) || check != nil && !check() {
		return
	}
	if s := a.stashFor(target.repository); s != nil && s.pending() {
		if s.params.Expected == p.Expected || s.unknown {
			return
		}
		// 另一个 worktree 也共用 common-dir；等它完成后复查固定前提。
		a.holdStash(s, target.repository)
	}
	for _, view := range a.files {
		for _, g := range view.repos.list {
			if (g == target.repository || g.identity.CommonDir != "" && g.identity.CommonDir == target.repository.identity.CommonDir) && g.busy != "" {
				g.queue = append(g.queue, gitOp{m: m, what: what, ready: func() { a.stashAfterQueue(m, target, what, p, check, completed) }})
				return
			}
		}
	}
	a.executeStash(m, target, what, p, completed)
}

func (a *app) executeStash(m locales.Messages, target gitOperationTarget, what string, p rpc.GitStashParams, completed func(rpc.GitStashResult)) {
	if !a.stashInputsCurrent(target, p) {
		return
	}
	if !a.reserveStash(len(p.Message)) {
		a.err = errtext.Text(errors.New("too many desktop operations are running; retry after they finish"))
		return
	}
	r := target.request
	op, err := r.client.PrepareGitStash(p)
	if err != nil {
		a.err = errtext.Text(err)
		return
	}
	s := &targetStashOperation{target: target, ref: op.Ref(), params: p.Metadata(), completed: completed, project: target.project, workspace: target.workspace, request: r, at: time.Now(), what: what, retainedBytes: len(p.Message)}
	a.targetStashes = append(a.targetStashes, s)
	for _, v := range a.files {
		for _, g := range v.repos.list {
			if s.matchesRepository(g) {
				a.holdStash(s, g)
			}
		}
	}
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
		defer cancel()
		if !r.current() {
			a.update(func() {
				s.done = true
				a.releaseStash(s)
			})
			return
		}
		out, err := op.Execute(ctx)
		a.update(func() {
			if s.done || !slices.Contains(a.targetStashes, s) {
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
					a.stashRejected(s)
					a.releaseStash(s)
				}
				return
			}
			a.finishStash(s, out, r, m)
		})
	}()
}

func (a *app) stashRejected(s *targetStashOperation) {
	s.completed = nil
	s.retainedBytes = 0
}

func (a *app) finishStash(s *targetStashOperation, out rpc.GitStashResult, r appRequest, m locales.Messages) {
	if s.done || !slices.Contains(a.targetStashes, s) {
		return
	}
	s.result, s.done, s.unknown = &out, true, false
	defer a.releaseStash(s)
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
	if g == nil || a.stashFor(g) != s {
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
	if s.params.Action == rpc.GitStashPop || s.params.Action == rpc.GitStashDrop {
		for _, view := range a.files {
			for _, repo := range view.repos.list {
				if s.matchesRepository(repo) && repo.autostash == s.params.OID {
					repo.autostash = ""
				}
			}
		}
	}
	if s.completed != nil {
		s.completed(out)
		s.completed = nil
	}
	a.refreshRepo(g)
}

func (a *app) queryStash(m locales.Messages, s *targetStashOperation) {
	r := a.request()
	if !r.current() || r.client == nil || r.client.Desktop == nil || s.done || s.checking || s.canceling || !slices.Contains(a.targetStashes, s) {
		return
	}
	s.checking = true
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		out, err := r.client.GitStashStatus(ctx, s.ref, s.params)
		a.update(func() {
			s.checking = false
			if s.done || !slices.Contains(a.targetStashes, s) {
				return
			}
			if err != nil {
				s.err = errtext.Text(err)
				return
			}
			if out.Result != nil {
				a.finishStash(s, *out.Result, r, m)
				return
			}
			s.err = ""
			s.unknown = out.Operation.State == "unknown"
			if out.Operation.State == "done" && out.Operation.Error != nil {
				s.done, s.err = true, errtext.Text(out.Operation.Error)
				a.stashRejected(s)
				a.releaseStash(s)
			}
		})
	}()
}
func (a *app) cancelStash(m locales.Messages, s *targetStashOperation) {
	r := a.request()
	if !a.pathsControls() || r.client == nil || r.client.Desktop == nil || !s.pending() || s.checking || s.canceling || !slices.Contains(a.targetStashes, s) {
		return
	}
	s.canceling = true
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		out, err := r.client.CancelDesktopOperation(ctx, s.ref)
		a.update(func() {
			s.canceling = false
			if s.done || !slices.Contains(a.targetStashes, s) {
				return
			}
			if err != nil {
				s.err = errtext.Text(err)
				return
			}
			s.unknown, s.cancelRequested = out.State == "unknown", out.State != "unknown"
			if out.State == "done" && r.current() {
				a.queryStash(m, s)
			}
		})
	}()
}
func (a *app) stashOperationState(c *ui.Context, s *targetStashOperation) {
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
				a.queryStash(m, s)
			}
			r := a.request()
			if ui.Button(c, m.CommonCancel()).Disabled(!a.targetGitReady() || r.host != nil && !r.snapshot.Controlled || s.checking || s.canceling || !s.pending()).Clicked() {
				a.cancelStash(m, s)
			}
		})
	}
}
func (a *app) stashConsole(project string) []gitOpRecord {
	var ops []gitOpRecord
	for _, s := range a.targetStashes {
		if s.project != project || !s.pending() && s.at.Before(a.state.GitConsoleClearedAt[project]) {
			continue
		}
		var runs []gitx.Run
		if s.result != nil {
			runs = s.result.Logs
		}
		ops = append(ops, gitOpRecord{name: s.what, runs: runs, repeat: 1, since: s.at, stash: s})
	}
	return ops
}
func (a *app) stashConsoleRow(c *ui.Context, s *targetStashOperation, day string) {
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
			a.stashOperationState(c, s)
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
