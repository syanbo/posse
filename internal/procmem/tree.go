package procmem

import (
	"context"
	"path/filepath"
	"slices"
	"strings"
	"unicode/utf8"
)

// TreeFootprint 是 pid 和它所有子孙进程的 phys_footprint 之和（字节）。
func TreeFootprint(pid int) uint64 {
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
		stack = append(stack, Children(p)...)
	}
	return sum
}

// TreeFootprintBounded 在 ctx 与全局进程访问预算内累加 pid 及其子孙进程内存（字节）。
// 预算耗尽或 ctx 取消时提前停止并返回 truncated=true，避免深进程树拖慢常驻进程。
func TreeFootprintBounded(ctx context.Context, pid int, budget *int) (sum uint64, truncated bool) {
	if pid <= 0 {
		return 0, false
	}
	seen := map[int]bool{}
	stack := []int{pid}
	for len(stack) > 0 {
		if ctx != nil && ctx.Err() != nil {
			return sum, true
		}
		if budget != nil && *budget <= 0 {
			return sum, true
		}
		p := stack[len(stack)-1]
		stack = stack[:len(stack)-1]
		if seen[p] {
			continue
		}
		seen[p] = true
		if budget != nil {
			*budget--
		}
		sum += Footprint(p)
		stack = append(stack, Children(p)...)
	}
	return sum, false
}

// TruncateName 清理无效 UTF-8 后，按字符边界把短名裁到不超过 maxBytes 字节。
func TruncateName(s string, maxBytes int) string {
	if maxBytes <= 0 {
		return ""
	}
	s = strings.ToValidUTF8(s, "")
	if len(s) <= maxBytes {
		return s
	}
	i := maxBytes
	for i > 0 && !utf8.RuneStart(s[i]) {
		i--
	}
	return s[:i]
}

// SafeExeName 只从内核可执行路径提取短名，不解析任何启动参数，用于远端只读 RPC 防参数/凭据泄露。
func SafeExeName(exe string) string {
	exe = strings.TrimSpace(strings.ToValidUTF8(exe, ""))
	if exe == "" {
		return ""
	}
	base := filepath.Base(exe)
	if base == "" || base == "." || base == "/" {
		return ""
	}
	return base
}

// SafeName 返回 pid 对应内核可执行文件的 basename，绝不读取 argv[1:]，避免向只读查看端泄露命令参数或内联脚本。
func SafeName(pid int) string {
	exe, _ := ExeArgs(pid)
	return SafeExeName(exe)
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
