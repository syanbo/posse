package app

import (
	"os"
	"testing"

	"posse/internal/files"
	"posse/locales"
)

func TestPreviewTargetDirectory(t *testing.T) {
	if os.Getenv("POSSE_PREVIEW_DIR") == "" {
		t.Skip("preview directory required")
	}
	for _, dark := range []bool{false, true} {
		a := previewApp(t)
		r := a.request()
		r.snapshot.Controlled = true
		m := locales.In("zh-Hans")
		a.targetDir = &targetDirectory{
			request: r, title: m.AppAddProjectTitle(), verb: m.AppAddProjectButton(), host: "Office Mac",
			input: "/Users/developer/Projects", path: "/Users/developer/Projects", loaded: true,
			entries: []files.Entry{{Name: "Applications", Dir: true}, {Name: "Library", Dir: true},
				{Name: "Shared", Dir: true, Link: "../Shared"}, {Name: "Workspace", Dir: true}},
		}
		suffix := map[bool]string{false: "light", true: "dark"}[dark]
		savePNG(t, newPreviewTester(a, dark), "target-directory-"+suffix+".png")
	}
}
