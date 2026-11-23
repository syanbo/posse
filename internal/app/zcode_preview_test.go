package app

import (
	"os"
	"path/filepath"
	"testing"

	"posse/internal/skills"
)

func TestPreviewZCode(t *testing.T) {
	if os.Getenv("POSSE_PREVIEW_DIR") == "" {
		t.Skip("set POSSE_PREVIEW_DIR to render previews")
	}
	a := previewApp(t)
	a.daemon.Agents = []string{"claude", "codex", "zcode"}
	a.workspaces[0].Terminals[0].Agent = "zcode"
	tt := newPreviewTester(a, false)
	if err := tt.Click("新建终端"); err != nil {
		t.Fatal(err)
	}
	settleMenu(tt)
	if _, ok := tt.Find("ZCode"); !ok {
		t.Fatal("missing ZCode menu entry")
	}
	savePNG(t, tt, "zcode-menu.png")

	a = previewApp(t)
	previewHistory(a, "disk")
	a.history.detail.Session.Agent = "zcode"
	a.history.detail.Context = nil
	savePNG(t, newPreviewTester(a, false), "zcode-history.png")

	a = previewApp(t)
	previewSkillsBoard(a, "")
	t.Setenv("ZCODE_STORAGE_DIR", "")
	home := t.TempDir()
	skillDir := filepath.Join(home, ".zcode", "skills", "review")
	if err := os.MkdirAll(skillDir, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(skillDir, "SKILL.md"), []byte("---\nname: review\ndescription: Review the working tree.\n---\n"), 0600); err != nil {
		t.Fatal(err)
	}
	a.skills.cat = skills.Scan(skills.Options{Home: home})
	a.skills.sel = skillDir
	tt = newPreviewTester(a, false)
	if _, ok := tt.Find("ZCode"); !ok {
		t.Fatal("missing ZCode skills filter")
	}
	savePNG(t, tt, "zcode-skills.png")
}
