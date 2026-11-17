package editor_test

import (
	"bytes"
	"strconv"
	"sync"
	"testing"

	"github.com/egoist/mygo/ui/editor"
)

func TestPosseMarkSnapshotSaved(t *testing.T) {
	b := editor.NewBuffer("first\r\n")
	b.Edit(editor.Edit{Start: 0, End: 5, Text: "saved"})
	s := b.Snapshot()
	b.Edit(editor.Edit{Start: 0, End: 5, Text: "newer"})
	b.SetLineEnding("\n")
	version := b.Version()
	b.MarkSnapshotSaved(s)
	if b.String() != "newer\n" || b.Version() != version || b.LineEnding() != "\n" {
		t.Fatal("marking a snapshot changed current text, version or line endings")
	}
	if !b.Modified() {
		t.Fatal("edits made after the saved snapshot must remain modified")
	}
	var out bytes.Buffer
	n, err := s.WriteTo(&out)
	if err != nil || out.String() != "saved\r\n" || n != int64(out.Len()) {
		t.Fatalf("snapshot WriteTo = %q, %d, %v", out.String(), n, err)
	}
	if s.Version() >= version || s.String() != "saved\n" {
		t.Fatal("snapshot changed after editing")
	}
	if !b.Undo() || b.String() != "saved\n" || b.Modified() {
		t.Fatal("undo to the saved snapshot must be clean")
	}
	if !b.Undo() || b.String() != "first\n" || !b.Modified() {
		t.Fatal("undo before the saved snapshot must remain available and modified")
	}
	if !b.Redo() || b.Modified() {
		t.Fatal("redo to the saved snapshot must be clean")
	}
	b.MarkSnapshotSaved(s)
	if !b.Redo() || b.String() != "newer\n" || !b.Modified() {
		t.Fatal("marking must preserve redo and its modified state")
	}
	b.Set("saved\n")
	if b.Modified() {
		t.Fatal("equal text at a new version must be clean")
	}
}

func TestPosseMarkSnapshotSavedCompatibility(t *testing.T) {
	b := editor.NewBuffer("initial")
	b.Set("saved")
	b.MarkSnapshotSaved(b.Snapshot())
	if b.Modified() {
		t.Fatal("marking the current snapshot must be clean")
	}
	b.Set("current")
	version := b.Version()
	b.MarkSaved()
	if b.Modified() || b.Version() != version {
		t.Fatal("MarkSaved must still mark current text without changing version")
	}
	if !b.Undo() || !b.Modified() || !b.Redo() || b.Modified() {
		t.Fatal("MarkSaved must preserve undo and redo")
	}
	b.Set("pending")
	b.Undo()
	b.Reset("reset\r\n")
	if b.Modified() || b.CanUndo() || b.CanRedo() || b.String() != "reset\n" || b.LineEnding() != "\r\n" {
		t.Fatal("Reset must replace saved text, history and line endings")
	}
}

func TestPosseMarkSnapshotSavedConcurrent(t *testing.T) {
	b := editor.NewBuffer("initial\r\n")
	snapshots := make(chan editor.Snapshot, 8)
	start := make(chan struct{})
	var wg sync.WaitGroup
	wg.Add(3)
	go func() {
		defer wg.Done()
		<-start
		for i := range 200 {
			b.Set(strconv.Itoa(i) + "\n")
		}
	}()
	go func() {
		defer wg.Done()
		defer close(snapshots)
		<-start
		for range 200 {
			snapshots <- b.Snapshot()
		}
	}()
	go func() {
		defer wg.Done()
		<-start
		for s := range snapshots {
			b.MarkSnapshotSaved(s)
			b.Modified()
			var out bytes.Buffer
			if _, err := s.WriteTo(&out); err != nil || out.String() != s.String()[:s.Len()-1]+"\r\n" {
				t.Errorf("concurrent snapshot WriteTo = %q, %v", out.String(), err)
			}
		}
	}()
	close(start)
	wg.Wait()
	if b.String() != "199\n" || b.Version() != 200 || b.LineEnding() != "\r\n" {
		t.Fatal("concurrent marking changed editing state")
	}
	b.MarkSnapshotSaved(b.Snapshot())
	if b.Modified() {
		t.Fatal("final snapshot must be clean")
	}
}
