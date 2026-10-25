package agent

import (
	"strings"

	"posse/internal/procmem"
)

// FromProcess 按终端前台进程的 argv 认出是哪个 agent，认不出返回空。
// 用户在 shell 里自己敲命令启动的 agent 只能这样认出来。
func FromProcess(args []string) string {
	switch procmem.Name(args) {
	case "claude":
		return "claude"
	case "codex":
		return "codex"
	case "grok":
		return "grok"
	case "cursor-agent":
		return "cursor"
	case "agent":
		// Cursor 另装了一个叫 agent 的入口，exec 时保留了 argv[0]，靠后面 index.js 的路径认
		for _, a := range args[1:] {
			if strings.Contains(a, "cursor-agent") {
				return "cursor"
			}
		}
	}
	return ""
}
