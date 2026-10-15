package procmem

import (
	"os"
	"testing"
)

func TestFootprint(t *testing.T) {
	if fp := Footprint(os.Getpid()); fp < 1<<20 || fp > 1<<34 {
		t.Fatalf("implausible footprint %d", fp)
	}
}
