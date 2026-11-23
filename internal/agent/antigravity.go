package agent

import (
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"strings"
)

var antigravityValueFlags = []string{"--add-dir", "--agent", "--conversation", "--effort", "-i", "--prompt-interactive", "--input-format", "--json-schema", "--log-file", "--mode", "--model", "--output-format", "--print-timeout", "--project", "--settings"}

const antigravityStrict = "strict"
const antigravityStrictSettings = `{"toolPermission":"` + antigravityStrict + `"}`

func (l Launcher) antigravityLaunchArgs() []string {
	settings := map[string]json.RawMessage{}
	args := l.antigravityPermissions(settings, false)
	l.addAntigravityStatusLine(settings)
	if len(settings) == 0 {
		return args
	}
	encoded, _ := json.Marshal(settings)
	return append(args, "--settings", Quote(string(encoded)))
}

// agy 的选项值可能长得像另一个选项，且多次给 --settings 时只有最后一个生效。
func (l Launcher) manualAntigravityArgs(args []string) []string {
	settingsIndex := -1
	var settingsValue, settingsPrefix string
	explicitPermission := false
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
			explicitPermission = true
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
	// 文件和无效输入交给 agy 本身读取、报错，避免把用户文件的内容展开到进程参数。
	settings := map[string]json.RawMessage{}
	if settingsIndex >= 0 && (json.Unmarshal([]byte(settingsValue), &settings) != nil || settings == nil) {
		return args
	}
	keys := len(settings)
	extra := l.antigravityPermissions(settings, explicitPermission)
	updatedStatus := l.addAntigravityStatusLine(settings)
	if settingsIndex < 0 && len(settings) > 0 {
		encoded, _ := json.Marshal(settings)
		return append(append(extra, "--settings", string(encoded)), args...)
	}
	if settingsIndex >= 0 && (updatedStatus || len(settings) != keys) {
		encoded, _ := json.Marshal(settings)
		args = slices.Clone(args)
		args[settingsIndex] = settingsPrefix + string(encoded)
	}
	return append(extra, args...)
}

func (l Launcher) antigravityPermissions(settings map[string]json.RawMessage, explicit bool) []string {
	for _, key := range []string{"toolPermission", "agentMode", "enableTerminalSandbox"} {
		if _, present := settings[key]; present {
			explicit = true
		}
	}
	if explicit {
		return nil
	}
	if PermissionMode(l.Home) == PermissionSkip {
		return slices.Clone(skipArgs["antigravity"])
	}
	settings["toolPermission"] = json.RawMessage(`"` + antigravityStrict + `"`)
	return nil
}

// addAntigravityStatusLine 沿用用户原来的显示选项。新加的空状态栏堆在原生状态栏下方。
func (l Launcher) addAntigravityStatusLine(settings map[string]json.RawMessage) bool {
	if l.Exe == "" {
		return false
	}
	raw, inline := settings["statusLine"]
	if !inline {
		var ok bool
		raw, ok = AntigravityUserStatusLine()
		if !ok {
			return false
		}
	}
	status := map[string]json.RawMessage{}
	var original AntigravityStatusLine
	if raw != nil && (json.Unmarshal(raw, &status) != nil || status == nil || json.Unmarshal(raw, &original) != nil) {
		return false
	}
	if original.Enabled != nil && !*original.Enabled {
		return false
	}
	command := Quote(l.Exe) + " statusline antigravity"
	if original.Command == command || strings.HasPrefix(original.Command, command+" ") {
		return false
	}
	switch {
	case original.Command == "":
		status["stack_with_default"] = json.RawMessage("true")
	case inline:
		command += " -- " + Quote(original.Command)
	default:
		// 用户配置文件里的命令在运行时读取，免得把原文暴露到新的进程参数里。
		command += " --user"
	}
	status["command"], _ = json.Marshal(command)
	settings["statusLine"], _ = json.Marshal(status)
	return true
}

type AntigravityStatusLine struct {
	Command string `json:"command"`
	Enabled *bool  `json:"enabled"`
}

// AntigravityUserStatusLine 只读用户的状态栏设置。找不到配置文件表示用原生状态栏。
func AntigravityUserStatusLine() (json.RawMessage, bool) {
	home, err := os.UserHomeDir()
	if err != nil {
		return nil, false
	}
	b, err := os.ReadFile(filepath.Join(home, ".gemini", "antigravity-cli", "settings.json"))
	if os.IsNotExist(err) {
		return nil, true
	}
	var config struct {
		StatusLine json.RawMessage `json:"statusLine"`
	}
	if err != nil || json.Unmarshal(b, &config) != nil {
		return nil, false
	}
	return config.StatusLine, true
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
