package app

import (
	"os"
	"testing"

	"posse/internal/history"
	"posse/internal/skills"
)

func TestPreviewAntigravity(t *testing.T) {
	if os.Getenv("POSSE_PREVIEW_DIR") == "" {
		t.Skip("set POSSE_PREVIEW_DIR to render previews")
	}
	for _, dark := range []bool{false, true} {
		suffix := "light"
		if dark {
			suffix = "dark"
		}
		a := previewApp(t)
		a.daemon.Agents = []string{"claude", "codex", "grok", "cursor", "antigravity"}
		a.workspaces[0].Terminals[0].Agent = "antigravity"
		tt := newPreviewTester(a, dark)
		if err := tt.Click("新建终端"); err != nil {
			t.Fatal(err)
		}
		settleMenu(tt)
		if _, ok := tt.Find("Antigravity"); !ok {
			t.Fatal("missing Antigravity menu entry")
		}
		savePNG(t, tt, "antigravity-menu-"+suffix+".png")

		a = previewApp(t)
		previewHistory(a, "disk")
		a.history.detail.Session.Agent = "antigravity"
		a.history.detail.Context = &history.Context{Used: 12345}
		savePNG(t, newPreviewTester(a, dark), "antigravity-history-"+suffix+".png")

		a = previewApp(t)
		previewSkillsBoard(a, "")
		a.skills.cat.Skills[0].Agents[skills.Antigravity] = skills.Status{State: skills.On, Via: homeDir + "/.gemini/config/skills/archify"}
		savePNG(t, newPreviewTester(a, dark), "antigravity-skills-"+suffix+".png")
	}
}
