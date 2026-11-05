package ui

import (
	"math"
	"testing"
	"time"
)

// followLog is a log that follows its end, as TrackScroll's example: rows
// of 40 DIPs in a 400 DIP window, at its end to start with.
func followLog(t *testing.T, rows *int, st *ScrollState, set func()) *Tester {
	t.Helper()
	tt := NewTester(func(c *Context) {
		if st.Y >= st.MaxY {
			st.Y = math.MaxFloat32
		}
		if set != nil {
			set()
		}
		Scroll(c).Fill().TrackScroll(st).Children(func() {
			Column(c).Children(func() {
				for range *rows {
					Box(c).Height(40)
				}
			})
		})
	}, 300, 400)
	tt.Frame()
	tt.Frame()
	if st.MaxY != float32(*rows*40-400) || st.Y != st.MaxY {
		t.Fatalf("not at the end: Y=%v MaxY=%v", st.Y, st.MaxY)
	}
	return tt
}

// The app asking for the end where the content already is does not take
// over from a wheel notch on its way up, even in the frame of the notch,
// when it has not moved yet. (Posse)
func TestWheelFromFollowedEnd(t *testing.T) {
	rows, st := 50, &ScrollState{}
	tt := followLog(t, &rows, st, nil)
	end := st.MaxY
	tt.Wheel(150, 200, 0, -120)
	tt.Advance(time.Second)
	tt.Frame()
	if st.Y != end-120 {
		t.Fatalf("wheel notch from the end: Y=%v want %v", st.Y, end-120)
	}
}

// Content growing in the frame of such a notch still takes the offset to
// the new end, from which the notch goes on. (Posse)
func TestWheelFromFollowedEndGrows(t *testing.T) {
	rows, st := 50, &ScrollState{}
	tt := followLog(t, &rows, st, nil)
	tt.Wheel(150, 200, 0, -120)
	rows = 60
	tt.Frame()
	if want := float32(60*40 - 400); st.MaxY != want || st.Y != want {
		t.Fatalf("grown content: Y=%v MaxY=%v want both %v", st.Y, st.MaxY, want)
	}
	tt.Advance(time.Second)
	tt.Frame()
	if st.Y != st.MaxY-120 {
		t.Fatalf("notch after growing: Y=%v want %v", st.Y, st.MaxY-120)
	}
}

// The app setting another offset still takes over from the wheel.
func TestAppTakesOverFromWheel(t *testing.T) {
	rows, st := 50, &ScrollState{}
	jump := false
	tt := followLog(t, &rows, st, func() {
		if jump {
			st.Y, jump = 0, false
		}
	})
	tt.Wheel(150, 200, 0, -120)
	jump = true
	tt.Frame()
	tt.Advance(time.Second)
	tt.Frame()
	if st.Y != 0 {
		t.Fatalf("app's offset during a wheel notch: Y=%v want 0", st.Y)
	}
}
