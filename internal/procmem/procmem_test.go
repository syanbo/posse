package procmem

import (
	"os"
	"os/exec"
	"strconv"
	"strings"
	"testing"
	"time"
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

// 子进程的内存算进进程树。
func TestTreeFootprint(t *testing.T) {
	cmd := exec.Command("/bin/sh", "-c", "sleep 30 & wait")
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	defer cmd.Process.Kill()
	var kids []int
	for range 50 {
		if kids = Children(cmd.Process.Pid); len(kids) > 0 {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if len(kids) == 0 {
		t.Fatal("child of sh not listed")
	}
	self, tree := Footprint(cmd.Process.Pid), TreeFootprint(cmd.Process.Pid)
	if self == 0 || tree <= self {
		t.Fatalf("tree %d should exceed sh alone %d", tree, self)
	}
	for _, k := range kids {
		exec.Command("kill", strconv.Itoa(k)).Run()
	}
}

func TestArgsAndName(t *testing.T) {
	cmd := exec.Command("/bin/sleep", "30")
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	defer cmd.Process.Kill()
	if args := Args(cmd.Process.Pid); len(args) != 2 || args[1] != "30" {
		t.Fatalf("args %q", args)
	}
	for in, want := range map[string]string{
		"npm exec @sentry/mcp-server@0.42.0":                        "@sentry/mcp-server",
		"node /Users/x/.npm/_npx/3d5/node_modules/.bin/sentry-mcp":  "sentry-mcp",
		"/usr/bin/python3 -I /Users/x/.local/bin/codex-cu-mcp":      "codex-cu-mcp",
		"/Applications/iPhoneUse.app/Contents/MacOS/iphone-use-mcp": "iphone-use-mcp",
		"claude --settings /x/hooks.json":                           "claude",
	} {
		if got := Name(strings.Fields(in)); got != want {
			t.Errorf("Name(%q) = %q, want %q", in, got, want)
		}
	}
	// 改写过的进程标题：整条命令是一个参数
	if got := Name([]string{"npm exec @mobilenext/mobile-mcp@latest", "", "", ""}); got != "@mobilenext/mobile-mcp" {
		t.Errorf("retitled npm: %q", got)
	}
}
