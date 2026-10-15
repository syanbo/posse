package cli

import (
	"flag"
	"reflect"
	"testing"
)

func TestParseInterleaved(t *testing.T) {
	fs := flag.NewFlagSet("t", flag.ContinueOnError)
	lines := fs.Int("lines", 0, "")
	screen := fs.Bool("screen", false, "")
	pos, err := parse(fs, []string{"t_1", "--lines", "5", "--screen", "extra", "--", "--not-a-flag", "x"})
	if err != nil {
		t.Fatal(err)
	}
	if *lines != 5 || !*screen {
		t.Fatalf("flags: lines=%d screen=%v", *lines, *screen)
	}
	if want := []string{"t_1", "extra", "--not-a-flag", "x"}; !reflect.DeepEqual(pos, want) {
		t.Fatalf("pos = %q, want %q", pos, want)
	}
}

func TestParseErrors(t *testing.T) {
	fs := flag.NewFlagSet("t", flag.ContinueOnError)
	fs.Int("lines", 0, "")
	if _, err := parse(fs, []string{"--nope"}); err == nil {
		t.Fatal("unknown flag accepted")
	}
	if _, err := parse(fs, []string{"--lines"}); err == nil {
		t.Fatal("missing value accepted")
	}
	if _, err := parse(fs, []string{"--lines=3"}); err != nil {
		t.Fatal(err)
	}
}

// 参数不足的报错用命令表里的用法，和帮助一致。
func TestNeedUsesCommandTable(t *testing.T) {
	err := need(nil, 1, "history show")
	if err == nil || err.Error() != "usage: hive history show <session> [--tail N] [--tools]" {
		t.Fatalf("need = %v", err)
	}
	if err := need([]string{"x"}, 1, "history show"); err != nil {
		t.Fatal(err)
	}
	for _, c := range commands {
		if c.args != "" && c.args[0] == ' ' {
			t.Errorf("%s: args %q", c.name, c.args)
		}
	}
}
