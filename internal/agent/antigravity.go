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
const antigravityApprovalTools = `^(run_command|write_to_file|replace_file_content|multi_replace_file_content|write_file|edit_file|call_mcp_tool)$`

func (l Launcher) antigravityLaunchArgs(permOverride string, externalApproval bool) ([]string, error) {
	settings := map[string]json.RawMessage{}
	// 飞书外部审批开启时：由专属 PreToolUse hook 独占把关（允许→allow、拒绝→deny、超时→force_ask），
	// 进程侧加 --dangerously-skip-permissions 避免 agy 在 hook 返回 allow 后再次弹出内置终端确认。
	effectivePerm := permOverride
	if externalApproval {
		effectivePerm = PermissionSkip
	}
	args := l.antigravityPermissions(settings, false, effectivePerm)
	dir, err := l.writeAntigravityHooks()
	if err != nil {
		return nil, err
	}
	if dir != "" {
		args = append(args, "--add-dir", Quote(dir))
	}
	if externalApproval {
		larkDir, err := l.writeAntigravityLarkHooks()
		if err != nil {
			return nil, err
		}
		if larkDir != "" {
			args = append(args, "--add-dir", Quote(larkDir))
		}
	}
	l.addAntigravityStatusLine(settings)
	if len(settings) == 0 {
		return args, nil
	}
	encoded, _ := json.Marshal(settings)
	return append(args, "--settings", Quote(string(encoded))), nil
}

// agy 的选项值可能长得像另一个选项，且多次给 --settings 时只有最后一个生效。
func (l Launcher) manualAntigravityArgs(args []string) ([]string, error) {
	settingsIndex := -1
	var settingsValue, settingsPrefix string
	explicitPermission := false
	hasHookDir := false
	for i := 0; i < len(args); i++ {
		flag, value, inline := strings.Cut(args[i], "=")
		if flag == "--" {
			if i != len(args)-1 {
				return args, nil
			}
			break
		}
		prefix := flag + "="
		if len(flag) > 2 && flag[0] == '-' && flag[1] != '-' {
			flag = "-" + flag
		}
		if !strings.HasPrefix(flag, "-") || slices.Contains(passthrough["agy"], flag) {
			return args, nil
		}
		switch flag {
		case "--dangerously-skip-permissions", "--mode", "--sandbox":
			explicitPermission = true
		}
		if !inline && slices.Contains(antigravityValueFlags, flag) {
			if i+1 == len(args) {
				return args, nil
			}
			i++
			value, prefix = args[i], ""
		}
		if flag == "--settings" {
			settingsIndex, settingsValue, settingsPrefix = i, value, prefix
		}
		if flag == "--add-dir" && value == AntigravityHookDir(l.Home) {
			hasHookDir = true
		}
	}
	var hooks []string
	if !hasHookDir {
		dir, err := l.writeAntigravityHooks()
		if err != nil {
			return nil, err
		}
		if dir != "" {
			hooks = []string{"--add-dir", dir}
		}
	}
	// 文件和无效输入交给 agy 本身读取、报错，避免把用户文件的内容展开到进程参数。
	settings := map[string]json.RawMessage{}
	if settingsIndex >= 0 && (json.Unmarshal([]byte(settingsValue), &settings) != nil || settings == nil) {
		return append(hooks, args...), nil
	}
	keys := len(settings)
	extra := append(l.antigravityPermissions(settings, explicitPermission, ""), hooks...)
	updatedStatus := l.addAntigravityStatusLine(settings)
	if settingsIndex < 0 && len(settings) > 0 {
		encoded, _ := json.Marshal(settings)
		return append(append(extra, "--settings", string(encoded)), args...), nil
	}
	if settingsIndex >= 0 && (updatedStatus || len(settings) != keys) {
		encoded, _ := json.Marshal(settings)
		args = slices.Clone(args)
		args[settingsIndex] = settingsPrefix + string(encoded)
	}
	return append(extra, args...), nil
}

const antigravityHookDirName = ".posse-antigravity-hooks"
const antigravityLarkHookDirName = ".posse-antigravity-lark-hooks"

// AntigravityHookDir 是只放 Posse hook 的附加目录，agy 会与原工作区的 hooks 一起读取。
func AntigravityHookDir(home string) string { return filepath.Join(home, antigravityHookDirName) }

// AntigravityLarkHookDir 是飞书卡片审批专用的附加目录，仅挂载 PreToolUse，不干扰普通 agy 终端。
func AntigravityLarkHookDir(home string) string {
	return filepath.Join(home, antigravityLarkHookDirName)
}

// IsAntigravityHookDir 识别 agy 可能写进会话摘要的 Posse 内部目录，包括其他 Posse 实例的目录。
func IsAntigravityHookDir(path string) bool {
	if !filepath.IsAbs(path) {
		return false
	}
	base := filepath.Base(path)
	return base == antigravityHookDirName || base == antigravityLarkHookDirName
}

func (l Launcher) writeAntigravityHooks() (string, error) {
	if l.Exe == "" || l.Home == "" {
		return "", nil
	}
	dir := AntigravityHookDir(l.Home)
	handler := func(event string, timeout int) map[string]any {
		return map[string]any{"type": "command", "command": l.hookCommand("antigravity", event), "timeout": timeout}
	}
	data, err := json.MarshalIndent(map[string]any{
		"posse-handoff-and-traces": map[string]any{
			"SessionStart": []any{handler("SessionStart", 3)},
			"PostToolUse": []any{map[string]any{
				"matcher": antigravityWriteTools,
				"hooks":   []any{handler("PostToolUse", 3)},
			}},
		},
	}, "", "  ")
	if err != nil {
		return "", err
	}
	if err := writeIfChanged(filepath.Join(dir, ".agents", "hooks.json"), data); err != nil {
		return "", err
	}
	return dir, nil
}

func (l Launcher) writeAntigravityLarkHooks() (string, error) {
	if l.Exe == "" || l.Home == "" {
		return "", nil
	}
	dir := AntigravityLarkHookDir(l.Home)
	data, err := json.MarshalIndent(map[string]any{
		"posse-lark-approval": map[string]any{
			"PreToolUse": []any{map[string]any{
				"matcher": antigravityApprovalTools,
				"hooks": []any{map[string]any{
					"type":    "command",
					"command": l.hookCommand("antigravity", "PreToolUse"),
					"timeout": 200,
				}},
			}},
		},
	}, "", "  ")
	if err != nil {
		return "", err
	}
	if err := writeIfChanged(filepath.Join(dir, ".agents", "hooks.json"), data); err != nil {
		return "", err
	}
	return dir, nil
}

func (l Launcher) antigravityPermissions(settings map[string]json.RawMessage, explicit bool, override string) []string {
	for _, key := range []string{"toolPermission", "agentMode", "enableTerminalSandbox"} {
		if _, present := settings[key]; present {
			explicit = true
		}
	}
	if explicit {
		return nil
	}
	mode := override
	if mode != PermissionAsk && mode != PermissionSkip {
		mode = PermissionMode(l.Home)
	}
	if mode == PermissionSkip {
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
