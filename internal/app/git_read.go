package app

import (
	"context"
	"slices"
	"time"

	"posse/internal/errtext"
	"posse/internal/gitx"
	"posse/internal/rpc"
)

// 每类读取独立换代；旧回复只能结束自己的 busy，不能结束后来那轮。
type gitRead struct {
	seq    uint64
	cancel context.CancelFunc
}

func (s *gitRead) stop() {
	s.seq++
	if s.cancel != nil {
		s.cancel()
		s.cancel = nil
	}
}

func (s *gitRead) start(r appRequest) (context.Context, uint64) {
	s.stop()
	ctx, cancel := context.WithTimeout(r.app.windowContext(), 30*time.Second)
	s.cancel = cancel
	// 主机身份也会在心跳线程变化；取消网络读取不等待下一次绘制。
	go func() {
		tick := time.NewTicker(100 * time.Millisecond)
		defer tick.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-tick.C:
				if !r.current() {
					cancel()
					return
				}
			}
		}
	}()
	return ctx, s.seq
}

func (v *filesView) cancelGitReads() {
	if !v.remote {
		return
	}
	if v.push != nil {
		v.push.cancelTargetPush()
	}
	v.cancelTargetLogs()
	v.repos.request = appRequest{}
	v.repos.read.stop()
	v.repos.scanning = false
	v.repos.waiting = nil
	for _, d := range v.docs {
		d.cancelGitContent()
	}
	for _, g := range v.repos.list {
		g.statusRead.stop()
		g.branchRead.stop()
		g.cancelTargetBase()
		g.poll, g.pollAgain = panelPoll{}, false
		g.branches.loading, g.branches.again = false, false
	}
}

func (a *app) targetGitReady() bool {
	r := a.request()
	return r.current() && r.client != nil && r.client.Desktop != nil
}

func (a *app) targetGitSession(v *filesView) bool {
	if !a.targetGitReady() || a.files[v.wsID] != v {
		return false
	}
	if v.repos.request.app == nil || !v.repos.request.current() {
		v.cancelGitReads()
		v.repos.request = a.request()
		v.repos.scanned = false
		v.repos.scanAt = time.Time{}
	}
	return true
}

func (a *app) targetGitCurrent(r appRequest, v *filesView) bool {
	return r.current() && a.files[v.wsID] == v
}

func (a *app) scanTargetRepos(v *filesView) {
	if !a.targetGitSession(v) || v.repos.scanning {
		return
	}
	rs := &v.repos
	r := a.request()
	ctx, seq := rs.read.start(r)
	cancel := rs.read.cancel
	rs.scanning = true
	path, single := v.root, a.projectSingleRepo(v)
	go func() {
		defer cancel()
		out, err := r.client.GitDiscover(ctx, rpc.GitDiscoverParams{Path: path, SingleRepo: single})
		a.update(func() {
			if rs.read.seq != seq {
				return
			}
			rs.scanning = false
			if !a.targetGitCurrent(r, v) || v.root != path {
				return
			}
			rs.scanAt = time.Now()
			if err != nil {
				rs.err = errtext.Text(err)
				a.err = rs.err
				return
			}
			rs.err = ""
			old := rs.list
			list := make([]*gitState, 0, len(out.Repos))
			for _, repo := range out.Repos {
				g := v.repoAt(repo.Root)
				if g == nil || g.identity != repo.Identity {
					g = &gitState{v: v, repo: repo.Root, identity: repo.Identity}
				}
				if operation := a.workspaceMergeFor(g); operation != nil && operation.pending() {
					a.holdWorkspaceMerge(operation, g)
				}
				if operation := a.historyFor(g); operation != nil && operation.pending() {
					a.holdHistory(operation, g)
				}
				if operation := a.integrateFor(g); operation != nil && operation.pending() {
					a.holdIntegrate(operation, g)
				}
				g.real, g.aliases = repo.Real, repo.Aliases
				g.marksOf = nil
				list = append(list, g)
			}
			for _, g := range old {
				if !slices.Contains(list, g) {
					if v.push != nil && v.push.repository == g {
						v.push.invalidateTargetPush()
					}
					v.cancelTargetLogs()
					for _, d := range v.docs {
						d.cancelGitContent()
					}
					g.statusRead.stop()
					g.branchRead.stop()
					g.cancelTargetBase()
				}
			}
			rs.list, rs.scanned, rs.partial, rs.single = list, true, out.Partial, single
			v.realRoot = out.RealPath
			rs.marksOf, rs.marks = nil, nil
			if len(list) == 0 {
				a.useRepo(v, rs.none)
			} else if !slices.Contains(list, v.git) {
				a.useRepo(v, a.defaultRepo(v))
			}
			waiting := rs.waiting
			rs.waiting = nil
			for _, fn := range waiting {
				fn()
			}
		})
	}()
}

func (a *app) targetGitIdentity(g *gitState, identity gitx.RepositoryIdentity) bool {
	if g.identity == identity {
		return true
	}
	v := g.v
	if v.push != nil && v.push.repository == g {
		v.push.invalidateTargetPush()
	}
	v.cancelTargetLogs()
	for _, d := range v.docs {
		d.cancelGitContent()
	}
	g.statusRead.stop()
	g.branchRead.stop()
	g.cancelTargetBase()
	next := &gitState{v: v, repo: g.repo, real: g.real, aliases: g.aliases, identity: identity}
	for i, old := range v.repos.list {
		if old == g {
			v.repos.list[i] = next
		}
	}
	if v.git == g {
		a.useRepo(v, next)
	}
	v.repos.scanned = false
	// 身份变化比已在途的发现更新；不能让那份旧列表把仓库再换回去。
	v.repos.read.stop()
	v.repos.scanning = false
	a.err = errtext.Text(gitx.ErrRepositoryChanged)
	a.scanTargetRepos(v)
	return false
}

func (a *app) pollTargetRepo(g *gitState, full bool) {
	v := g.v
	if !a.targetGitSession(v) || v.repoAt(g.repo) != g || g.repo == "" {
		return
	}
	r := a.request()
	ctx, seq := g.statusRead.start(r)
	cancel := g.statusRead.cancel
	g.poll.busy = true
	root, identity := g.repo, g.identity
	go func() {
		defer cancel()
		out, err := r.client.GitStatus(ctx, rpc.GitStatusParams{Path: root, Full: full})
		a.update(func() {
			if g.statusRead.seq != seq {
				return
			}
			g.poll.done()
			if g.pollAgain {
				g.pollAgain = false
				g.poll.at = time.Time{}
			}
			if !a.targetGitCurrent(r, v) || v.repoAt(root) != g || g.identity != identity {
				return
			}
			if err != nil {
				g.err = errtext.Text(err)
				a.err = g.err
				return
			}
			if !a.targetGitIdentity(g, out.Identity) {
				return
			}
			g.err = ""
			out.Status.Entries = slices.DeleteFunc(out.Status.Entries, func(e gitx.Entry) bool { return v.nestedEntry(g, e) })
			old := g.status
			g.status = out.Status
			if full {
				g.head, g.stashes = out.Head, out.Stashes
			}
			g.rows = changeRows(g, g.status, g.stashes)
			a.targetGitChanged(g, old)
		})
	}()
}

func (a *app) loadTargetBranches(g *gitState) {
	v := g.v
	if !a.targetGitSession(v) || v.repoAt(g.repo) != g || g.repo == "" {
		return
	}
	r := a.request()
	ctx, seq := g.branchRead.start(r)
	cancel := g.branchRead.cancel
	g.branches.loading = true
	root, identity := g.repo, g.identity
	go func() {
		defer cancel()
		out, err := r.client.GitBranches(ctx, rpc.GitBranchesParams{Path: root})
		a.update(func() {
			if g.branchRead.seq != seq {
				return
			}
			g.branches.loading = false
			g.branches.at = time.Now()
			if !a.targetGitCurrent(r, v) || v.repoAt(root) != g || g.identity != identity {
				return
			}
			if err != nil {
				a.err = errtext.Text(err)
				return
			}
			if !a.targetGitIdentity(g, out.Identity) {
				return
			}
			if out.Branches == nil {
				out.Branches = []gitx.Branch{}
			}
			g.branches.list, g.branches.recent, g.branches.tags = out.Branches, out.Recent, out.Tags
		})
	}()
}

func statusBranch(g *gitState) string {
	if g == nil || g.status == nil {
		return ""
	}
	if g.status.Head != "" {
		return g.status.Head
	}
	return g.status.Commit[:min(8, len(g.status.Commit))]
}

func (a *app) rejectTargetGit(g *gitState, op gitOp) bool {
	if a.remoteHost() || g.v != nil && g.v.remote {
		op.fail(a, errtext.Text(targetFileError("git.write")))
		return true
	}
	return !a.request().mayWrite()
}
