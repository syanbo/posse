package history

import (
	"crypto/sha256"
	"encoding/base64"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestImageScanner(t *testing.T) {
	path := filepath.Join(t.TempDir(), "s.jsonl")
	img := func(data string) string {
		return fmt.Sprintf(`{"type":"image","source":{"type":"base64","media_type":"image/png","data":"%s"}}`, base64.StdEncoding.EncodeToString([]byte(data)))
	}
	lines := []string{
		`{"type":"assistant","message":{"content":[{"type":"tool_use","id":"t1","name":"Read","input":{"file_path":"/tmp/a.png"}}]}}`,
		`{"type":"user","timestamp":"2026-10-19T06:43:23.481Z","message":{"content":[{"type":"tool_result","tool_use_id":"t1","content":[` + img("one") + `]}]}}`,
		// 请求的副本，不算
		`{"type":"api-request-blob","message":{"content":[{"type":"tool_result","tool_use_id":"t1","content":[` + img("dup") + `]}]}}`,
		// 用户粘贴的图片，不算
		`{"type":"user","message":{"content":[` + img("pasted") + `]}}`,
	}
	write := func(ls ...string) {
		f, _ := os.OpenFile(path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644)
		for _, l := range ls {
			f.WriteString(l + "\n")
		}
		f.Close()
	}
	write(lines...)
	s := NewImageScanner("claude", path)
	got, err := s.Scan()
	if err != nil || len(got) != 1 || got[0].Tool != "Read" || got[0].Path != "/tmp/a.png" || got[0].At.IsZero() {
		t.Fatalf("first scan %+v %v", got, err)
	}
	// 增量：新的截图，一行里两张
	write(`{"type":"assistant","message":{"content":[{"type":"tool_use","id":"t2","name":"mcp__iphone-use__phone_screenshot","input":{}}]}}`,
		`{"type":"user","message":{"content":[{"type":"tool_result","tool_use_id":"t2","content":[{"type":"text","text":"ok"},`+img("two")+`,`+img("three")+`]}]}}`)
	got, _ = s.Scan()
	if len(got) != 3 || got[2].Tool != "mcp__iphone-use__phone_screenshot" || got[2].Index != 1 {
		t.Fatalf("second scan %+v", got)
	}
	for i, want := range []string{"one", "two", "three"} {
		b, err := ReadToolImage(path, got[i])
		if err != nil || string(b) != want {
			t.Fatalf("image %d = %q %v", i, b, err)
		}
	}

	// 会话被压缩重写：文件不比原来短，但内容全换了，旧偏移作废，列表只剩新文件里的
	old := got[1]
	pad := strings.Repeat("x", 4000)
	os.WriteFile(path, []byte(`{"type":"user","message":{"content":[{"type":"text","text":"`+pad+`"}]}}`+"\n"+
		`{"type":"assistant","message":{"content":[{"type":"tool_use","id":"t3","name":"Read","input":{"file_path":"/tmp/c.png"}}]}}`+"\n"+
		`{"type":"user","message":{"content":[{"type":"tool_result","tool_use_id":"t3","content":[`+img("four")+`]}]}}`+"\n"), 0o644)
	if _, err := ReadToolImage(path, old); err != errImageGone {
		t.Fatalf("stale offset read err = %v, want errImageGone", err)
	}
	got, err = s.Scan()
	if err != nil || len(got) != 1 || got[0].Path != "/tmp/c.png" {
		t.Fatalf("scan after rewrite %+v %v", got, err)
	}
	if b, err := ReadToolImage(path, got[0]); err != nil || string(b) != "four" {
		t.Fatalf("image after rewrite = %q %v", b, err)
	}
}

func TestCodexImageScanner(t *testing.T) {
	path := filepath.Join(t.TempDir(), "rollout.jsonl")
	url := "data:image/png;base64," + base64.StdEncoding.EncodeToString([]byte("pasted"))
	lines := []string{
		`{"timestamp":"2026-10-20T06:00:36.562Z","type":"response_item","payload":{"type":"message","role":"user","content":[` +
			`{"type":"input_text","text":"<image name=[Image #2] path=\"/tmp/posse-paste-1.png\">"},{"type":"input_image","image_url":"` + url + `"},` +
			`{"type":"input_text","text":"</image>"},{"type":"input_text","text":"[Image #2] 核实"}]}}`,
		// 新格式里同一条消息的完成事件只带路径，不重复算
		`{"type":"event_msg","payload":{"type":"item_completed","item":{"type":"UserMessage","content":[{"type":"local_image","path":"/tmp/posse-paste-1.png"}]}}}`,
		// 工具输出也收录，但不算粘贴的
		`{"type":"response_item","payload":{"type":"function_call_output","output":[{"type":"input_image","image_url":"` + url + `"}]}}`,
	}
	if err := os.WriteFile(path, []byte(strings.Join(lines, "\n")+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	got, err := NewImageScanner("codex", path).Scan()
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 || got[1].Pasted || got[0].N != 2 || got[0].Path != "/tmp/posse-paste-1.png" || got[0].Media != "image/png" || got[0].At.IsZero() {
		t.Fatalf("got %+v", got)
	}
	b, err := ReadToolImage(path, got[0])
	if err != nil || string(b) != "pasted" {
		t.Fatalf("read %q, %v", b, err)
	}
}

func TestGrokImageScanner(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "chat_history.jsonl")
	asset := filepath.Join(dir, "assets", "image-1.png")
	os.MkdirAll(filepath.Dir(asset), 0o755)
	os.WriteFile(asset, []byte("pasted"), 0o644)
	url := func(s string) string { return "data:image/png;base64," + base64.StdEncoding.EncodeToString([]byte(s)) }
	lines := []string{
		// 子 agent 开头带的图片不是工具返回的
		`{"type":"user","synthetic_reason":"system_reminder","content":[{"type":"text","text":"[Image extracted from tool result above]"},{"type":"image","url":"` + url("x") + `"}]}`,
		`{"type":"user","content":[{"type":"text","text":"<image_files>\nThe following images were provided by the user and saved to the workspace for future use:\n1. ` + asset + `\n\nThese images can be copied.\n</image_files>\n\n<user_query>\n[Image #1] 看看\n</user_query>"},{"type":"image","url":"` + url("pasted") + `"}]}`,
		`{"type":"assistant","tool_calls":[{"id":"c1","name":"read_file","arguments":"{\"target_file\":\"/tmp/a.png\"}"},{"id":"c2","name":"use_tool","arguments":"{\"tool_name\":\"phone_screenshot\"}"}]}`,
		`{"type":"tool_result","tool_call_id":"c1","content":"Read image file: /tmp/a.png","images":[{"type":"image","url":"` + url("read") + `"}]}`,
		`{"type":"tool_result","tool_call_id":"c2","content":"[image content will be provided separately]"}`,
		`{"type":"user","synthetic_reason":"system_reminder","content":[{"type":"text","text":"[Image extracted from tool result above]"},{"type":"image","url":"` + url("shot") + `"}]}`,
	}
	if err := os.WriteFile(path, []byte(strings.Join(lines, "\n")+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	got, err := NewImageScanner("grok", path).Scan()
	if err != nil {
		t.Fatal(err)
	}
	want := []struct {
		pasted bool
		n      int
		tool   string
		data   string
	}{{true, 1, "", "pasted"}, {false, 0, "read_file", "read"}, {false, 0, "phone_screenshot", "shot"}}
	if len(got) != len(want) {
		t.Fatalf("got %+v", got)
	}
	for i, w := range want {
		b, err := ReadToolImage(path, got[i])
		if got[i].Pasted != w.pasted || got[i].N != w.n || got[i].Tool != w.tool || err != nil || string(b) != w.data {
			t.Fatalf("%d: %+v %q %v", i, got[i], b, err)
		}
	}
	if got[0].Path != asset || !got[0].OnDisk || got[1].Path != "/tmp/a.png" {
		t.Fatalf("paths %+v", got)
	}
}

func TestCursorImageScanner(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "s.jsonl")
	a, b := filepath.Join(dir, "image-1-0.jpg"), filepath.Join(dir, "image-1-1.jpg")
	os.WriteFile(a, []byte("a"), 0o644)
	line := `{"role":"user","message":{"content":[{"type":"text","text":"[Image]\n<image_files>\nThe following images were provided by the user and saved to disk for future use:\n1. ` + a + `\n2. ` + b + `\n\nThese files can be read with tools.\n</image_files>\n<user_query>\n[Image #1] [Image #2] 这是什么\n</user_query>"}]}}`
	os.WriteFile(path, []byte(line+"\n"), 0o644)
	got, err := NewImageScanner("cursor", path).Scan()
	if err != nil || len(got) != 2 || got[0].N != 1 || got[1].N != 2 || got[1].Path != b || !got[0].Pasted || got[0].At.IsZero() {
		t.Fatalf("got %+v %v", got, err)
	}
	if data, err := ReadToolImage(path, got[0]); err != nil || string(data) != "a" {
		t.Fatalf("read %q %v", data, err)
	}
}

func TestCodexToolImages(t *testing.T) {
	path := filepath.Join(t.TempDir(), "rollout.jsonl")
	imageBlock := func(data string) string {
		return `{"type":"input_image","image_url":"data:image/png;base64,` + base64.StdEncoding.EncodeToString([]byte(data)) + `"}`
	}
	output := func(id, typ, data string) string {
		return `{"type":"response_item","timestamp":"2026-11-05T01:00:00Z","payload":{"type":"` + typ + `","call_id":"` + id + `","output":[{"type":"input_text","text":"/tmp/not-an-artifact.png"},` + imageBlock(data) + `]}}`
	}
	lines := []string{
		`{"type":"response_item","payload":{"type":"custom_tool_call","call_id":"a","name":"exec","input":"dynamic JS"}}`,
		`{"type":"event_msg","payload":{"type":"item_completed","item":{"type":"ImageView","path":"file:///tmp/wrong.png"}}}`,
		`{"type":"event_msg","payload":{"type":"item_completed","item":{"type":"McpToolCall","result":{"content":[{"type":"image","data":"b25l","mimeType":"image/png"}]}}}}`,
		output("a", "custom_tool_call_output", "one"),
		`{"type":"response_item","payload":{"type":"function_call","call_id":"b","name":"view_image","arguments":"{}"}}`,
		output("b", "function_call_output", "two"),
		`{"type":"response_item","payload":{"type":"function_call","call_id":"fail","name":"view_image","arguments":"{}"}}`,
		`{"type":"response_item","payload":{"type":"function_call_output","call_id":"fail","output":"failed /tmp/missing.png"}}`,
		`{"type":"response_item","payload":{"type":"custom_tool_call","call_id":"text","name":"exec","input":"ls"}}`,
		`{"type":"response_item","payload":{"type":"custom_tool_call_output","call_id":"text","output":[{"type":"input_text","text":"/tmp/another.png"}]}}`,
	}
	os.WriteFile(path, []byte(strings.Join(lines, "\n")+"\n"), 0o600)
	s := NewImageScanner("codex", path)
	got, err := s.Scan()
	if err != nil || len(got) != 2 {
		t.Fatalf("images=%+v err=%v", got, err)
	}
	for i, want := range []string{"one", "two"} {
		im := got[i]
		raw, err := ReadToolImage(path, im)
		if err != nil || string(raw) != want || im.Pasted || im.OnDisk || im.Path != "" || im.At.IsZero() {
			t.Fatalf("image=%+v raw=%q err=%v", im, raw, err)
		}
		if im.Digest != sha256.Sum256(raw) {
			t.Fatal("digest differs from embedded bytes")
		}
	}
	if got[0].Tool != "exec" || got[1].Tool != "view_image" || len(s.tools) != 0 {
		t.Fatalf("tool metadata: %+v pending=%v", got, s.tools)
	}
	oldOffset := s.offset
	if again, err := s.Scan(); err != nil || len(again) != 2 || s.offset != oldOffset {
		t.Fatal("unchanged scan duplicated images")
	}
	// 一行多张图，Index 只数图片；相同内容的另一次查看也保留。
	next := `{"type":"response_item","payload":{"type":"custom_tool_call_output","call_id":"unknown","output":[` + imageBlock("one") + `,{"type":"input_text","text":"between"},` + imageBlock("three") + `]}}`
	f, _ := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0o600)
	f.WriteString(next) // 未完整写入的行留到下次。
	f.Close()
	if got, _ = s.Scan(); len(got) != 2 {
		t.Fatal("partial line was consumed")
	}
	f, _ = os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0o600)
	f.WriteString("\n")
	f.Close()
	got, err = s.Scan()
	if err != nil || len(got) != 4 || got[3].Index != 1 || got[2].Tool != "tool" {
		t.Fatalf("append=%+v %v", got, err)
	}
	if raw, err := ReadToolImage(path, got[3]); err != nil || string(raw) != "three" {
		t.Fatalf("second image=%q %v", raw, err)
	}
	// 同长度以上的重写也清掉旧快照和调用映射。
	rewritten := `{"type":"response_item","payload":{"type":"message","role":"assistant","content":[{"type":"input_text","text":"` + strings.Repeat("x", int(s.offset)) + `"}]}}` + "\n" + output("a", "custom_tool_call_output", "replacement") + "\n"
	os.WriteFile(path, []byte(rewritten), 0o600)
	got, err = s.Scan()
	if err != nil || len(got) != 1 || got[0].Tool != "tool" {
		t.Fatalf("rewritten=%+v %v", got, err)
	}
}
