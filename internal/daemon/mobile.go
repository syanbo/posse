package daemon

import (
	"context"
	"encoding/json"
	"log"
	"os"
	"strconv"
	"time"

	"posse/internal/agent"
	"posse/internal/desk"
	"posse/internal/gitx"
	"posse/internal/host"
	"posse/internal/mobile"
	"posse/internal/rpc"
	"posse/internal/usage"
)

// mobileHost 把常驻进程的项目、工作区和终端交给手机服务。
type mobileHost struct{ d *Daemon }

// startMobile 打开手机服务；已经配对过手机时立刻监听。端口可用 POSSE_MOBILE_PORT 改（端到端测试用，免得和日常的冲突），
// 开发包不设时用 devMobilePort。
// newMobile 读手机配置，还不监听：交接过来时端口还在旧进程手里。
func (d *Daemon) newMobile() {
	port := mobile.DefaultPort
	if runningDevBundle() {
		port = devMobilePort
	}
	if v, err := strconv.Atoi(os.Getenv("POSSE_MOBILE_PORT")); err == nil {
		port = v
	}
	m, err := mobile.New(d.cfg.Home, mobileHost{d}, port)
	if err != nil {
		log.Printf("mobile: %v", err)
		return
	}
	m.SetQUIC(mobile.QUICOptions{Relay: d.deskRelay, ListenOut: desk.ListenPhysical})
	d.mobile = m
}

func (d *Daemon) startMobile() {
	if d.mobile == nil {
		return
	}
	if err := d.mobile.Start(); err != nil {
		log.Printf("%v", err)
	}
}

func (h mobileHost) State() mobile.State {
	d := h.d
	var st mobile.State
	ps, err := d.store.Projects()
	if err != nil {
		return st
	}
	for _, p := range ps {
		st.Projects = append(st.Projects, mobile.Project{ID: p.ID, Name: p.Name, Path: p.Path})
	}
	ws, err := d.store.Workspaces("")
	if err != nil {
		return st
	}
	byWS := d.sessionsByWorkspace()
	for _, w := range ws {
		// 数据库里的分支只在建工作区时记一次，main 工作区切分支后就过时了；和界面一样以 HEAD 为准
		if b := gitx.Head(w.Path); b != "" {
			w.Branch = b
		}
		mw := mobile.Workspace{ID: w.ID, ProjectID: w.ProjectID, Name: w.Name, Path: w.Path, Kind: w.Kind, Branch: w.Branch, CreatedAt: w.CreatedAt}
		for _, s := range byWS[w.ID] {
			// 手机上只显示还在运行的终端：退出的终端没有输出可看，订阅会立刻结束
			if !s.Running() {
				continue
			}
			a := s.Agent()
			// 用户起的名字优先，其次起过名的会话用会话名当标签名，和桌面一样
			title := s.Title()
			switch name := s.agentName(); {
			case s.userName() != "":
				title = s.userName()
			case a.Title != "" && name != "":
				title = a.Title
			case name == "grok":
				s.mu.Lock()
				title = s.grokName
				s.mu.Unlock()
				if n, ok := agent.GrokName(s.Title()); ok {
					title = n
				}
				if title == "" {
					title = "grok"
				}
			}
			mw.Terminals = append(mw.Terminals, mobile.Terminal{
				ID: s.ID, Title: title, Agent: s.agentName(), Status: a.Status, StatusAt: a.At, Message: a.Message,
				SessionID: a.SessionID, TranscriptPath: a.TranscriptPath,
				ToolName: a.ToolName, ToolInput: a.ToolInput, Prompt: a.Prompt,
				Running: true, CreatedAt: s.CreatedAt, LastOutput: s.LastOutput(),
			})
		}
		st.Workspaces = append(st.Workspaces, mw)
	}
	return st
}

func (h mobileHost) Terminal(id string) host.Terminal {
	s, err := h.d.session(id)
	if err != nil {
		return nil
	}
	return s.Terminal
}

func (h mobileHost) CreateTerminal(workspaceID, agentName, prompt, command string) (string, error) {
	w, err := h.d.workspace(workspaceID)
	if err != nil {
		return "", err
	}
	var s *session
	if agentName != "" {
		s, err = h.d.startAgent(w, agentName, agent.Model{}, prompt, 0, 0)
	} else {
		s, err = h.d.startTerminal(w, agent.ShellArgv(command), "", 0, 0)
	}
	if err != nil {
		return "", err
	}
	return s.ID, nil
}

func (h mobileHost) CloseTerminal(id string) error {
	s, err := h.d.session(id)
	if err != nil {
		return err
	}
	h.d.closeSession(s, 2*time.Second)
	return nil
}

func (h mobileHost) Agents() []string {
	if got := h.d.enabledAgents(); got != nil {
		return got
	}
	return agent.Known
}

func (h mobileHost) Usage() any {
	if st := h.d.usageStatus(); st != nil {
		return st
	}
	return usageStatus{Usages: []usage.Usage{}}
}

func (h mobileHost) RefreshUsage(ctx context.Context) (any, error) {
	return h.d.usageRefresh(ctx, nil)
}

func (h mobileHost) History(ctx context.Context, method string, params json.RawMessage) (any, error) {
	d := h.d
	switch method {
	case "history.list":
		return d.historyList(ctx, params)
	case "history.search":
		return d.historySearch(ctx, params)
	case "history.resume":
		res, err := d.historyResume(ctx, params)
		if err != nil {
			return nil, err
		}
		r := res.(historyResumeResult)
		w, err := d.workspace(r.WorkspaceID)
		if err != nil {
			return nil, err
		}
		return mobile.ResumeResult{Terminal: r.ID, Worktree: w.ProjectID + "::" + w.ID, AlreadyOpen: r.AlreadyOpen}, nil
	}
	return nil, rpc.Errorf(rpc.CodeBadRequest, "unknown method %s", method)
}

type mobilePairParams struct {
	Rotate  bool   `json:"rotate"`
	Address string `json:"address"`
}

func (d *Daemon) mobilePair(ctx context.Context, raw json.RawMessage) (any, error) {
	var p mobilePairParams
	if err := rpc.Decode(raw, &p); err != nil {
		return nil, err
	}
	if d.mobile == nil {
		return nil, rpc.Errorf(rpc.CodeInternal, "mobile service unavailable, see daemon log")
	}
	info, err := d.mobile.Pair(p.Rotate, p.Address)
	if err != nil {
		return nil, rpc.Errorf(rpc.CodeInternal, "%v", err)
	}
	return info, nil
}

func (d *Daemon) mobileDevices(ctx context.Context, _ json.RawMessage) (any, error) {
	if d.mobile == nil {
		return []mobile.Device{}, nil
	}
	out := d.mobile.Devices()
	if out == nil {
		out = []mobile.Device{}
	}
	return out, nil
}

type mobileRmParams struct {
	Device string `json:"device"`
}

func (d *Daemon) mobileRm(ctx context.Context, raw json.RawMessage) (any, error) {
	var p mobileRmParams
	if err := rpc.Decode(raw, &p); err != nil {
		return nil, err
	}
	if d.mobile == nil || !d.mobile.RemoveDevice(p.Device) {
		return nil, rpc.Errorf(rpc.CodeNotFound, "device %q not found", p.Device)
	}
	return map[string]any{"removed": p.Device}, nil
}
