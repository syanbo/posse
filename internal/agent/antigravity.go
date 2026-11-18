package agent

import "strings"

// agy 把位置参数视为子命令；选项值（包括 -i 的提示词）不能被当成管理命令。
func antigravityInteractive(args []string) bool {
	values := []string{"--add-dir", "--agent", "--conversation", "--effort", "-i", "--prompt-interactive", "--input-format", "--json-schema", "--log-file", "--mode", "--model", "--output-format", "--print-timeout", "--project", "--settings"}
	for i := 0; i < len(args); i++ {
		flag, _, inline := strings.Cut(args[i], "=")
		if flag == "--" {
			return i == len(args)-1
		}
		if !strings.HasPrefix(flag, "-") || hasFlag([]string{flag}, passthrough["agy"]) {
			return false
		}
		if !inline && hasFlag([]string{flag}, values) {
			i++
		}
	}
	return true
}
