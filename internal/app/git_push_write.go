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

// 已发送操作归主机所有，关闭预览和连接换代都不取消它。
type targetPushOperation struct {
	ref                                  rpc.DesktopOperationRef
	params                               rpc.GitPushParams
	project, workspace                   string
	request                              appRequest
	at                                   time.Time
	commits                              int
	repos                                []*gitState
	result                               *rpc.GitPushResult
	err                                  string
	unknown, done, acknowledged          bool
	checking, canceling, cancelRequested bool
}

func (s *targetPushOperation) pending() bool { return !s.done && !s.acknowledged }
func (s *targetPushOperation) key() string {
	return fmt.Sprintf("push:%s:%s:%d:%d", s.ref.Desktop.ClientID, s.ref.Desktop.InstanceID, s.ref.Desktop.Epoch, s.ref.Sequence)
}
func (a *app) pushFor(g *gitState) *targetPushOperation {
	if g == nil {
		return nil
	}
	for i := len(a.targetPushes) - 1; i >= 0; i-- {
		s := a.targetPushes[i]
		if s.matchesRepository(g) {
			return s
		}
	}
	return nil
}

func (s *targetPushOperation) matchesRepository(g *gitState) bool {
	return s.params.Expected == g.identity || s.params.Expected.CommonDir != "" && s.params.Expected.CommonDir == g.identity.CommonDir
}

func (a *app) holdPush(s *targetPushOperation, g *gitState) {
	if s.pending() && g.busy == "" && !slices.Contains(s.repos, g) {
		g.busy, g.busyKind, g.busyPush = "Push", gitPush, s
		g.cancel = func() { a.cancelPush(appL(), s) }
		s.repos = append(s.repos, g)
	}
}

func (a *app) releasePush(s *targetPushOperation) {
	for _, g := range s.repos {
		if g.busyPush == s {
			g.busy, g.busyKind, g.cancel, g.busyPush = "", "", nil, nil
			a.gitNext(g)
		}
	}
	s.repos = nil
}
func (a *app) reservePush() bool {
	for {
		used := 0
		for _, s := range a.targetPushes {
			b, _ := json.Marshal(s.params)
			used += len(b) + len(s.err) + len(s.project) + len(s.workspace) + 1024
			if !s.done {
				used += rpc.GitPushResultLimit
			} else {
				b, _ = json.Marshal(s.result)
				used += len(b)
			}
		}
		if len(a.targetPushes) < gitConsoleMax && used+rpc.GitPushResultLimit+rpc.GitPushParamsLimit+1024 <= targetFetchBytes {
			return true
		}
		i := slices.IndexFunc(a.targetPushes, func(s *targetPushOperation) bool { return !s.pending() })
		if i < 0 {
			return false
		}
		a.targetPushes = slices.Delete(a.targetPushes, i, i+1)
	}
}
func (a *app) pushWritable(ps *pushState) bool {
	if ps == nil || !ps.remote || !ps.loaded || ps.preview == nil || ps.loading || ps.waiting || ps.resume || ps.err != "" || ps.pushing || ps.repository == nil || ps.view == nil || ps.request.app == nil {
		return false
	}
	v, g := ps.view, ps.repository
	if !a.targetPushCurrent(ps.request, ps, g) || ps.request.host != nil && !ps.request.snapshot.Controlled || ps.request.client == nil || ps.request.client.Desktop == nil || !v.repos.scanned || v.repos.request.app == nil || !v.repos.request.current() || g.identity.Root == "" || g.identity != ps.previewIdentity {
		return false
	}
	s := a.pushFor(g)
	for _, view := range a.files {
		for _, candidate := range view.repos.list {
			if candidate == g || candidate.identity.CommonDir != "" && candidate.identity.CommonDir == g.identity.CommonDir {
				if candidate.busy != "" && (s == nil || candidate.busyPush != s) {
					return false
				}
			}
		}
	}
	return s == nil || !s.pending() || s.unknown && !s.checking && !s.canceling
}
func pushParams(target rpc.GitFetchParams, preview gitx.PushPreviewRead, force, tags bool) rpc.GitPushParams {
	return rpc.GitPushParams{GitFetchParams: target, Branch: preview.Plan.Branch, Remote: preview.Plan.Remote, RemoteBranch: preview.Plan.RemoteBranch, Baseline: preview.Baseline, BranchOID: preview.BranchOID, TrackingRef: preview.TrackingRef, TrackingOID: preview.TrackingOID, TrackingMissing: preview.TrackingMissing, Force: force, Tags: tags}
}
func (a *app) startPush(m locales.Messages, ps *pushState, force bool) {
	if !a.pushWritable(ps) {
		return
	}
	w := a.workspace(ps.ws)
	if w == nil {
		return
	}
	target := a.gitTarget(w, ps.view, ps.repository)
	p := pushParams(target.params, *ps.preview, force, ps.tags)
	seq := ps.read.seq
	current := func() bool {
		return target.writable() && a.pushWritable(ps) && ps.read.seq == seq && p.Matches(*ps.preview) && p.Tags == ps.tags
	}
	send := func() {
		if !current() {
			return
		}
		if previous := a.pushFor(ps.repository); previous != nil && previous.pending() {
			if !previous.unknown {
				return
			}
			a.confirm(m.PushNewTitle(), m.FetchNewBody(), m.PushNewAction(), func() {
				a.update(func() {
					if !current() || !previous.pending() || !previous.unknown || previous.checking || previous.canceling {
						return
					}
					previous.acknowledged = true
					a.releasePush(previous)
					a.executePush(m, ps, target, p)
				})
			})
			return
		}
		a.executePush(m, ps, target, p)
	}
	if force {
		a.confirm(m.CompareForcePushConfirm(p.Branch), m.CompareForcePushBody(p.Remote+"/"+p.RemoteBranch), m.CompareForcePush(), func() { a.update(send) })
	} else {
		send()
	}
}
func (a *app) executePush(m locales.Messages, ps *pushState, target gitOperationTarget, p rpc.GitPushParams) {
	if !target.writable() || !a.pushWritable(ps) {
		return
	}
	if !a.reservePush() {
		a.err = m.PushCapacity()
		return
	}
	r := target.request
	op, err := r.client.PrepareGitPush(p)
	if err != nil {
		a.err = errtext.Text(err)
		return
	}
	s := &targetPushOperation{ref: op.Ref(), params: p, project: target.project, workspace: target.workspace, request: r, at: time.Now(), commits: len(ps.preview.Plan.Commits)}
	a.targetPushes = append(a.targetPushes, s)
	for _, v := range a.files {
		for _, g := range v.repos.list {
			if s.matchesRepository(g) {
				a.holdPush(s, g)
			}
		}
	}
	ps.pushing = true
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
		defer cancel()
		if !r.current() {
			a.update(func() {
				s.done = true
				ps.pushing = false
				a.releasePush(s)
				a.reloadPushIfOpen(ps)
			})
			return
		}
		out, err := op.Execute(ctx)
		a.update(func() {
			ps.pushing = false
			if s.done || !slices.Contains(a.targetPushes, s) {
				if s.done && !s.unknown {
					a.reloadPushIfOpen(ps)
				}
				return
			}
			if err != nil {
				var re *rpc.Error
				s.unknown = !errors.As(err, &re) || re.Code == rpc.CodeOutcomeUnknown
				s.done, s.err = !s.unknown, errtext.Text(err)
				if s.done {
					a.releasePush(s)
					// 结果未知时不补读：补读会把预览标成加载中，另开推送的确认框出不来。
					a.reloadPushIfOpen(ps)
				}
				return
			}
			a.finishPush(s, out, r, m)
		})
	}()
}
func (a *app) finishPush(s *targetPushOperation, out rpc.GitPushResult, r appRequest, m locales.Messages) {
	if s.done || !slices.Contains(a.targetPushes, s) {
		return
	}
	s.result, s.done, s.unknown = &out, true, false
	defer a.releasePush(s)
	s.err = ""
	if out.Error != nil {
		s.err = errtext.Text(out.Error)
	}
	if out.Rejected {
		if s.params.Force {
			s.err = m.CompareForceRejected()
		} else {
			s.err = m.ComparePushRejected()
		}
	}
	if !r.current() || s.acknowledged {
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
	if g == nil || a.pushFor(g) != s {
		return
	}
	a.refreshGit(v)
	if ps := a.push; ps != nil && ps.repository == g && out.Status == rpc.GitFetchSucceeded && ps.preview != nil && s.params.Matches(*ps.preview) {
		ps.pushing = false
		a.closePush(ps)
	}
	a.refsChanged(s.project)
	if ps := a.push; ps != nil && ps.repository == g {
		ps.pushing = false
	}
	if out.Status == rpc.GitFetchSucceeded && s.request.current() {
		msg := m.ComparePushedTo(s.params.Remote + "/" + s.params.RemoteBranch)
		if s.commits > 0 {
			msg = m.ComparePushed(s.commits, s.params.Remote+"/"+s.params.RemoteBranch)
		}
		a.notify(msg, "", nil)
	}
}
func (a *app) queryPush(m locales.Messages, s *targetPushOperation) {
	r := a.request()
	if !r.current() || r.client == nil || r.client.Desktop == nil || s.done || s.checking || s.canceling || !slices.Contains(a.targetPushes, s) {
		return
	}
	s.checking = true
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		out, err := r.client.GitPushStatus(ctx, s.ref, s.params)
		a.update(func() {
			s.checking = false
			if s.done || !slices.Contains(a.targetPushes, s) {
				return
			}
			if err != nil {
				s.err = errtext.Text(err)
				return
			}
			if out.Result != nil {
				a.finishPush(s, *out.Result, r, m)
				return
			}
			s.err = ""
			s.unknown = out.Operation.State == "unknown"
			if out.Operation.State == "done" && out.Operation.Error != nil {
				s.done, s.err = true, errtext.Text(out.Operation.Error)
				a.releasePush(s)
				if ps := a.push; ps != nil && !ps.pushing && ps.repository != nil && s.matchesRepository(ps.repository) {
					a.reloadPushIfOpen(ps)
				}
			}
		})
	}()
}
func (a *app) cancelPush(m locales.Messages, s *targetPushOperation) {
	r := a.request()
	if !r.mayWrite() || r.client == nil || r.client.Desktop == nil || !s.pending() || s.checking || s.canceling || !slices.Contains(a.targetPushes, s) {
		return
	}
	s.canceling = true
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		out, err := r.client.CancelDesktopOperation(ctx, s.ref)
		a.update(func() {
			s.canceling = false
			if s.done || !slices.Contains(a.targetPushes, s) {
				return
			}
			if err != nil {
				s.err = errtext.Text(err)
				return
			}
			s.unknown, s.cancelRequested = out.State == "unknown", out.State != "unknown"
			if out.State == "done" && r.current() {
				a.queryPush(m, s)
			}
		})
	}()
}
func pushStatusText(m locales.Messages, s *targetPushOperation) string {
	switch {
	case s.result != nil:
		return m.PushResult(s.result.Status)
	case s.done:
		return m.CommonFailed()
	case s.unknown:
		return m.PushUnknown()
	case s.cancelRequested:
		return m.FetchCancelRequested()
	default:
		return m.PushRunning()
	}
}

func (a *app) pushOperationState(c *ui.Context, s *targetPushOperation) {
	m, t := L(c), c.Theme()
	text := pushStatusText(m, s)
	ui.Text(c, text).FontSize(fsSmall).TextColor(t.TextMuted)
	if s.result != nil && s.result.Started && s.result.Status != rpc.GitFetchSucceeded {
		ui.Text(c, m.PushPartial()).FontSize(fsSmall).TextColor(t.TextMuted)
	}
	if s.err != "" {
		ui.Text(c, s.err).FontSize(fsSmall).TextColor(t.Danger)
	}
	if !s.done {
		ui.Row(c).Gap(4).Children(func() {
			if ui.Button(c, m.FetchQuery()).Disabled(!a.targetGitReady() || s.checking || s.canceling).Clicked() {
				a.queryPush(m, s)
			}
			r := a.request()
			if ui.Button(c, m.PushCancel()).Disabled(!a.targetGitReady() || r.host != nil && !r.snapshot.Controlled || s.checking || s.canceling || !s.pending()).Clicked() {
				a.cancelPush(m, s)
			}
		})
	}
}

// pushOperationBar 把进行中的推送状态放进对话框底部那一排，不在列表上面另起一行。
func (a *app) pushOperationBar(c *ui.Context, s *targetPushOperation) {
	m, t := L(c), c.Theme()
	ui.Text(c, pushStatusText(m, s)).FontSize(fsSmall).TextColor(t.TextMuted).SingleLine().Shrink(1)
	if s.result != nil && s.result.Started && s.result.Status != rpc.GitFetchSucceeded {
		ui.Text(c, m.PushPartial()).FontSize(fsSmall).TextColor(t.TextMuted).SingleLine().Shrink(1)
	}
	if s.err != "" {
		ui.Text(c, s.err).FontSize(fsSmall).TextColor(t.Danger).SingleLine().Shrink(1)
	}
	if s.done {
		return
	}
	r := a.request()
	if pressable(textButton(c, m.FetchQuery())).Disabled(!a.targetGitReady() || s.checking || s.canceling).Clicked() {
		a.queryPush(m, s)
	}
	if pressable(textButton(c, m.PushCancel())).Disabled(!a.targetGitReady() || r.host != nil && !r.snapshot.Controlled || s.checking || s.canceling || !s.pending()).Clicked() {
		a.cancelPush(m, s)
	}
}

func (a *app) pushConsole(project string) []gitOpRecord {
	var ops []gitOpRecord
	for _, s := range a.targetPushes {
		if s.project != project || !s.pending() && s.at.Before(a.state.GitConsoleClearedAt[project]) {
			continue
		}
		var runs []gitx.Run
		if s.result != nil {
			runs = s.result.Logs
		}
		ops = append(ops, gitOpRecord{name: "Push", runs: runs, repeat: 1, since: s.at, push: s})
	}
	return ops
}
func (a *app) pushConsoleRow(c *ui.Context, s *targetPushOperation, day string) {
	t, m := c.Theme(), L(c)
	ui.Column(c.Key(s.key())).Shrink(0).Children(func() {
		if day != "" {
			ui.Text(c, day).FontSize(fsSmall).FontWeight(fwMedium).TextColor(t.TextMuted).Padding(12, 16, 4, 16)
		}
		ui.Column(c).Margin(2, 6).Padding(6, 8).Gap(4).Children(func() {
			ui.Row(c).Gap(8).Children(func() {
				ui.Text(c, "Push").FontSize(fsBody).FontWeight(fwMedium)
				ui.Text(c, s.workspace).FontSize(fsSmall).TextColor(t.TextMuted)
				ui.Spacer(c)
				ui.Text(c, m.AppTimeSeconds(s.at.Local())).FontSize(fsSmall).Font(monoFont).TextColor(t.TextMuted)
			})
			ui.Text(c, s.params.Expected.Root).FontSize(fsSmall).Font(monoFont).TextColor(t.TextMuted)
			a.pushOperationState(c, s)
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
