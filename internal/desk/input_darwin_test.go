//go:build darwin

package desk

import (
	"math"
	"testing"
)

func TestDisplayBoundsAndMousePoint(t *testing.T) {
	mac, err := loadMac()
	if err != nil {
		t.Fatal(err)
	}
	if mac.displayBounds == nil || mac.cgMouse == nil || mac.cgGetLoc == nil {
		t.Fatal("input symbols missing")
	}
	b := mac.displayBounds(mac.mainDisplay())
	if b.W < 1 || b.H < 1 {
		t.Fatalf("bounds %+v", b)
	}
	x, y := NormPoint(b.X, b.Y, b.W, b.H, 32768, 32768)
	if x < b.X || x > b.X+b.W || y < b.Y || y > b.Y+b.H {
		t.Fatalf("center %v,%v outside %+v", x, y, b)
	}
	mac.withPool(func() {
		ev := mac.cgMouse(0, cgMouseMoved, cgPoint{X: 12, Y: 34}, cgButtonLeft)
		if ev == 0 {
			t.Fatal("mouse event is nil")
		}
		defer mac.cfRelease(ev)
		got := mac.cgGetLoc(ev)
		if math.Abs(got.X-12) > 0.1 || math.Abs(got.Y-34) > 0.1 {
			t.Fatalf("location (%v,%v), want (12,34)", got.X, got.Y)
		}
	})
}

func TestScrollEventCreate(t *testing.T) {
	mac, err := loadMac()
	if err != nil {
		t.Fatal(err)
	}
	if mac.cgScroll == nil || mac.cgSetInt == nil || mac.cgGetInt == nil {
		t.Fatal("scroll symbols missing")
	}
	mac.withPool(func() {
		ev := mac.cgScroll(0, cgScrollPixel, 2, 12, -4, 0)
		if ev == 0 {
			t.Fatal("scroll event is nil")
		}
		defer mac.cfRelease(ev)
		mac.cgSetInt(ev, cgFieldScrollContinuous, 1)
		if got := mac.cgGetInt(ev, cgFieldScrollContinuous); got != 1 {
			t.Fatalf("continuous = %d", got)
		}
	})
}

func TestKeyLayoutLetters(t *testing.T) {
	mac, err := loadMac()
	if err != nil {
		t.Fatal(err)
	}
	var layout map[rune]keyStroke
	mac.withPool(func() { layout = mac.keyLayout() })
	if len(layout) == 0 {
		t.Fatal("keyboard layout empty")
	}
	a, okA := layout['a']
	upper, okU := layout['A']
	if !okA || !okU {
		t.Fatalf("a=%v A=%v", okA, okU)
	}
	if a.code != upper.code || a.shift || !upper.shift {
		t.Fatalf("a=%+v A=%+v", a, upper)
	}
}

func TestAccessibilitySymbol(t *testing.T) {
	mac, err := loadMac()
	if err != nil {
		t.Fatal(err)
	}
	if mac.axTrusted == nil {
		t.Fatal("AXIsProcessTrusted missing")
	}
	var trusted bool
	mac.withPool(func() { trusted = mac.axTrusted() })
	t.Logf("accessibility trusted=%v", trusted)
}
