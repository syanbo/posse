package app

import (
	"os"
	"testing"
)

func TestLocalPasteImageDiscarded(t *testing.T) {
	root := t.TempDir()
	t.Setenv("TMPDIR", root)
	a := &app{}
	v, conn := recTerminal(t, a)
	a.pasteImage(v, clipboardTestPNG(t))
	waitFileUpdate(t, a)
	entries, err := os.ReadDir(root)
	if err != nil || len(entries) != 1 {
		t.Fatalf("image was not saved before UI callback: %v %v", entries, err)
	}
	a.panes[0] = nil
	a.runPending()
	entries, err = os.ReadDir(root)
	if err != nil || len(entries) != 0 {
		t.Fatalf("discarded local image was not removed: %v %v", entries, err)
	}
	select {
	case <-conn.in:
		t.Fatal("closed terminal received a late path")
	default:
	}
}
