package history

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

func TestVideoScanner(t *testing.T) {
	for _, tc := range []struct {
		agent string
		lines []string
		want  []string
	}{
		{"claude", []string{
			// MCP 录屏工具把结果写成 JSON 字符串
			`{"type":"user","timestamp":"2026-10-19T11:37:58Z","message":{"content":[{"type":"tool_result","tool_use_id":"t1","content":[{"type":"text","text":"{\"durationMs\":25213,\"path\":\"/Users/x/Downloads/AndroidMirror/rec-1.mp4\"}"}]}]}}`,
			// 同一个文件再出现不重复
			`{"type":"user","timestamp":"2026-10-19T11:38:30Z","message":{"content":[{"type":"tool_result","tool_use_id":"t2","content":"saved /tmp/out/demo.MOV and /Users/x/Downloads/AndroidMirror/rec-1.mp4"}]}}`,
			// 相对路径和用户消息里的视频不算
			`{"type":"user","message":{"content":[{"type":"tool_result","tool_use_id":"t3","content":"wrote video/005/clip.mp4"}]}}`,
			`{"type":"user","message":{"content":"看看 /Users/x/Movies/mine.mp4"}}`,
			`{"type":"assistant","message":{"content":[{"type":"text","text":"录好了 /tmp/assistant.mp4"}]}}`,
		}, []string{"/Users/x/Downloads/AndroidMirror/rec-1.mp4", "/tmp/out/demo.MOV"}},
		{"codex", []string{
			`{"timestamp":"2026-10-19T11:37:58Z","type":"response_item","payload":{"type":"function_call_output","call_id":"c1","output":"Output:\n/tmp/a/screen.webm\n"}}`,
			`{"timestamp":"2026-10-19T11:38:00Z","type":"event_msg","payload":{"type":"item_completed","item":{"type":"CommandExecution","command":["zsh","-lc","ls ~/v.mp4"],"aggregated_output":"~/Movies/b.m4v\n"}}}`,
			`{"timestamp":"2026-10-19T11:38:02Z","type":"event_msg","payload":{"type":"item_completed","item":{"type":"McpToolCall","arguments":{"outputPath":"/tmp/arg.mp4"},"result":{"content":[{"type":"text","text":"{\"path\":\"/tmp/c.mp4\"}"}]}}}}`,
			`{"timestamp":"2026-10-19T11:38:04Z","type":"response_item","payload":{"type":"message","role":"user","content":[{"type":"input_text","text":"/tmp/user.mp4"}]}}`,
		}, []string{"/tmp/a/screen.webm", "~/Movies/b.m4v", "/tmp/c.mp4"}},
	} {
		path := filepath.Join(t.TempDir(), "s.jsonl")
		os.WriteFile(path, []byte(strings.Join(tc.lines, "\n")+"\n"), 0o644)
		got, err := NewVideoScanner(tc.agent, path).Scan()
		if err != nil {
			t.Fatal(err)
		}
		var paths []string
		for _, v := range got {
			paths = append(paths, v.Path)
		}
		want := slices.Clone(tc.want)
		for i, w := range want {
			want[i] = expandHome(w)
		}
		if !slices.Equal(paths, want) {
			t.Errorf("%s: videos = %q, want %q", tc.agent, paths, want)
		}
	}
}

func TestVideoScannerIncremental(t *testing.T) {
	path := filepath.Join(t.TempDir(), "s.jsonl")
	line := func(p, at string) string {
		return `{"type":"user","timestamp":"` + at + `","message":{"content":[{"type":"tool_result","tool_use_id":"t","content":"` + p + `"}]}}` + "\n"
	}
	os.WriteFile(path, []byte(line("/tmp/a.mp4", "2026-10-19T01:00:00Z")), 0o644)
	s := NewVideoScanner("claude", path)
	if v, _ := s.Scan(); len(v) != 1 {
		t.Fatalf("first scan = %v", v)
	}
	f, _ := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0o644)
	f.WriteString(line("/tmp/b.mp4", "2026-10-19T02:00:00Z") + line("/tmp/a.mp4", "2026-10-19T03:00:00Z"))
	f.Close()
	v, _ := s.Scan()
	if len(v) != 2 || v[0].Path != "/tmp/a.mp4" || v[0].At.Hour() != 3 {
		t.Fatalf("second scan = %+v", v)
	}
}
