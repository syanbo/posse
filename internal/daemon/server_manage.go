package daemon

import (
	"bytes"
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os/exec"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"sync"
	"time"

	"posse/internal/procmem"
	"posse/internal/rpc"
	"posse/internal/spawn"
	"posse/internal/writecoord"
)

const (
	daemonMemoryMaxTerminals  = 128
	daemonMemoryMaxExpanded   = 128
	daemonMemoryMaxProcs      = 64
	daemonMemorySummaryBudget = 2048
	daemonMemoryExpandBudget  = 1024
	daemonMemoryMaxNameBytes  = 64
	daemonMemoryMaxWireBytes  = 64 << 10
	daemonMemoryMaxConcurrent = 2
	daemonMemoryMaxParamBytes = 16 << 10
	daemonMemoryTimeout       = 2 * time.Second
	daemonMemoryCacheTTL      = 2 * time.Second
	autostartQueryTimeout     = 10 * time.Second
	autostartSetTimeout       = 30 * time.Second
)

type daemonMemoryCall struct {
	done chan struct{}
	res  rpc.DaemonMemoryResult
	err  error
}

type daemonMemoryCacheEntry struct {
	at  time.Time
	res rpc.DaemonMemoryResult
}

type daemonMemoryState struct {
	mu       sync.Mutex
	inFlight map[string]*daemonMemoryCall
	cache    map[string]daemonMemoryCacheEntry
	sem      chan struct{}
}

func (s *daemonMemoryState) acquire(ctx context.Context) error {
	s.mu.Lock()
	if s.sem == nil {
		s.sem = make(chan struct{}, daemonMemoryMaxConcurrent)
	}
	sem := s.sem
	s.mu.Unlock()
	select {
	case sem <- struct{}{}:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (s *daemonMemoryState) release() {
	s.mu.Lock()
	sem := s.sem
	s.mu.Unlock()
	if sem != nil {
		<-sem
	}
}

func cloneDaemonMemoryResult(in rpc.DaemonMemoryResult) rpc.DaemonMemoryResult {
	out := rpc.DaemonMemoryResult{
		TotalKB:    in.TotalKB,
		Truncated:  in.Truncated,
		Workspaces: make([]rpc.DaemonMemoryWorkspace, len(in.Workspaces)),
	}
	for i, w := range in.Workspaces {
		wc := rpc.DaemonMemoryWorkspace{
			ID:        w.ID,
			Label:     w.Label,
			KB:        w.KB,
			Terminals: make([]rpc.DaemonMemoryTerminal, len(w.Terminals)),
		}
		for j, t := range w.Terminals {
			tc := t
			if len(t.Procs) > 0 {
				tc.Procs = slices.Clone(t.Procs)
			}
			wc.Terminals[j] = tc
		}
		out.Workspaces[i] = wc
	}
	return out
}

func normalizeExpandedIDs(raw []string, running map[string]bool) ([]string, map[string]bool, string) {
	if len(raw) == 0 {
		return nil, nil, ""
	}
	seen := make(map[string]bool, min(len(raw), daemonMemoryMaxExpanded))
	ids := make([]string, 0, min(len(raw), daemonMemoryMaxExpanded))
	for _, id := range raw {
		id = strings.TrimSpace(id)
		if id == "" || seen[id] {
			continue
		}
		// 先按真实运行中终端过滤，避免任意垃圾字符串制造无限个 singleflight/缓存键。
		if running != nil && !running[id] {
			continue
		}
		seen[id] = true
		ids = append(ids, id)
		if len(ids) >= daemonMemoryMaxExpanded {
			break
		}
	}
	slices.Sort(ids)
	return ids, seen, strings.Join(ids, "\x00")
}

// daemonMemory 在锁外按有界进程预算统计运行中终端的内存树明细，供主机详情与底栏远端内存浮窗按需调用。
func (d *Daemon) daemonMemory(ctx context.Context, raw json.RawMessage) (any, error) {
	if len(raw) > daemonMemoryMaxParamBytes {
		return nil, rpc.Errorf(rpc.CodeBadRequest, "daemon.memory parameters exceed size limit")
	}
	var p rpc.DaemonMemoryParams
	if len(raw) > 0 {
		if err := rpc.Decode(raw, &p); err != nil {
			return nil, rpc.Errorf(rpc.CodeBadRequest, "invalid daemon.memory parameters")
		}
	}

	// 仅在 d.mu 内拍轻量终端快照，并据此过滤 expanded，防止无效 ID 制造无限单飞键。
	d.mu.Lock()
	runningSet := make(map[string]bool, len(d.terms))
	allSnaps := make([]memTermSnap, 0, len(d.terms))
	for _, s := range d.terms {
		if !s.Running() {
			continue
		}
		pid := s.Pid()
		if pid <= 0 {
			continue
		}
		runningSet[s.ID] = true
		view := s.snapshotView()
		title := view.name
		if title == "" {
			title = view.confirmedTitle
		}
		if title == "" {
			title = view.ptyTitle
		}
		allSnaps = append(allSnaps, memTermSnap{
			id:          s.ID,
			workspaceID: s.WorkspaceID,
			title:       procmem.TruncateName(title, daemonMemoryMaxNameBytes),
			agent:       view.agent,
			pid:         pid,
		})
	}
	d.mu.Unlock()

	_, expanded, key := normalizeExpandedIDs(p.Expanded, runningSet)

	d.mem.mu.Lock()
	if entry, ok := d.mem.cache[key]; ok && time.Since(entry.at) < daemonMemoryCacheTTL {
		res := cloneDaemonMemoryResult(entry.res)
		d.mem.mu.Unlock()
		return res, nil
	}
	if call, ok := d.mem.inFlight[key]; ok {
		d.mem.mu.Unlock()
		select {
		case <-call.done:
			if call.err != nil {
				return nil, call.err
			}
			return cloneDaemonMemoryResult(call.res), nil
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
	call := &daemonMemoryCall{done: make(chan struct{})}
	if d.mem.inFlight == nil {
		d.mem.inFlight = make(map[string]*daemonMemoryCall)
	}
	d.mem.inFlight[key] = call
	d.mem.mu.Unlock()

	var res rpc.DaemonMemoryResult
	qctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), daemonMemoryTimeout)
	err := d.mem.acquire(qctx)
	if err == nil {
		res, err = d.collectDaemonMemory(qctx, allSnaps, expanded)
		d.mem.release()
	}
	cancel()

	d.mem.mu.Lock()
	call.res, call.err = res, err
	close(call.done)
	delete(d.mem.inFlight, key)
	if err == nil {
		if d.mem.cache == nil {
			d.mem.cache = make(map[string]daemonMemoryCacheEntry)
		}
		now := time.Now()
		for k, v := range d.mem.cache {
			if now.Sub(v.at) >= daemonMemoryCacheTTL*4 {
				delete(d.mem.cache, k)
			}
		}
		if len(d.mem.cache) >= 16 {
			clear(d.mem.cache)
		}
		d.mem.cache[key] = daemonMemoryCacheEntry{at: now, res: res}
	}
	d.mem.mu.Unlock()

	if err != nil {
		return nil, err
	}
	if ctx.Err() != nil {
		return nil, ctx.Err()
	}
	return cloneDaemonMemoryResult(res), nil
}

type memTermSnap struct {
	id          string
	workspaceID string
	title       string
	agent       string
	pid         int
}

func (d *Daemon) collectDaemonMemory(ctx context.Context, allSnaps []memTermSnap, expanded map[string]bool) (rpc.DaemonMemoryResult, error) {
	// 确定性排序：优先保留请求展开的终端，再按 ID 排序，最后截断至上限。
	slices.SortFunc(allSnaps, func(a, b memTermSnap) int {
		ea, eb := expanded[a.id], expanded[b.id]
		if ea != eb {
			if ea {
				return -1
			}
			return 1
		}
		return cmp.Compare(a.id, b.id)
	})
	truncated := false
	snaps := allSnaps
	if len(snaps) > daemonMemoryMaxTerminals {
		snaps = snaps[:daemonMemoryMaxTerminals]
		truncated = true
	}

	var wsIdx *workspaceIndex
	if d.store != nil && ctx.Err() == nil {
		wsIdx, _ = d.loadWorkspaceIndex(true)
	}
	labelOf := func(wsID string) string {
		if wsIdx != nil {
			if w, ok := wsIdx.workspaces[wsID]; ok {
				return procmem.TruncateName(wsIdx.label(w), daemonMemoryMaxNameBytes)
			}
		}
		return procmem.TruncateName(wsID, daemonMemoryMaxNameBytes)
	}

	// 第一遍：独立汇总预算统计所有采样终端的 TotalKB，展开与否不影响汇总数字。
	summaryBudget := daemonMemorySummaryBudget
	termKBs := make([]int64, len(snaps))
	sampledCount := 0
	var totalKB int64
	for i, s := range snaps {
		if ctx.Err() != nil || summaryBudget <= 0 {
			truncated = true
			break
		}
		bytes, hit := procmem.TreeFootprintBounded(ctx, s.pid, &summaryBudget)
		if hit {
			truncated = true
		}
		termKBs[i] = int64(bytes >> 10)
		totalKB += termKBs[i]
		sampledCount = i + 1
	}

	// 第二遍：用独立展开预算采集请求展开项的进程明细，绝不抢占其他终端的汇总预算。
	expandBudget := daemonMemoryExpandBudget
	byWS := make(map[string]*rpc.DaemonMemoryWorkspace)
	for i := 0; i < sampledCount; i++ {
		s := snaps[i]
		kb := termKBs[i]
		var procs []rpc.DaemonMemoryProc
		if expanded[s.id] {
			if ctx.Err() != nil || expandBudget <= 0 {
				truncated = true
			} else {
				var pHit bool
				procs, pHit = daemonMemBreakdown(ctx, s.pid, &expandBudget)
				if pHit {
					truncated = true
				}
			}
		}
		ws := byWS[s.workspaceID]
		if ws == nil {
			ws = &rpc.DaemonMemoryWorkspace{ID: s.workspaceID, Label: labelOf(s.workspaceID)}
			byWS[s.workspaceID] = ws
		}
		ws.KB += kb
		ws.Terminals = append(ws.Terminals, rpc.DaemonMemoryTerminal{
			ID:    s.id,
			Title: s.title,
			Agent: s.agent,
			KB:    kb,
			Procs: procs,
		})
	}

	out := rpc.DaemonMemoryResult{
		TotalKB:    totalKB,
		Workspaces: make([]rpc.DaemonMemoryWorkspace, 0, len(byWS)),
		Truncated:  truncated,
	}
	for _, ws := range byWS {
		slices.SortFunc(ws.Terminals, func(a, b rpc.DaemonMemoryTerminal) int {
			if c := cmp.Compare(b.KB, a.KB); c != 0 {
				return c
			}
			return cmp.Compare(a.ID, b.ID)
		})
		out.Workspaces = append(out.Workspaces, *ws)
	}
	slices.SortFunc(out.Workspaces, func(a, b rpc.DaemonMemoryWorkspace) int {
		if c := cmp.Compare(b.KB, a.KB); c != 0 {
			return c
		}
		return cmp.Compare(a.ID, b.ID)
	})

	enforceDaemonMemorySizeBudget(&out, expanded)
	return out, nil
}

// daemonMemBreakdown 只返回内核可执行文件短名（SafeName）、KB 和计数，绝不读取 argv 防止参数或凭据泄露。
func daemonMemBreakdown(ctx context.Context, shell int, budget *int) ([]rpc.DaemonMemoryProc, bool) {
	if ctx.Err() != nil || *budget <= 0 {
		return nil, true
	}
	truncated := false
	kb := func(pid int) int64 {
		if *budget <= 0 || ctx.Err() != nil {
			truncated = true
			return 0
		}
		*budget--
		return int64(procmem.Footprint(pid) >> 10)
	}
	shortName := func(pid int) string {
		if n := procmem.SafeName(pid); n != "" {
			return procmem.TruncateName(n, daemonMemoryMaxNameBytes)
		}
		return fmt.Sprintf("pid %d", pid)
	}
	procs := []rpc.DaemonMemoryProc{{Name: shortName(shell), KB: kb(shell), Count: 1}}
	kids := procmem.Children(shell)
	if len(kids) == 1 && *budget > 0 && ctx.Err() == nil {
		main := kids[0]
		procs = append(procs, rpc.DaemonMemoryProc{Name: shortName(main), KB: kb(main), Count: 1})
		kids = procmem.Children(main)
	}
	for _, k := range kids {
		if ctx.Err() != nil || *budget <= 0 {
			truncated = true
			break
		}
		b, hit := procmem.TreeFootprintBounded(ctx, k, budget)
		if hit {
			truncated = true
		}
		name := shortName(k)
		if idx := slices.IndexFunc(procs, func(x rpc.DaemonMemoryProc) bool { return x.Name == name }); idx >= 0 {
			procs[idx].KB += int64(b >> 10)
			procs[idx].Count++
			continue
		}
		if len(procs) >= daemonMemoryMaxProcs {
			truncated = true
			continue
		}
		procs = append(procs, rpc.DaemonMemoryProc{Name: name, KB: int64(b >> 10), Count: 1})
	}
	slices.SortStableFunc(procs, func(a, b rpc.DaemonMemoryProc) int { return cmp.Compare(b.KB, a.KB) })
	return procs, truncated
}

func daemonMemoryWireSize(res *rpc.DaemonMemoryResult) int {
	resultBytes, err := json.Marshal(res)
	if err != nil {
		return daemonMemoryMaxWireBytes + 1
	}
	frame, err := json.Marshal(rpc.Response{Result: resultBytes})
	if err != nil {
		return daemonMemoryMaxWireBytes + 1
	}
	return len(frame) + 1 // 包含行尾换行符
}

func enforceDaemonMemorySizeBudget(res *rpc.DaemonMemoryResult, expanded map[string]bool) {
	if daemonMemoryWireSize(res) <= daemonMemoryMaxWireBytes {
		return
	}
	res.Truncated = true
	// 1. 先逐个清理展开的进程明细。
	for i := range res.Workspaces {
		for j := range res.Workspaces[i].Terminals {
			res.Workspaces[i].Terminals[j].Procs = nil
		}
	}
	if daemonMemoryWireSize(res) <= daemonMemoryMaxWireBytes {
		return
	}
	// 2. 删掉非展开的低占用尾部终端。
	for i := range res.Workspaces {
		kept := res.Workspaces[i].Terminals[:0]
		for _, t := range res.Workspaces[i].Terminals {
			if expanded[t.ID] {
				kept = append(kept, t)
			}
		}
		res.Workspaces[i].Terminals = kept
	}
	if daemonMemoryWireSize(res) <= daemonMemoryMaxWireBytes {
		return
	}
	// 3. 按尾部缩减工作区直到满足整行 64 KiB 限制。
	for len(res.Workspaces) > 0 && daemonMemoryWireSize(res) > daemonMemoryMaxWireBytes {
		res.Workspaces = res.Workspaces[:len(res.Workspaces)-1]
	}
	if daemonMemoryWireSize(res) > daemonMemoryMaxWireBytes {
		res.Workspaces = []rpc.DaemonMemoryWorkspace{}
	}
}

func (d *Daemon) autostartExpectedExecutable(managed bool, hint string) string {
	if managed && d.cfg.Home != "" {
		return filepath.Join(d.cfg.Home, "server", "current", "posse")
	}
	return hint
}

func decodeAutostartSetParams(raw json.RawMessage) (bool, error) {
	if len(bytes.TrimSpace(raw)) == 0 {
		return false, rpc.Errorf(rpc.CodeBadRequest, "invalid server.autostart.set parameters")
	}
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	var p rpc.AutostartSetParams
	if err := dec.Decode(&p); err != nil {
		return false, rpc.Errorf(rpc.CodeBadRequest, "invalid server.autostart.set parameters")
	}
	var trailing any
	if err := dec.Decode(&trailing); !errors.Is(err, io.EOF) {
		return false, rpc.Errorf(rpc.CodeBadRequest, "invalid server.autostart.set parameters")
	}
	if p.Enabled == nil {
		return false, rpc.Errorf(rpc.CodeBadRequest, "invalid server.autostart.set parameters")
	}
	return *p.Enabled, nil
}

func (d *Daemon) serverAutostartStatus(ctx context.Context, _ json.RawMessage) (any, error) {
	qctx, cancel := context.WithTimeout(ctx, autostartQueryTimeout)
	defer cancel()
	if d.autostart != nil {
		return d.autostart(qctx)
	}
	managed := Edition == "headless" && d.runtime != ""
	switch runtime.GOOS {
	case "darwin":
		st, err := spawn.QueryLaunchAgent(qctx, spawn.LaunchAgentOptions{Home: d.cfg.Home})
		if err != nil {
			if errors.Is(err, exec.ErrNotFound) {
				return rpc.AutostartStatus{Supported: false, Managed: managed}, nil
			}
			return nil, fmt.Errorf("launchd status query failed: %w", err)
		}
		return rpc.AutostartStatus{
			Supported:          true,
			Kind:               "launchd",
			Managed:            managed,
			Writable:           managed,
			Installed:          st.Installed,
			Enabled:            st.Installed && !st.Disabled,
			Active:             st.Loaded,
			Unit:               st.Label,
			Path:               st.Plist,
			ExpectedExecutable: d.autostartExpectedExecutable(managed, st.Executable),
			NoteCode:           st.NoteCode,
		}, nil
	case "linux":
		st, err := spawn.QuerySystemdUser(qctx, spawn.SystemdUserOptions{Home: d.cfg.Home})
		if err != nil {
			if errors.Is(err, exec.ErrNotFound) {
				return rpc.AutostartStatus{Supported: false, Managed: managed}, nil
			}
			return nil, fmt.Errorf("systemd status query failed: %w", err)
		}
		return rpc.AutostartStatus{
			Supported:          true,
			Kind:               "systemd",
			Managed:            managed,
			Writable:           managed,
			Installed:          st.Installed,
			Enabled:            st.Enabled,
			Active:             st.Active,
			Unit:               st.Unit,
			Path:               st.ServicePath,
			TimerPath:          st.TimerPath,
			ExpectedExecutable: d.autostartExpectedExecutable(managed, st.Executable),
			Linger:             st.Linger,
			NoteCode:           st.NoteCode,
		}, nil
	default:
		return rpc.AutostartStatus{Supported: false, Managed: managed}, nil
	}
}

func (d *Daemon) serverAutostartSet(ctx context.Context, raw json.RawMessage) (any, error) {
	// 仅允许通过 desktop.call 校验过控制权、序号去重与 handoff 门禁的桌面写请求进入，禁止经转发 socket 直调绕过。
	if !isDesktopWriteRequest(ctx) {
		return nil, rpc.Errorf(rpc.CodeReadOnly, "desktop control required for server.autostart.set")
	}
	enabled, err := decodeAutostartSetParams(raw)
	if err != nil {
		return nil, err
	}
	managed := Edition == "headless" && d.runtime != ""
	if d.setAutostart == nil && !managed {
		return nil, rpc.Errorf(rpc.CodeBadRequest, "autostart can only be managed on managed headless servers")
	}
	qctx, cancel := context.WithTimeout(ctx, autostartSetTimeout)
	defer cancel()

	// 进程内经 writecoord 串行化同一 home 的自启修改（零值可用且支持 ctx 取消）。
	release, err := d.writes.Acquire(qctx, writecoord.Scope{Keys: []string{"autostart:" + d.cfg.Home}})
	if err != nil {
		return nil, err
	}
	defer release()

	if d.setAutostart != nil {
		return d.setAutostart(qctx, enabled)
	}

	switch runtime.GOOS {
	case "darwin":
		var (
			st  spawn.LaunchAgentStatus
			err error
		)
		if enabled {
			st, err = spawn.InstallLaunchAgent(qctx, spawn.LaunchAgentOptions{
				Home:                  d.cfg.Home,
				RequireManagedCurrent: true,
			})
		} else {
			st, err = spawn.UninstallLaunchAgent(qctx, spawn.LaunchAgentOptions{Home: d.cfg.Home})
		}
		if err != nil {
			if errors.Is(err, exec.ErrNotFound) {
				return nil, rpc.Errorf(rpc.CodeBadRequest, "startup service tool is not available on this host")
			}
			return nil, fmt.Errorf("launchd autostart update failed: %w", err)
		}
		return rpc.AutostartStatus{
			Supported:          true,
			Kind:               "launchd",
			Managed:            managed,
			Writable:           managed,
			Installed:          st.Installed,
			Enabled:            st.Installed && !st.Disabled,
			Active:             st.Loaded,
			Unit:               st.Label,
			Path:               st.Plist,
			ExpectedExecutable: d.autostartExpectedExecutable(managed, st.Executable),
			NoteCode:           st.NoteCode,
		}, nil
	case "linux":
		var (
			st  spawn.SystemdUserStatus
			err error
		)
		if enabled {
			st, err = spawn.InstallSystemdUser(qctx, spawn.SystemdUserOptions{
				Home:                  d.cfg.Home,
				RequireManagedCurrent: true,
			})
		} else {
			st, err = spawn.UninstallSystemdUser(qctx, spawn.SystemdUserOptions{Home: d.cfg.Home})
		}
		if err != nil {
			if errors.Is(err, exec.ErrNotFound) {
				return nil, rpc.Errorf(rpc.CodeBadRequest, "startup service tool is not available on this host")
			}
			return nil, fmt.Errorf("systemd autostart update failed: %w", err)
		}
		return rpc.AutostartStatus{
			Supported:          true,
			Kind:               "systemd",
			Managed:            managed,
			Writable:           managed,
			Installed:          st.Installed,
			Enabled:            st.Enabled,
			Active:             st.Active,
			Unit:               st.Unit,
			Path:               st.ServicePath,
			TimerPath:          st.TimerPath,
			ExpectedExecutable: d.autostartExpectedExecutable(managed, st.Executable),
			Linger:             st.Linger,
			NoteCode:           st.NoteCode,
		}, nil
	default:
		return nil, rpc.Errorf(rpc.CodeBadRequest, "startup service management is not supported on this platform")
	}
}
