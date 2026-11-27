package agent

import (
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

func TestCursorPlugin(t *testing.T) {
	l := Launcher{Exe: "/Applications/Posse.app/Contents/MacOS/Posse", Home: t.TempDir()}
	SetPermissionMode(l.Home, PermissionAsk)
	r, err := l.Launch("cursor", Model{}, "do it")
	if err != nil {
		t.Fatal(err)
	}
	dir := filepath.Join(l.Home, "cursor-plugin")
	if cmd := r.Argv[len(r.Argv)-1]; !strings.HasPrefix(cmd, "cursor-agent --plugin-dir '"+dir+"' 'do it'; if [ ") {
		t.Fatalf("launch: %q", cmd)
	}
	var manifest map[string]string
	b, _ := os.ReadFile(filepath.Join(dir, ".cursor-plugin", "plugin.json"))
	if json.Unmarshal(b, &manifest) != nil || manifest["hooks"] != "./hooks/hooks.json" || manifest["skills"] != "" {
		t.Fatalf("manifest %s", b)
	}
	var hooks struct {
		Version int
		Hooks   map[string][]struct{ Command string }
	}
	b, _ = os.ReadFile(filepath.Join(dir, "hooks", "hooks.json"))
	if err := json.Unmarshal(b, &hooks); err != nil || hooks.Version != 1 {
		t.Fatalf("hooks %s %v", b, err)
	}
	for _, ev := range cursorEvents {
		if h := hooks.Hooks[ev]; len(h) != 1 || h[0].Command != "'/Applications/Posse.app/Contents/MacOS/Posse' hook cursor "+ev {
			t.Errorf("%s: %+v", ev, h)
		}
	}
	// 工具事件只订阅做完时的，免得每条 shell 命令拉起三次 posse
	for _, ev := range []string{"preToolUse", "afterShellExecution"} {
		if _, ok := hooks.Hooks[ev]; ok {
			t.Errorf("subscribed to %s", ev)
		}
	}
	if _, err := os.Stat(filepath.Join(dir, "skills")); err == nil {
		t.Fatal("skills still in the cursor plugin")
	}
	if !slices.Contains(Known, "cursor") {
		t.Fatal("cursor not launchable")
	}
}

func TestCursorEvents(t *testing.T) {
	payload := json.RawMessage(`{"session_id":"c1","conversation_id":"c1","hook_event_name":"sessionStart","transcript_path":null}`)
	for ev, want := range map[string]string{
		"beforeSubmitPrompt": StatusWorking, "preToolUse": StatusWorking,
		"afterShellExecution": StatusWorking, "stop": StatusIdle, "sessionEnd": StatusExited,
	} {
		st, ok := MapEvent("cursor", ev, payload)
		if !ok || st.Status != want || st.SessionID != "c1" {
			t.Errorf("%s: %+v %v", ev, st, ok)
		}
	}
	for _, ev := range []string{"sessionStart", "afterAgentResponse"} {
		if _, ok := MapEvent("cursor", ev, payload); ok {
			t.Errorf("%s should not change state", ev)
		}
	}
}

func TestCursorIdlePrompt(t *testing.T) {
	cases := []struct {
		name   string
		screen []string
		want   bool
	}{
		{
			name: "standard follow-up prompt",
			screen: []string{
				"已回复完成。",
				"",
				"  → Add a follow-up",
				"",
				"  Auto | ctx 7% 17.4k/256k",
				"  /Users/me/repo · Session",
			},
			want: true,
		},
		{
			name: "bordered follow-up with plan hint",
			screen: []string{
				"┌──────────────────────────────┐",
				"│ → Add a follow-up — /plan to review and build │",
				"└──────────────────────────────┘",
			},
			want: true,
		},
		{
			name: "empty session prompt",
			screen: []string{
				"  → Plan, search, build anything",
				"  Auto | ctx 0%",
			},
			want: true,
		},
		{
			name: "generating with ctrl+c to stop",
			screen: []string{
				"  → Add a follow-up                              ctrl+c to stop",
				"  Auto | ctx 7% 17.4k/256k",
			},
			want: false,
		},
		{
			name: "waiting for approval decision",
			screen: []string{
				"  → Waiting for decision (y/n/p)...",
				"  Auto | ctx 7%",
			},
			want: false,
		},
		{
			name: "confirmation dialog alongside prompt",
			screen: []string{
				"  → Add a follow-up",
				"  y to confirm · n or Esc to cancel",
			},
			want: false,
		},
		{
			name: "user typing draft replaces placeholder",
			screen: []string{
				"  → 帮我看看这个报错",
				"  Auto | ctx 7%",
			},
			want: false,
		},
		{
			name: "body text mentioning Add a follow-up without prompt arrow",
			screen: []string{
				"  终端停在 Add a follow-up 输入框",
				"  Running shell command...",
			},
			want: false,
		},
		{
			name: "prompt scrolled far above bottom 12 lines",
			screen: []string{
				"  → Add a follow-up",
				"1", "2", "3", "4", "5", "6", "7", "8", "9", "10", "11", "12", "13",
			},
			want: false,
		},
	}
	for _, tc := range cases {
		if got := CursorIdlePrompt(tc.screen); got != tc.want {
			t.Errorf("%s: CursorIdlePrompt() = %v, want %v", tc.name, got, tc.want)
		}
	}
}
