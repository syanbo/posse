package cli

import (
	"fmt"
	"os"
	"path/filepath"
	"syscall"

	"posse/internal/agent"
)

// cmdAgentExec 是 $POSSE_HOME/bin 里同名脚本的入口：找到真正的程序，补上 hooks 参数后换成它。
// 不在 posse 终端里、已经在经过脚本启动的 agent 里、或是非交互的用法时原样运行。
func cmdAgentExec(e *env, args []string) error {
	if len(args) > 0 && args[0] == "--" {
		args = args[1:]
	}
	if len(args) == 0 {
		return fmt.Errorf("usage: posse agent exec <command> -- [args...]")
	}
	cmd, rest := args[0], args[1:]
	if len(rest) > 0 && rest[0] == "--" {
		rest = rest[1:]
	}
	real := agent.LookPathAfter(cmd, os.Getenv("PATH"), agent.ShimDir(e.home))
	if real == "" {
		return fmt.Errorf("%s: command not found", cmd)
	}
	argv := rest
	target := agent.ShimTarget(cmd, real)
	if _, ok := agent.Shimmed[cmd]; ok && target != "" && os.Getenv("POSSE_TERMINAL_ID") != "" && os.Getenv(agent.ShimEnv) == "" {
		exe, err := os.Executable()
		if err != nil {
			return err
		}
		if resolved, err := filepath.EvalSymlinks(exe); err == nil {
			exe = resolved
		}
		if argv, err = (agent.Launcher{Exe: exe, Home: e.home}).ManualArgs(target, rest); err != nil {
			// 补不上 hooks 也照常启动，只是 posse 看不到它的状态
			fmt.Fprintf(e.errOut, "posse: %v\n", err)
			argv = rest
		}
	}
	env := append(os.Environ(), agent.ShimEnv+"=1")
	return syscall.Exec(real, append([]string{cmd}, argv...), env)
}
