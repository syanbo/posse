package skills

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/BurntSushi/toml"
)

// MCP 服务按名字合并四家的用户级配置，算出每家用不用、从哪读到。只读，不改配置。
//   - Claude 读 ~/.claude.json 的 mcpServers（按项目记的、项目里的 .mcp.json 不在这里）。
//   - Codex 问 `codex mcp list --json`：带插件带的服务和开关，秒回、不启动服务。找不到 codex 时读 config.toml。
//   - Grok 问 `grok inspect --json`：合并后实际用的服务，每项带来源。它默认也读 Claude 和 Cursor 的配置，
//     同名时自己的 config.toml 优先（docs/user-guide/07-mcp-servers.md）。找不到 grok 时按这条规则推算。
//   - Cursor 读 ~/.cursor/mcp.json。它的批准和开关存在哪没核实，读到的都算启用、标推断。
// 不用 `claude mcp list`：它要把每个服务都启动一遍做健康检查。

// MCPDef 是一份配置里的一个服务。
type MCPDef struct {
	Agent     Agent  // 这份配置是哪家的
	File      string // 配置所在的文件；插件带的是插件目录
	Plugin    bool
	Transport string // stdio、http、sse
	Command   string
	Args      []string
	URL       string
	// EnvKeys、HeaderKeys 只记名字：值里常有密钥
	EnvKeys    []string
	HeaderKeys []string
	Disabled   bool
}

// MCPServer 是同名的一个服务：各家的配置和各家的状态。
type MCPServer struct {
	Name   string
	Defs   []MCPDef
	Agents [NumAgents]Status
}

// MCPIssueKind 是 MCP 体检发现的问题种类。
type MCPIssueKind string

const (
	MCPMissingCommand MCPIssueKind = "missing"  // 启动命令是绝对路径，文件不在了
	MCPMismatch       MCPIssueKind = "mismatch" // 同名的服务在几份配置里写得不一样
)

// MCPIssue 是一条问题。
type MCPIssue struct {
	Kind   MCPIssueKind
	Name   string
	Detail string
}

// MCPCatalog 是一次扫描的结果。Live 记哪几家是问它自己拿到的（其余是读文件推算）。
type MCPCatalog struct {
	Servers []MCPServer
	Issues  []MCPIssue
	Live    [NumAgents]bool
}

// MCPOptions 是扫描的输入。Codex、Grok 是它们的可执行文件，空时读文件推算。
type MCPOptions struct {
	Home        string
	Codex, Grok string
}

// mcpTimeout 是问一家要多久算失败。
const mcpTimeout = 15 * time.Second

// ScanMCP 扫描四家的用户级 MCP 配置。
func ScanMCP(ctx context.Context, o MCPOptions) MCPCatalog {
	var cat MCPCatalog
	byName := map[string]*MCPServer{}
	server := func(name string) *MCPServer {
		s := byName[name]
		if s == nil {
			s = &MCPServer{Name: name}
			byName[name] = s
		}
		return s
	}
	claudeFile := filepath.Join(o.Home, ".claude.json")
	cursorFile := filepath.Join(o.Home, ".cursor", "mcp.json")
	claude := readMCPJSON(claudeFile, Claude)
	cursor := readMCPJSON(cursorFile, Cursor)
	for name, d := range claude {
		s := server(name)
		s.Defs = append(s.Defs, d)
		s.Agents[Claude] = Status{State: On, Via: claudeFile}
	}
	for name, d := range cursor {
		s := server(name)
		s.Defs = append(s.Defs, d)
		s.Agents[Cursor] = Status{State: On, Via: cursorFile, Inferred: true}
	}

	codexDefs, live := codexMCP(ctx, o)
	cat.Live[Codex] = live
	for name, d := range codexDefs {
		s := server(name)
		s.Defs = append(s.Defs, d)
		st := Status{State: On, Via: d.File}
		if d.Disabled {
			st.State, st.Why = Off, "config.toml 里 enabled = false"
		}
		s.Agents[Codex] = st
	}

	grokFile := filepath.Join(o.Home, ".grok", "config.toml")
	grokDefs, compat := readGrokMCP(grokFile)
	for name, d := range grokDefs {
		server(name).Defs = append(server(name).Defs, d)
	}
	used, live := grokUsed(ctx, o)
	cat.Live[Grok] = live
	if !live {
		// 按文档推算：自己的 config.toml > Claude > Cursor，Claude、Cursor 的可以在 [compat.<家>] mcps 关掉
		// 自己 config.toml 里有的名字（关着的也算）不再从 Claude、Cursor 补
		used = map[string]string{}
		for name, d := range grokDefs {
			if !d.Disabled {
				used[name] = grokFile
			}
		}
		for _, src := range []struct {
			on   bool
			defs map[string]MCPDef
			file string
		}{{compat["claude"], claude, claudeFile}, {compat["cursor"], cursor, cursorFile}} {
			if !src.on {
				continue
			}
			for name := range src.defs {
				if _, ok := used[name]; !ok && grokDefs[name].File == "" {
					used[name] = src.file
				}
			}
		}
	}
	for name, file := range used {
		server(name).Agents[Grok] = Status{State: On, Via: file, Inferred: !live}
	}
	for name, d := range grokDefs {
		if _, ok := used[name]; !ok && d.Disabled {
			server(name).Agents[Grok] = Status{State: Off, Via: grokFile, Why: "config.toml 里 enabled = false"}
		}
	}

	for _, s := range byName {
		cat.Servers = append(cat.Servers, *s)
	}
	slices.SortFunc(cat.Servers, func(x, y MCPServer) int { return strings.Compare(strings.ToLower(x.Name), strings.ToLower(y.Name)) })
	cat.Issues = mcpIssues(cat.Servers)
	return cat
}

// readMCPJSON 读 Claude、Cursor 那种 {"mcpServers": {名字: {command, args, url, env, headers}}} 的文件。
func readMCPJSON(file string, ag Agent) map[string]MCPDef {
	b, err := os.ReadFile(file)
	if err != nil {
		return nil
	}
	var f struct {
		MCPServers map[string]struct {
			Type    string            `json:"type"`
			Command string            `json:"command"`
			Args    []string          `json:"args"`
			URL     string            `json:"url"`
			Env     map[string]string `json:"env"`
			Headers map[string]string `json:"headers"`
		} `json:"mcpServers"`
	}
	if json.Unmarshal(b, &f) != nil {
		return nil
	}
	out := map[string]MCPDef{}
	for name, s := range f.MCPServers {
		d := MCPDef{Agent: ag, File: file, Transport: s.Type, Command: s.Command, Args: s.Args, URL: s.URL, EnvKeys: keys(s.Env), HeaderKeys: keys(s.Headers)}
		if d.Transport == "" {
			d.Transport = "stdio"
			if d.URL != "" {
				d.Transport = "http"
			}
		}
		out[name] = d
	}
	return out
}

// tomlMCP 是 Codex、Grok 的 config.toml 里的 [mcp_servers.<名字>]。
type tomlMCP struct {
	Command string            `toml:"command"`
	Args    []string          `toml:"args"`
	URL     string            `toml:"url"`
	Env     map[string]string `toml:"env"`
	EnvVars []string          `toml:"env_vars"`
	Headers map[string]string `toml:"headers"`
	Enabled *bool             `toml:"enabled"`
}

func (t tomlMCP) def(ag Agent, file string) MCPDef {
	d := MCPDef{Agent: ag, File: file, Transport: "stdio", Command: t.Command, Args: t.Args, URL: t.URL,
		EnvKeys: append(keys(t.Env), t.EnvVars...), HeaderKeys: keys(t.Headers), Disabled: t.Enabled != nil && !*t.Enabled}
	if d.URL != "" {
		d.Transport = "http"
	}
	slices.Sort(d.EnvKeys)
	d.EnvKeys = slices.Compact(d.EnvKeys)
	return d
}

// readGrokMCP 读 Grok 自己的服务，和 [compat.claude]、[compat.cursor] 的 mcps（默认读）。
func readGrokMCP(file string) (map[string]MCPDef, map[string]bool) {
	var f struct {
		MCPServers map[string]tomlMCP `toml:"mcp_servers"`
		Compat     map[string]struct {
			MCPs *bool `toml:"mcps"`
		} `toml:"compat"`
	}
	compat := map[string]bool{"claude": true, "cursor": true}
	if _, err := toml.DecodeFile(file, &f); err != nil {
		return nil, compat
	}
	for k, c := range f.Compat {
		if c.MCPs != nil {
			compat[k] = *c.MCPs
		}
	}
	out := map[string]MCPDef{}
	for name, t := range f.MCPServers {
		out[name] = t.def(Grok, file)
	}
	return out, compat
}

// codexMCP 问 codex 它的服务（含插件带的）；问不到时读 config.toml。第二个返回值是不是问到的。
func codexMCP(ctx context.Context, o MCPOptions) (map[string]MCPDef, bool) {
	file := filepath.Join(codexHome(o.Home), "config.toml")
	if o.Codex != "" {
		var list []struct {
			Name      string `json:"name"`
			Enabled   bool   `json:"enabled"`
			Transport struct {
				Type    string            `json:"type"`
				Command string            `json:"command"`
				Args    []string          `json:"args"`
				URL     string            `json:"url"`
				Env     map[string]string `json:"env"`
				EnvVars []string          `json:"env_vars"`
				Cwd     string            `json:"cwd"`
				Headers map[string]string `json:"http_headers"`
			} `json:"transport"`
		}
		if b, err := runQuiet(ctx, o.Home, o.Codex, "mcp", "list", "--json"); err == nil && json.Unmarshal(b, &list) == nil {
			out := map[string]MCPDef{}
			for _, s := range list {
				t := s.Transport
				d := MCPDef{Agent: Codex, File: file, Transport: t.Type, Command: t.Command, Args: t.Args, URL: t.URL,
					EnvKeys: append(keys(t.Env), t.EnvVars...), HeaderKeys: keys(t.Headers), Disabled: !s.Enabled}
				// 插件带的服务从插件缓存目录启动
				if dir := filepath.Join(codexHome(o.Home), "plugins") + string(filepath.Separator); strings.HasPrefix(t.Cwd, dir) || strings.HasPrefix(t.Command, dir) {
					d.Plugin, d.File = true, t.Cwd
					if !strings.HasPrefix(t.Cwd, dir) {
						d.File = filepath.Dir(t.Command)
					}
				}
				if d.Transport == "streamable_http" {
					d.Transport = "http"
				}
				slices.Sort(d.EnvKeys)
				d.EnvKeys = slices.Compact(d.EnvKeys)
				out[s.Name] = d
			}
			return out, true
		}
	}
	var f struct {
		MCPServers map[string]tomlMCP `toml:"mcp_servers"`
	}
	if _, err := toml.DecodeFile(file, &f); err != nil {
		return nil, false
	}
	out := map[string]MCPDef{}
	for name, t := range f.MCPServers {
		out[name] = t.def(Codex, file)
	}
	return out, false
}

// grokUsed 问 grok 合并后实际用的服务：名字 → 读到它的文件。
func grokUsed(ctx context.Context, o MCPOptions) (map[string]string, bool) {
	if o.Grok == "" {
		return nil, false
	}
	b, err := runQuiet(ctx, o.Home, o.Grok, "inspect", "--json")
	if err != nil {
		return nil, false
	}
	var r struct {
		MCPServers []struct {
			Name   string `json:"name"`
			Source struct {
				Type string `json:"type"`
				Path string `json:"path"`
			} `json:"source"`
		} `json:"mcpServers"`
	}
	if json.Unmarshal(b, &r) != nil {
		return nil, false
	}
	out := map[string]string{}
	for _, s := range r.MCPServers {
		out[s.Name] = s.Source.Path
	}
	return out, true
}

// runQuiet 在 dir 里跑一个命令拿标准输出，超时算失败。
func runQuiet(ctx context.Context, dir, bin string, args ...string) ([]byte, error) {
	ctx, cancel := context.WithTimeout(ctx, mcpTimeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, bin, args...)
	cmd.Dir = dir
	cmd.WaitDelay = time.Second
	return cmd.Output()
}

// FindGrok 找 grok 可执行文件：窗口进程从访达启动时 PATH 里没有它的目录。找不到返回空。
func FindGrok(home string) string {
	if p, err := exec.LookPath("grok"); err == nil {
		return p
	}
	for _, p := range []string{filepath.Join(home, ".grok", "bin", "grok"), filepath.Join(home, ".local", "bin", "grok"), "/opt/homebrew/bin/grok", "/usr/local/bin/grok"} {
		if fi, err := os.Stat(p); err == nil && !fi.IsDir() && fi.Mode()&0o111 != 0 {
			return p
		}
	}
	return ""
}

// mcpIssues 体检：绝对路径的启动命令不在了，同名的服务在几份配置里写得不一样。
func mcpIssues(servers []MCPServer) []MCPIssue {
	var out []MCPIssue
	for _, s := range servers {
		missing := map[string]bool{}
		for _, d := range s.Defs {
			if filepath.IsAbs(d.Command) && !missing[d.Command] {
				if _, err := os.Stat(d.Command); err != nil {
					missing[d.Command] = true
					out = append(out, MCPIssue{Kind: MCPMissingCommand, Name: s.Name, Detail: d.Command})
				}
			}
		}
		var sigs []string
		for _, d := range s.Defs {
			if d.Plugin {
				continue
			}
			if sig := d.signature(); !slices.Contains(sigs, sig) {
				sigs = append(sigs, sig)
			}
		}
		if len(sigs) > 1 {
			out = append(out, MCPIssue{Kind: MCPMismatch, Name: s.Name, Detail: fmt.Sprintf("%d 种写法", len(sigs))})
		}
	}
	return out
}

// signature 是比较两份配置是否一样时看的部分：怎么连、启动什么。环境变量的值看不到，不比。
func (d MCPDef) signature() string {
	if d.URL != "" {
		return d.URL
	}
	return strings.Join(append([]string{d.Command}, d.Args...), "\x00")
}

func keys(m map[string]string) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	slices.Sort(out)
	return out
}
