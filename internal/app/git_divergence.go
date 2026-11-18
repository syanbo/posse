package app

import (
	"path/filepath"

	"github.com/egoist/mygo/ui"
	"posse/internal/errtext"
	"posse/internal/gitx"
	"posse/internal/rpc"
)

type targetBaseState struct {
	track         trackInfo
	read          gitRead
	request       appRequest
	ref           string
	result        gitx.DivergenceRead
	baseline      string
	drawn, resume bool
}

func (g *gitState) cancelTargetBase() {
	if s := g.baseTrack; s != nil {
		s.read.stop()
		s.track.poll.busy = false
		s.baseline = ""
		s.resume = true
	}
}

func baseRepository(w *workspace, v *filesView) bool {
	if !hasBase(w) || v.git.repo == "" {
		return false
	}
	if v.remote {
		return v.realRoot != "" && filepath.Clean(v.git.identity.Root) == filepath.Clean(v.realRoot)
	}
	return filepath.Clean(v.git.repo) == filepath.Clean(v.root)
}

func (a *app) pollTargetTrack(c *ui.Context, w *workspace) *trackInfo {
	v := a.filesFor(w)
	a.pollRepos(c, v)
	g := v.git
	tr := &trackInfo{}
	if baseRepository(w, v) {
		s := g.baseTrack
		if s == nil || s.ref != w.BaseRef {
			g.cancelTargetBase()
			s = &targetBaseState{ref: w.BaseRef}
			g.baseTrack = s
		}
		tr = &s.track
		s.drawn = true
		if !a.targetGitReady() {
			g.cancelTargetBase()
		} else if v.repos.scanned {
			if s.request.app != nil && !s.request.current() {
				g.cancelTargetBase()
			}
			if !tr.poll.busy && (s.resume || tr.baseErr == "") && (s.resume || tr.poll.due(c, trackRefresh)) {
				a.loadTargetBase(w, v, g)
			}
		}
	}
	if st := g.status; st != nil {
		tr.ahead, tr.behind = st.Ahead, st.Behind
	}
	a.autoFetch(c, w, g.repo, tr)
	return tr
}

func (a *app) targetBaseCurrent(r appRequest, v *filesView, g *gitState, s *targetBaseState) bool {
	w := a.workspace(v.wsID)
	return a.targetGitCurrent(r, v) && v.git == g && v.repoAt(g.repo) == g && g.baseTrack == s && w != nil && w.BaseRef == s.ref && baseRepository(w, v)
}

func (a *app) loadTargetBase(w *workspace, v *filesView, g *gitState) {
	if !a.targetGitSession(v) || !v.repos.scanned || !baseRepository(w, v) || v.git != g || g.baseTrack == nil {
		return
	}
	s := g.baseTrack
	r, id, base, previous, old := a.request(), g.identity, s.ref, s.baseline, s.result
	ctx, seq := s.read.start(r)
	cancel := s.read.cancel
	s.request, s.resume, s.track.poll.busy = r, false, true
	go func() {
		defer cancel()
		out, err := r.client.GitDivergence(ctx, rpc.GitDivergenceParams{Path: g.repo, Expected: id, BaseRef: base, PreviousBaseline: previous})
		a.update(func() {
			if s.read.seq != seq {
				return
			}
			s.track.poll.done()
			if !a.targetBaseCurrent(r, v, g, s) || g.identity != id {
				return
			}
			if err == nil && out.Unchanged && (!s.track.baseOK || previous == "" || out.Baseline != previous || out.Head != old.Head || out.Base != old.Base) {
				err = gitx.ErrDivergenceOutput
			}
			if err != nil {
				s.track.baseErr = errtext.Text(err)
				a.err = s.track.baseErr
				return
			}
			if !a.targetGitIdentity(g, out.Identity) {
				return
			}
			if !out.Unchanged {
				s.result = out.DivergenceRead
				s.track.base = gitx.Divergence{Head: out.Head, Base: out.Base, Ahead: out.Ahead, Behind: out.Behind, Conflicts: out.Conflicts}
				s.track.basePrediction, s.track.baseReason = out.Prediction, out.Reason
			}
			s.baseline, s.track.baseOK, s.track.baseErr = out.Baseline, true, ""
		})
	}()
}
