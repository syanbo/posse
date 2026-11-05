//go:build darwin

package desk

import (
	"encoding/binary"
	"math"
	"os"
	"path/filepath"
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

// 时长按值返回 24 字节的 CMTime：x86_64 上走错消息入口会崩，用 GOARCH=amd64 在 Rosetta 下也跑一遍。
func TestMediaDuration(t *testing.T) {
	mac, err := loadMac()
	if err != nil {
		t.Fatal(err)
	}
	// 1 秒 8 kHz 单声道 8 位静音 WAV
	const rate = 8000
	wav := make([]byte, 44+rate)
	copy(wav, "RIFF")
	binary.LittleEndian.PutUint32(wav[4:], uint32(36+rate))
	copy(wav[8:], "WAVEfmt ")
	binary.LittleEndian.PutUint32(wav[16:], 16)
	binary.LittleEndian.PutUint16(wav[20:], 1)
	binary.LittleEndian.PutUint16(wav[22:], 1)
	binary.LittleEndian.PutUint32(wav[24:], rate)
	binary.LittleEndian.PutUint32(wav[28:], rate)
	binary.LittleEndian.PutUint16(wav[32:], 1)
	binary.LittleEndian.PutUint16(wav[34:], 8)
	copy(wav[36:], "data")
	binary.LittleEndian.PutUint32(wav[40:], rate)
	for i := 44; i < len(wav); i++ {
		wav[i] = 0x80
	}
	path := filepath.Join(t.TempDir(), "silence.wav")
	if err := os.WriteFile(path, wav, 0o644); err != nil {
		t.Fatal(err)
	}
	mac.withPool(func() {
		d := mac.mediaDuration(mac.fileURL(mac.clsURL, mac.sel("fileURLWithPath:"), mac.string(path)))
		if d.Timescale <= 0 {
			t.Fatalf("duration %+v", d)
		}
		if s := float64(d.Value) / float64(d.Timescale); math.Abs(s-1) > 0.01 {
			t.Fatalf("duration %.3fs, want 1s", s)
		}
	})
}
