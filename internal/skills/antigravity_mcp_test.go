package skills

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

const testMCPInitReply = `{"jsonrpc":"2.0","id":1,"result":{"protocolVersion":"2025-06-18","serverInfo":{"name":"sample"}}}`

func antigravityProbeFixture(t *testing.T, s mcpJSONEntry) (MCPOptions, MCPDef) {
	t.Helper()
	o := MCPOptions{Home: t.TempDir()}
	file := filepath.Join(o.Home, ".gemini", "config", "mcp_config.json")
	b, err := json.Marshal(map[string]any{"mcpServers": map[string]mcpJSONEntry{"test": s}})
	if err != nil {
		t.Fatal(err)
	}
	write(t, file, string(b))
	return o, readMCPJSON(file, Antigravity)["test"]
}

func TestAntigravityMCPProbeHTTP(t *testing.T) {
	pool := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer test-token" || r.URL.Path == "/auth" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		if r.Method == http.MethodDelete {
			if r.Header.Get("Mcp-Session-Id") != "test-session" || r.Header.Get("MCP-Protocol-Version") != "2025-06-18" {
				t.Error("invalid session teardown")
			}
			w.WriteHeader(http.StatusNoContent)
			return
		}
		b, _ := io.ReadAll(r.Body)
		if r.Method != http.MethodPost || string(b) != mcpInitialize {
			t.Error("probe did not send initialize")
		}
		if r.URL.Path == "/redirect" {
			http.Redirect(w, r, "/json", http.StatusTemporaryRedirect)
			return
		}
		w.Header().Set("Mcp-Session-Id", "test-session")
		if r.URL.Path == "/sse" {
			w.Header().Set("Content-Type", "text/event-stream")
			io.WriteString(w, ": keep-alive\n\ndata: "+testMCPInitReply+"\n\n")
			return
		}
		w.Header().Set("Content-Type", "application/json")
		io.WriteString(w, testMCPInitReply)
	}))
	defer pool.Close()
	for path, want := range map[string]MCPProbeResult{"/json": MCPProbeReady, "/sse": MCPProbeReady, "/auth": MCPProbeAuth, "/redirect": MCPProbeFailed} {
		t.Run(path, func(t *testing.T) {
			o, def := antigravityProbeFixture(t, mcpJSONEntry{ServerURL: pool.URL + path, Headers: map[string]string{"Authorization": "Bearer test-token"}})
			if got := ProbeAntigravityMCP(t.Context(), o, "test", def); got != want {
				t.Fatalf("probe = %q, want %q", got, want)
			}
		})
	}
}

func TestAntigravityMCPProbeStdio(t *testing.T) {
	body := `read -r request
case "$request" in *'"method":"initialize"'*) ;; *) exit 2 ;; esac
test "$PROBE_TOKEN" = fixture || exit 2
test -f fixture || exit 2
printf '%s\n' '` + testMCPInitReply + `'`
	dir := t.TempDir()
	write(t, filepath.Join(dir, "fixture"), "")
	o, def := antigravityProbeFixture(t, mcpJSONEntry{Command: "/bin/sh", Args: []string{"-c", body}, Cwd: dir, Env: map[string]string{"PROBE_TOKEN": "fixture"}})
	if got := ProbeAntigravityMCP(t.Context(), o, "test", def); got != MCPProbeReady {
		t.Fatalf("stdio probe = %q", got)
	}
	if err := os.WriteFile(def.File, []byte(`{"mcpServers":{"test":{"command":"/bin/false"}}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if got := ProbeAntigravityMCP(t.Context(), o, "test", def); got != MCPProbeChanged {
		t.Fatalf("stale stdio probe = %q", got)
	}
	ctx, cancel := context.WithTimeout(t.Context(), 250*time.Millisecond)
	defer cancel()
	o, blocked := antigravityProbeFixture(t, mcpJSONEntry{Command: "/bin/sh", Args: []string{"-c", "read -r request; exec /bin/sleep 10"}})
	if got := ProbeAntigravityMCP(ctx, o, "test", blocked); got != MCPProbeTimeout {
		t.Fatalf("blocked stdio probe = %q", got)
	}
	if got := readMCPInitialize(strings.NewReader(`{"jsonrpc":"2.0","id":1,"result":{}}`), false); got != "" {
		t.Fatalf("incomplete reply reported protocol version %q", got)
	}
}

func TestAntigravityMCPProbeRechecksPlugin(t *testing.T) {
	o := MCPOptions{Home: t.TempDir()}
	cfg := filepath.Join(o.Home, ".gemini", "config")
	plug := filepath.Join(cfg, "plugins", "fixture")
	write(t, filepath.Join(plug, "plugin.json"), `{}`)
	write(t, filepath.Join(plug, "mcp_config.json"), `{"mcpServers":{"server":{"command":"/bin/false"}}}`)
	cat := ScanMCP(t.Context(), o)
	if len(cat.Servers) != 1 || cat.Servers[0].Agents[Antigravity].State != On {
		t.Fatalf("expected an enabled fixture plugin, got %+v", cat.Servers)
	}
	server := cat.Servers[0]
	def := server.Defs[0]
	write(t, filepath.Join(cfg, "config.json"), `{"plugins":{"fixture":{"enabled":false}}}`)
	if got := ProbeAntigravityMCP(t.Context(), o, server.Name, def); got != MCPProbeChanged {
		t.Fatalf("disabled plugin probe = %q", got)
	}
	write(t, filepath.Join(cfg, "config.json"), `{"plugins":`)
	if got := ProbeAntigravityMCP(t.Context(), o, server.Name, def); got != MCPProbeChanged {
		t.Fatalf("unreadable plugin state probe = %q", got)
	}
	write(t, filepath.Join(cfg, "config.json"), `{}`)
	write(t, filepath.Join(cfg, "mcp_config.json"), `{"mcpServers":{"fixture_server":{"command":"/bin/false"}}}`)
	if got := ProbeAntigravityMCP(t.Context(), o, server.Name, def); got != MCPProbeChanged {
		t.Fatalf("shadowed plugin probe = %q", got)
	}
}
