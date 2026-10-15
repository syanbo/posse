package procmem

import (
	"os"
	"os/exec"
	"strconv"
	"strings"
	"testing"
)

func TestFootprint(t *testing.T) {
	if fp := Footprint(os.Getpid()); fp < 1<<20 || fp > 1<<34 {
		t.Fatalf("implausible footprint %d", fp)
	}
}

func TestResidentMatchesPS(t *testing.T) {
	rss := Resident(os.Getpid()) >> 10
	out, err := exec.Command("ps", "-o", "rss=", "-p", strconv.Itoa(os.Getpid())).Output()
	if err != nil {
		t.Skip("ps unavailable:", err)
	}
	kb, _ := strconv.ParseUint(strings.TrimSpace(string(out)), 10, 64)
	// 两次读取之间内存会有小幅变化
	if rss < kb/2 || rss > kb*2 {
		t.Fatalf("Resident %d KB, ps %d KB", rss, kb)
	}
}
