package app

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"slices"
	"strings"
	"time"

	"github.com/egoist/mygo/ui"
	"posse/internal/errtext"
	"posse/internal/gitx"
	"posse/internal/rpc"
	"posse/locales"
)

type workspaceMergeResult struct {
	Merged string   `json:"merged"`
	Into   string   `json:"into"`
	Repos  []string `json:"repos"`
}

type targetWorkspaceMerge struct {
	request                                                                  appRequest
	workspace                                                                workspace
	identities                                                               []rpc.GitRepositoryIdentity
	prediction                                                               *rpc.GitDivergenceResult
	ref                                                                      rpc.DesktopOperationRef
	repos                                                                    []*gitState
	at                                                                       time.Time
	result                                                                   *workspaceMergeResult
	err                                                                      string
	multi, done, unknown, acknowledged, checking, canceling, cancelRequested bool
}

func (s *targetWorkspaceMerge) pending() bool { return !s.done && !s.acknowledged }
func (s *targetWorkspaceMerge) key() string {
	return fmt.Sprintf("workspace-merge:%s:%s:%d:%d", s.ref.Desktop.ClientID, s.ref.Desktop.InstanceID, s.ref.Desktop.Epoch, s.ref.Sequence)
}
func (s *targetWorkspaceMerge) matches(g *gitState) bool {
	return g.v != nil && g.v.wsID == s.workspace.ID || slices.ContainsFunc(s.identities, func(id rpc.GitRepositoryIdentity) bool {
		return id == g.identity || id.CommonDir != "" && id.CommonDir == g.identity.CommonDir
	})
}
func (a *app) workspaceMergeFor(g *gitState) *targetWorkspaceMerge {
	for i := len(a.targetWorkspaceMerges) - 1; i >= 0; i-- {
		if s := a.targetWorkspaceMerges[i]; s.matches(g) {
			return s
		}
	}
	return nil
}
func (a *app) holdWorkspaceMerge(s *targetWorkspaceMerge, g *gitState) {
	if s.pending() && g.busy == "" && !slices.Contains(s.repos, g) {
		g.busy, g.busyKind, g.busyWorkspaceMerge = appL().GitMerge(), gitWorkspaceMerge, s
		g.cancel = func() { a.cancelWorkspaceMerge(s) }
		s.repos = append(s.repos, g)
	}
}
func (a *app) releaseWorkspaceMerge(s *targetWorkspaceMerge) {
	// 先清除全部占用，再按各仓库原有顺序续跑，确认的新操作最后排入。
	repos := s.repos
	s.repos = nil
	for _, g := range repos {
		if g.busyWorkspaceMerge == s {
			g.busy, g.busyKind, g.cancel, g.busyWorkspaceMerge = "", "", nil, nil
		}
	}
	for _, g := range repos {
		a.gitNext(g)
	}
}
func (a *app) mergeCurrent(s *targetWorkspaceMerge) bool {
	if !a.pathsControls() || !s.request.current() {
		return false
	}
	w := a.workspace(s.workspace.ID)
	if w == nil || w.Path != s.workspace.Path || w.ProjectID != s.workspace.ProjectID || w.Kind != s.workspace.Kind || w.Branch != s.workspace.Branch || w.BaseRef != s.workspace.BaseRef || !reflect.DeepEqual(w.Repos, s.workspace.Repos) {
		return false
	}
	v := a.files[w.ID]
	if v == nil || !v.repos.scanned {
		return false
	}
	for _, id := range s.identities {
		if !slices.ContainsFunc(v.repos.list, func(g *gitState) bool { return g.identity == id }) {
			return false
		}
	}
	if p := s.prediction; p != nil && s.ref.Sequence == 0 {
		var g *gitState
		for _, candidate := range v.repos.list {
			if candidate.identity == p.Identity {
				g = candidate
				break
			}
		}
		if g == nil || g.status == nil || g.status.Commit != p.Head {
			return false
		}
		tr := a.cachedBase(w, g)
		if tr == nil || !tr.baseOK || tr.base.Base != p.Base || tr.base.Head != p.Head {
			return false
		}
	}
	return true
}
func (a *app) mergeBusy(s *targetWorkspaceMerge) *gitState {
	for _, v := range a.files {
		for _, g := range v.repos.list {
			if s.matches(g) && g.busy != "" {
				return g
			}
		}
	}
	return nil
}

func (a *app) askTargetMerge(m locales.Messages, wsID string, multi bool) {
	if !a.pathsControls() {
		return
	}
	w := a.workspace(wsID)
	if w == nil || !multi && !hasBase(w) || multi && len(w.Repos) == 0 {
		return
	}
	r := a.request()
	ws := workspace{ID: w.ID, Path: w.Path, Name: w.Name, ProjectID: w.ProjectID, Kind: w.Kind, Branch: w.Branch, BaseRef: w.BaseRef, Repos: slices.Clone(w.Repos)}
	v := a.filesFor(w)
	a.withRepos(v, func() {
		if !r.current() || !a.pathsControls() {
			return
		}
		s := &targetWorkspaceMerge{request: r, workspace: ws, multi: multi}
		for _, g := range v.repos.list {
			if g.identity.Root != "" {
				s.identities = append(s.identities, g.identity)
			}
		}
		if len(s.identities) == 0 || !a.mergeCurrent(s) {
			return
		}
		for _, g := range v.repos.list {
			if previous := a.workspaceMergeFor(g); previous != nil && previous.pending() {
				if !previous.unknown || previous.checking || previous.canceling {
					return
				}
				a.confirm(m.GitMerge(), m.FetchNewBody(), m.FetchNewAction(), func() {
					a.update(func() {
						if !a.mergeCurrent(s) || !previous.pending() || !previous.unknown || previous.checking || previous.canceling {
							return
						}
						previous.acknowledged = true
						a.releaseWorkspaceMerge(previous)
						a.mergeAfterQueue(m, s)
					})
				})
				return
			}
		}
		if a.mergeBusy(s) != nil {
			return
		}
		a.predictWorkspaceMerge(m, s)
	})
}
func (a *app) mergeAfterQueue(m locales.Messages, s *targetWorkspaceMerge) {
	if !a.mergeCurrent(s) {
		return
	}
	if g := a.mergeBusy(s); g != nil {
		g.queue = append(g.queue, gitOp{m: m, what: m.GitMerge(), ready: func() { a.mergeAfterQueue(m, s) }})
		return
	}
	a.predictWorkspaceMerge(m, s)
}

func (a *app) predictWorkspaceMerge(m locales.Messages, s *targetWorkspaceMerge) {
	if !a.mergeCurrent(s) || a.mergeBusy(s) != nil {
		return
	}
	confirm := func() {
		title, body := m.BranchMergeWorkspaceConfirm(s.workspace.Branch, s.workspace.BaseRef), m.BranchMergeWorkspaceBody(s.workspace.BaseRef)
		if s.multi {
			title, body = m.BranchMergeReposConfirm(s.workspace.Branch, len(s.workspace.Repos)), m.BranchMergeReposBody()
		}
		a.confirm(title, body, m.GitMerge(), func() {
			a.update(func() {
				if a.mergeCurrent(s) && a.mergeBusy(s) == nil {
					a.executeWorkspaceMerge(m, s)
				}
			})
		})
	}
	if s.multi {
		confirm()
		return
	}
	v := a.files[s.workspace.ID]
	var g *gitState
	for _, candidate := range v.repos.list {
		if candidate.identity.Root == v.realRoot || candidate.repo == v.root {
			g = candidate
			break
		}
	}
	if g == nil {
		return
	}
	id, root := g.identity, g.repo
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		out, err := s.request.client.GitDivergence(ctx, rpc.GitDivergenceParams{Path: root, Expected: id, BaseRef: s.workspace.BaseRef})
		a.update(func() {
			if !a.mergeCurrent(s) || a.mergeBusy(s) != nil {
				return
			}
			if err != nil {
				a.err = errtext.Text(err)
				return
			}
			if out.Unchanged {
				a.err = errtext.Text(gitx.ErrDivergenceOutput)
				return
			}
			g.baseTrack = &targetBaseState{ref: s.workspace.BaseRef, request: s.request, result: out.DivergenceRead, baseline: out.Baseline, track: trackInfo{baseOK: true, base: gitx.Divergence{Head: out.Head, Base: out.Base, Ahead: out.Ahead, Behind: out.Behind, Conflicts: out.Conflicts}, basePrediction: out.Prediction, baseReason: out.Reason}}
			s.prediction = &out
			if !a.remoteHost() {
				if a.track == nil {
					a.track = map[string]*trackInfo{}
				}
				a.track[trackKey(&s.workspace, g.repo)] = &g.baseTrack.track
			}
			if len(out.Conflicts) > 0 {
				a.confirm(m.BranchMergeConflicts(), m.BranchSyncFirstBody(s.workspace.BaseRef, s.workspace.Branch, fileList(m, out.Conflicts, 3)), m.BranchMergeFrom(s.workspace.BaseRef), func() {
					a.update(func() {
						if a.mergeCurrent(s) {
							a.syncBase(m, a.workspace(s.workspace.ID), false)
						}
					})
				})
				return
			}
			if out.Prediction == gitx.DivergenceUnavailable {
				a.err = m.BranchPredictionUnavailable(out.Reason)
				return
			}
			confirm()
		})
	}()
}

func (a *app) executeWorkspaceMerge(m locales.Messages, s *targetWorkspaceMerge) {
	if s.ref.Sequence != 0 {
		return
	}
	if !a.mergeCurrent(s) || a.mergeBusy(s) != nil {
		return
	}
	if len(a.targetWorkspaceMerges) >= gitConsoleMax {
		i := slices.IndexFunc(a.targetWorkspaceMerges, func(s *targetWorkspaceMerge) bool { return !s.pending() })
		if i < 0 {
			a.err = m.FetchCapacity()
			return
		}
		a.targetWorkspaceMerges = slices.Delete(a.targetWorkspaceMerges, i, i+1)
	}
	op, err := s.request.client.PrepareDesktopOperation("workspace.merge", map[string]any{"workspace": s.workspace.ID})
	if err != nil {
		a.err = errtext.Text(err)
		return
	}
	s.ref, s.at = op.Ref(), time.Now()
	a.targetWorkspaceMerges = append(a.targetWorkspaceMerges, s)
	for _, v := range a.files {
		for _, g := range v.repos.list {
			if s.matches(g) {
				a.holdWorkspaceMerge(s, g)
			}
		}
	}
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
		defer cancel()
		if !s.request.current() {
			a.update(func() { s.done = true; a.releaseWorkspaceMerge(s) })
			return
		}
		var out workspaceMergeResult
		err := op.Execute(ctx, &out)
		a.update(func() {
			if err != nil {
				var re *rpc.Error
				if errors.As(err, &re) && re.Code == rpc.CodeOutcomeUnknown {
					var ref rpc.DesktopOperationRef
					if json.Unmarshal(re.Data, &ref) == nil && rpc.ValidDesktopOperationRef(ref) {
						s.ref = ref
					}
					s.unknown, s.err = true, errtext.Text(err)
					return
				}
			}
			a.finishWorkspaceMerge(m, s, out, err)
		})
	}()
}
func (a *app) finishWorkspaceMerge(m locales.Messages, s *targetWorkspaceMerge, out workspaceMergeResult, err error) {
	if err == nil {
		valid := out.Merged == s.workspace.Branch
		if s.multi {
			valid = valid && len(out.Repos) > 0 && len(out.Repos) <= len(s.workspace.Repos)
			seen := map[string]bool{}
			for _, path := range out.Repos {
				if seen[path] || !slices.ContainsFunc(s.workspace.Repos, func(r wsRepo) bool { return r.Path == path }) {
					valid = false
				}
				seen[path] = true
			}
		} else {
			valid = valid && out.Into == s.workspace.BaseRef
		}
		if !valid {
			s.unknown = true
			s.err = errtext.Text(errors.New("invalid workspace merge result"))
			return
		}
	}
	if s.done {
		return
	}
	s.done, s.unknown = true, false
	defer a.releaseWorkspaceMerge(s)
	if err != nil {
		s.err = errtext.Text(errors.New(gitx.SanitizeFetchText(err.Error())))
	} else {
		s.result = &out
		s.err = ""
	}
	if s.acknowledged || !a.mergeCurrent(s) {
		return
	}
	if err != nil {
		a.err = s.err
		return
	}
	a.refsChanged(s.workspace.ProjectID)
	s.request.refresh()
	title := m.BranchMergedTo(s.workspace.BaseRef)
	if s.multi {
		title = m.BranchMergedRepos(strings.Join(out.Repos, m.BranchFileListSeparator()))
	}
	a.confirm(title, mergedBody(m, s.workspace.Branch, nil), m.BranchDeleteWorkspace(), func() {
		a.update(func() {
			// 删除确认只属于原连接；不能由重连后的新控制身份承接。
			if !s.request.current() || !a.pathsControls() {
				return
			}
			go func() {
				s.request.call("workspace.rm", map[string]any{"workspace": s.workspace.ID, "force": true, "deleteBranch": true}, nil, 30*time.Second)
				s.request.refresh()
			}()
		})
	})
}
func (a *app) queryWorkspaceMerge(m locales.Messages, s *targetWorkspaceMerge) {
	r := a.request()
	if !r.current() || r.client == nil || r.client.Desktop == nil || s.done || s.checking || s.canceling {
		return
	}
	s.checking = true
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		out, err := r.client.DesktopOperationStatus(ctx, s.ref)
		a.update(func() {
			s.checking = false
			if s.done {
				return
			}
			if err != nil {
				s.err = errtext.Text(err)
				return
			}
			s.unknown = out.State == "unknown"
			if out.State != "done" {
				return
			}
			if out.Method != "workspace.merge" {
				s.unknown = true
				return
			}
			var result workspaceMergeResult
			if out.Error != nil {
				a.finishWorkspaceMerge(m, s, result, out.Error)
				return
			}
			if json.Unmarshal(out.Result, &result) != nil {
				s.unknown = true
				return
			}
			a.finishWorkspaceMerge(m, s, result, nil)
		})
	}()
}
func (a *app) cancelWorkspaceMerge(s *targetWorkspaceMerge) {
	r := a.request()
	if !a.pathsControls() || !s.pending() || s.checking || s.canceling {
		return
	}
	s.canceling = true
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		out, err := r.client.CancelDesktopOperation(ctx, s.ref)
		a.update(func() {
			s.canceling = false
			if s.done {
				return
			}
			if err != nil {
				s.err = errtext.Text(err)
				return
			}
			s.unknown, s.cancelRequested = out.State == "unknown", out.State != "unknown"
			if out.State == "done" {
				a.queryWorkspaceMerge(appL(), s)
			}
		})
	}()
}
func (a *app) workspaceMergeConsole(project string) []gitOpRecord {
	var ops []gitOpRecord
	for _, s := range a.targetWorkspaceMerges {
		if s.workspace.ProjectID == project && (s.pending() || !s.at.Before(a.state.GitConsoleClearedAt[project])) {
			ops = append(ops, gitOpRecord{name: appL().GitMerge(), repeat: 1, since: s.at, workspaceMerge: s})
		}
	}
	return ops
}
func (a *app) workspaceMergeConsoleRow(c *ui.Context, s *targetWorkspaceMerge, day string) {
	m, t := L(c), c.Theme()
	ui.Column(c.Key(s.key())).Margin(2, 6).Padding(6, 8).Gap(4).Children(func() {
		if day != "" {
			ui.Text(c, day).FontSize(fsSmall).TextColor(t.TextMuted)
		}
		ui.Text(c, m.GitMerge()).FontSize(fsBody).FontWeight(fwMedium)
		ui.Text(c, s.workspace.Name).FontSize(fsSmall).TextColor(t.TextMuted)
		text := m.GitMerge()
		switch {
		case s.result != nil:
			text = m.CommonDone()
		case s.done:
			text = m.CommonFailed()
		case s.unknown:
			text = errtext.Text(errors.New("desktop operation result is no longer available; inspect the current state before trying again"))
		case s.cancelRequested:
			text = m.FetchCancelRequested()
		}
		ui.Text(c, text).FontSize(fsSmall).TextColor(t.TextMuted)
		if s.err != "" {
			ui.Text(c, s.err).FontSize(fsSmall).TextColor(t.Danger)
		}
		if !s.done {
			ui.Row(c).Gap(4).Children(func() {
				if ui.Button(c, m.FetchQuery()).Disabled(!a.targetGitReady() || s.checking || s.canceling).Clicked() {
					a.queryWorkspaceMerge(m, s)
				}
				if ui.Button(c, m.CommonCancel()).Disabled(!a.pathsControls() || !s.pending() || s.checking || s.canceling).Clicked() {
					a.cancelWorkspaceMerge(s)
				}
			})
		}
	})
}

const gitWorkspaceMerge gitKind = "workspace-merge"
