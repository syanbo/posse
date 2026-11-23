package app

import (
	"os"
	"testing"
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
}
