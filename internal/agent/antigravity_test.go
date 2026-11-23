package agent

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

func TestAntigravityLaunch(t *testing.T) {
	l := Launcher{Exe: "/bin/posse", Home: t.TempDir()}
	launch, err := l.Launch("antigravity", Model{Name: "model name", Effort: "high"}, "do 'this'\nnext")
	if err != nil {
		t.Fatal(err)
	}
	cmd := launch.Argv[len(launch.Argv)-1]
	if !strings.HasPrefix(cmd, "agy --model 'model name' --effort 'high' --dangerously-skip-permissions -i "+Quote("do 'this'\nnext")) {
		t.Fatal(cmd)
	}
	if HasHooks("antigravity") {
		t.Fatal("unverified hooks enabled")
	}
	resume, err := l.Resume("antigravity", "11111111-1111-4111-8111-111111111111", "/a b")
	if err != nil || !strings.Contains(strings.Join(resume.Argv, " "), "cd '/a b' && agy --conversation '") {
		t.Fatal(resume, err)
	}
	entries, _ := os.ReadDir(l.Home)
	if len(entries) != 0 {
		t.Fatal("launch wrote settings", entries)
	}
	if fromProcess("/bin/agy", []string{"agy"}) != "antigravity" {
		t.Fatal("process not recognized")
	}
	if got := SessionFromArgs("antigravity", []string{"agy", "--conversation=" + resume.SessionID}); got != resume.SessionID {
		t.Fatal(got)
	}
	if err := WriteShellFiles(l.Home, l.Exe); err != nil {
		t.Fatal(err)
	}
	b, _ := os.ReadFile(filepath.Join(l.Home, "bin", "agy"))
	if !strings.Contains(string(b), "agent exec agy --") {
		t.Fatal(string(b))
	}
}

func TestAntigravityManualArgs(t *testing.T) {
	l := Launcher{Home: t.TempDir()}
	for _, tc := range []struct {
		args []string
		skip bool
	}{
		{nil, true}, {[]string{"-i", "models"}, true}, {[]string{"--model", "models", "-i", "--help"}, true},
		{[]string{"--settings", `{"model":"x"}`}, true},
		{[]string{"--mode=plan"}, false}, {[]string{"--sandbox"}, false}, {[]string{"--dangerously-skip-permissions"}, false},
		{[]string{"--print=x"}, false}, {[]string{"-p", "prompt"}, false}, {[]string{"--prompt", "prompt"}, false},
		{[]string{"models"}, false}, {[]string{"--model=x", "mcp", "list"}, false}, {[]string{"--", "-i"}, false},
	} {
		got, err := l.ManualArgs("agy", tc.args)
		if err != nil {
			t.Fatal(err)
		}
		want := slices.Clone(tc.args)
		if tc.skip {
			want = append([]string{"--dangerously-skip-permissions"}, want...)
		}
		if !slices.Equal(got, want) {
			t.Errorf("%q -> %q; want %q", tc.args, got, want)
		}
	}
}

func TestAntigravityAddedDirs(t *testing.T) {
	args := []string{"agy", "-i", "--add-dir=not-an-option", "--add-dir", "../shared", "--add-dir=/work/shared", "--add-dir=/tools", "--", "--add-dir=/ignored"}
	if got, want := AntigravityAddedDirs(args, "/work/repo"), []string{"/work/shared", "/tools"}; !slices.Equal(got, want) {
		t.Fatalf("added dirs = %q, want %q", got, want)
	}
}
