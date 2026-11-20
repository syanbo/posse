package app

import (
	"context"
	"slices"
	"strings"
	"time"

	"posse/internal/errtext"
	"posse/internal/gitx"
	"posse/internal/rpc"
)

func (ld *logDoc) cancelTargetLog() {
	ld.read.stop()
	ld.gen++
	ld.filterGen++
	ld.resume = ld.resume || ld.loading || ld.filterPending
	ld.loading = false
	if ld.commit != nil {
		ld.commit.read.stop()
		ld.commit.loading = false
	}
	if ld.hist != nil {
		ld.hist.read.stop()
		ld.hist.loading = false
		if ld.hist.diff != nil {
			ld.hist.diff.cancelGitContent()
		}
	}
}

func (v *filesView) cancelTargetLogs() {
	for _, d := range v.logs {
		d.log.cancelTargetLog()
	}
	v.logOpening = false
}

func (v *filesView) ownsGitContent(d *fileDoc) bool {
	if v.push != nil && v.push.detail == d {
		return true
	}
	if slices.Contains(v.docs, d) {
		return true
	}
	for _, log := range v.logs {
		if log.log.hist != nil && log.log.hist.diff == d {
			return true
		}
	}
	return false
}

func (a *app) targetLogCurrent(r appRequest, v *filesView, d *fileDoc, g *gitState) bool {
	return a.targetGitCurrent(r, v) && slices.Contains(v.logs, d) && d.log.root == g.repo && v.repoAt(g.repo) == g && d.log.repository == g
}

func (a *app) readTargetLog(v *filesView, d *fileDoc, more bool, limit int, follow bool) {
	if !a.targetGitSession(v) || !slices.Contains(v.logs, d) {
		return
	}
	ld := d.log
	g := v.repoAt(ld.root)
	if g == nil {
		ld.err = errtext.Text(errRepoGone)
		d.kind = docLog
		return
	}
	if ld.repository != g {
		ld.cancelTargetLog()
		ld.repository = g
		ld.commits, ld.graph, ld.commit, ld.hist = nil, nil, nil, nil
		ld.baseline, ld.nextSkip, ld.more = "", 0, false
		more = false
	}
	if ld.resume && ld.filterPending {
		ld.filter.Text, ld.filter.Author = ld.text, ld.author
		ld.filterPending = false
		// 这次是把没读完的筛选套上，不是引用变了。refsChanged 也会进这里，不能跟着跳到新列表第一条。
		follow = false
	}
	f := ld.filter
	f.Branches = slices.Clone(f.Branches)
	if ld.head {
		f.Branches = []string{"HEAD"}
		if g.status != nil && g.status.Head != "" {
			f.Branches = []string{"refs/heads/" + g.status.Head}
		}
	}
	// 旧列表在筛选失败后仍显示，不能把新条件的下一页接到旧条件上。
	if more && (ld.baseline == "" || !sameLogFilter(f, ld.loadedFilter)) {
		more = false
	}
	r, identity := a.request(), g.identity
	ctx, seq := ld.read.start(r)
	cancel := ld.read.cancel
	ld.gen++
	gen := ld.gen
	ld.loading, ld.resume = true, false
	skip, baseline := 0, ""
	if more {
		skip, baseline = ld.nextSkip, ld.baseline
	}
	params := rpc.GitLogParams{Path: g.repo, Expected: identity, Filter: f, Skip: skip, Limit: min(limit, 1000), Baseline: baseline}
	go func() {
		defer cancel()
		var commits []gitx.CommitInfo
		var out rpc.GitLogResult
		var err error
		// 刷新已展开的多页时整批成功才替换，任何一页失败都保留旧列表。
		for {
			out, err = r.client.GitLog(ctx, params)
			if err != nil {
				break
			}
			commits = append(commits, out.Commits...)
			if more || !out.HasMore || len(commits) >= limit {
				break
			}
			params.Skip, params.Baseline, params.Limit = out.NextSkip, out.Baseline, min(limit-len(commits), 1000)
		}
		a.update(func() {
			if ld.read.seq != seq || ld.gen != gen {
				return
			}
			ld.loading = false
			if !a.targetLogCurrent(r, v, d, g) || g.identity != identity {
				ld.again = false
				return
			}
			if a.logCatchUp(v, d) {
				return
			}
			d.kind = docLog
			if err != nil {
				ld.err = errtext.Text(err)
				a.err = ld.err
				return
			}
			if !a.targetGitIdentity(g, out.Identity) {
				return
			}
			ld.err, ld.head = "", false
			ld.filter.Branches = f.Branches
			ld.loadedFilter = f
			ld.baseline, ld.nextSkip, ld.more = out.Baseline, out.NextSkip, out.HasMore
			prevTip := ""
			if !more && len(ld.commits) > 0 {
				prevTip = ld.commits[0].Hash
			}
			if more {
				ld.commits = append(ld.commits, commits...)
			} else {
				ld.commits = commits
			}
			ld.graph = nil
			if f.Author == "" && strings.TrimSpace(f.Text) == "" && f.Path == "" {
				ld.graph = buildGraph(ld.commits)
			}
			if ld.hist != nil && !containsCommit(ld.commits, ld.hist.sel) {
				ld.hist.read.stop()
				ld.hist.loading, ld.hist.resolved, ld.hist.err = false, false, ""
				if ld.hist.diff != nil {
					ld.hist.diff.cancelGitContent()
				}
				ld.hist.sel, ld.hist.diff = "", nil
			}
			if ld.commit != nil && !containsCommit(ld.commits, ld.commit.info.Hash) {
				ld.commit.read.stop()
				ld.commit = nil
			}
			if follow {
				a.followLogTip(v, d, prevTip)
			}
			if ld.jump != "" && more {
				a.jumpToCommit(v, d, ld.jump)
			} else {
				ld.jump = ""
			}
		})
	}()
}

// followTargetDiffTip 把远端开着的、对着旧顶端提交的 diff 换成新提交里的同一个文件。
func (a *app) followTargetDiffTip(v *filesView, src, doc *fileDoc, info gitx.CommitInfo, prev string) {
	if !a.targetGitSession(v) || doc.diff == nil {
		return
	}
	g := v.repoAt(doc.diff.root)
	w := a.workspace(v.wsID)
	if g == nil || w == nil {
		return
	}
	r, identity := a.request(), g.identity
	rel, root := doc.diff.rel, doc.diff.root
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	go func() {
		defer cancel()
		out, err := r.client.GitCommitFiles(ctx, rpc.GitCommitFilesParams{Path: g.repo, Expected: identity, Hash: info.Hash})
		a.update(func() {
			if !diffStillOpen(v, doc, prev, rel) || g.identity != identity {
				return
			}
			if err != nil || !a.targetGitIdentity(g, out.Identity) {
				return
			}
			if src != nil && slices.Contains(v.logs, src) && src.log != nil && len(src.log.commits) > 0 && src.log.commits[0].Hash != info.Hash {
				a.followLogTip(v, src, prev)
				return
			}
			base := out.DiffBase
			cv := &commitView{root: root, info: out.Commit, files: out.Files, diffBase: &base}
			specs := cv.diffSpecs()
			for _, s := range specs {
				if s.rel == rel {
					a.reopenDiff(w, v, s, specs, true)
					return
				}
			}
		})
	}()
}

func sameLogFilter(a, b gitx.LogFilter) bool {
	return a.Path == b.Path && a.Text == b.Text && a.Author == b.Author && slices.Equal(a.Branches, b.Branches)
}

func (a *app) selectTargetCommit(root string, holder **commitView, ci gitx.CommitInfo) {
	if ps := a.push; ps != nil && ps.remote && ps.root == root && &ps.commit == holder {
		a.loadTargetPushCommit(ps, ci)
		return
	}
	for _, v := range a.files {
		for _, d := range v.docs {
			if d.comp != nil && &d.comp.commit == holder && d.comp.root == root {
				a.loadTargetCompareCommit(v, d, ci)
				return
			}
			if d.blame != nil && &d.blame.sel == holder && d.blame.root == root {
				a.loadTargetBlameCommit(v, d, ci)
				return
			}
		}
		for _, d := range v.logs {
			if &d.log.commit == holder && d.log.root == root {
				a.loadTargetCommit(v, d, ci)
				return
			}
		}
	}
	a.targetFileUnsupported("git.commitFiles")
}

func (a *app) loadTargetCommit(v *filesView, d *fileDoc, ci gitx.CommitInfo) {
	if !a.targetGitSession(v) || !slices.Contains(v.logs, d) {
		return
	}
	ld := d.log
	g := v.repoAt(ld.root)
	if g == nil || ld.repository != g {
		return
	}
	cv := ld.commit
	if cv != nil {
		cv.read.stop()
	}
	if cv == nil || cv.info.Hash != ci.Hash {
		cv = &commitView{root: g.repo, info: ci}
		ld.commit = cv
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
			if !a.targetLogCurrent(r, v, d, g) || ld.commit != cv || g.identity != identity {
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
			cv.info, cv.files, cv.err = out.Commit, out.Files, ""
			cv.diffBase = &out.DiffBase
			if cv.files == nil {
				cv.files = []gitx.FileChange{}
			}
		})
	}()
}

func (a *app) resumeTargetLog(v *filesView, d *fileDoc) {
	if !a.targetGitSession(v) {
		return
	}
	ld := d.log
	if ld.repository != v.repoAt(ld.root) || ld.resume || d.kind == docLoading && !ld.loading {
		// 筛选还没读完被打断时，恢复是换筛选，不是引用变了，不能跟着跳到新列表的第一条
		if ld.filterPending {
			a.loadLog(v, d, false)
		} else {
			a.reloadLog(v, d)
		}
	}
	if cv := ld.commit; cv != nil && cv.files == nil && cv.err == "" && !cv.loading {
		a.loadTargetCommit(v, d, cv.info)
	}
	if h := ld.hist; h != nil && !h.resolved && !h.loading && h.err == "" && h.sel != "" {
		a.retryTargetHistory(v, d)
	}
	if ld.hist != nil && ld.hist.resolved && ld.hist.diff != nil {
		hd := ld.hist.diff
		if !hd.diff.loading && !hd.diff.loaded && hd.err == "" {
			a.loadDiff(v, hd)
		}
	}
}

func (cv *commitView) diffSpecs() []diffSpec {
	specs := commitSpecs(cv.root, cv.info, cv.files)
	if cv.diffBase != nil {
		for i := range specs {
			if cv.files[i].Status != 'A' {
				specs[i].oldRev = *cv.diffBase
			}
		}
	}
	return specs
}

func (a *app) retryTargetHistory(v *filesView, d *fileDoc) {
	for _, ci := range d.log.commits {
		if ci.Hash == d.log.hist.sel {
			a.selectHistory(v, d, ci)
			return
		}
	}
}

func (a *app) loadTargetHistory(v *filesView, d *fileDoc, ci gitx.CommitInfo, hd *fileDoc) {
	if !a.targetGitSession(v) || !slices.Contains(v.logs, d) {
		return
	}
	ld, h := d.log, d.log.hist
	g := v.repoAt(ld.root)
	if g == nil || ld.repository != g {
		return
	}
	r, identity := a.request(), g.identity
	ctx, seq := h.read.start(r)
	cancel := h.read.cancel
	h.loading, h.resolved, h.err = true, false, ""
	go func() {
		defer cancel()
		out, err := r.client.GitCommitFiles(ctx, rpc.GitCommitFilesParams{Path: g.repo, Expected: identity, Hash: ci.Hash})
		a.update(func() {
			if h.read.seq != seq {
				return
			}
			h.loading = false
			if !a.targetLogCurrent(r, v, d, g) || g.identity != identity || ld.hist != h || h.sel != ci.Hash {
				return
			}
			if err != nil {
				h.err = errtext.Text(err)
				a.err = h.err
				return
			}
			if !a.targetGitIdentity(g, out.Identity) {
				return
			}
			hd.diff.oldRev = out.DiffBase
			for _, f := range out.Files {
				if f.Path == ld.filter.Path {
					hd.diff.orig = f.Orig
					if f.Status == 'A' {
						hd.diff.oldRev = ""
					}
					break
				}
			}
			h.resolved, h.diff = true, hd
			a.loadDiff(v, hd)
		})
	}()
}
