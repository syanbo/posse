package app

import (
	"context"
	"os"

	"github.com/egoist/mygo/ui/editor"

	"posse/internal/errtext"
	"posse/internal/files"
	"posse/internal/gitx"
	"posse/internal/rpc"
)

func (d *fileDoc) cancelGitContent() {
	if d.comp != nil {
		d.comp.cancelTargetCompare()
	}
	if d.blame != nil {
		d.blame.cancelTargetBlame()
	}
	d.headRead.stop()
	d.headLoading = false
	d.headFor = ""
	if df := d.diff; df != nil {
		df.read.stop()
		df.contentSeq++
		df.loading, df.loaded, df.comparing = false, false, false
	}
}

func (a *app) targetContentCurrent(r appRequest, v *filesView, g *gitState, d *fileDoc, identity gitx.RepositoryIdentity) bool {
	return a.targetGitCurrent(r, v) && v.ownsGitContent(d) && v.repoAt(g.repo) == g && g.identity == identity
}

func (a *app) loadTargetHead(v *filesView, d *fileDoc) {
	if !a.targetGitSession(v) || d.kind != docText || d.ed.Buffer == nil || !v.ownsGitContent(d) {
		return
	}
	g, rel := v.relIn(d.path)
	if g == nil || g.status == nil {
		return
	}
	commit := g.status.Commit
	if commit == "" {
		d.headRead.stop()
		d.headLoading = false
		d.headFor, d.headBuf, d.ed.DiffBase = "", nil, nil
		return
	}
	if d.headLoading && d.headFor == commit {
		return
	}
	r, identity := a.request(), g.identity
	ctx, seq := d.headRead.start(r)
	cancel := d.headRead.cancel
	path, loadSeq, buffer := d.path, d.loadSeq, d.ed.Buffer
	d.headFor, d.headLoading = commit, true
	params := rpc.GitFileParams{Path: g.repo, Expected: identity, File: rel, Source: gitx.FileCommit, Revision: commit}
	go func() {
		defer cancel()
		out, data, err := r.client.ReadGitFile(ctx, params)
		a.update(func() {
			if d.headRead.seq != seq {
				return
			}
			d.headLoading = false
			if !a.targetContentCurrent(r, v, g, d, identity) || d.path != path || d.loadSeq != loadSeq || d.ed.Buffer != buffer || d.kind != docText || g.status == nil || g.status.Commit != commit {
				d.headFor = ""
				return
			}
			if err != nil {
				d.headFor = ""
				a.err = errtext.Text(err)
				return
			}
			if out.File.Missing || files.IsBinary(data) {
				d.headBuf, d.ed.DiffBase = nil, nil
				return
			}
			d.headBuf = editor.NewBuffer(string(data))
			d.ed.DiffBase = d.headBuf
		})
	}()
}

func targetRevision(ctx context.Context, r appRequest, root string, identity gitx.RepositoryIdentity, rev, path string) ([]byte, bool, error) {
	if rev == "" {
		return nil, false, nil
	}
	p := rpc.GitFileParams{Path: root, Expected: identity, File: path, Source: gitx.FileCommit, Revision: rev}
	if rev == ":" {
		p.Source, p.Revision = gitx.FileIndex, ""
	}
	out, data, err := r.client.ReadGitFile(ctx, p)
	return data, err == nil && !out.File.Missing, err
}

func (a *app) loadTargetDiff(v *filesView, d *fileDoc) {
	if !a.targetGitSession(v) || d.diff == nil || !v.ownsGitContent(d) {
		return
	}
	df := d.diff
	df.contentSeq++
	df.comparing = false
	g := v.repoAt(df.root)
	if g == nil {
		d.kind, d.err = docError, errtext.Text(errRepoGone)
		return
	}
	r, identity := a.request(), g.identity
	ctx, seq := df.read.start(r)
	cancel := df.read.cancel
	df.loading = true
	spec, ignore, force := df.diffSpec, df.state.IgnoreWhitespace, df.force
	go func() {
		defer cancel()
		oldPath := spec.rel
		if spec.orig != "" {
			oldPath = spec.orig
		}
		var old, nw []byte
		var stamp files.Stamp
		var err error
		oldExists, newExists := false, true
		if spec.worktreeBase != nil {
			old, nw, oldExists, newExists, stamp, err = readWorktreeDiff(ctx, r, spec, identity)
		} else {
			old, oldExists, err = targetRevision(ctx, r, spec.root, identity, spec.oldRev, oldPath)
			if err == nil {
				if spec.worktree() {
					nw, stamp, err = r.client.ReadResource(ctx, spec.root, spec.rel, files.MaxSize)
					if os.IsNotExist(err) {
						err, newExists, stamp = nil, false, files.Stamp{Missing: true}
					}
					// 工作树读取也是同一次比较的一部分，不能越过仓库替换。
					if err == nil {
						_, err = r.client.GitFile(ctx, rpc.GitFileParams{Path: spec.root, Expected: identity, File: spec.rel, Source: gitx.FileCommit, Revision: "HEAD"})
					}
				} else {
					nw, newExists, err = targetRevision(ctx, r, spec.root, identity, spec.newRev, spec.rel)
				}
			}
		}
		if err == nil {
			err = ctx.Err()
		}
		var images [2]diffImage
		isImage := err == nil && isImagePath(spec.rel)
		if base := spec.worktreeBase; base != nil && (base.file.Kind == "gitlink" || base.file.Kind == "symlink" || base.file.BaseMode == "160000" || base.file.BaseMode == "120000") {
			isImage = false
		}
		if isImage {
			images = [2]diffImage{loadDiffImage(old, oldExists), loadDiffImage(nw, newExists)}
		}
		tooLarge := err == nil && !isImage && !force && max(len(old), len(nw)) > diffMaxSize
		var lines [2]int
		if tooLarge {
			lines = [2]int{lineCount(old), lineCount(nw)}
		}
		var cmp *editor.DiffComparison
		if err == nil && !isImage && !tooLarge && !files.IsBinary(old) && !files.IsBinary(nw) {
			cmp = editor.CompareTexts(string(old), string(nw), ignore)
		}
		a.update(func() {
			if df.read.seq != seq {
				return
			}
			df.loading, df.comparing = false, false
			if !a.targetContentCurrent(r, v, g, d, identity) || d.diff != df || df.diffSpec != spec {
				return
			}
			if err != nil {
				// HEAD 已经更新，不能只靠工作树戳来触发失败后的重试。
				df.retry = spec.worktreeBase == nil
				a.err = errtext.Text(err)
				if spec.worktreeBase != nil {
					df.compareErr = a.err
				}
				if !df.loaded {
					d.kind, d.err = docError, a.err
				}
				return
			}
			df.loaded, df.retry, df.stamp = true, false, stamp
			d.err = ""
			if tooLarge {
				df.sizes, df.lines = [2]int{len(old), len(nw)}, lines
				d.kind = docTooLarge
				return
			}
			if isImage {
				df.binary, df.images, d.kind = true, images, docImageDiff
				return
			}
			if files.IsBinary(old) || files.IsBinary(nw) {
				df.binary, d.kind = true, docBinary
				return
			}
			df.binary = false
			lang := editor.LanguageFor(spec.rel)
			if df.state.Old == nil {
				df.state.Old, df.state.New = editor.NewBuffer(string(old)), editor.NewBuffer(string(nw))
				df.state.Old.SetLanguage(lang)
				df.state.New.SetLanguage(lang)
			} else {
				df.state.Old.Reset(string(old))
				df.state.New.Reset(string(nw))
			}
			// 比较期间用户切换了忽略空白时，由 DiffState 按当前选项重算。
			if cmp != nil && df.state.IgnoreWhitespace == ignore {
				df.state.UseComparison(cmp)
			}
			if len(old)+len(nw) > diffAheadSize {
				trackBigDiff(df)
				releaseMemorySoon()
			}
			d.kind = docDiff
			clampHunk(df)
		})
	}()
}

func (a *app) reloadTargetDiff(v *filesView, d *fileDoc) {
	df := d.diff
	if df.loading || df.worktreeBase != nil && (df.compareErr != "" || !df.drawn) {
		return
	}
	if !df.loaded || df.retry || !df.worktree() && (!df.fixedRevs() || df.newRev == ":") {
		a.loadTargetDiff(v, d)
		return
	}
	if df.worktreeBase != nil {
		a.pollWorktreeDiff(v, d)
		return
	}
	if !df.worktree() || !a.targetGitSession(v) {
		return
	}
	g := v.repoAt(df.root)
	if g == nil {
		return
	}
	r, identity := a.request(), g.identity
	ctx, seq := df.read.start(r)
	cancel := df.read.cancel
	df.loading = true
	path := df.abs()
	go func() {
		defer cancel()
		stats, err := r.fileStats(ctx, []string{path})
		a.update(func() {
			if df.read.seq != seq {
				return
			}
			df.loading = false
			if !a.targetContentCurrent(r, v, g, d, identity) || d.diff != df {
				return
			}
			if err == nil {
				err = filePathError(path, stats[0].Error)
			}
			if err != nil {
				a.err = errtext.Text(err)
				return
			}
			if !stats[0].Stamp.Same(df.stamp) {
				a.loadTargetDiff(v, d)
			}
		})
	}()
}

func (a *app) targetGitChanged(g *gitState, old *gitx.Status) {
	v := g.v
	headMoved := old != nil && old.Commit != g.status.Commit
	for _, d := range v.docs {
		switch {
		case d.diff != nil && d.diff.root == g.repo:
			if headMoved && !d.diff.fixedRevs() {
				a.loadTargetDiff(v, d)
			} else {
				a.reloadTargetDiff(v, d)
			}
		case d.kind == docText && v.repoFor(d.path) == g && (headMoved || d.headFor != g.status.Commit):
			a.loadTargetHead(v, d)
		}
	}
}
