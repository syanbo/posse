package skills

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
)

// CodexSkill 是 Codex app-server 的 skills/list 列出的一项。
type CodexSkill struct {
	Name        string `json:"name"`
	Description string `json:"description"`
	Path        string `json:"path"`
	Enabled     bool   `json:"enabled"`
	Scope       string `json:"scope"`
	PluginID    string `json:"pluginId"`
}

// CodexList 拉起一次 codex app-server 问 dir 下 Codex 能用的技能（含插件带的和开关）。
// Codex 的插件和它的开关只有它自己算得准，按文件推算不了。
func CodexList(ctx context.Context, codex, dir string) ([]CodexSkill, error) {
	var res struct {
		Data []struct {
			Skills []CodexSkill `json:"skills"`
		} `json:"data"`
	}
	params := map[string]any{"forceReload": true}
	if dir != "" {
		params["cwds"] = []string{dir}
	}
	if err := codexCall(ctx, codex, "skills/list", params, &res); err != nil {
		return nil, err
	}
	var out []CodexSkill
	for _, d := range res.Data {
		out = append(out, d.Skills...)
	}
	return out, nil
}

// CodexSetEnabled 经 app-server 开关一个技能（Codex 写进自己的 config.toml 的 [[skills.config]]）。
func CodexSetEnabled(ctx context.Context, codex, name string, enabled bool) error {
	return codexCall(ctx, codex, "skills/config/write", map[string]any{"name": name, "enabled": enabled}, nil)
}

// codexCall 拉起 codex app-server，握手后调一个方法，把结果解进 out。
func codexCall(ctx context.Context, codex, method string, params any, out any) error {
	cmd := exec.CommandContext(ctx, codex, "app-server")
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return err
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return err
	}
	if err := cmd.Start(); err != nil {
		return err
	}
	defer func() {
		stdin.Close()
		cmd.Process.Kill()
		cmd.Wait()
	}()
	send := func(v any) error {
		b, _ := json.Marshal(v)
		_, err := stdin.Write(append(b, '\n'))
		return err
	}
	lines := bufio.NewScanner(stdout)
	lines.Buffer(make([]byte, 64<<10), 16<<20)
	// recv 读到 id 对上的回应；中间的通知跳过
	recv := func(id int) (json.RawMessage, error) {
		for lines.Scan() {
			var m struct {
				ID     *int            `json:"id"`
				Result json.RawMessage `json:"result"`
				Error  *struct {
					Message string `json:"message"`
				} `json:"error"`
			}
			if json.Unmarshal(lines.Bytes(), &m) != nil || m.ID == nil || *m.ID != id {
				continue
			}
			if m.Error != nil {
				return nil, errors.New("codex: " + m.Error.Message)
			}
			return m.Result, nil
		}
		if err := lines.Err(); err != nil {
			return nil, err
		}
		return nil, io.ErrUnexpectedEOF
	}
	if err := send(map[string]any{"jsonrpc": "2.0", "id": 1, "method": "initialize",
		"params": map[string]any{"clientInfo": map[string]string{"name": "posse", "version": "1"}}}); err != nil {
		return err
	}
	if _, err := recv(1); err != nil {
		return err
	}
	send(map[string]any{"jsonrpc": "2.0", "method": "initialized"})
	if err := send(map[string]any{"jsonrpc": "2.0", "id": 2, "method": method, "params": params}); err != nil {
		return err
	}
	raw, err := recv(2)
	if err != nil || out == nil {
		return err
	}
	return json.Unmarshal(raw, out)
}

// applyCodex 用 skills/list 的结果换掉按文件推算的 Codex 状态：列出来的按它的 enabled，没列的是读不到；
// 插件和系统技能文件扫描不到的补进来。
func applyCodex(ss []Skill, list []CodexSkill) []Skill {
	byDir := map[string]int{}
	for i := range ss {
		byDir[ss[i].Dir] = i
		// 没列出来就是读不到：按文件推算也读不到的留着原因，推算读得到的说明是 app-server 没列
		switch prev := ss[i].Agents[Codex]; prev.State {
		case Shadowed:
		case Unseen:
			ss[i].Agents[Codex] = Status{Why: prev.Why}
		default:
			ss[i].Agents[Codex] = Status{Why: "Codex 的 skills/list 里没有它"}
		}
	}
	for _, c := range list {
		dir := realDir(filepath.Dir(c.Path))
		st := Status{State: On, Via: c.Path}
		if !c.Enabled {
			st.State, st.Why = Off, "config.toml 的 [[skills.config]] 设为 enabled = false"
		}
		if i, ok := byDir[dir]; ok {
			if ss[i].Agents[Codex].State != Shadowed {
				ss[i].Agents[Codex] = st
			}
			continue
		}
		scope := ScopeUser
		switch {
		case c.PluginID != "":
			scope = ScopePlugin
		case c.Scope == "system" || c.Scope == "admin":
			scope = ScopeSystem
		case c.Scope == "repo":
			scope = ScopeProject
		}
		s := Skill{Name: c.Name, Description: c.Description, Dir: dir, File: c.Path, Scope: scope, Paths: []string{c.Path}}
		s.Agents[Codex] = st
		byDir[dir] = len(ss)
		ss = append(ss, s)
	}
	return ss
}

// FindCodex 找 codex 可执行文件。窗口从访达启动时 PATH 只有系统目录，再到常见的安装位置找。
func FindCodex(home string) string {
	if p, err := exec.LookPath("codex"); err == nil {
		return p
	}
	for _, d := range []string{"/opt/homebrew/bin", "/usr/local/bin", filepath.Join(home, ".local", "bin"), filepath.Join(home, ".bun", "bin")} {
		p := filepath.Join(d, "codex")
		if fi, err := os.Stat(p); err == nil && !fi.IsDir() && fi.Mode()&0o111 != 0 {
			return p
		}
	}
	return ""
}
