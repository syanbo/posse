package app

import (
	"slices"

	"posse/internal/errtext"
	"posse/internal/gitx"
	"posse/internal/rpc"
)

func (cd *compareDoc) cancelTargetCompare() {
	if cd.repository == nil {
		return
	}
	cd.read.stop()
	cd.resume = cd.resume || cd.loading
	cd.loading = false
	if cd.commit != nil {
		cd.commit.read.stop()
		cd.commit.loading = false
	}
}

func (a *app) targetCompareCurrent(r appRequest, v *filesView, d *fileDoc, cd *compareDoc, g *gitState) bool {
	return a.targetGitCurrent(r, v) && slices.Contains(v.docs, d) && d.comp == cd && v.repoAt(cd.root) == g && cd.repository == g
}

func (a *app) loadTargetCompare(v *filesView, d *fileDoc) {
	if !a.targetGitSession(v) || !slices.Contains(v.docs, d) || d.comp == nil || d.comp.files {
		return
	}
	cd := d.comp
	g := v.repoAt(cd.root)
	if g == nil {
		cd.err = errtext.Text(errRepoGone)
		cd.request, cd.resume = a.request(), false
		return
	}
	if cd.repository != g {
		cd.cancelTargetCompare()
		cd.repository = g
		cd.onlyOther, cd.onlyCur, cd.commit, cd.picked = nil, nil, nil, nil
		cd.otherOID, cd.curOID = "", ""
		cd.moreOther, cd.moreCur, cd.loaded = false, false, false
	}
	r, identity := a.request(), g.identity
	other, cur := cd.other, cd.cur
	ctx, seq := cd.read.start(r)
	cancel := cd.read.cancel
	cd.request, cd.loading, cd.resume = r, true, false
	go func() {
		defer cancel()
		out, err := r.client.GitCompare(ctx, rpc.GitCompareParams{Path: g.repo, Expected: identity, Current: cur, Other: other, Limit: 1000})
		a.update(func() {
			if cd.read.seq != seq {
				return
			}
			cd.loading = false
			if !a.targetCompareCurrent(r, v, d, cd, g) || g.identity != identity || cd.other != other || cd.cur != cur {
				return
			}
			if err != nil {
				cd.err = errtext.Text(err)
				a.err = cd.err
				return
			}
			if !a.targetGitIdentity(g, out.Identity) {
				return
			}
			cd.onlyOther, cd.onlyCur = out.OnlyOther, out.OnlyCurrent
			cd.otherOID, cd.curOID = out.OtherOID, out.CurrentOID
			cd.moreOther, cd.moreCur = out.MoreOther, out.MoreCurrent
			cd.loaded, cd.err, d.kind = true, "", docCompare
			contains := func(hash string) bool {
				has := func(ci gitx.CommitInfo) bool { return ci.Hash == hash }
				return slices.ContainsFunc(cd.onlyOther, has) || slices.ContainsFunc(cd.onlyCur, has)
			}
			cd.picked = slices.DeleteFunc(cd.picked, func(h string) bool { return !contains(h) })
			if cd.commit != nil && !contains(cd.commit.info.Hash) {
				cd.commit.read.stop()
				cd.commit = nil
			}
		})
	}()
}

func (a *app) resumeTargetCompare(v *filesView, d *fileDoc) {
	cd := d.comp
	if !a.targetGitReady() {
		cd.cancelTargetCompare()
		return
	}
	if !a.targetGitSession(v) {
		return
	}
	if cd.request.app != nil && !cd.request.current() {
		cd.cancelTargetCompare()
		cd.resume = true
	}
	g := v.repoAt(cd.root)
	if !cd.loading && (cd.resume || g != nil && cd.repository != g || cd.err == "" && !cd.loaded) {
		a.loadCompare(v, d)
	}
	if cv := cd.commit; cv != nil && !cv.loading && cv.files == nil && cv.err == "" {
		a.loadTargetCompareCommit(v, d, cv.info)
	}
}

func (a *app) swapTargetCompare(v *filesView, d *fileDoc) {
	cd := d.comp
	pending := cd.loading || cd.resume
	cd.cancelTargetCompare()
	cd.other, cd.cur = cd.cur, cd.other
	cd.otherOID, cd.curOID = cd.curOID, cd.otherOID
	cd.onlyOther, cd.onlyCur = cd.onlyCur, cd.onlyOther
	cd.moreOther, cd.moreCur = cd.moreCur, cd.moreOther
	cd.listOther, cd.listCur = cd.listCur, cd.listOther
	cd.queryOther, cd.queryCur = cd.queryCur, cd.queryOther
	d.path = compareKey(cd.root, cd.other, cd.cur, false)
	if pending {
		a.loadTargetCompare(v, d)
	}
}

func (a *app) loadTargetCompareCommit(v *filesView, d *fileDoc, ci gitx.CommitInfo) {
	if !a.targetGitSession(v) || !slices.Contains(v.docs, d) || d.comp == nil {
		return
	}
	cd := d.comp
	g := v.repoAt(cd.root)
	if g == nil || cd.repository != g {
		return
	}
	cv := cd.commit
	if cv != nil {
		cv.read.stop()
	}
	if cv == nil || cv.info.Hash != ci.Hash {
		cv = &commitView{root: cd.root, info: ci}
		cd.commit = cv
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
			if !a.targetCompareCurrent(r, v, d, cd, g) || cd.commit != cv || g.identity != identity {
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
