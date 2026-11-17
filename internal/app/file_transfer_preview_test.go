package app

import (
	"os"
	"testing"
)

func TestPreviewFileTransfer(t *testing.T) {
	if os.Getenv("POSSE_PREVIEW_DIR") == "" {
		t.Skip("preview directory required")
	}
	for _, dark := range []bool{false, true} {
		a := previewApp(t)
		v := previewFiles(t, a, "README.md", "main.go")
		h, _ := requestTestApp(t, "remote")
		takeRequestTestControl(t, h)
		a.host, a.viewID = h.host, "remote"
		v.remote = true
		suffix := map[bool]string{false: "light", true: "dark"}[dark]
		savePNG(t, newPreviewTester(a, dark), "file-transfer-"+suffix+".png")
		v.transfer = &fileTransfer{name: "screenshot.png"}
		savePNG(t, newPreviewTester(a, dark), "file-transfer-running-"+suffix+".png")
		v.transfer = nil
		v.cur.kind = docBinary
		savePNG(t, newPreviewTester(a, dark), "file-open-copy-"+suffix+".png")
	}
}
