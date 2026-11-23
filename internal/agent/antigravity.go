package agent

import (
	"path/filepath"
	"slices"
	"strings"
)

var antigravityValueFlags = []string{"--add-dir", "--agent", "--conversation", "--effort", "-i", "--prompt-interactive", "--input-format", "--json-schema", "--log-file", "--mode", "--model", "--output-format", "--print-timeout", "--project", "--settings"}

// agy 把位置参数视为子命令；选项值（包括 -i 的提示词）不能被当成管理命令。
func antigravityInteractive(args []string) bool {
	for i := 0; i < len(args); i++ {
		flag, _, inline := strings.Cut(args[i], "=")
		if flag == "--" {
			return i == len(args)-1
		}
		if !strings.HasPrefix(flag, "-") || hasFlag([]string{flag}, passthrough["agy"]) {
			return false
		}
		if !inline && slices.Contains(antigravityValueFlags, flag) {
			i++
		}
	}
	return true
}

// AntigravityAddedDirs 从 agy 的启动参数取 --add-dir，跳过提示词等选项值。
func AntigravityAddedDirs(args []string, cwd string) []string {
	var dirs []string
	for i := 0; i < len(args); i++ {
		flag, value, inline := strings.Cut(args[i], "=")
		if flag == "--" {
			break
		}
		if !slices.Contains(antigravityValueFlags, flag) {
			continue
		}
		if !inline && i+1 < len(args) {
			i++
			value = args[i]
		}
		if flag != "--add-dir" || value == "" {
			continue
		}
		if !filepath.IsAbs(value) {
			if cwd == "" {
				continue
			}
			value = filepath.Join(cwd, value)
		}
		value = filepath.Clean(value)
		if value != cwd && !slices.Contains(dirs, value) {
			dirs = append(dirs, value)
		}
	}
	return dirs
}
