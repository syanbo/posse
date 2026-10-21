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
	l := Launcher{Exe: "/Applications/Hive.app/Contents/MacOS/Hive", Home: t.TempDir()}
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
	if json.Unmarshal(b, &manifest) != nil || manifest["hooks"] != "./hooks/hooks.json" || manifest["skills"] != "./skills/" {
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
		if h := hooks.Hooks[ev]; len(h) != 1 || h[0].Command != "'/Applications/Hive.app/Contents/MacOS/Hive' hook cursor "+ev {
			t.Errorf("%s: %+v", ev, h)
		}
	}
	if _, err := os.Stat(filepath.Join(dir, "skills", SkillName, "SKILL.md")); err != nil {
		t.Fatal(err)
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
