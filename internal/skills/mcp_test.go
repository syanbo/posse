package skills

import (
	"context"
	"path/filepath"
	"slices"
	"testing"
)

// 读文件推算：Grok 自己的优先，其次 Claude、Cursor；关掉 Claude 的兼容后不读它的。
func TestScanMCPFiles(t *testing.T) {
	h := t.TempDir()
	t.Setenv("CODEX_HOME", filepath.Join(h, ".codex"))
	write(t, filepath.Join(h, ".claude.json"), `{"mcpServers":{
		"shared":{"type":"stdio","command":"/bin/sh","args":["-c","x"],"env":{"TOKEN":"secret"}},
		"cl":{"command":"/no/such/bin"}}}`)
	write(t, filepath.Join(h, ".cursor", "mcp.json"), `{"mcpServers":{"shared":{"command":"/bin/sh","args":["-c","y"]},"remote":{"url":"https://x.test/mcp","headers":{"Authorization":"Bearer z"}}}}`)
	write(t, filepath.Join(h, ".codex", "config.toml"), "[mcp_servers.shared]\ncommand = \"/bin/sh\"\nargs = [\"-c\", \"x\"]\n\n[mcp_servers.off]\ncommand = \"/bin/sh\"\nenabled = false\n")
	write(t, filepath.Join(h, ".grok", "config.toml"), "[mcp_servers.gk]\ncommand = \"/bin/sh\"\n")

	c := ScanMCP(context.Background(), MCPOptions{Home: h})
	get := func(name string) MCPServer {
		i := slices.IndexFunc(c.Servers, func(s MCPServer) bool { return s.Name == name })
		if i < 0 {
			t.Fatalf("没有 %s：%+v", name, c.Servers)
		}
		return c.Servers[i]
	}
	states := func(s MCPServer) [NumAgents]State {
		var out [NumAgents]State
		for _, ag := range Agents {
			out[ag] = s.Agents[ag].State
		}
		return out
	}
	if got := states(get("shared")); got != [NumAgents]State{On, On, On, On} {
		t.Errorf("shared %v", got)
	}
	// Grok 推算时用的是 Claude 那份（Claude 优先于 Cursor）
	if via := get("shared").Agents[Grok].Via; via != filepath.Join(h, ".claude.json") || !get("shared").Agents[Grok].Inferred {
		t.Errorf("Grok 读到 shared 的地方 %q", via)
	}
	if got := states(get("off")); got != [NumAgents]State{Unseen, Off, Unseen, Unseen} {
		t.Errorf("off %v", got)
	}
	if got := states(get("remote")); got != [NumAgents]State{Unseen, Unseen, On, On} {
		t.Errorf("remote %v", got)
	}
	if d := get("remote").Defs[0]; d.Transport != "http" || !slices.Equal(d.HeaderKeys, []string{"Authorization"}) {
		t.Errorf("remote 配置 %+v", d)
	}
	if d := get("shared").Defs[0]; !slices.Equal(d.EnvKeys, []string{"TOKEN"}) {
		t.Errorf("环境变量只记名字 %+v", d)
	}
	kinds := map[string]MCPIssueKind{}
	for _, is := range c.Issues {
		kinds[is.Name] = is.Kind
	}
	if kinds["cl"] != MCPMissingCommand || kinds["shared"] != MCPMismatch || len(c.Issues) != 2 {
		t.Errorf("体检 %+v", c.Issues)
	}

	// Grok 自己关掉的同名服务不从 Claude 补回来
	write(t, filepath.Join(h, ".grok", "config.toml"), "[mcp_servers.cl]\ncommand = \"/bin/sh\"\nenabled = false\n")
	c = ScanMCP(context.Background(), MCPOptions{Home: h})
	if st := get("cl").Agents[Grok].State; st != Off {
		t.Errorf("Grok 自己关掉的 cl 应是关着：%v", st)
	}

	write(t, filepath.Join(h, ".grok", "config.toml"), "[compat.claude]\nmcps = false\n")
	c = ScanMCP(context.Background(), MCPOptions{Home: h})
	if st := get("cl").Agents[Grok].State; st != Unseen {
		t.Errorf("关了 Claude 兼容后 Grok 不该读到 cl：%v", st)
	}
	if via := get("shared").Agents[Grok].Via; via != filepath.Join(h, ".cursor", "mcp.json") {
		t.Errorf("关了 Claude 兼容后 shared 应来自 Cursor：%q", via)
	}
}
