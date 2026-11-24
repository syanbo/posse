package cli

import (
	"path/filepath"
	"testing"
)

func TestAntigravityPreToolUseHelpers(t *testing.T) {
	if got := antigravityPermissionDecision("allow"); got != `{"decision":"allow"}` {
		t.Fatalf("allow = %q", got)
	}
	if got := antigravityPermissionDecision("deny"); got == "" || got == `{"decision":"allow"}` {
		t.Fatalf("deny = %q", got)
	}
	if got := antigravityPermissionDecision(""); got != `{"decision":"deny","reason":"Permission approval timed out in Posse/Feishu"}` {
		t.Fatalf("timeout fallback = %q", got)
	}

	notesDir := t.TempDir()
	t.Setenv("POSSE_NOTES", notesDir)

	allowed := []string{
		`{"toolCall":{"name":"run_command","args":{"CommandLine":"posse discussion read --execution x_123"}}}`,
		`{"toolCall":{"name":"run_command","args":{"CommandLine":"posse task done k_1 --execution x_1 --body-file /tmp/a.md"}}}`,
		`{"toolCall":{"name":"run_command","args":{"CommandLine":"ls -la ~/Desktop/Works | grep -iE 'av4|710|lyg|godfrey' ; echo done"}}}`,
		`{"toolCall":{"name":"run_command","args":{"CommandLine":"git -C ~/Desktop/Works/app_av4 diff -- android/README.md"}}}`,
		`{"toolCall":{"name":"run_command","args":{"CommandLine":"cat ~/Desktop/Works/app_av4/ic_launcher.xml 2>/dev/null"}}}`,
		`{"toolCall":{"name":"run_command","args":{"CommandLine":"echo \"POSSE_NOTES=${POSSE_NOTES}\" ; posse task check k_57f89c0a --execution x_1"}}}`,
		`{"toolCall":{"name":"write_file","args":{"Path":"` + filepath.Join(notesDir, "discussions", "mb_1", "reply.md") + `"}}}`,
	}
	for _, tc := range allowed {
		if !isSafeLarkToolCall([]byte(tc)) {
			t.Fatalf("expected auto-allow for %s", tc)
		}
	}

	denied := []string{
		`{"toolCall":{"name":"run_command","args":{"CommandLine":"posse discussion read --execution x_1 && rm -rf /"}}}`,
		`{"toolCall":{"name":"run_command","args":{"CommandLine":"posse discussion read --execution x_1; whoami"}}}`,
		`{"toolCall":{"name":"run_command","args":{"CommandLine":"echo hi > /tmp/out.txt"}}}`,
		`{"toolCall":{"name":"run_command","args":{"CommandLine":"echo $(rm -rf /)"}}}`,
		`{"toolCall":{"name":"run_command","args":{"CommandLine":"git commit -m test"}}}`,
		`{"toolCall":{"name":"run_command","args":{"CommandLine":"git branch -D old"}}}`,
		`{"toolCall":{"name":"run_command","args":{"CommandLine":"find . -delete"}}}`,
		`{"toolCall":{"name":"run_command","args":{"CommandLine":"sed -i '' 's/a/b/' file.txt"}}}`,
		`{"toolCall":{"name":"write_file","args":{"Path":"/Users/dengbo/Desktop/Works/app_av4/README.md"}}}`,
		`{"toolCall":{"name":"write_file","args":{"Path":"` + filepath.Join(notesDir, "discussions", "..", "escape.md") + `"}}}`,
		`{"toolCall":{"name":"edit_file","args":{"Path":"` + filepath.Join(notesDir, "discussions", "mb_1", "reply.md") + `"}}}`,
		`{"toolCall":{"name":"call_mcp_tool","args":{"ToolName":"click"}}}`,
	}
	for _, tc := range denied {
		if isSafeLarkToolCall([]byte(tc)) {
			t.Fatalf("should require approval for %s", tc)
		}
	}
}
