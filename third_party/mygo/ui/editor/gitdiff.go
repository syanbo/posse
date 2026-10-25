package editor

import (
	"slices"

	"github.com/egoist/mygo/internal/diff"
)

// gitDiff marks an editor's changes against its DiffBase in the
// gutter, as Zed marks a file's changes against Git: a bar for lines
// added or modified, a wedge where lines were deleted.
type gitDiff struct {
	d diffModel
}

// syncGitDiff follows the DiffBase.
func (ed *editor) syncGitDiff() {
	base := ed.o.diffBase
	if ed.o.decor != nil {
		base = nil
	}
	if base == nil {
		ed.gitDiff = nil
		return
	}
	if ed.gitDiff == nil {
		ed.gitDiff = &gitDiff{}
	}
	ed.gitDiff.d.set(base, ed.buf())
	ed.gitDiff.d.update()
}

// diffModel returns the diff the editor's gutter and hunk commands use:
// its DiffBase's, or the diff viewer's it shows a side of.
func (ed *editor) diffModel() (*diffModel, bool) {
	if ed.gitDiff != nil {
		return &ed.gitDiff.d, false
	}
	if dv, ok := ed.o.decor.(*diffSide); ok && dv.d != nil {
		return &dv.d.model, dv.old
	}
	return nil, false
}

// status returns how the gutter marks line against the DiffBase:
// 1 added, 2 modified, 3 deleted above it, 0 nothing.
func (g *gitDiff) status(line int) int {
	i := g.d.hunkAtNew(line)
	if i < 0 {
		return 0
	}
	h := g.d.hunks[i]
	switch {
	case h.B0 == h.B1:
		return 3
	case h.A0 == h.A1:
		return 1
	}
	return 2
}

// gotoHunk moves the caret to the next hunk after it (dir 1) or the one
// before.
func (ed *editor) gotoHunk(dir int) bool {
	d, old := ed.diffModel()
	if d == nil || len(d.hunks) == 0 {
		return false
	}
	r := &ed.view.text
	line := r.LineOf(ed.primary().head)
	start := func(h diff.Hunk) int {
		if old {
			return h.A0
		}
		return h.B0
	}
	var target = -1
	if dir > 0 {
		for _, h := range d.hunks {
			if start(h) > line {
				target = start(h)
				break
			}
		}
		if target < 0 {
			target = start(d.hunks[0])
		}
	} else {
		for i := len(d.hunks) - 1; i >= 0; i-- {
			if s := start(d.hunks[i]); s < line {
				target = s
				break
			}
		}
		if target < 0 {
			target = start(d.hunks[len(d.hunks)-1])
		}
	}
	target = min(target, r.LineCount()-1)
	off := r.LineStart(target)
	ed.setSels([]sel{ed.newSel(off, off)}, revealCenter)
	return true
}

// revertHunks puts back the old lines of the hunks the selections touch.
func (ed *editor) revertHunks() bool {
	d, old := ed.diffModel()
	if d == nil || old || ed.o.readOnly {
		return false
	}
	var idx []int
	for _, sp := range ed.selLines() {
		for _, i := range d.hunksTouching(sp.from, sp.to) {
			if !slices.Contains(idx, i) {
				idx = append(idx, i)
			}
		}
	}
	if len(idx) == 0 {
		return false
	}
	edits := make([]Edit, 0, len(idx))
	for _, i := range idx {
		edits = append(edits, hunkEdit(d.hunks[i], d.oldText, d.newText))
	}
	return ed.apply(edits, editOther, nil)
}

// changeBarX is where the change bars are, from the editor's left
// (Posse: shared by painting and OnChangeClick).
func (ed *editor) changeBarX() float32 {
	x := ed.gutterW - 4 - 3
	if ed.o.folding && ed.o.decor == nil {
		x -= ed.view.lineHeight * 0.8
	}
	return x
}

// BaseChange is a change against the DiffBase (Posse): lines OldStart up to
// OldEnd of the base became lines NewStart up to NewEnd of the buffer,
// one of the ranges empty where lines were only added or deleted. Old is
// the base's lines.
type BaseChange struct {
	OldStart, OldEnd, NewStart, NewEnd int
	Old                                string
}

// Changes returns the changes against the DiffBase, in order (Posse).
func (st *State) Changes() []BaseChange {
	if st.ed.gitDiff == nil {
		return nil
	}
	d := &st.ed.gitDiff.d
	out := make([]BaseChange, len(d.hunks))
	for i, h := range d.hunks {
		ch := BaseChange{OldStart: h.A0, OldEnd: h.A1, NewStart: h.B0, NewEnd: h.B1}
		if h.A1 > h.A0 {
			ch.Old = d.oldText.Slice(d.oldText.LineStart(h.A0), d.oldText.LineEnd(h.A1-1))
		}
		out[i] = ch
	}
	return out
}

// RevertChange puts back the base's lines of change i of Changes, as an
// edit Undo takes back (Posse).
func (st *State) RevertChange(i int) bool {
	if st.ed.gitDiff == nil || st.ed.o.readOnly {
		return false
	}
	d := &st.ed.gitDiff.d
	if i < 0 || i >= len(d.hunks) {
		return false
	}
	return st.ed.apply([]Edit{hunkEdit(d.hunks[i], d.oldText, d.newText)}, editOther, nil)
}

// LineBottom returns the bottom of line, relative to the editor's top
// as it scrolls now (Posse).
func (st *State) LineBottom(line int) float32 {
	h := &st.ed.view.heights
	n := st.ed.view.text.LineCount()
	if n == 0 {
		return 0
	}
	line = max(0, min(line, n-1))
	return float32(float64(h.geometry(line).below) - st.ed.scrollY)
}
