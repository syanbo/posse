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
type targetBranchOperation struct {
	target                               gitOperationTarget
	ref                                  rpc.DesktopOperationRef
	params                               rpc.GitBranchParams
	project, workspace                   string
	request                              appRequest
	at                                   time.Time
	what                                 string
	repos                                []*gitState
	result                               *rpc.GitBranchResult
	err                                  string
	unknown, done, acknowledged          bool
	checking, canceling, cancelRequested bool
}

func (s *targetBranchOperation) pending() bool { return !s.done && !s.acknowledged }
func (s *targetBranchOperation) key() string {
	return fmt.Sprintf("branch:%s:%s:%d:%d", s.ref.Desktop.ClientID, s.ref.Desktop.InstanceID, s.ref.Desktop.Epoch, s.ref.Sequence)
}
func (a *app) branchFor(g *gitState) *targetBranchOperation {
	if g == nil {
		return nil
	}
	for i := len(a.targetBranches) - 1; i >= 0; i-- {
		s := a.targetBranches[i]
		if s.matchesRepository(g) {
			return s
		}
	}
	return nil
}

func (s *targetBranchOperation) matchesRepository(g *gitState) bool {
	return s.params.Expected == g.identity || s.params.Expected.CommonDir != "" && s.params.Expected.CommonDir == g.identity.CommonDir
}

func (a *app) holdBranch(s *targetBranchOperation, g *gitState) {
	if s.pending() && g.busy == "" && !slices.Contains(s.repos, g) {
		g.busy, g.busyKind, g.busyBranch = s.what, gitBranch, s
		g.cancel = func() { a.cancelBranch(appL(), s) }
		s.repos = append(s.repos, g)
	}
}

func (a *app) releaseBranch(s *targetBranchOperation) {
	for _, g := range s.repos {
		if g.busyBranch == s {
			g.busy, g.busyKind, g.cancel, g.busyBranch = "", "", nil, nil
			a.gitNext(g)
		}
	}
	s.repos = nil
}
func (a *app) reserveBranch() bool {
	for {
		used := 0
		for _, s := range a.targetBranches {
			b, _ := json.Marshal(s.params)
			used += len(b) + len(s.err) + len(s.project) + len(s.workspace) + 1024
			if !s.done {
				used += rpc.GitBranchResultLimit
			} else {
				b, _ = json.Marshal(s.result)
				used += len(b)
			}
		}
		if len(a.targetBranches) < gitConsoleMax && used+rpc.GitBranchResultLimit+rpc.GitBranchParamsLimit+1024 <= targetFetchBytes {
			return true
		}
		i := slices.IndexFunc(a.targetBranches, func(s *targetBranchOperation) bool { return !s.pending() })
		if i < 0 {
			return false
		}
		a.targetBranches = slices.Delete(a.targetBranches, i, i+1)
	}
}
func (a *app) branchWritable(v *filesView, g *gitState) bool {
	if !a.fetchWritable(v) || !a.request().mayWrite() {
		return false
	}
	s := a.branchFor(g)
	for _, view := range a.files {
		for _, candidate := range view.repos.list {
			if candidate == g || candidate.identity.CommonDir != "" && candidate.identity.CommonDir == g.identity.CommonDir {
				if candidate.busy != "" && (s == nil || candidate.busyBranch != s) {
					return false
				}
			}
		}
	}
	return s == nil || !s.pending() || s.unknown && !s.checking && !s.canceling
}

func (a *app) branchInputsCurrent(target gitOperationTarget, p rpc.GitBranchParams) bool {
	if !a.branchWritable(target.view, target.repository) || !target.writable() || !rpc.ValidGitBranchParams(p) {
		return false
	}
	return branchInputsMatch(target.repository, p)
}

func branchInputsMatch(g *gitState, p rpc.GitBranchParams) bool {
	if p.Action == rpc.GitBranchRevision && p.RevisionOID != "" && branchSource(g, p.Revision) != p.RevisionOID {
		return false
	}
	if p.Action == rpc.GitBranchCheckout || p.Action == rpc.GitBranchDelete || p.Action == rpc.GitBranchRename {
		found := false
		for _, b := range g.branches.list {
			if !b.Remote && p.Action == rpc.GitBranchRename && b.Name == p.NewBranch {
				return false
			}
			if b.Name == p.Branch && b.Remote == p.Remote {
				if b.OID != p.BranchOID || !b.Remote && p.Action == rpc.GitBranchDelete && (b.Current || b.Worktree != "") || p.Action == rpc.GitBranchRename && !b.Current && b.Worktree != "" {
					return false
				}
				found = true
			}
		}
		if !found {
			return false
		}
		if p.Remote && p.Action == rpc.GitBranchCheckout {
			localOID := ""
			for _, b := range g.branches.list {
				if !b.Remote && b.Name == gitx.LocalOf(p.Branch) {
					localOID = b.OID
				}
			}
			if localOID != p.LocalOID {
				return false
			}
		}
	}
	return true
}

// check 绑定新建起点等仅在窗口可见的输入；确认后不可用新缓存替换点击时的版本。
func (a *app) startTargetBranch(m locales.Messages, target gitOperationTarget, what string, p rpc.GitBranchParams, check func() bool) {
	current := func() bool { return a.branchInputsCurrent(target, p) && (check == nil || check()) }
	if !current() {
		return
	}
	send := func() {
		if !current() {
			return
		}
		if previous := a.branchFor(target.repository); previous != nil && previous.pending() {
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
					a.releaseBranch(previous)
					a.branchAfterQueue(m, target, what, p, check)
				})
			})
			return
		}
		a.executeBranch(m, target, what, p)
	}
	if p.Action == rpc.GitBranchCheckout || p.Action == rpc.GitBranchRevision || p.Action == rpc.GitBranchCreate && p.Checkout {
		if w := a.workspace(target.params.WorkspaceID); w != nil {
			a.guardAgent(m, w, what, send)
		}
	} else {
		send()
	}
}

// 只在用户确认解除未知结果的阻塞后排队；旧闭包的执行路径保持原样。
func (a *app) branchAfterQueue(m locales.Messages, target gitOperationTarget, what string, p rpc.GitBranchParams, check func() bool) {
	if !a.fetchWritable(target.view) || !target.writable() || !branchInputsMatch(target.repository, p) || check != nil && !check() {
		return
	}
	if s := a.branchFor(target.repository); s != nil && s.pending() {
		return
	}
	for _, view := range a.files {
		for _, g := range view.repos.list {
			if (g == target.repository || g.identity.CommonDir != "" && g.identity.CommonDir == target.repository.identity.CommonDir) && g.busy != "" {
				g.queue = append(g.queue, gitOp{m: m, what: what, ready: func() { a.branchAfterQueue(m, target, what, p, check) }})
				return
			}
		}
	}
	a.executeBranch(m, target, what, p)
}

func (a *app) executeBranch(m locales.Messages, target gitOperationTarget, what string, p rpc.GitBranchParams) {
	if !a.branchInputsCurrent(target, p) {
		return
	}
	if !a.reserveBranch() {
		a.err = errtext.Text(errors.New("too many desktop operations are running; retry after they finish"))
		return
	}
	r := target.request
	op, err := r.client.PrepareGitBranch(p)
	if err != nil {
		a.err = errtext.Text(err)
		return
	}
	s := &targetBranchOperation{target: target, ref: op.Ref(), params: p, project: target.project, workspace: target.workspace, request: r, at: time.Now(), what: what}
	a.targetBranches = append(a.targetBranches, s)
	for _, v := range a.files {
		for _, g := range v.repos.list {
			if s.matchesRepository(g) {
				a.holdBranch(s, g)
			}
		}
	}
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
		defer cancel()
		if !r.current() {
			a.update(func() {
				s.done = true
				a.releaseBranch(s)
			})
			return
		}
		out, err := op.Execute(ctx)
		a.update(func() {
			if s.done || !slices.Contains(a.targetBranches, s) {
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
					a.releaseBranch(s)
				}
				return
			}
			a.finishBranch(s, out, r, m)
		})
	}()
}
func (a *app) finishBranch(s *targetBranchOperation, out rpc.GitBranchResult, r appRequest, m locales.Messages) {
	if s.done || !slices.Contains(a.targetBranches, s) {
		return
	}
	s.result, s.done, s.unknown = &out, true, false
	defer a.releaseBranch(s)
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
	if g == nil || a.branchFor(g) != s {
		return
	}
	if !r.current() {
		return
	}
	a.refreshGit(v)
	a.refsChanged(s.project)
	if s.err != "" {
		a.err = m.GitOperationFailed(s.what, s.err)
	}
	if out.Unmerged {
		p := s.params
		p.Force = true
		a.confirm(m.BranchUnmergedTitle(p.Branch), m.BranchUnmergedBody(), m.BranchForceDelete(), func() {
			a.update(func() { a.startTargetBranch(m, s.target, m.BranchForceDeleteOperation(), p, nil) })
		})
	} else if out.Status == rpc.GitFetchSucceeded && s.params.Action == rpc.GitBranchDelete && !s.params.Remote {
		a.notify(m.BranchDeleted(s.params.Branch), m.CommonRestore(), func() {
			p := rpc.GitBranchParams{GitFetchParams: s.params.GitFetchParams, Action: rpc.GitBranchCreate, Branch: s.params.Branch, FromOID: out.OID}
			a.startTargetBranch(m, s.target, m.BranchRestore(), p, nil)
		})
	}
}

func (a *app) queryBranch(m locales.Messages, s *targetBranchOperation) {
	r := a.request()
	if !r.current() || r.client == nil || r.client.Desktop == nil || s.done || s.checking || s.canceling || !slices.Contains(a.targetBranches, s) {
		return
	}
	s.checking = true
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		out, err := r.client.GitBranchStatus(ctx, s.ref, s.params)
		a.update(func() {
			s.checking = false
			if s.done || !slices.Contains(a.targetBranches, s) {
				return
			}
			if err != nil {
				s.err = errtext.Text(err)
				return
			}
			if out.Result != nil {
				a.finishBranch(s, *out.Result, r, m)
				return
			}
			s.err = ""
			s.unknown = out.Operation.State == "unknown"
			if out.Operation.State == "done" && out.Operation.Error != nil {
				s.done, s.err = true, errtext.Text(out.Operation.Error)
				a.releaseBranch(s)
			}
		})
	}()
}
func (a *app) cancelBranch(m locales.Messages, s *targetBranchOperation) {
	r := a.request()
	if !r.mayWrite() || r.client == nil || r.client.Desktop == nil || !s.pending() || s.checking || s.canceling || !slices.Contains(a.targetBranches, s) {
		return
	}
	s.canceling = true
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		out, err := r.client.CancelDesktopOperation(ctx, s.ref)
		a.update(func() {
			s.canceling = false
			if s.done || !slices.Contains(a.targetBranches, s) {
				return
			}
			if err != nil {
				s.err = errtext.Text(err)
				return
			}
			s.unknown, s.cancelRequested = out.State == "unknown", out.State != "unknown"
			if out.State == "done" && r.current() {
				a.queryBranch(m, s)
			}
		})
	}()
}
func (a *app) branchOperationState(c *ui.Context, s *targetBranchOperation) {
	m, t := L(c), c.Theme()
	text := s.what
	switch {
	case s.result != nil && s.result.Unmerged:
		text = m.BranchUnmergedTitle(s.params.Branch)
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
				a.queryBranch(m, s)
			}
			r := a.request()
			if ui.Button(c, m.CommonCancel()).Disabled(!a.targetGitReady() || r.host != nil && !r.snapshot.Controlled || s.checking || s.canceling || !s.pending()).Clicked() {
				a.cancelBranch(m, s)
			}
		})
	}
}
func (a *app) branchConsole(project string) []gitOpRecord {
	var ops []gitOpRecord
	for _, s := range a.targetBranches {
		if s.project != project || !s.pending() && s.at.Before(a.state.GitConsoleClearedAt[project]) {
			continue
		}
		var runs []gitx.Run
		if s.result != nil {
			runs = s.result.Logs
		}
		ops = append(ops, gitOpRecord{name: s.what, runs: runs, repeat: 1, since: s.at, branch: s})
	}
	return ops
}
func (a *app) branchConsoleRow(c *ui.Context, s *targetBranchOperation, day string) {
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
			a.branchOperationState(c, s)
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
func (a *app) branchControls() bool {
	if !a.typedFetch() {
		return true
	}
	r := a.request()
	return r.current() && r.client != nil && r.client.Desktop != nil && (r.host == nil || r.snapshot.Controlled)
}

func branchParams(target gitOperationTarget, b gitx.Branch, action string) rpc.GitBranchParams {
	p := rpc.GitBranchParams{GitFetchParams: target.params, Action: action, Branch: b.Name, BranchOID: b.OID, Remote: b.Remote}
	if b.Remote && action == rpc.GitBranchCheckout {
		for _, local := range target.repository.branches.list {
			if !local.Remote && local.Name == gitx.LocalOf(b.Name) {
				p.LocalOID = local.OID
			}
		}
	}
	return p
}

// 起点只从已显示的提交读取；没有输入时固定此刻 HEAD，包含分离 HEAD。
func branchSource(g *gitState, from string) string {
	if from == "" || from == "HEAD" {
		if g.status != nil {
			return g.status.Commit
		}
		return ""
	}
	if rpc.ValidGitBranchOID(from) {
		return from
	}
	for _, b := range g.branches.list {
		if b.Name == from || branchRef(b) == from {
			return b.OID
		}
	}
	return ""
}

func (a *app) askCheckoutRevision(m locales.Messages, w *workspace, v *filesView) {
	if !a.branchControls() {
		return
	}
	g := v.git
	target := a.gitTarget(w, v, g)
	a.prompt(promptReq{title: m.BranchCheckoutRevision(), placeholder: m.BranchRevisionPlaceholder(), verb: m.BranchCheckout(), do: func(rev string, _ bool) {
		if a.typedFetch() {
			p := rpc.GitBranchParams{GitFetchParams: target.params, Action: rpc.GitBranchRevision, Revision: rev, RevisionOID: branchSource(g, rev)}
			a.startTargetBranch(m, target, m.BranchCheckout(), p, nil)
			return
		}
		a.guardAgent(m, w, m.BranchCheckout(), func() {
			a.gitDo(m, g, m.BranchCheckout(), func(ctx context.Context, root string) error { return gitx.CheckoutRevision(ctx, root, rev) })
		})
	}})
}

func (a *app) checkoutCommit(m locales.Messages, w *workspace, v *filesView, g *gitState, oid string) {
	if !a.branchControls() {
		return
	}
	if a.typedFetch() {
		target := a.gitTarget(w, v, g)
		p := rpc.GitBranchParams{GitFetchParams: target.params, Action: rpc.GitBranchRevision, Revision: oid, RevisionOID: oid}
		a.startTargetBranch(m, target, m.BranchCheckout(), p, nil)
		return
	}
	a.guardAgent(m, w, m.BranchCheckout(), func() {
		a.gitDo(m, g, m.BranchCheckout(), func(ctx context.Context, root string) error { return gitx.CheckoutRevision(ctx, root, oid) })
	})
}

func (a *app) askTargetNewBranch(m locales.Messages, w *workspace, v *filesView, from string, source ...gitx.Branch) {
	if !a.branchControls() || !a.fetchWritable(v) {
		return
	}
	target := a.gitTarget(w, v, v.git)
	oid := branchSource(v.git, from)
	check := func() bool { return branchSource(target.repository, from) == oid }
	if len(source) != 0 {
		selected := source[0]
		oid = selected.OID
		check = func() bool {
			for _, b := range target.repository.branches.list {
				if b.Name == selected.Name && b.Remote == selected.Remote {
					return b.OID == selected.OID
				}
			}
			return false
		}
	}
	title := m.BranchNew()
	if from != "" {
		title = m.BranchNewFromTitle(from)
	}
	a.prompt(promptReq{title: title, placeholder: m.BranchName(), verb: m.CommonNew(), check: m.BranchCheckoutNew(), checked: true, do: func(name string, checkout bool) {
		p := rpc.GitBranchParams{GitFetchParams: target.params, Action: rpc.GitBranchCreate, Branch: name, FromOID: oid, Checkout: checkout}
		a.startTargetBranch(m, target, m.BranchNew(), p, check)
	}})
}

func (a *app) deleteTargetBranch(m locales.Messages, v *filesView, b gitx.Branch) {
	if !a.branchControls() || !a.fetchWritable(v) {
		return
	}
	w := a.workspace(v.wsID)
	if w == nil {
		return
	}
	target := a.gitTarget(w, v, v.git)
	p := branchParams(target, b, rpc.GitBranchDelete)
	if b.Remote {
		a.confirm(m.BranchDeleteRemoteConfirm(b.Name), m.BranchDeleteRemoteBody(), m.CommonDelete(), func() {
			a.update(func() { a.startTargetBranch(m, target, m.BranchDeleteRemote(), p, nil) })
		})
	} else {
		a.startTargetBranch(m, target, m.BranchDelete(), p, nil)
	}
}

func (a *app) askRenameBranch(m locales.Messages, w *workspace, v *filesView, b gitx.Branch) {
	if b.Remote || !a.branchControls() {
		return
	}
	if a.typedFetch() {
		if !a.fetchWritable(v) {
			return
		}
		target := a.gitTarget(w, v, v.git)
		a.prompt(promptReq{title: m.BranchRenameTitle(b.Name), initial: b.Name, verb: m.BranchRename(), do: func(name string, _ bool) {
			if name == b.Name {
				return
			}
			p := branchParams(target, b, rpc.GitBranchRename)
			p.NewBranch = name
			a.startTargetBranch(m, target, m.BranchRename(), p, nil)
		}})
		return
	}
	g := v.git
	a.prompt(promptReq{title: m.BranchRenameTitle(b.Name), initial: b.Name, verb: m.BranchRename(), do: func(name string, _ bool) {
		if name == b.Name {
			return
		}
		a.gitDo(m, g, m.BranchRename(), func(ctx context.Context, root string) error { return gitx.RenameBranch(ctx, root, b.Name, name) })
	}})
}
