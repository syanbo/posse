package procmem

import (
	"path/filepath"
	"slices"
	"strings"
)

// TreeFootprint 是 pid 和它所有子孙进程的 phys_footprint 之和（字节）；children 来自 Children。
func TreeFootprint(pid int, children map[int][]int) uint64 {
	var sum uint64
	seen := map[int]bool{}
	stack := []int{pid}
	for len(stack) > 0 {
		p := stack[len(stack)-1]
		stack = stack[:len(stack)-1]
		if seen[p] {
			continue
		}
		seen[p] = true
		sum += Footprint(p)
		stack = append(stack, children[p]...)
	}
	return sum
}

// launchers 是只负责启动别的程序的命令：起名时跳过它们，用后面真正运行的包或脚本。
var launchers = map[string]bool{
	"npm": true, "npx": true, "node": true, "bun": true, "bunx": true, "deno": true, "uv": true, "uvx": true,
	"pnpm": true, "yarn": true, "env": true, "exec": true, "run": true, "-c": true,
}

// Name 给进程起个短名字：argv 里第一个不是启动器、解释器或选项的参数，取最后一段路径，去掉版本号。
func Name(args []string) string {
	// npm 这类程序会改写进程标题：整条命令挤在第一个参数里，后面的参数清成空串
	args = slices.DeleteFunc(slices.Clone(args), func(a string) bool { return a == "" })
	if len(args) == 1 && strings.Contains(args[0], " ") {
		args = strings.Fields(args[0])
	}
	for i, a := range args {
		base := filepath.Base(a)
		if strings.HasPrefix(a, "-") || launchers[base] || strings.HasPrefix(base, "python") || strings.HasPrefix(base, "Python") {
			continue
		}
		if strings.HasPrefix(a, "@") {
			// npm 包名：@scope/name@1.2.3
			if at := strings.LastIndex(a, "@"); at > 0 {
				a = a[:at]
			}
			return a
		}
		if i == 0 || strings.Contains(a, "/") || !strings.Contains(a, " ") {
			return strings.TrimSuffix(strings.TrimSuffix(base, ".js"), ".py")
		}
	}
	if len(args) > 0 {
		return filepath.Base(args[0])
	}
	return ""
}
