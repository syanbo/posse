package agent

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestQuoteRoundTrip(t *testing.T) {
	for _, s := range []string{"plain", "it's", `a "b" $HOME; rm -rf /`, "多行\n文本"} {
		out, err := exec.Command("/bin/sh", "-c", "printf %s "+Quote(s)).Output()
		if err != nil {
			t.Fatal(err)
		}
		if string(out) != s {
			t.Fatalf("Quote(%q) round-trips to %q", s, out)
		}
	}
}

// PreToolUse 只为认出向用户提问，按工具名过滤；SubagentStart 用不上，不订阅
func TestClaudeHookMatchers(t *testing.T) {
	l := Launcher{Exe: "/bin/posse", Home: t.TempDir()}
	if _, err := l.claudeSettings(); err != nil {
		t.Fatal(err)
	}
	b, _ := os.ReadFile(filepath.Join(l.Home, "claude-hooks.json"))
	var doc struct {
		Hooks map[string][]struct {
			Matcher string `json:"matcher"`
		} `json:"hooks"`
	}
	if err := json.Unmarshal(b, &doc); err != nil {
		t.Fatal(err)
	}
	if g := doc.Hooks["PreToolUse"]; len(g) != 1 || g[0].Matcher != "AskUserQuestion" {
		t.Errorf("PreToolUse %+v", g)
	}
	if g := doc.Hooks["PostToolUse"]; len(g) != 1 || g[0].Matcher != "*" {
		t.Errorf("PostToolUse %+v", g)
	}
	if _, ok := doc.Hooks["SubagentStart"]; ok {
		t.Error("subscribed to SubagentStart")
	}
}

func TestClaudePermissionTimeout(t *testing.T) {
	l := Launcher{Exe: "/bin/posse", Home: t.TempDir()}
	if _, err := l.claudeSettings(); err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(filepath.Join(l.Home, "claude-hooks.json"))
	if err != nil {
		t.Fatal(err)
	}
	var doc struct {
		Hooks map[string][]struct {
			Hooks []struct {
				Timeout int `json:"timeout"`
			} `json:"hooks"`
		} `json:"hooks"`
	}
	if err := json.Unmarshal(b, &doc); err != nil {
		t.Fatal(err)
	}
	if doc.Hooks["PermissionRequest"][0].Hooks[0].Timeout != 40 {
		t.Fatalf("PermissionRequest timeout %+v", doc.Hooks["PermissionRequest"])
	}
	if doc.Hooks["Stop"][0].Hooks[0].Timeout != 5 {
		t.Fatalf("Stop timeout %+v", doc.Hooks["Stop"])
	}
}

func TestLaunchUnknown(t *testing.T) {
	if _, err := (Launcher{Exe: "/bin/posse", Home: t.TempDir()}).Launch("nope", Model{}, ""); err == nil {
		t.Fatal("unknown agent accepted")
	}
}

// 期望值由 Codex 指纹算法的 Python 复刻（已对照 codex hooks/list 校验）算出
func TestCodexHookHash(t *testing.T) {
	cases := []struct {
		key, matcher, cmd string
		timeout           int
		want              string
	}{
		{"stop", "", `'/bin/posse' hook codex Stop`, 5, "sha256:4d22164ceb95d089a544a2f443783c043da43b7c56e2e7c78b647e8958325730"},
		{"session_end", "", `'/a b/posse' hook codex SessionEnd`, 3, "sha256:5a4bf0b5be1231d3cac40835b755fb7b15375fdee1d494c58d6113fdc0293f2a"},
		// 期望值是 codex 0.166 的 hooks/list 给的 currentHash
		{"pre_tool_use", "request_user_input", `'/bin/posse' hook codex PreToolUse`, 5, "sha256:02414a3a276a94d192e0925918dd4ee02d679c470de061d0d1c78ed9a45f2ef3"},
	}
	for _, c := range cases {
		if got := codexHookHash(c.key, c.matcher, c.cmd, c.timeout); got != c.want {
			t.Errorf("codexHookHash(%s) = %s, want %s", c.key, got, c.want)
		}
	}
}

func TestResume(t *testing.T) {
	l := Launcher{Exe: "/bin/posse", Home: t.TempDir()}
	SetPermissionMode(l.Home, PermissionAsk)
	for agent, want := range map[string]string{
		"claude": "cd '/w/it'\\''s' && claude --settings '" + l.Home + "/claude-hooks.json' --plugin-dir '" + l.Home + "/claude-plugin' --resume 'sid'",
		"grok":   "cd '/w/it'\\''s' && grok --resume 'sid' --rules " + Quote(guideHint),
		"cursor": "cd '/w/it'\\''s' && cursor-agent --plugin-dir '" + l.Home + "/cursor-plugin' --resume 'sid'",
	} {
		r, err := l.Resume(agent, "sid", "/w/it's")
		if err != nil {
			t.Fatal(err)
		}
		if cmd := r.Argv[len(r.Argv)-1]; !strings.HasPrefix(cmd, want+"; if [ ") || r.SessionID != "sid" {
			t.Errorf("%s: %q", agent, cmd)
		}
	}
	r, err := l.Resume("codex", "sid", "")
	if err != nil {
		t.Fatal(err)
	}
	if cmd := r.Argv[len(r.Argv)-1]; !strings.HasPrefix(cmd, "codex '-c'") || !strings.Contains(cmd, " resume 'sid'; if [ ") {
		t.Errorf("codex: %q", cmd)
	}
	if _, err := l.Resume("claude", "", ""); err == nil {
		t.Error("empty session id accepted")
	}
}

func TestClaudeStatusLineKeepsUsers(t *testing.T) {
	user := filepath.Join(t.TempDir(), "settings.json")
	old := claudeUserSettings
	claudeUserSettings = func() string { return user }
	defer func() { claudeUserSettings = old }()
	l := Launcher{Exe: "/bin/posse", Home: t.TempDir()}
	var refresh float64
	statusLine := func() string {
		if _, err := l.claudeSettings(); err != nil {
			t.Fatal(err)
		}
		b, _ := os.ReadFile(filepath.Join(l.Home, "claude-hooks.json"))
		var doc struct {
			StatusLine struct {
				Command         string  `json:"command"`
				RefreshInterval float64 `json:"refreshInterval"`
			} `json:"statusLine"`
		}
		json.Unmarshal(b, &doc)
		refresh = doc.StatusLine.RefreshInterval
		return doc.StatusLine.Command
	}
	// 没有用户设置、用户设置里没有 statusLine：注入
	if got := statusLine(); got != "'/bin/posse' statusline" {
		t.Fatalf("no user settings: %q", got)
	}
	os.WriteFile(user, []byte(`{"model":"opus","statusLine":null}`), 0o600)
	if got := statusLine(); got == "" {
		t.Fatal("null statusLine: not injected")
	}
	// 用户自己配了：照样注入，沿用其余字段，命令由 posse statusline 替它跑
	os.WriteFile(user, []byte(`{"statusLine":{"type":"command","command":"node sl.js","refreshInterval":10}}`), 0o600)
	if got := statusLine(); got != "'/bin/posse' statusline" || refresh != 10 {
		t.Fatalf("user statusLine: %q refresh %v", got, refresh)
	}
	// 设置文件坏了：不注入，免得把用户的状态行弄没
	os.WriteFile(user, []byte(`{"statusLine":`), 0o600)
	if got := statusLine(); got != "" {
		t.Fatalf("broken settings: %q", got)
	}
}
