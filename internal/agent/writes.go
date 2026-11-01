package agent

import (
	"encoding/json"
	"regexp"
	"strings"
)

// Writes 是一次工具调用可能写到的地方，给痕迹账本用：写文件工具的路径，shell 命令原文（由调用方猜写入目标）。
type Writes struct {
	Files    []string
	Commands []string
	Cwd      string // agent 当时的目录，相对路径按它算；hook 没带时为空
}

// readOnlyTool 是只读不写的工具：它们的 file_path、path 是读的。
var readOnlyTool = regexp.MustCompile(`(?i)read|grep|glob|search|fetch|view|^ls$|list|find|todo`)

// patchAddRe 是 apply_patch 里新建的文件。
var patchAddRe = regexp.MustCompile(`(?m)^\*\*\* Add File: (.+)$`)

// ToolWrites 从工具做完后的 hook（Claude、Codex 的 PostToolUse，Cursor 的 postToolUse）里取出写入。
func ToolWrites(agentName, event string, payload json.RawMessage) Writes {
	if event != "PostToolUse" && event != "postToolUse" {
		return Writes{}
	}
	var p struct {
		ToolName  string          `json:"tool_name"`
		ToolInput json.RawMessage `json:"tool_input"`
		Cwd       string          `json:"cwd"`
	}
	if json.Unmarshal(payload, &p) != nil || len(p.ToolInput) == 0 {
		return Writes{}
	}
	var in map[string]json.RawMessage
	if json.Unmarshal(p.ToolInput, &in) != nil {
		// apply_patch 的参数可能就是补丁原文
		var s string
		if json.Unmarshal(p.ToolInput, &s) == nil {
			return Writes{Files: patchFiles(s), Cwd: p.Cwd}
		}
		return Writes{}
	}
	w := Writes{Cwd: p.Cwd}
	str := func(k string) string {
		var s string
		json.Unmarshal(in[k], &s)
		return s
	}
	for _, k := range []string{"command", "cmd"} {
		if c := shellCommand(in[k]); c != "" {
			w.Commands = append(w.Commands, c)
		}
	}
	for _, k := range []string{"patch", "input"} {
		w.Files = append(w.Files, patchFiles(str(k))...)
	}
	if !readOnlyTool.MatchString(p.ToolName) {
		for _, k := range []string{"file_path", "notebook_path", "path", "target_file"} {
			if f := str(k); f != "" {
				w.Files = append(w.Files, f)
			}
		}
	}
	return w
}

// shellCommand 取出命令原文：字符串，或 Codex 那样的 ["/bin/zsh","-lc","脚本"]。
func shellCommand(raw json.RawMessage) string {
	if len(raw) == 0 {
		return ""
	}
	var s string
	if json.Unmarshal(raw, &s) == nil {
		return s
	}
	var argv []string
	if json.Unmarshal(raw, &argv) != nil || len(argv) == 0 {
		return ""
	}
	if n := len(argv); n >= 3 && strings.HasPrefix(argv[n-2], "-") && strings.Contains(argv[n-2], "c") {
		return argv[n-1]
	}
	return strings.Join(argv, " ")
}

func patchFiles(s string) []string {
	if !strings.Contains(s, "*** Begin Patch") {
		return nil
	}
	var out []string
	for _, m := range patchAddRe.FindAllStringSubmatch(s, -1) {
		out = append(out, strings.TrimSpace(m[1]))
	}
	return out
}
