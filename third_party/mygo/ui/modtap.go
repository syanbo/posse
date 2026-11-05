package ui

import "time"

// modTap tells a modifier key tapped on its own, pressed and let go with no
// other key or pointer press between, and counts two taps of the same one in
// a row, as an IDE opens its search on Shift tapped twice. Posse's change.
type modTap struct {
	down    Modifiers // the one modifier held alone now, 0 when this press counts no more
	downAt  time.Time
	last    Modifiers // the modifier of the tap before, waiting for a second
	lastAt  time.Time
	double  Modifiers
	doubles uint64
}

const (
	modTapHold = 350 * time.Millisecond // longest a tap holds the key
	modTapGap  = 450 * time.Millisecond // longest between the two taps
)

func (rt *engine) modTapChange(prev, mods Modifiers) {
	t := &rt.modTap
	now := rt.now()
	switch {
	case prev == 0 && mods != 0 && mods&(mods-1) == 0:
		t.down, t.downAt = mods, now
	case mods == 0 && t.down != 0 && prev == t.down && now.Sub(t.downAt) <= modTapHold:
		if t.last == t.down && now.Sub(t.lastAt) <= modTapGap {
			t.double, t.doubles, t.last = t.down, t.doubles+1, 0
		} else {
			t.last, t.lastAt = t.down, now
		}
		t.down = 0
	default:
		t.down, t.last = 0, 0
	}
}

// modTapCancel forgets the taps so far: a key or a pointer press came between.
func (rt *engine) modTapCancel() { rt.modTap.down, rt.modTap.last = 0, 0 }

// DoubleTap returns the modifier tapped twice on its own last, and how many
// such double taps there have been: a view reads the count each frame and
// acts when it grew.
func (c *context) DoubleTap() (Modifiers, uint64) {
	return c.rt.modTap.double, c.rt.modTap.doubles
}
