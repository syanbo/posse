package procmem

import (
	"reflect"
	"testing"
)

func TestProcParsers(t *testing.T) {
	smaps := []byte("00400000-7fff0000 ---p 00000000 00:00 0 [rollup]\nRss:               12000 kB\nPss:                4096 kB\n")
	if got := parseSmapsRollup(smaps); got != 4096*1024 {
		t.Fatalf("parseSmapsRollup = %d, want %d", got, 4096*1024)
	}
	smapsNoPSS := []byte("Rss: 512 kB\n")
	if got := parseSmapsRollup(smapsNoPSS); got != 512*1024 {
		t.Fatalf("parseSmapsRollup fallback = %d, want %d", got, 512*1024)
	}
	status := []byte("Name:\tposse\nVmPeak:\t100000 kB\nVmRSS:\t   2048 kB\n")
	if got := parseProcStatusRSS(status); got != 2048*1024 {
		t.Fatalf("parseProcStatusRSS = %d, want %d", got, 2048*1024)
	}
	if got := parseProcChildren([]byte("101 202\n303 ")); !reflect.DeepEqual(got, []int{101, 202, 303}) {
		t.Fatalf("parseProcChildren = %v", got)
	}
	if got := parseProcCmdline([]byte("/usr/bin/node\x00--flag\x00hello world\x00")); !reflect.DeepEqual(got, []string{"/usr/bin/node", "--flag", "hello world"}) {
		t.Fatalf("parseProcCmdline = %v", got)
	}
	if got := parseProcCmdline([]byte("prog\x00\x00")); !reflect.DeepEqual(got, []string{"prog", ""}) {
		t.Fatalf("parseProcCmdline empty trailing arg = %v", got)
	}
	if got := parseProcKB("1024 MB"); got != 0 {
		t.Fatalf("parseProcKB accepted non-kB unit: %d", got)
	}
	if got := parseProcKB("18446744073709551615 kB"); got != 0 {
		t.Fatalf("parseProcKB did not guard overflow: %d", got)
	}
	if got := parseProcExeLink("/home/user/.posse/server/versions/v1/posse (deleted)"); got != "/home/user/.posse/server/versions/v1/posse" {
		t.Fatalf("parseProcExeLink = %q", got)
	}
}
