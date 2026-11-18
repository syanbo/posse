package app

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"path/filepath"
	"slices"
	"time"

	"github.com/egoist/mygo/ui/editor"
	"posse/internal/errtext"
	"posse/internal/gitx"
	"posse/internal/rpc"
	"posse/locales"
)

func (a *app) pathAction(m locales.Messages, g *gitState, what, action string, e gitx.Entry, ours bool) {
	if !a.pathsControls() {
		return
	}
	if !a.typedFetch() {
		a.gitDo(m, g, what, func(ctx context.Context, root string) error {
			switch action {
			case rpc.GitPathsTake:
				return gitx.TakeSide(ctx, root, e.Path, ours)
			case rpc.GitPathsUnstage:
				return gitx.Unstage(ctx, root, e.Path)
			default:
				return gitx.Stage(ctx, root, e.Path)
			}
		})
		return
	}
	w := a.workspace(g.v.wsID)
	if w == nil || g.status == nil {
		return
	}
	target := a.gitTarget(w, g.v, g)
	p := rpc.GitPathsParams{GitFetchParams: target.params, Action: action, Head: g.status.Commit, Paths: []rpc.GitPath{{Entry: e}}, Ours: ours}
	a.preparePaths(m, target, what, p, nil, nil)
}

// 覆盖前提在点击后读取一次；确认另开或重连查询都不能替换它。
func (a *app) preparePaths(m locales.Messages, target gitOperationTarget, what string, p rpc.GitPathsParams, check func() bool, completed func(rpc.GitPathsResult)) {
	if !a.pathsControls() || !target.writable() || !a.pathsWritable(target.view, target.repository) {
		return
	}
	if p.Action != rpc.GitPathsRollback && p.Action != rpc.GitPathsTake {
		a.startTargetPaths(m, target, what, p, check, completed)
		return
	}
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		var err error
		groups := [][]rpc.GitPath{p.Restore, p.Remove}
		if p.Action == rpc.GitPathsTake {
			groups = [][]rpc.GitPath{p.Paths}
		}
		for _, group := range groups {
			for i := range group {
				item := &group[i]
				version, readErr := target.request.saveVersion(ctx, filepath.Join(p.Expected.Root, filepath.FromSlash(item.Entry.Path)))
				if readErr != nil {
					err = readErr
					break
				}
				item.Version = &version
				if item.Entry.Orig != "" && p.Action == rpc.GitPathsRollback {
					version, readErr := target.request.saveVersion(ctx, filepath.Join(p.Expected.Root, filepath.FromSlash(item.Entry.Orig)))
					if readErr != nil {
						err = readErr
						break
					}
					item.OrigVersion = &version
				}
			}
			if err != nil {
				break
			}
		}
		a.update(func() {
			if !target.writable() || !a.pathsControls() {
				return
			}
			if err != nil {
				a.err = errtext.Text(err)
				return
			}
			a.startTargetPaths(m, target, what, p, check, completed)
		})
	}()
}

func (a *app) targetRollback(m locales.Messages, r rollbackDlg, done func()) {
	if !a.pathsControls() || r.g.status == nil {
		return
	}
	w := a.workspace(r.v.wsID)
	if w == nil {
		return
	}
	target := a.gitTarget(w, r.v, r.g)
	p := rpc.GitPathsParams{GitFetchParams: target.params, Action: rpc.GitPathsRollback, Head: r.statusFor.Commit}
	for _, e := range r.entries {
		if !r.on[e.Path] {
			continue
		}
		item := rpc.GitPath{Entry: e}
		switch {
		case isNewEntry(e) && r.delNew:
			p.Remove = append(p.Remove, item)
		case e.X == 'A':
			p.Unstage = append(p.Unstage, item)
		case e.Untracked:
			continue
		default:
			p.Restore = append(p.Restore, item)
		}
	}
	a.preparePaths(m, target, m.GitRollback(), p, nil, func(out rpc.GitPathsResult) {
		if out.Status == rpc.GitFetchSucceeded && done != nil {
			done()
		}
	})
}

func (a *app) targetRevertHunk(m locales.Messages, v *filesView, d *fileDoc, i int) {
	df := d.diff
	if df == nil || !df.worktree() || d.kind != docDiff || i < 0 || i >= len(df.state.Hunks()) {
		return
	}
	g := v.repoAt(df.root)
	w := a.workspace(v.wsID)
	if g == nil || w == nil || g.status == nil || !a.pathsWritable(v, g) {
		return
	}
	index := slices.IndexFunc(g.status.Entries, func(e gitx.Entry) bool { return e.Path == df.rel })
	if index < 0 {
		return
	}
	target := a.gitTarget(w, v, g)
	stamp := df.stamp
	// 在独立缓冲区计算；未知结果和服务端拒绝都不把尚未保存的回滚留在可见 diff。
	state := editor.DiffState{Old: editor.NewBuffer(df.state.Old.String()), New: editor.NewBuffer(df.state.New.String()), IgnoreWhitespace: df.state.IgnoreWhitespace}
	state.Revert(i)
	data := []byte(state.New.String())
	sum := sha256.Sum256(data)
	p := rpc.GitPathsParams{GitFetchParams: target.params, Action: rpc.GitPathsWrite, Head: g.status.Commit, Paths: []rpc.GitPath{{Entry: g.status.Entries[index]}}, Data: data, Hash: hex.EncodeToString(sum[:])}
	seq := df.contentSeq
	current := func() bool { return d.diff == df && df.contentSeq == seq && df.stamp.Same(stamp) && d.kind == docDiff }
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		version, err := target.request.saveVersion(ctx, filepath.Join(p.Expected.Root, filepath.FromSlash(df.rel)))
		a.update(func() {
			if !target.writable() || !a.pathsControls() || !current() {
				return
			}
			if err != nil {
				a.err = errtext.Text(err)
				return
			}
			if !stamp.Same(version.Stamp) {
				a.err = m.GitFileChanged()
				a.loadDiff(v, d)
				return
			}
			p.Paths[0].Version = &version
			a.startTargetPaths(m, target, m.GitRollbackHunk(), p, current, func(out rpc.GitPathsResult) {
				if !current() {
					return
				}
				if out.Status != rpc.GitFetchSucceeded {
					a.loadDiff(v, d)
					return
				}
				df.state.Revert(i)
				clampHunk(df)
				df.stamp = out.Version.Stamp
			})
		})
	}()
}
