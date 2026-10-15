package agent

import (
	"os/exec"
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

func TestLaunchUnknown(t *testing.T) {
	if _, err := (Launcher{Exe: "/bin/hive", Home: t.TempDir()}).Launch("nope", ""); err == nil {
		t.Fatal("unknown agent accepted")
	}
}

// 期望值由 Codex 指纹算法的 Python 复刻（已对照 codex hooks/list 校验）算出
func TestCodexHookHash(t *testing.T) {
	cases := []struct {
		key, cmd string
		timeout  int
		want     string
	}{
		{"stop", `'/bin/hive' hook codex Stop`, 5, "sha256:86dc4d2d195e9dd08c0a07b358c18502cd70869d3dc47d9d12821234a4faf552"},
		{"session_end", `'/a b/hive' hook codex SessionEnd`, 3, "sha256:9f963f855d8b5f90017bac2b01db9553480d1157851453d8e82161bb7abbd6cb"},
	}
	for _, c := range cases {
		if got := codexHookHash(c.key, c.cmd, c.timeout); got != c.want {
			t.Errorf("codexHookHash(%s) = %s, want %s", c.key, got, c.want)
		}
	}
}

func TestResume(t *testing.T) {
	l := Launcher{Exe: "/bin/hive", Home: t.TempDir()}
	for agent, want := range map[string]string{
		"claude": "cd '/w/it'\\''s' && claude --settings '" + l.Home + "/claude-hooks.json' --plugin-dir '" + l.Home + "/claude-plugin' --resume 'sid'",
		"grok":   "cd '/w/it'\\''s' && grok --resume 'sid' --rules " + Quote(guideHint),
		"cursor": "cd '/w/it'\\''s' && cursor-agent --resume 'sid'",
	} {
		r, err := l.Resume(agent, "sid", "/w/it's")
		if err != nil {
			t.Fatal(err)
		}
		if cmd := r.Argv[len(r.Argv)-1]; !strings.HasPrefix(cmd, want+"; exec ") || r.SessionID != "sid" {
			t.Errorf("%s: %q", agent, cmd)
		}
	}
	r, err := l.Resume("codex", "sid", "")
	if err != nil {
		t.Fatal(err)
	}
	if cmd := r.Argv[len(r.Argv)-1]; !strings.HasPrefix(cmd, "codex '-c'") || !strings.Contains(cmd, " resume 'sid'; exec ") {
		t.Errorf("codex: %q", cmd)
	}
	if _, err := l.Resume("claude", "", ""); err == nil {
		t.Error("empty session id accepted")
	}
}
