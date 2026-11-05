package ui

import (
	"runtime"
	"testing"
	"weak"
)

// The states of elements a larger frame built go once a smaller frame no
// longer builds them: nothing the engine keeps from frame to frame, as the
// hit list cut short, refers to them still. (Posse)
func TestPrunedStatesCollected(t *testing.T) {
	n := 3000
	tt := NewTester(func(c *Context) {
		Scroll(c).Fill().Children(func() {
			Column(c).Children(func() {
				for range n {
					Box(c).Height(20)
				}
			})
		})
	}, 300, 400)
	tt.Frame()
	if len(tt.rt.states) < n {
		t.Fatalf("states=%d, want at least %d", len(tt.rt.states), n)
	}
	var marked []weak.Pointer[state]
	for _, s := range tt.rt.states {
		marked = append(marked, weak.Make(s))
	}
	n = 10
	tt.Frame()
	tt.Frame()
	runtime.GC()
	runtime.GC()
	kept := map[*state]bool{}
	for _, s := range tt.rt.states {
		kept[s] = true
	}
	for _, s := range tt.rt.free {
		kept[s] = true
	}
	alive := 0
	for _, w := range marked {
		if s := w.Value(); s != nil && !kept[s] {
			alive++
		}
	}
	if alive > 0 {
		t.Fatalf("%d of %d pruned states still reachable after the frame shrank", alive, len(marked))
	}
}
