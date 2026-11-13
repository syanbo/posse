package gitx

import (
	"maps"
	"path/filepath"
	"strings"
	"testing"
)

func TestTreeMarks(t *testing.T) {
	root := filepath.Join(t.TempDir(), "repo")
	entries := []Entry{
		{Path: "src/am.go", X: 'A', Y: 'M'},
		{Path: "src/added.go", X: 'A', Y: '.'},
		{Path: "new/file.go", Untracked: true},
		{Path: "conflict.go", X: 'A', Y: 'A', Conflict: true},
		{Path: "deleted/deep/gone.go", X: '.', Y: 'D'},
		{Path: "renamed.go", X: 'R', Y: '.'},
	}
	got := TreeMarks(root, entries, nil)
	want := map[string]byte{}
	for p, mark := range map[string]byte{
		"src/am.go": 'M', "src/added.go": 'A', "src": 'M',
		"new/file.go": '?', "new": 'M', "conflict.go": 'U',
		"deleted/deep/gone.go": 'M', "deleted/deep": 'M', "deleted": 'M', "renamed.go": 'M',
	} {
		want[filepath.Join(root, p)] = mark
	}
	if !maps.Equal(got, want) {
		t.Fatalf("marks = %v, want %v", got, want)
	}
	if StatusLetter(entries[0]) != 'M' || StatusLetter(entries[4]) != 'D' || StatusLetter(entries[5]) != 'R' {
		t.Fatal("status letters must retain worktree priority and non-tree states")
	}
}

func TestMergeTreeMarks(t *testing.T) {
	ws := filepath.Join(t.TempDir(), "ws")
	outer := filepath.Join(ws, "group", "outer")
	inner := filepath.Join(outer, "inner")
	other := filepath.Join(ws, "other")
	outerMarks := TreeMarks(outer, []Entry{{Path: "inner/added", X: '.', Y: 'M'}}, nil)
	innerMarks := TreeMarks(inner, []Entry{{Path: "added", X: 'A', Y: '.'}}, nil)
	otherMarks := TreeMarks(other, []Entry{{Path: "gone/file", X: '.', Y: 'D'}}, nil)
	for _, reverse := range []bool{false, true} {
		got := map[string]byte{}
		if reverse {
			MergeTreeMarks(got, ws, inner, innerMarks)
			MergeTreeMarks(got, ws, outer, outerMarks)
		} else {
			MergeTreeMarks(got, ws, outer, outerMarks)
			MergeTreeMarks(got, ws, inner, innerMarks)
		}
		MergeTreeMarks(got, ws, other, otherMarks)
		if got[filepath.Join(inner, "added")] != 'A' || got[ws] != 0 {
			t.Fatalf("reverse=%v marks=%v", reverse, got)
		}
		for _, dir := range []string{outer, inner, other, filepath.Dir(outer), filepath.Join(other, "gone")} {
			if got[dir] != 'M' {
				t.Fatalf("missing dirty directory %s: %v", dir, got)
			}
		}
	}
}

func TestTreeMarksShownPath(t *testing.T) {
	root := "/real/long/repository"
	realSub := root + "/src"
	shown := func(p string) string {
		if p == realSub || strings.HasPrefix(p, realSub+"/") {
			return "/ws" + strings.TrimPrefix(p, realSub)
		}
		return p
	}
	got := TreeMarks(root, []Entry{{Path: "src/deep/added", X: 'A', Y: '.'}}, shown)
	if got["/ws/deep/added"] != 'A' || got["/ws/deep"] != 'M' || got[root] != 0 {
		t.Fatalf("shown marks %v", got)
	}
}
