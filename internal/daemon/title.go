package daemon

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"posse/internal/errtext"
	"posse/internal/namer"
	"posse/internal/rpc"
)

// 会话标题（posse session title）：常驻进程按终端里的 agent 写进它自己存标题的地方，
// 再记在终端的 agent 状态上，界面的标签、侧栏和手机都用它当会话名。
//   - claude：只接受 UserPromptSubmit hook 返回的 sessionTitle，先记成待交，下一次提交时交过去
//   - codex：state 库的 threads 和 session_index.jsonl
//   - grok：会话目录的 summary.json
//   - cursor：没有核实过能写的地方，只在 Posse 里显示

type sessionTitleParams struct {
	Terminal string `json:"terminal"`
	Title    string `json:"title"`
	// SessionIDs 是调用方环境里各 agent 注入的会话 ID（按 agent 名），比常驻进程记下的更准：
	// 那是 agent 自己正在用的会话
	SessionIDs map[string]string `json:"sessionIds"`
	Cwd        string            `json:"cwd"` // grok 按工作目录分组存会话
}

type sessionTitleResult struct {
	Title string `json:"title"`
	Agent string `json:"agent"`
	// Applies 说明什么时候在哪里生效，给 agent 转述
	Applies string `json:"applies"`
}

func (d *Daemon) sessionTitle(ctx context.Context, raw json.RawMessage) (any, error) {
	var p sessionTitleParams
	if err := rpc.Decode(raw, &p); err != nil {
		return nil, err
	}
	s, err := d.session(p.Terminal)
	if err != nil {
		return nil, err
	}
	name := s.agentName()
	if name == "" {
		return nil, rpc.Errorf(rpc.CodeBadRequest, "no agent session is running in this terminal")
	}
	title, err := namer.Normalize(p.Title, time.Now(), errtext.SystemLang() == errtext.EN)
	if err != nil {
		return nil, rpc.Errorf(rpc.CodeBadRequest, "%v", err)
	}
	sid := p.SessionIDs[name]
	if sid == "" {
		sid = s.Agent().SessionID
	}
	if sid == "" && name != "cursor" {
		return nil, rpc.Errorf(rpc.CodeBadRequest, "the ID of this %s session is not known yet; send a message and try again", name)
	}
	var applies string
	switch name {
	case "claude":
		applies = "shown in Posse now; Claude Code’s session name updates on the next message"
	case "codex":
		err = namer.WriteCodex(agentHome("CODEX_HOME", ".codex"), sid, title)
		applies = "updated in Posse and the codex resume list; the name in Codex changes after the session is reopened"
	case "grok":
		err = namer.WriteGrok(agentHome("GROK_HOME", ".grok"), sid, p.Cwd, title)
		applies = "updated in Posse and grok’s session list; the name in grok changes after the session is reopened"
	default:
		applies = fmt.Sprintf("shown in Posse only; %s keeps its own session name", name)
	}
	if err != nil {
		return nil, rpc.Errorf(rpc.CodeInternal, "write the title: %v", err)
	}
	// 显式给会话改名也恢复标签跟随会话，不能让旧的自定义名字遮住成功的结果。
	if err := d.setTerminalName(s, ""); err != nil {
		return nil, err
	}
	s.SetTitle(title, sid, name == "claude")
	return sessionTitleResult{Title: title, Agent: name, Applies: applies}, nil
}

// agentHome 是 agent 的数据目录：环境变量优先，否则在家目录下。
func agentHome(env, dir string) string {
	if v := os.Getenv(env); v != "" {
		return v
	}
	home, _ := os.UserHomeDir()
	return filepath.Join(home, dir)
}
