package editor

import (
	"github.com/egoist/mygo/internal/diff"
	"github.com/egoist/mygo/internal/rope"
)

// DiffComparison is a comparison of two texts made ahead of showing them,
// as on a goroutine while the UI thread goes on: comparing the texts of a
// file of megabytes takes a fraction of a second or more (Posse).
type DiffComparison struct {
	old, new    string
	hunks       []diff.Hunk
	ignoreSpace bool
}

// CompareTexts compares old and new as a DiffState showing them would,
// whitespace-blind when ignoreSpace is set. It is safe to call from any
// goroutine (Posse).
func CompareTexts(old, new string, ignoreSpace bool) *DiffComparison {
	m := diffModel{ignoreSpace: ignoreSpace}
	o, n := lf(old), lf(new)
	m.full(rope.New(o), rope.New(n), 0, 0)
	return &DiffComparison{old: o, new: n, hunks: m.hunks, ignoreSpace: ignoreSpace}
}

// UseComparison has the next comparison of the whole texts take c's
// hunks rather than compare them again, when Old and New then hold the
// texts c compared and IgnoreWhitespace is as c had it; otherwise c is
// dropped and they are compared as usual (Posse).
func (d *DiffState) UseComparison(c *DiffComparison) { d.model.ahead = c }

// takeAhead adopts the comparison made ahead when it is of these texts.
func (d *diffModel) takeAhead(oldText, newText rope.Text) bool {
	c := d.ahead
	d.ahead = nil
	if c == nil || c.ignoreSpace != d.ignoreSpace ||
		oldText.Len() != len(c.old) || newText.Len() != len(c.new) ||
		oldText.String() != c.old || newText.String() != c.new {
		return false
	}
	d.hunks = c.hunks
	return true
}
