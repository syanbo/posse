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
	if !strings.HasPrefix(cmd, "agy --model 'model name' --effort 'high' --dangerously-skip-permissions") ||
		!strings.Contains(cmd, " -i "+Quote("do 'this'\nnext")) {
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

func TestAntigravityStatusLineWrapsExistingCommand(t *testing.T) {
	l := Launcher{Exe: "/opt/My Posse/bin/posse", Home: t.TempDir()}
	args := []string{"--mode=plan", "--settings", `{"statusLine":{"command":"printf 'custom'","padding":2},"model":"x"}`}
	got := l.manualAntigravityArgs(args)
	if len(got) != len(args) || got[0] != args[0] || got[1] != args[1] ||
		!strings.Contains(got[2], `"model":"x"`) || !strings.Contains(got[2], `"padding":2`) ||
		!strings.Contains(got[2], Quote(l.Exe)+" statusline antigravity -- ") || strings.Contains(got[2], "stack_with_default") {
		t.Fatalf("wrapped settings: %q", got)
	}
	if twice := l.manualAntigravityArgs(got); !slices.Equal(twice, got) {
		t.Fatalf("shim wrapped the same status line twice: %q", twice)
	}
	disabled := []string{"--mode=plan", "--settings", `{"statusLine":{"command":"printf custom","enabled":false}}`}
	if got := l.manualAntigravityArgs(disabled); !slices.Equal(got, disabled) {
		t.Fatalf("modified disabled status line: %q", got)
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
		{[]string{"-i", "--sandbox"}, true},
		{[]string{"--settings", `{"toolPermission":"strict"}`}, false},
		{[]string{"-settings={\"agentMode\":\"auto\"}"}, false},
		{[]string{"--settings", "preferences.json"}, false},
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

func TestAntigravityAskPermissions(t *testing.T) {
	l := Launcher{Home: t.TempDir()}
	if err := SetPermissionMode(l.Home, PermissionAsk); err != nil {
		t.Fatal(err)
	}
	launch, err := l.Launch("antigravity", Model{Worker: true}, "initial prompt")
	if err != nil {
		t.Fatal(err)
	}
	resume, err := l.Resume("antigravity", "previous", "")
	if err != nil {
		t.Fatal(err)
	}
	for _, command := range []Launch{launch, resume} {
		cmd := command.Argv[len(command.Argv)-1]
		if !strings.Contains(cmd, " --settings "+Quote(antigravityStrictSettings)) || strings.Contains(cmd, "--dangerously-skip-permissions") {
			t.Errorf("ask mode: %s", cmd)
		}
	}
	for _, c := range []struct{ args, want []string }{
		{nil, []string{"--settings", antigravityStrictSettings}},
		{[]string{"-i", "--mode"}, []string{"--settings", antigravityStrictSettings, "-i", "--mode"}},
		{[]string{`-settings={"notifications":true}`}, []string{`-settings={"notifications":true,"toolPermission":"strict"}`}},
		{[]string{"--settings", `{"toolPermission":"always-proceed"}`, "--settings", `{}`},
			[]string{"--settings", `{"toolPermission":"always-proceed"}`, "--settings", antigravityStrictSettings}},
	} {
		got, err := l.ManualArgs("agy", c.args)
		if err != nil || !slices.Equal(got, c.want) {
			t.Errorf("%q -> %q, %v; want %q", c.args, got, err, c.want)
		}
	}
	for _, args := range [][]string{
		{"--mode=auto"}, {"-sandbox"}, {"--dangerously-skip-permissions"},
		{"--settings", `{"toolPermission":"always-proceed"}`},
		{"--settings", `{"enableTerminalSandbox":true}`}, {"--settings", "preferences.json"},
		{"-p", "/usage"}, {"--help"}, {"acp"},
	} {
		got, err := l.ManualArgs("agy", args)
		if err != nil || !slices.Equal(got, args) {
			t.Errorf("%q -> %q, %v; want passthrough", args, got, err)
		}
	}
}

func TestAntigravityAddedDirs(t *testing.T) {
	args := []string{"agy", "-i", "--add-dir=not-an-option", "--add-dir", "../shared", "--add-dir=/work/shared", "--add-dir=/tools", "--", "--add-dir=/ignored"}
	if got, want := AntigravityAddedDirs(args, "/work/repo"), []string{"/work/shared", "/tools"}; !slices.Equal(got, want) {
		t.Fatalf("added dirs = %q, want %q", got, want)
	}
}
