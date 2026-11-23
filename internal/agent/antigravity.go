package agent

import (
	"encoding/json"
	"path/filepath"
	"slices"
	"strings"
)

var antigravityValueFlags = []string{"--add-dir", "--agent", "--conversation", "--effort", "-i", "--prompt-interactive", "--input-format", "--json-schema", "--log-file", "--mode", "--model", "--output-format", "--print-timeout", "--project", "--settings"}

const antigravityStrict = "strict"
const antigravityStrictSettings = `{"toolPermission":"` + antigravityStrict + `"}`

// agy 的选项值可能长得像另一个选项，且多次给 --settings 时只有最后一个生效。
func (l Launcher) manualAntigravityArgs(args []string) []string {
	settingsIndex := -1
	var settingsValue, settingsPrefix string
	for i := 0; i < len(args); i++ {
		flag, value, inline := strings.Cut(args[i], "=")
		if flag == "--" {
			if i != len(args)-1 {
				return args
			}
			break
		}
		prefix := flag + "="
		if len(flag) > 2 && flag[0] == '-' && flag[1] != '-' {
			flag = "-" + flag
		}
		if !strings.HasPrefix(flag, "-") || slices.Contains(passthrough["agy"], flag) {
			return args
		}
		switch flag {
		case "--dangerously-skip-permissions", "--mode", "--sandbox":
			return args
		}
		if !inline && slices.Contains(antigravityValueFlags, flag) {
			if i+1 == len(args) {
				return args
			}
			i++
			value, prefix = args[i], ""
		}
		if flag == "--settings" {
			settingsIndex, settingsValue, settingsPrefix = i, value, prefix
		}
	}
	mode := PermissionMode(l.Home)
	if settingsIndex == -1 {
		if mode == PermissionAsk {
			return append([]string{"--settings", antigravityStrictSettings}, args...)
		}
		return append(slices.Clone(skipArgs["antigravity"]), args...)
	}

	// 文件和无效输入交给 agy 本身读取、报错，避免把用户文件的内容展开到进程参数。
	var settings map[string]json.RawMessage
	if json.Unmarshal([]byte(settingsValue), &settings) != nil || settings == nil {
		return args
	}
	for _, key := range []string{"toolPermission", "agentMode", "enableTerminalSandbox"} {
		if _, explicit := settings[key]; explicit {
			return args
		}
	}
	if mode == PermissionSkip {
		return append(slices.Clone(skipArgs["antigravity"]), args...)
	}
	settings["toolPermission"] = json.RawMessage(`"` + antigravityStrict + `"`)
	merged, err := json.Marshal(settings)
	if err != nil {
		return args
	}
	updated := slices.Clone(args)
	updated[settingsIndex] = settingsPrefix + string(merged)
	return updated
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
