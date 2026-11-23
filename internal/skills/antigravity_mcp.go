package skills

import (
	"bufio"
	"context"
	"encoding/json"
	"io"
	"mime"
	"net/http"
	"net/url"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"syscall"
	"time"
)

type MCPProbeResult string

const (
	MCPProbeReady   MCPProbeResult = "ready"
	MCPProbeAuth    MCPProbeResult = "auth"
	MCPProbeFailed  MCPProbeResult = "failed"
	MCPProbeTimeout MCPProbeResult = "timeout"
	MCPProbeChanged MCPProbeResult = "changed"
)

const mcpInitialize = `{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-11-25","capabilities":{},"clientInfo":{"name":"Posse","version":"1"}}}`

func visitAntigravityMCP(o MCPOptions, visit func(string, MCPDef, mcpJSONEntry)) {
	locations := antigravityLocations(o.Home, o.Dir, nil)
	add := func(file, prefix string, enabled bool) {
		for name, entry := range readMCPEntries(file) {
			def := entry.definition(file, Antigravity)
			def.Plugin = prefix != ""
			def.Disabled = def.Disabled || !enabled
			visit(prefix+name, def, entry)
		}
	}
	for _, p := range antigravityPlugins(locations) {
		add(filepath.Join(p.dir, "mcp_config.json"), p.name+"_", p.enabled)
	}
	// 同名工作区配置优先，插件的命名空间仍可被显式配置覆盖。
	for _, loc := range slices.Backward(locations) {
		if !loc.project || filepath.Base(loc.base) == ".agents" {
			add(filepath.Join(loc.base, "mcp_config.json"), "", true)
		}
	}
}

// ProbeAntigravityMCP 只握手验证这一份已启用的配置。令牌和响应不送进界面，也不执行任何 MCP 工具。
func ProbeAntigravityMCP(ctx context.Context, o MCPOptions, name string, def MCPDef) MCPProbeResult {
	if def.Agent != Antigravity || def.Disabled || def.File == "" {
		return MCPProbeChanged
	}
	var current MCPDef
	var entry mcpJSONEntry
	visitAntigravityMCP(o, func(key string, effective MCPDef, raw mcpJSONEntry) {
		if key == name {
			current, entry = effective, raw
		}
	})
	if current.Disabled || current.File != def.File || current.Plugin != def.Plugin ||
		current.Transport != def.Transport || current.Command != def.Command || current.Cwd != def.Cwd ||
		current.URL != def.URL || !slices.Equal(current.Args, def.Args) {
		return MCPProbeChanged
	}
	ctx, cancel := context.WithTimeout(ctx, 8*time.Second)
	defer cancel()
	result := MCPProbeFailed
	switch {
	case current.Transport == "stdio" && current.Command != "":
		dir := filepath.Dir(current.File)
		if filepath.IsAbs(current.Cwd) {
			dir = current.Cwd
		} else if current.Cwd != "" {
			dir = filepath.Join(dir, current.Cwd)
		}
		if probeMCPStdio(ctx, dir, entry) {
			result = MCPProbeReady
		}
	case current.Transport == "http" || current.Transport == "streamable-http":
		result = probeMCPHTTP(ctx, current.URL, entry.Headers)
	}
	if ctx.Err() != nil {
		return MCPProbeTimeout
	}
	return result
}

func probeMCPStdio(ctx context.Context, dir string, s mcpJSONEntry) bool {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	cmd := exec.CommandContext(ctx, s.Command, s.Args...)
	cmd.Dir = dir
	cmd.Env = cmd.Environ()
	for key, val := range s.Env {
		cmd.Env = append(cmd.Env, key+"="+val)
	}
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	cmd.Cancel = func() error { return syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL) }
	cmd.WaitDelay = time.Second
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return false
	}
	defer stdin.Close()
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return false
	}
	defer stdout.Close()
	if cmd.Start() != nil {
		return false
	}
	defer func() {
		cancel()
		cmd.Wait()
	}()
	if _, err := io.WriteString(stdin, mcpInitialize+"\n"); err != nil {
		return false
	}
	return readMCPInitialize(stdout, false) != ""
}

func probeMCPHTTP(ctx context.Context, endpoint string, headers map[string]string) MCPProbeResult {
	u, err := url.Parse(endpoint)
	if err != nil || u.Host == "" || u.User != nil || u.Scheme != "http" && u.Scheme != "https" {
		return MCPProbeFailed
	}
	client := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, strings.NewReader(mcpInitialize))
	if err != nil {
		return MCPProbeFailed
	}
	for key, val := range headers {
		req.Header.Set(key, val)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json, text/event-stream")
	resp, err := client.Do(req)
	if err != nil {
		return MCPProbeFailed
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden {
		return MCPProbeAuth
	}
	media, _, _ := mime.ParseMediaType(resp.Header.Get("Content-Type"))
	if resp.StatusCode != http.StatusOK {
		return MCPProbeFailed
	}
	version := readMCPInitialize(resp.Body, media == "text/event-stream")
	resp.Body.Close()
	if version != "" {
		if id := resp.Header.Get("Mcp-Session-Id"); id != "" {
			// 有些服务只在收到 DELETE 时回收分配的会话。
			endCtx, cancel := context.WithTimeout(ctx, 500*time.Millisecond)
			defer cancel()
			end, _ := http.NewRequestWithContext(endCtx, http.MethodDelete, endpoint, nil)
			end.Header = req.Header.Clone()
			end.Header.Set("Mcp-Session-Id", id)
			end.Header.Set("MCP-Protocol-Version", version)
			if reply, err := client.Do(end); err == nil {
				reply.Body.Close()
			}
		}
		return MCPProbeReady
	}
	return MCPProbeFailed
}

func readMCPInitialize(r io.Reader, sse bool) string {
	type reply struct {
		JSONRPC string `json:"jsonrpc"`
		ID      int    `json:"id"`
		Result  struct {
			Protocol string `json:"protocolVersion"`
			Server   struct {
				Name string `json:"name"`
			} `json:"serverInfo"`
		} `json:"result"`
	}
	read := func(raw []byte) (version string, matched bool) {
		var response reply
		if json.Unmarshal(raw, &response) != nil || response.JSONRPC != "2.0" || response.ID != 1 {
			return "", false
		}
		if response.Result.Server.Name == "" {
			return "", true
		}
		return response.Result.Protocol, true
	}
	if !sse {
		dec := json.NewDecoder(io.LimitReader(r, 1<<20))
		var raw json.RawMessage
		for dec.Decode(&raw) == nil {
			if version, matched := read(raw); matched {
				return version
			}
		}
		return ""
	}
	scanner := bufio.NewScanner(io.LimitReader(r, 1<<20))
	scanner.Buffer(make([]byte, 4096), 1<<20)
	var data strings.Builder
	for scanner.Scan() {
		line := scanner.Text()
		if value, ok := strings.CutPrefix(line, "data:"); ok {
			data.WriteString(strings.TrimPrefix(value, " ") + "\n")
		} else if line == "" && data.Len() > 0 {
			if version, matched := read([]byte(data.String())); matched {
				return version
			}
			data.Reset()
		}
	}
	return ""
}
