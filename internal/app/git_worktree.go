package app

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"os"
	"slices"

	"posse/internal/errtext"
	"posse/internal/files"
	"posse/internal/gitx"
	"posse/internal/rpc"
	"posse/locales"
)

// 比较列表固定旧提交和工作树输入；普通改动页的 diff 不带这个基线。
type worktreeBaseline struct {
	baseline string
	revision string
	file     gitx.WorktreeFile
}

func (cd *compareDoc) worktreeSpecs(m locales.Messages) []diffSpec {
	if cd.worktree == nil {
		return nil
	}
	out := make([]diffSpec, len(cd.worktree.Files))
	for i, f := range cd.worktree.Files {
		out[i] = diffSpec{root: cd.root, rel: f.Path, orig: f.Orig, oldRev: cd.worktree.Commit, label: cd.other + " → " + m.GitWorkspace(), worktreeBase: &worktreeBaseline{baseline: cd.worktree.Baseline, revision: cd.other, file: f}}
	}
	return out
}

func (a *app) loadTargetWorktreeCompare(v *filesView, d *fileDoc) {
	if !a.targetGitSession(v) || !slices.Contains(v.docs, d) || d.comp == nil {
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
		cd.changes, cd.worktree, cd.loaded = nil, nil, false
	}
	r, identity, revision := a.request(), g.identity, cd.other
	ctx, seq := cd.read.start(r)
	cancel := cd.read.cancel
	cd.request, cd.loading, cd.resume = r, true, false
	go func() {
		defer cancel()
		out, err := r.client.GitWorktreeCompare(ctx, rpc.GitWorktreeCompareParams{Path: g.repo, Expected: identity, Revision: revision})
		a.update(func() {
			if cd.read.seq != seq {
				return
			}
			cd.loading = false
			if !a.targetCompareCurrent(r, v, d, cd, g) || g.identity != identity || cd.other != revision {
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
			cd.worktree = &out.WorktreeComparison
			cd.changes = make([]gitx.FileChange, len(out.Files))
			for i, f := range out.Files {
				cd.changes[i] = f.FileChange
			}
			cd.loaded, cd.err, d.kind = true, "", docCompare
			a.syncCompareFileDiff(v, cd)
		})
	}()
}

func checkWorktreeDiff(ctx context.Context, r appRequest, s diffSpec, identity gitx.RepositoryIdentity) error {
	out, err := r.client.GitWorktreeCompare(ctx, rpc.GitWorktreeCompareParams{Path: s.root, Expected: identity, Revision: s.oldRev, Baseline: s.worktreeBase.baseline})
	if err != nil {
		return err
	}
	if out.Commit != s.oldRev || out.Baseline != s.worktreeBase.baseline || !slices.Contains(out.Files, s.worktreeBase.file) {
		return gitx.ErrWorktreeChanged
	}
	return nil
}

// gitlink 的正文沿用 Git patch 的表示，不把子模块目录当文件读取。
func gitlinkText(oid string, dirty bool) []byte {
	if oid == "" {
		return nil
	}
	if dirty {
		oid += "-dirty"
	}
	return []byte("Subproject commit " + oid + "\n")
}

func readWorktreeDiff(ctx context.Context, r appRequest, s diffSpec, identity gitx.RepositoryIdentity) (old, nw []byte, oldExists, newExists bool, stamp files.Stamp, err error) {
	f := s.worktreeBase.file
	oldExists, newExists = f.BaseMode != "", f.Kind != "missing" && f.Kind != "directory"
	switch f.BaseMode {
	case "":
	case "160000":
		old = gitlinkText(f.BaseOID, false)
	default:
		path := s.rel
		if s.orig != "" {
			path = s.orig
		}
		old, oldExists, err = targetRevision(ctx, r, s.root, identity, s.oldRev, path)
	}
	if err != nil {
		return
	}
	stamp = f.Version.Stamp
	switch f.Kind {
	case "file":
		nw, stamp, err = r.client.ReadResource(ctx, s.root, s.rel, files.MaxSize)
		if err == nil {
			sum := sha256.Sum256(nw)
			if !stamp.Same(f.Version.Stamp) || stamp.Mode != f.Version.Stamp.Mode || hex.EncodeToString(sum[:]) != f.Version.Hash {
				err = gitx.ErrWorktreeChanged
			}
		}
	case "symlink":
		nw = []byte(f.Link)
	case "gitlink":
		nw = gitlinkText(f.OID, f.Dirty)
	case "missing", "directory":
	default:
		err = gitx.ErrWorktreeOutput
	}
	// 包括索引、类型切换和删除：仅比较资源戳不足以证明列表仍代表当前工作树。
	if err == nil {
		err = checkWorktreeDiff(ctx, r, s, identity)
	}
	return
}

func (a *app) pollWorktreeDiff(v *filesView, d *fileDoc) {
	df := d.diff
	if !df.drawn || df.compareErr != "" || df.worktreeBase.file.Kind != "file" || !a.targetGitSession(v) {
		return
	}
	g := v.repoAt(df.root)
	if g == nil {
		return
	}
	r, identity, spec := a.request(), g.identity, df.diffSpec
	ctx, seq := df.read.start(r)
	cancel := df.read.cancel
	df.loading = true
	go func() {
		defer cancel()
		entry, err := r.client.FileEntry(ctx, spec.abs())
		expected := spec.worktreeBase.file.Version
		if os.IsNotExist(err) || err == nil && (entry.Path != expected.Path || !entry.Stamp.Same(expected.Stamp) || entry.Stamp.Mode != expected.Stamp.Mode) {
			err = gitx.ErrWorktreeChanged
		}
		a.update(func() {
			if df.read.seq != seq {
				return
			}
			df.loading = false
			if !a.targetContentCurrent(r, v, g, d, identity) || d.diff != df || df.diffSpec != spec {
				return
			}
			if err != nil {
				df.compareErr = errtext.Text(err)
				a.err = df.compareErr
			}
		})
	}()
}

func (df *diffDoc) cancelWorktreeDiff() {
	df.read.stop()
	df.contentSeq++
	df.loading, df.comparing = false, false
}

func (a *app) refreshWorktreeDiff(w *workspace, v *filesView, d *fileDoc) {
	s := d.diff.diffSpec
	key := compareKey(s.root, s.worktreeBase.revision, "", true)
	for _, owner := range v.docs {
		if owner.path == key && owner.comp != nil {
			v.cur = owner
			a.loadCompare(v, owner)
			return
		}
	}
	// 原比较标签已经关闭；仍使用该 diff 所属仓库，不借当前仓库的路径。
	owner := &fileDoc{path: key, started: true, comp: &compareDoc{root: s.root, other: s.worktreeBase.revision, files: true}}
	v.insertDoc(owner)
	a.loadCompare(v, owner)
}
