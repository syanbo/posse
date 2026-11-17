package app

import (
	"slices"

	"posse/internal/errtext"
	"posse/internal/gitx"
	"posse/internal/rpc"
)

func (bd *blameDoc) cancelTargetBlame() {
	if bd.repository == nil {
		return
	}
	bd.read.stop()
	bd.resume = bd.resume || bd.loading
	bd.loading = false
	if bd.sel != nil {
		bd.sel.read.stop()
		bd.sel.loading = false
	}
}

func (d *fileDoc) closeBlame() {
	if d.blame != nil {
		d.blame.cancelTargetBlame()
		d.blame = nil
	}
}

func (a *app) targetBlameCurrent(r appRequest, v *filesView, d *fileDoc, bd *blameDoc, g *gitState) bool {
	return a.targetGitCurrent(r, v) && slices.Contains(v.docs, d) && d.blame == bd && v.repoAt(bd.root) == g && bd.repository == g && d.path == bd.path
}

func (a *app) loadTargetBlame(v *filesView, d *fileDoc) {
	if !a.targetGitSession(v) || !slices.Contains(v.docs, d) || d.blame == nil {
		return
	}
	bd := d.blame
	g := v.repoAt(bd.root)
	if g == nil {
		bd.err = errtext.Text(errRepoGone)
		bd.request = a.request()
		bd.resume = false
		return
	}
	if bd.repository != g {
		bd.cancelTargetBlame()
		bd.repository = g
		bd.lines, bd.sel, bd.loaded = nil, nil, false
	}
	r, identity := a.request(), g.identity
	ctx, seq := bd.read.start(r)
	cancel := bd.read.cancel
	bd.request, bd.loading, bd.resume = r, true, false
	go func() {
		defer cancel()
		out, err := r.client.GitBlame(ctx, rpc.GitBlameParams{Path: g.repo, Expected: identity, File: bd.rel})
		a.update(func() {
			if bd.read.seq != seq {
				return
			}
			bd.loading = false
			if !a.targetBlameCurrent(r, v, d, bd, g) || g.identity != identity {
				return
			}
			if err != nil {
				bd.err = errtext.Text(err)
				a.err = bd.err
				return
			}
			if !a.targetGitIdentity(g, out.Identity) {
				return
			}
			bd.lines, bd.loaded, bd.err = out.Lines, true, ""
			bd.hits, bd.hitQ = nil, ""
			if bd.sel != nil && !slices.ContainsFunc(out.Lines, func(l gitx.BlameLine) bool { return l.Hash == bd.sel.info.Hash }) {
				bd.sel.read.stop()
				bd.sel = nil
			}
		})
	}()
}

func (a *app) resumeTargetBlame(v *filesView, d *fileDoc) {
	bd := d.blame
	if !a.targetGitReady() {
		bd.cancelTargetBlame()
		return
	}
	if !a.targetGitSession(v) {
		return
	}
	if bd.request.app != nil && !bd.request.current() {
		bd.cancelTargetBlame()
		bd.resume = true
	}
	g := v.repoAt(bd.root)
	if !bd.loading && (bd.resume || g != nil && bd.repository != g || bd.err == "" && !bd.loaded) {
		a.loadTargetBlame(v, d)
	}
	if cv := bd.sel; cv != nil && !cv.loading && cv.files == nil && cv.err == "" {
		a.loadTargetBlameCommit(v, d, cv.info)
	}
}

func (a *app) loadTargetBlameCommit(v *filesView, d *fileDoc, ci gitx.CommitInfo) {
	if !a.targetGitSession(v) || !slices.Contains(v.docs, d) || d.blame == nil {
		return
	}
	bd := d.blame
	g := v.repoAt(bd.root)
	if g == nil || bd.repository != g {
		return
	}
	cv := bd.sel
	if cv != nil {
		cv.read.stop()
	}
	if cv == nil || cv.info.Hash != ci.Hash {
		cv = &commitView{root: bd.root, info: ci}
		bd.sel = cv
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
			if !a.targetBlameCurrent(r, v, d, bd, g) || bd.sel != cv || g.identity != identity {
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
