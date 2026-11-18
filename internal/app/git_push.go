package app

import (
	"slices"

	"posse/internal/errtext"
	"posse/internal/gitx"
	"posse/internal/rpc"
)

func (ps *pushState) clearDetail() {
	if ps.detail != nil {
		ps.detail.cancelGitContent()
		ps.detail = nil
	}
}

func (ps *pushState) cancelTargetPush() {
	if !ps.remote {
		return
	}
	ps.read.stop()
	ps.resume = ps.resume || ps.loading || ps.waiting
	ps.loading, ps.waiting = false, false
	if ps.commit != nil {
		ps.commit.read.stop()
		ps.commit.loading = false
	}
	if ps.detail != nil {
		ps.detail.cancelGitContent()
	}
}

func (ps *pushState) invalidateTargetPush() {
	ps.cancelTargetPush()
	ps.clearDetail()
	ps.commit, ps.preview, ps.plan, ps.loaded = nil, nil, gitx.PushPlan{}, false
	ps.resume = true
}

func (a *app) closePush(ps *pushState) {
	ps.cancelTargetPush()
	ps.clearDetail()
	if ps.view != nil && ps.view.push == ps {
		ps.view.push = nil
	}
	if a.push == ps {
		a.push = nil
	}
}

func (a *app) openTargetPush(w *workspace, branch string) {
	if !a.targetGitReady() {
		a.targetFileUnsupported("git.pushPreview")
		return
	}
	if a.push != nil {
		a.closePush(a.push)
	}
	v := a.filesFor(w)
	ps := &pushState{ws: w.ID, branch: branch, followHead: branch == "", remote: true, view: v}
	a.push, v.push = ps, ps
	a.pushBox.reset()
	a.prepareTargetPush(ps)
}

func (a *app) prepareTargetPush(ps *pushState) {
	v := ps.view
	if a.push != ps || !a.targetGitSession(v) {
		return
	}
	r := a.request()
	ps.request, ps.waiting = r, true
	a.withRepos(v, func() {
		if a.push != ps || v.push != ps || !ps.waiting || !r.current() {
			return
		}
		ps.waiting = false
		if ps.root == "" {
			ps.root = v.git.repo
			if ps.root == "" {
				ps.err, ps.resume = errtext.Text(errRepoGone), false
				return
			}
		}
		a.loadTargetPush(ps)
	})
}

func (a *app) targetPushCurrent(r appRequest, ps *pushState, g *gitState) bool {
	v := ps.view
	return a.push == ps && a.targetGitCurrent(r, v) && v.push == ps && v.repoAt(ps.root) == g && ps.repository == g
}

func (a *app) loadTargetPush(ps *pushState) {
	v := ps.view
	if a.push != ps || !a.targetGitSession(v) || v.push != ps {
		return
	}
	if ps.root == "" || !v.repos.scanned {
		a.prepareTargetPush(ps)
		return
	}
	g := v.repoAt(ps.root)
	if g == nil || g.repo == "" {
		ps.invalidateTargetPush()
		ps.repository = nil
		ps.err, ps.resume, ps.request = errtext.Text(errRepoGone), false, a.request()
		return
	}
	if ps.repository != g {
		ps.invalidateTargetPush()
		ps.repository = g
	}
	r, identity, branch, follow := a.request(), g.identity, ps.branch, ps.followHead
	ctx, seq := ps.read.start(r)
	cancel := ps.read.cancel
	ps.request, ps.loading, ps.resume = r, true, false
	go func() {
		defer cancel()
		var out rpc.GitPushPreviewResult
		var err error
		var changedIdentity *gitx.RepositoryIdentity
		detached := false
		if follow {
			var status rpc.GitStatusResult
			status, err = r.client.GitStatus(ctx, rpc.GitStatusParams{Path: g.repo})
			if err == nil && status.Identity != identity {
				err = gitx.ErrRepositoryChanged
				changedIdentity = &status.Identity
			}
			if err == nil {
				branch = status.Status.Head
				detached = branch == ""
			}
		}
		if err == nil && !detached {
			out, err = r.client.GitPushPreview(ctx, rpc.GitPushPreviewParams{Path: g.repo, Expected: identity, Branch: branch, Limit: 500})
		}
		a.update(func() {
			if ps.read.seq != seq {
				return
			}
			ps.loading = false
			if !a.targetPushCurrent(r, ps, g) || g.identity != identity {
				return
			}
			if changedIdentity != nil {
				a.targetGitIdentity(g, *changedIdentity)
				return
			}
			if branch != ps.branch {
				ps.clearDetail()
				if ps.commit != nil {
					ps.commit.read.stop()
				}
				ps.plan, ps.preview, ps.commit, ps.loaded = gitx.PushPlan{}, nil, nil, false
				ps.branch = branch
			}
			if detached {
				ps.err = appL().CompareDetachedPush()
				return
			}
			if err != nil {
				ps.err = errtext.Text(err)
				a.err = ps.err
				return
			}
			if !a.targetGitIdentity(g, out.Identity) {
				return
			}
			if ps.preview != nil && (ps.plan.Remote != out.Plan.Remote || ps.plan.RemoteBranch != out.Plan.RemoteBranch || ps.preview.Baseline != out.Baseline) {
				ps.clearDetail()
				if ps.commit != nil {
					ps.commit.read.stop()
				}
				ps.commit = nil
			}
			ps.preview, ps.plan, ps.loaded, ps.err = &out.PushPreviewRead, out.Plan, true, ""
			ps.previewIdentity = out.Identity
			if ps.commit != nil && !slices.ContainsFunc(ps.plan.Commits, func(ci gitx.CommitInfo) bool { return ci.Hash == ps.commit.info.Hash }) {
				ps.commit.read.stop()
				ps.commit = nil
				ps.clearDetail()
			}
		})
	}()
}

func (a *app) resumeTargetPush(ps *pushState) {
	if !a.targetGitReady() {
		ps.cancelTargetPush()
		ps.resume = true
		return
	}
	if ps.request.app != nil && !ps.request.current() {
		ps.cancelTargetPush()
		ps.resume = true
	}
	if !a.targetGitSession(ps.view) {
		return
	}
	ps.request = a.request()
	if ps.waiting {
		if !ps.view.repos.scanning && !ps.view.repos.scanned {
			ps.waiting, ps.resume, ps.err = false, false, a.err
		}
		return
	}
	if ps.root == "" {
		if !ps.waiting && ps.err == "" {
			a.prepareTargetPush(ps)
		}
		return
	}
	if !ps.loading && (ps.resume || ps.repository != ps.view.repoAt(ps.root) || !ps.loaded && ps.err == "") {
		a.loadTargetPush(ps)
	}
	if cv := ps.commit; cv != nil && !cv.loading && cv.files == nil && cv.err == "" {
		a.loadTargetPushCommit(ps, cv.info)
	}
	if d := ps.detail; d != nil && !d.diff.loading && !d.diff.loaded && d.err == "" {
		a.loadDiff(ps.view, d)
	}
}

func (a *app) loadTargetPushCommit(ps *pushState, ci gitx.CommitInfo) {
	v := ps.view
	if a.push != ps || !a.targetGitSession(v) || v.push != ps || ps.repository != v.repoAt(ps.root) {
		return
	}
	g := ps.repository
	if g == nil {
		return
	}
	cv := ps.commit
	if cv != nil {
		cv.read.stop()
	}
	if cv == nil || cv.info.Hash != ci.Hash {
		ps.clearDetail()
		cv = &commitView{root: ps.root, info: ci}
		ps.commit = cv
	}
	r, identity := a.request(), g.identity
	ctx, seq := cv.read.start(r)
	cancel := cv.read.cancel
	cv.loading = true
	go func() {
		defer cancel()
		out, err := r.client.GitCommitFiles(ctx, rpc.GitCommitFilesParams{Path: g.repo, Expected: identity, Hash: ci.Hash})
		a.update(func() {
			if cv.read.seq != seq {
				return
			}
			cv.loading = false
			if !a.targetPushCurrent(r, ps, g) || ps.commit != cv || g.identity != identity {
				return
			}
			if err != nil {
				cv.err = errtext.Text(err)
				a.err = cv.err
				return
			}
			if !a.targetGitIdentity(g, out.Identity) {
				return
			}
			cv.info, cv.files, cv.diffBase, cv.err = out.Commit, out.Files, &out.DiffBase, ""
		})
	}()
}
