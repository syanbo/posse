package agent

import (
	"encoding/json"
	"slices"
	"testing"
)

func TestToolWrites(t *testing.T) {
	cases := []struct {
		agent, event, payload string
		files, cmds           []string
	}{
		{"claude", "PostToolUse", `{"tool_name":"Write","cwd":"/w","tool_input":{"file_path":"/Users/x/Downloads/a.md","content":"x"}}`, []string{"/Users/x/Downloads/a.md"}, nil},
		{"claude", "PostToolUse", `{"tool_name":"Read","tool_input":{"file_path":"/Users/x/.zshrc"}}`, nil, nil},
		{"claude", "PostToolUse", `{"tool_name":"Bash","tool_input":{"command":"mkdir -p ~/Downloads/x"}}`, nil, []string{"mkdir -p ~/Downloads/x"}},
		{"codex", "PostToolUse", `{"tool_name":"Bash","tool_input":{"command":["/bin/zsh","-lc","echo hi > /tmp/a"]}}`, nil, []string{"echo hi > /tmp/a"}},
		{"codex", "PostToolUse", `{"tool_name":"apply_patch","tool_input":{"input":"*** Begin Patch\n*** Add File: /Users/x/n.md\n+hi\n*** End Patch"}}`, []string{"/Users/x/n.md"}, nil},
		{"cursor", "postToolUse", `{"tool_name":"Shell","tool_input":{"command":"touch /Users/x/y"}}`, nil, []string{"touch /Users/x/y"}},
		{"claude", "PreToolUse", `{"tool_name":"Write","tool_input":{"file_path":"/a"}}`, nil, nil},
	}
	for _, c := range cases {
		w := ToolWrites(c.agent, c.event, json.RawMessage(c.payload))
		if !slices.Equal(w.Files, c.files) || !slices.Equal(w.Commands, c.cmds) {
			t.Errorf("%s: files %q cmds %q", c.payload, w.Files, w.Commands)
		}
	}
}
