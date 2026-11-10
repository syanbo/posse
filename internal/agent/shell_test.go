package agent

import (
	"bytes"
	"context"
	"errors"
	"os"
	"os/exec"
	"slices"
	"strings"
	"testing"
	"time"
)

// 一键启动、恢复会话、手机带命令开终端都经 ShellArgv：本机有的 shell 逐个真跑一遍，
// 命令要执行（带单引号也行），跑完换回用户的 shell（stdin 是空的，它读到 EOF 就退出）。
func TestShellArgvRuns(t *testing.T) {
	cmd := `printf '%s\n' ` + Quote("posse-ok it's")
	for _, sh := range []string{"/bin/zsh", "/bin/bash", "/opt/homebrew/bin/bash", "/bin/dash", "/bin/ksh", "/bin/sh", "/bin/tcsh", "/bin/csh", "/opt/homebrew/bin/fish", "/usr/local/bin/fish"} {
		if _, err := os.Stat(sh); err != nil {
			continue
		}
		t.Run(sh, func(t *testing.T) {
			t.Setenv("SHELL", sh)
			t.Setenv("POSSE_ZDOTDIR", "")
			t.Setenv("HOME", t.TempDir())
			argv := ShellArgv(cmd)
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			c := exec.CommandContext(ctx, argv[0], argv[1:]...)
			c.Stdin = strings.NewReader("")
			var out bytes.Buffer
			c.Stdout, c.Stderr = &out, &out
			err := c.Run()
			if !strings.Contains(out.String(), "posse-ok it's") {
				t.Fatalf("argv %q: command did not run (err %v):\n%s", argv, err, out.String())
			}
			if strings.Contains(out.String(), "syntax error") || strings.Contains(out.String(), "Syntax Error") {
				t.Fatalf("argv %q: syntax error:\n%s", argv, out.String())
			}
		})
	}
}

// 本机没装的 shell 只核对交给谁跑：fish 读完自己的配置再交给 /bin/sh，nushell 这类直接用 /bin/sh。
func TestShellArgvFamilies(t *testing.T) {
	for _, tc := range []struct{ sh, first string }{
		{"/opt/homebrew/bin/fish", "/opt/homebrew/bin/fish"},
		{"/opt/homebrew/bin/nu", "/bin/sh"},
		{"/usr/local/bin/pwsh", "/bin/sh"},
		{"/bin/zsh", "/bin/zsh"},
	} {
		t.Setenv("SHELL", tc.sh)
		argv := ShellArgv("claude")
		if argv[0] != tc.first {
			t.Fatalf("%s: argv %q", tc.sh, argv)
		}
		if tc.first != tc.sh && !slices.Contains(argv, "-c") {
			t.Fatalf("%s: argv %q", tc.sh, argv)
		}
	}
}

// 跑技能命令（npx skills）用的 ShellCommand：本机有的 shell 逐个真跑，命令要执行，退出码就是命令的。
func TestShellCommandRuns(t *testing.T) {
	for _, sh := range []string{"/bin/zsh", "/bin/bash", "/bin/dash", "/bin/ksh", "/bin/tcsh", "/bin/csh", "/opt/homebrew/bin/fish"} {
		if _, err := os.Stat(sh); err != nil {
			continue
		}
		t.Run(sh, func(t *testing.T) {
			t.Setenv("SHELL", sh)
			t.Setenv("HOME", t.TempDir())
			argv := ShellCommand(`printf '%s\n' 'it'"'"'s ok'; exit 7`)
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			c := exec.CommandContext(ctx, argv[0], argv[1:]...)
			c.Stdin = strings.NewReader("")
			out, err := c.CombinedOutput()
			if !strings.Contains(string(out), "it's ok") {
				t.Fatalf("argv %q: %s", argv, out)
			}
			var ee *exec.ExitError
			if !errors.As(err, &ee) || ee.ExitCode() != 7 {
				t.Fatalf("argv %q: exit %v", argv, err)
			}
		})
	}
}
