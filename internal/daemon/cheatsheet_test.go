package daemon

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"posse/internal/cheatsheet"
	"posse/internal/history"
	"posse/internal/rpc"
)

func TestDesktopAntigravityCheatsheetReadsTargetSkills(t *testing.T) {
	d, w := newTestDaemon(t)
	d.cfg.HistoryRoots = &history.Roots{Home: t.TempDir()}
	extra := t.TempDir()
	file := filepath.Join(extra, ".agents", "skills", "target-agy", "SKILL.md")
	if err := os.MkdirAll(filepath.Dir(file), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(file, []byte("---\nname: target-agy\ndescription: Remote skill\n---\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	s, err := d.startTerminal(w, []string{"/bin/sh", "-c", "cat"}, "antigravity", 80, 24)
	if err != nil {
		t.Fatal(err)
	}
	s.mu.Lock()
	s.agyDir, s.agyDirs = w.Path, []string{extra}
	s.mu.Unlock()
	socket := desktopServer(t, d)
	params, err := json.Marshal(map[string]string{"terminal": s.ID, "agent": "antigravity"})
	if err != nil {
		t.Fatal(err)
	}
	var got cheatsheet.TerminalInfo
	if err := rpc.Call(t.Context(), socket, "desktop.call", rpc.DesktopCall{
		Desktop: rpc.DesktopIdentity{ClientID: "viewer", InstanceID: d.instanceID},
		Method:  "terminal.cheatsheet",
		Params:  params,
	}, &got); err != nil {
		t.Fatal(err)
	}
	if len(got.Skills) != 1 || got.Skills[0].Name != "target-agy" {
		t.Fatalf("target skills = %+v", got.Skills)
	}
}
