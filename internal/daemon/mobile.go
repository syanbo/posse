package daemon

import (
	"context"
	"encoding/json"
	"log"
	"os"
	"strconv"
	"time"

	"hive/internal/agent"
	"hive/internal/mobile"
	"hive/internal/rpc"
	"hive/internal/term"
)

// mobileHost 把常驻进程的项目、工作区和终端交给手机服务。
type mobileHost struct{ d *Daemon }

// startMobile 打开手机服务；已经配对过手机时立刻监听。端口可用 HIVE_MOBILE_PORT 改（端到端测试用，免得和日常的冲突）。
func (d *Daemon) startMobile() {
	port := mobile.DefaultPort
	if v, err := strconv.Atoi(os.Getenv("HIVE_MOBILE_PORT")); err == nil {
		port = v
	}
	m, err := mobile.New(d.cfg.Home, mobileHost{d}, port)
	if err != nil {
		log.Printf("mobile: %v", err)
		return
	}
	if err := m.Start(); err != nil {
		log.Printf("%v", err)
	}
	d.mobile = m
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
		mw := mobile.Workspace{ID: w.ID, ProjectID: w.ProjectID, Name: w.Name, Path: w.Path, Kind: w.Kind, Branch: w.Branch, CreatedAt: w.CreatedAt}
		for _, s := range byWS[w.ID] {
			// 手机上只显示还在运行的终端：退出的终端没有输出可看，订阅会立刻结束
			if !s.Running() {
				continue
			}
			a := s.Agent()
			mw.Terminals = append(mw.Terminals, mobile.Terminal{
				ID: s.ID, Title: s.Title(), Agent: s.agent, Status: a.Status, StatusAt: a.At, Message: a.Message,
				SessionID: a.SessionID, TranscriptPath: a.TranscriptPath,
				ToolName: a.ToolName, ToolInput: a.ToolInput, Prompt: a.Prompt,
				Running: true, CreatedAt: s.CreatedAt, LastOutput: s.LastOutput(),
			})
		}
		st.Workspaces = append(st.Workspaces, mw)
	}
	return st
}

func (h mobileHost) Terminal(id string) *term.Terminal {
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
		s, err = h.d.startAgent(w, agentName, prompt, 0, 0)
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

func (h mobileHost) Agents() []string { return agent.Known }

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
