package app

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"time"

	"github.com/egoist/mygo/ui"
	"posse/internal/errtext"
	"posse/internal/gitx"
	"posse/internal/rpc"
	"posse/locales"
)

const targetFetchBytes = 4 << 20

// 操作归主机 app 所有，仓库重新发现或窗口切走不能丢掉已发送的 ref。
// acknowledged 只解除用户明确核对过的未知操作的阻塞，不把它记成成功。
type targetFetch struct {
	ref                                  rpc.DesktopOperationRef
	params                               rpc.GitFetchParams
	project, workspace                   string
	request                              appRequest
	at                                   time.Time
	result                               *rpc.GitFetchResult
	err                                  string
	unknown, done, acknowledged          bool
	checking, canceling, cancelRequested bool
	checkSeq                             uint64
	seen                                 bool
}

func (s *targetFetch) pending() bool { return !s.done && !s.acknowledged }

func (s *targetFetch) key() string {
	return fmt.Sprintf("fetch:%s:%s:%d:%d", s.ref.Desktop.ClientID, s.ref.Desktop.InstanceID, s.ref.Desktop.Epoch, s.ref.Sequence)
}

func (a *app) typedFetch() bool {
	r := a.request()
	return r.host != nil || a.remoteHost() || r.client != nil && r.client.Desktop != nil
}

func (a *app) fetchFor(g *gitState) *targetFetch {
	for i := len(a.targetFetches) - 1; i >= 0; i-- {
		s := a.targetFetches[i]
		if s.params.Expected.Root == g.identity.Root || s.params.Expected.Root == g.repo || g.identity.CommonDir != "" && s.params.Expected.CommonDir == g.identity.CommonDir {
			return s
		}
	}
	return nil
}

func (a *app) fetchWritable(v *filesView) bool {
	r := a.request()
	if r.host != nil && !a.remoteHost() && (v.repos.request.app == nil || !v.repos.request.current()) {
		return false
	}
	return r.current() && r.client != nil && r.client.Desktop != nil && (r.host == nil || r.snapshot.Controlled) && a.files[v.wsID] == v && v.repos.scanned && v.git.identity.Root != "" && v.repoAt(v.git.repo) == v.git && v.git.repo != ""
}

// 每个未决操作预留最大响应空间；只回收完成或明确处置的历史。
func (a *app) reserveFetch() bool {
	return a.reserveFetchWithin(gitConsoleMax, targetFetchBytes)
}

func (a *app) reserveFetchWithin(maxItems, maxBytes int) bool {
	for {
		bytes := 0
		for _, s := range a.targetFetches {
			b, _ := json.Marshal(s.params)
			bytes += len(b) + len(s.err) + len(s.project) + len(s.workspace) + len(s.ref.Desktop.ClientID) + len(s.ref.Desktop.InstanceID) + 1024
			// 用户确认新操作只解除阻塞，原执行和查询仍可能交回完整结果。
			if !s.done {
				bytes += rpc.GitFetchResultLimit
			} else {
				b, _ := json.Marshal(s.result)
				bytes += len(b)
			}
		}
		if len(a.targetFetches) < maxItems && bytes+rpc.GitFetchResultLimit+rpc.GitFetchParamsLimit+1024 <= maxBytes {
			return true
		}
		i := slices.IndexFunc(a.targetFetches, func(s *targetFetch) bool { return !s.pending() })
		if i < 0 {
			return false
		}
		a.targetFetches = slices.Delete(a.targetFetches, i, i+1)
	}
}

func (a *app) startFetch(m locales.Messages, w *workspace, v *filesView) {
	fixed := *w
	w = &fixed
	current := a.workspace(w.ID)
	if current == nil || current.Path != w.Path || v.wsID != w.ID {
		return
	}
	if !a.typedFetch() {
		a.gitDoKind(m, v.git, gitFetch, "Fetch", func(ctx context.Context, root string) error { return gitx.Fetch(ctx, root) }, func() { a.notify(m.BranchFetched(), "", nil) })
		return
	}
	if !a.fetchWritable(v) || !a.request().mayWrite() || v.git.busy != "" {
		return
	}
	target := a.gitTarget(w, v, v.git)
	if !target.writable() {
		return
	}
	if s := a.fetchFor(v.git); s != nil && s.pending() {
		if !s.unknown {
			return
		}
		a.confirm(m.FetchNewTitle(), m.FetchNewBody(), m.FetchNewAction(), func() {
			if !target.writable() || !s.pending() || !s.unknown || s.checking || s.canceling {
				return
			}
			s.acknowledged = true
			a.startFetch(m, w, v)
		})
		return
	}
	if !a.reserveFetch() {
		a.err = m.FetchCapacity()
		return
	}
	r, p := target.request, target.params
	op, err := r.client.PrepareGitFetch(p)
	if err != nil {
		a.err = errtext.Text(err)
		return
	}
	s := &targetFetch{ref: op.Ref(), params: p, project: w.ProjectID, workspace: w.Name, request: r, at: time.Now()}
	a.targetFetches = append(a.targetFetches, s)
	go func() {
		// 生命周期不绑定当前视图；网络请求结束也不调用操作取消。
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
		defer cancel()
		if !r.current() {
			a.update(func() { s.done = true })
			return
		}
		out, err := op.Execute(ctx)
		a.update(func() {
			if s.done {
				return
			}
			if err != nil {
				var re *rpc.Error
				s.unknown = !errors.As(err, &re) || re.Code == rpc.CodeOutcomeUnknown
				s.done, s.err = !s.unknown, errtext.Text(err)
				return
			}
			a.finishFetch(s, out, r, m)
		})
	}()
}

func (a *app) finishFetch(s *targetFetch, out rpc.GitFetchResult, r appRequest, m locales.Messages) {
	// 回收后的旧回调不再持有结果，也不能把记录重新放回 Console。
	if s.done || !slices.Contains(a.targetFetches, s) {
		return
	}
	s.result, s.done, s.unknown = &out, true, false
	s.err = ""
	if out.Error != nil {
		s.err = errtext.Text(out.Error)
	}
	if !r.current() || s.acknowledged || a.fetchFor(&gitState{repo: s.params.Expected.Root, identity: s.params.Expected}) != s {
		return
	}
	v := a.files[s.params.WorkspaceID]
	if v == nil || v.git.identity != s.params.Expected {
		return
	}
	a.refreshFetch(s, v)
	if out.Status == rpc.GitFetchSucceeded && s.request.current() {
		a.notify(m.BranchFetched(), "", nil)
	}
}

func (a *app) refreshFetch(s *targetFetch, v *filesView) {
	if !a.targetGitReady() || a.files[v.wsID] != v {
		return
	}
	a.refreshGit(v)
	a.refsChanged(s.project)
}

func (a *app) queryFetch(m locales.Messages, s *targetFetch) {
	r := a.request()
	if !r.current() || r.client == nil || r.client.Desktop == nil || s.checking || s.canceling || s.done || !slices.Contains(a.targetFetches, s) {
		return
	}
	s.checkSeq++
	seq := s.checkSeq
	s.checking = true
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		out, err := r.client.GitFetchStatus(ctx, s.ref, s.params)
		a.update(func() {
			if s.checkSeq != seq {
				return
			}
			s.checking = false
			if s.done {
				return
			}
			if err != nil {
				s.err = errtext.Text(err)
				return
			}
			if out.Result != nil {
				a.finishFetch(s, *out.Result, r, m)
				return
			}
			s.err = ""
			s.unknown = out.Operation.State == "unknown"
			if out.Operation.State == "done" && out.Operation.Error != nil {
				s.done, s.err = true, errtext.Text(out.Operation.Error)
			}
		})
	}()
}

func (a *app) cancelFetch(m locales.Messages, s *targetFetch) {
	r := a.request()
	if !r.mayWrite() || r.client == nil || r.client.Desktop == nil || !s.pending() || s.canceling || s.checking {
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
			// cancel 的确认不等于执行完成；done 也经 typed status 核对目标和日志。
			if out.State == "done" && r.current() {
				a.queryFetch(m, s)
			}
		})
	}()
}

func (a *app) fetchButton(c *ui.Context, w *workspace, v *filesView) {
	g, m := v.git, L(c)
	if !a.typedFetch() {
		if g.busyKind == gitFetch {
			spin := ui.Row(c.Key("fetching")).Size(buttonBox(), buttonBox()).Shrink(0).Justify(ui.Center).AlignItems(ui.Center).Role(ui.RoleButton).Label(m.BranchCancelFetch()).Tooltip(m.BranchFetchingTip())
			spin.Children(func() { ui.Spinner(c).Label(m.BranchFetching()) })
			if spin.Clicked() && g.cancel != nil {
				g.cancel()
			}
			return
		}
		if pressable(iconButtonSized(c, iconFetch, m.BranchFetchTip(), buttonBox(), iconSize)).Disabled(g.busy != "").Clicked() {
			a.startFetch(m, w, v)
		}
		return
	}
	s := a.fetchFor(g)
	blocked := !a.fetchWritable(v) || g.busy != "" || s != nil && s.pending() && (!s.unknown || s.checking || s.canceling)
	if pressable(iconButtonSized(c, iconFetch, m.FetchManual(), buttonBox(), iconSize)).Disabled(blocked).Clicked() {
		a.startFetch(m, w, v)
	}
}

func (a *app) fetchStatus(c *ui.Context, v *filesView) {
	if !a.typedFetch() {
		return
	}
	s := a.fetchFor(v.git)
	if s == nil {
		return
	}
	a.fetchState(c, s, v)
}

func (a *app) fetchState(c *ui.Context, s *targetFetch, v *filesView) {
	m, t := L(c), c.Theme()
	ui.Column(c.Key("fetch-status")).Padding(4, 12).Gap(4).Children(func() {
		text := m.BranchFetching()
		switch {
		case s.result != nil:
			text = m.FetchResult(s.result.Status)
		case s.done:
			text = m.CommonFailed()
		case s.unknown:
			text = m.FetchUnknown()
		case s.cancelRequested:
			text = m.FetchCancelRequested()
		}
		ui.Text(c, text).FontSize(fsSmall).TextColor(t.TextMuted)
		if s.result != nil && s.result.Started && s.result.Status != rpc.GitFetchSucceeded {
			ui.Text(c, m.FetchPartial()).FontSize(fsSmall).TextColor(t.TextMuted)
		}
		if s.err != "" {
			ui.Text(c, s.err).FontSize(fsSmall).TextColor(t.Danger)
		}
		if !s.done || s.result != nil && s.result.Started && s.result.Status != rpc.GitFetchSucceeded {
			ui.Row(c).Gap(4).Children(func() {
				if !s.done && ui.Button(c, m.FetchQuery()).Disabled(!a.targetGitReady() || s.checking || s.canceling).Clicked() {
					a.queryFetch(m, s)
				}
				if ui.Button(c, m.FetchRefresh()).Disabled(!a.targetGitReady() || v == nil).Clicked() {
					a.refreshFetch(s, v)
				}
				r := a.request()
				if !s.done && ui.Button(c, m.BranchCancelFetch()).Disabled(!a.targetGitReady() || r.host != nil && !r.snapshot.Controlled || s.checking || s.canceling || !s.pending()).Clicked() {
					a.cancelFetch(m, s)
				}
			})
		}
	})
}

func (a *app) fetchConsole(project string) []gitOpRecord {
	var ops []gitOpRecord
	cleared := a.state.GitConsoleClearedAt[project]
	for _, s := range a.targetFetches {
		if s.project != project || !s.pending() && s.at.Before(cleared) {
			continue
		}
		var runs []gitx.Run
		if s.result != nil {
			runs = s.result.Logs
		}
		ops = append(ops, gitOpRecord{name: "Fetch", runs: runs, repeat: 1, since: s.at, target: s})
	}
	return ops
}

func (a *app) fetchConsoleRow(c *ui.Context, s *targetFetch, day string) {
	t, m := c.Theme(), L(c)
	ui.Column(c.Key(s.key())).Shrink(0).Children(func() {
		if day != "" {
			ui.Text(c, day).FontSize(fsSmall).FontWeight(fwMedium).TextColor(t.TextMuted).Padding(12, 16, 4, 16)
		}
		ui.Column(c).Margin(2, 6).Padding(6, 8).Gap(4).Children(func() {
			ui.Row(c).Gap(8).Children(func() {
				ui.Text(c, "Fetch").FontSize(fsBody).FontWeight(fwMedium)
				ui.Text(c, s.workspace).FontSize(fsSmall).TextColor(t.TextMuted)
				ui.Spacer(c)
				ui.Text(c, m.AppTimeSeconds(s.at.Local())).FontSize(fsSmall).Font(monoFont).TextColor(t.TextMuted)
			})
			ui.Text(c, s.params.Expected.Root).FontSize(fsSmall).Font(monoFont).TextColor(t.TextMuted)
			a.fetchState(c, s, a.files[s.params.WorkspaceID])
			if s.result != nil {
				if s.result.LogsTruncated {
					ui.Text(c, m.FetchLogsTruncated()).FontSize(fsSmall).TextColor(t.TextMuted)
				}
				for _, run := range s.result.Logs {
					gitRunRow(c, run)
				}
			}
		})
	})
}
