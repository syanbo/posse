package history

import (
	"bytes"
	"encoding/json"
	"errors"
	"maps"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
	"unicode/utf8"
)

func TestChatSchemaUnchanged(t *testing.T) {
	if schemaVersion != "5" {
		t.Fatalf("schemaVersion %s", schemaVersion)
	}
	if parserVersions["claude"] != "4" || parserVersions["codex"] != "2" || parserVersions["grok"] != "4" {
		t.Fatalf("parserVersions %v", parserVersions)
	}
}

func TestChatTranscriptPath(t *testing.T) {
	dir := t.TempDir()
	reported := filepath.Join(dir, "sess.jsonl")
	if err := os.WriteFile(reported, []byte("{}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if got := TranscriptPath("claude", "ignored", reported); got != reported {
		t.Fatalf("reported path %s", got)
	}
	if got := TranscriptPath("claude", "a/b", filepath.Join(dir, "missing.jsonl")); got != "" {
		t.Fatalf("rejected session id returned %q", got)
	}
	if got := TranscriptPath("claude", "hive-test-absent-7c1e0b9a-4d22-4b0e-9c11-000000000001", ""); got != "" {
		t.Fatalf("unknown session resolved to %s", got)
	}
}

func TestChatClaude(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "c.jsonl")
	bigText := strings.Repeat("界", chatTextCap+1)
	bigOut := strings.Repeat("界", chatBlockCap+1)
	ordered := `{"z":1,"a":2}`
	var deep5, deep6 string
	deep5 = `"x"`
	for i := 0; i < 5; i++ {
		deep5 = `{"k":` + deep5 + `}`
	}
	deep6 = `{"k":` + deep5 + `}`
	var wide strings.Builder
	wide.WriteByte('{')
	for i := 0; i < 21; i++ {
		if i > 0 {
			wide.WriteByte(',')
		}
		wide.WriteString(`"k`)
		if i < 10 {
			wide.WriteByte('0')
		}
		wide.WriteString(itoa(i))
		wide.WriteString(`":`)
		wide.WriteString(itoa(i))
	}
	wide.WriteByte('}')
	budget := strings.Repeat("界", 3000)
	lines := []string{
		`{"type":"system","uuid":"nope","message":{"content":"skip"}}`,
		`{"type":"user","uuid":"u1","parentUuid":"p0","timestamp":"2026-10-01T10:00:01Z","message":{"content":"hello"}}`,
		`{"type":"user","uuid":"u2","message":{"content":[{"type":"tool_result","tool_use_id":"t1","is_error":true,"content":[{"type":"text","text":"file listed"}]}]}}`,
		`{"type":"assistant","uuid":"u3","message":{"content":[{"type":"text","text":"[Request interrupted by user] for real"}]}}`,
		`{"type":"user","isMeta":true,"uuid":"u4","message":{"content":[{"type":"text","text":"Base directory for this skill: injected"},{"type":"tool_result","tool_use_id":"t2","content":"kept"},{"type":"text","text":"/tmp/shot.png"},{"type":"image","source":{"path":"/tmp/other.webp"}}]}}`,
		`{"type":"user","isCompactSummary":true,"uuid":"u5","message":{"content":"summary only"}}`,
		`{"type":"user","isSynthetic":true,"uuid":"u6","message":{"content":[{"type":"text","text":"noise"},{"type":"text","text":"/var/x.jpg"}]}}`,
		`{"type":"assistant","uuid":"u7","message":{"content":[{"type":"thinking","thinking":"hmm"},{"type":"redacted_thinking","data":"xx"}]}}`,
		`{"type":"assistant","uuid":"u8","message":{"content":[{"type":"thinking","thinking":"plan"},{"type":"tool_use","name":"Read","id":"tu1","input":{"path":"a.go"}}]}}`,
		`{"type":"assistant","uuid":"tx","message":{"content":"` + bigText + `"}}`,
		`{"type":"user","uuid":"tr","message":{"content":[{"type":"tool_result","tool_use_id":"tr1","content":"` + bigOut + `"}]}}`,
		`{"type":"assistant","uuid":"ord","message":{"content":[{"type":"tool_use","name":"Bash","id":"ord1","input":` + ordered + `}]}}`,
		`{"type":"assistant","uuid":"d5","message":{"content":[{"type":"tool_use","name":"Edit","id":"d5","input":` + deep5 + `}]}}`,
		`{"type":"assistant","uuid":"d6","message":{"content":[{"type":"tool_use","name":"Edit","id":"d6","input":` + deep6 + `}]}}`,
		`{"type":"assistant","uuid":"fan","message":{"content":[{"type":"tool_use","name":"Edit","id":"fan","input":` + wide.String() + `}]}}`,
		`{"type":"assistant","uuid":"bud","message":{"content":[{"type":"tool_use","name":"Edit","id":"bud","input":["` + budget + `","` + budget + `"]}]}}`,
	}
	writeFile(t, path, lines...)
	page, err := ReadChatTail("claude", "", path, 40, 0)
	if err != nil {
		t.Fatal(err)
	}
	byID := map[string]ChatMessage{}
	for _, m := range page.Messages {
		byID[m.ID] = m
	}
	if _, ok := byID["nope"]; ok {
		t.Fatal("non user/assistant line kept")
	}
	if _, ok := byID["u5"]; ok {
		t.Fatal("compact summary text kept")
	}
	u1 := byID["u1"]
	if u1.Role != "user" || u1.ParentID != "p0" || u1.Source != "transcript" || len(u1.Blocks) != 1 || u1.Blocks[0].Text != "hello" {
		t.Fatalf("u1 %+v", u1)
	}
	wantAt := parseTime("2026-10-01T10:00:01Z")
	if u1.Timestamp == nil || *u1.Timestamp != wantAt {
		t.Fatalf("u1 time %v", u1.Timestamp)
	}
	raw, _ := json.Marshal(u1)
	var wire map[string]any
	json.Unmarshal(raw, &wire)
	if wire["role"] != "user" || wire["parentId"] != "p0" || wire["source"] != "transcript" {
		t.Fatalf("wire %s", raw)
	}
	if _, ok := wire["transcriptOffset"]; !ok {
		t.Fatalf("missing transcriptOffset %s", raw)
	}

	u2 := byID["u2"]
	if u2.Role != "tool" || len(u2.Blocks) != 1 || u2.Blocks[0].Type != "tool-result" || u2.Blocks[0].Output != "file listed" ||
		!u2.Blocks[0].IsError || u2.Blocks[0].CallID != "t1" {
		t.Fatalf("u2 %+v", u2)
	}
	b2, _ := json.Marshal(u2.Blocks[0])
	if !bytes.Contains(b2, []byte(`"isError":true`)) || !bytes.Contains(b2, []byte(`"callId":"t1"`)) {
		t.Fatalf("tool result wire %s", b2)
	}

	u3 := byID["u3"]
	if u3.Role != "system" || len(u3.Blocks) != 1 || u3.Blocks[0].Text != "Request interrupted by user" {
		t.Fatalf("interrupt %+v", u3)
	}

	u4 := byID["u4"]
	if u4.Role != "user" || len(u4.Blocks) != 3 || u4.Blocks[0].Type != "tool-result" || u4.Blocks[0].Output != "kept" ||
		u4.Blocks[1].Type != "image-ref" || u4.Blocks[1].Path != "/tmp/shot.png" ||
		u4.Blocks[2].Type != "image-ref" || u4.Blocks[2].Path != "/tmp/other.webp" {
		t.Fatalf("meta %+v", u4)
	}
	u6 := byID["u6"]
	if len(u6.Blocks) != 1 || u6.Blocks[0].Type != "image-ref" || u6.Blocks[0].Path != "/var/x.jpg" {
		t.Fatalf("synthetic %+v", u6)
	}
	u7 := byID["u7"]
	if u7.Role != "reasoning" || len(u7.Blocks) != 1 || u7.Blocks[0].Text != "hmm" {
		t.Fatalf("thinking %+v", u7)
	}
	u8 := byID["u8"]
	if u8.Role != "assistant" || len(u8.Blocks) != 2 || u8.Blocks[0].Text != "plan" || u8.Blocks[1].Type != "tool-call" ||
		u8.Blocks[1].Name != "Read" || u8.Blocks[1].CallID != "tu1" || string(u8.Blocks[1].Input) != `{"path":"a.go"}` {
		t.Fatalf("thinking+tool %+v", u8)
	}

	tx := byID["tx"]
	if utf8.RuneCountInString(tx.Blocks[0].Text) != chatTextCap || strings.Contains(tx.Blocks[0].Text, "…") {
		t.Fatalf("text cap %d", utf8.RuneCountInString(tx.Blocks[0].Text))
	}
	tr := byID["tr"]
	out := tr.Blocks[0].Output
	if !strings.HasSuffix(out, "\n… (truncated)") {
		t.Fatalf("tool result suffix %q", out[len(out)-20:])
	}
	body := strings.TrimSuffix(out, "\n… (truncated)")
	if utf8.RuneCountInString(body) != chatBlockCap {
		t.Fatalf("tool result body %d", utf8.RuneCountInString(body))
	}
	if string(byID["ord"].Blocks[0].Input) != ordered {
		t.Fatalf("input reencoded %s", byID["ord"].Blocks[0].Input)
	}
	if string(byID["d5"].Blocks[0].Input) != deep5 {
		t.Fatalf("depth 5 reencoded %s", byID["d5"].Blocks[0].Input)
	}
	var nest any
	if json.Unmarshal(byID["d6"].Blocks[0].Input, &nest) != nil || leafOf(nest) != "…" {
		t.Fatalf("depth 6 %+v", string(byID["d6"].Blocks[0].Input))
	}
	var fan map[string]any
	if json.Unmarshal(byID["fan"].Blocks[0].Input, &fan) != nil || len(fan) != 20 {
		t.Fatalf("fanout %s", byID["fan"].Blocks[0].Input)
	}
	if _, ok := fan["k20"]; ok {
		t.Fatal("21st key kept")
	}
	var bud []any
	if json.Unmarshal(byID["bud"].Blocks[0].Input, &bud) != nil || len(bud) != 2 {
		t.Fatalf("budget %s", byID["bud"].Blocks[0].Input)
	}
	if utf8.RuneCountInString(bud[0].(string)) != 3000 || utf8.RuneCountInString(bud[1].(string)) != 1000 {
		t.Fatalf("budget lens %d %d", utf8.RuneCountInString(bud[0].(string)), utf8.RuneCountInString(bud[1].(string)))
	}
}

// 斜杠命令的外壳写成命令本身，本地命令输出、后台任务通知不当成用户消息，中断标记照旧是系统消息。
func TestChatClaudeCommands(t *testing.T) {
	path := filepath.Join(t.TempDir(), "c.jsonl")
	writeFile(t, path,
		`{"type":"user","uuid":"c1","message":{"content":"<command-name>/compact</command-name>\n            <command-message>compact</command-message>\n            <command-args></command-args>"}}`,
		`{"type":"user","uuid":"c2","message":{"content":"<command-name>/effort</command-name>\n<command-message>effort</command-message>\n<command-args>high</command-args>"}}`,
		`{"type":"user","uuid":"o1","message":{"content":"<local-command-stdout>\u001b[2mCompacted\u001b[22m</local-command-stdout>"}}`,
		`{"type":"user","uuid":"n1","message":{"content":"<task-notification>\n<task-id>x</task-id>\n</task-notification>"}}`,
		`{"type":"user","uuid":"b1","message":{"content":"<bash-input>ls</bash-input>"}}`,
		`{"type":"user","uuid":"i1","message":{"content":[{"type":"text","text":"[Request interrupted by user]"}]}}`,
		`{"type":"user","uuid":"r1","message":{"content":"看看<system-reminder>injected</system-reminder>"}}`,
	)
	page, err := ReadChatTail("claude", "", path, 40, 0)
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]string{}
	for _, m := range page.Messages {
		got[m.ID] = m.Role + ":" + m.Blocks[0].Text
	}
	want := map[string]string{
		"c1": "user:/compact",
		"c2": "user:/effort high",
		"b1": "user:! ls",
		"i1": "system:Request interrupted by user",
		"r1": "user:看看",
	}
	if !maps.Equal(got, want) {
		t.Fatalf("messages = %v, want %v", got, want)
	}
}

func TestChatTail(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "c.jsonl")
	var b bytes.Buffer
	add := func(line string) int64 {
		off := int64(b.Len())
		b.WriteString(line)
		b.WriteByte('\n')
		return off
	}
	offA := add(`{"type":"user","uuid":"A","message":{"content":"A"}}`)
	pad := `{"type":"progress","uuid":"` + strings.Repeat("p", 180) + `"}`
	for i := 0; i < 400; i++ {
		add(pad)
	}
	offB := add(`{"type":"user","uuid":"B","message":{"content":"B"}}`)
	add(`{"type":"user","uuid":"C","message":{"content":"C"}}`)
	if b.Len() <= chatChunk {
		t.Fatalf("fixture %d does not cross a chunk", b.Len())
	}
	if err := os.WriteFile(path, b.Bytes(), 0o644); err != nil {
		t.Fatal(err)
	}
	page, err := ReadChatTail("claude", "", path, 2, 0)
	if err != nil {
		t.Fatal(err)
	}
	if !page.HasMore || page.BeforeOffset != offB || chatIDs(page) != "B,C" {
		t.Fatalf("page1 %+v ids %s", page.BeforeOffset, chatIDs(page))
	}
	page2, err := ReadChatTail("claude", "", path, 2, page.BeforeOffset)
	if err != nil {
		t.Fatal(err)
	}
	if page2.HasMore || page2.BeforeOffset != offA || chatIDs(page2) != "A" {
		t.Fatalf("page2 hasMore %v off %d ids %s", page2.HasMore, page2.BeforeOffset, chatIDs(page2))
	}
	if _, err := ReadChatTail("claude", "missing", filepath.Join(dir, "nope.jsonl"), 10, 0); !errors.Is(err, ErrChatNotFound) {
		t.Fatalf("missing file: %v", err)
	}
}

func TestChatNew(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "c.jsonl")
	line1 := `{"type":"user","uuid":"u1","message":{"content":"one"}}` + "\n"
	partial := `{"type":"user","uuid":"u2","message":{"content":"two"}}`
	if err := os.WriteFile(path, []byte(line1+partial), 0o644); err != nil {
		t.Fatal(err)
	}
	msgs, next, err := ReadChatNew("claude", path, 0)
	if err != nil {
		t.Fatal(err)
	}
	if next != int64(len(line1)) || len(msgs) != 1 || msgs[0].ID != "u1" {
		t.Fatalf("partial held next=%d msgs=%v", next, msgs)
	}
	f, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.WriteString("\n"); err != nil {
		t.Fatal(err)
	}
	f.Close()
	msgs, _, err = ReadChatNew("claude", path, next)
	if err != nil {
		t.Fatal(err)
	}
	if len(msgs) != 1 || msgs[0].ID != "u2" || msgs[0].Blocks[0].Text != "two" {
		t.Fatalf("completed %v", msgs)
	}
}

func TestChatCodex(t *testing.T) {
	dir := t.TempDir()
	legacy := filepath.Join(dir, "legacy.jsonl")
	writeFile(t, legacy,
		`{"timestamp":"2026-09-01T09:00:02Z","type":"response_item","payload":{"type":"message","role":"user","content":[{"type":"input_text","text":"<skill>\nname"},{"type":"input_text","text":"列出大文件"}]}}`,
		`{"timestamp":"2026-09-01T09:00:03Z","type":"response_item","payload":{"type":"message","role":"user","content":[{"type":"input_text","text":"<environment_context>\ncwd"}]}}`,
		`{"timestamp":"2026-09-01T09:00:04Z","type":"response_item","payload":{"id":"r1","type":"reasoning","summary":[{"text":"think"}]}}`,
		`{"timestamp":"2026-09-01T09:00:05Z","type":"response_item","payload":{"type":"function_call","name":"shell","call_id":"c1","arguments":"{\"z\":1,\"a\":2}"}}`,
		`{"timestamp":"2026-09-01T09:00:06Z","type":"response_item","payload":{"type":"function_call_output","call_id":"c1","output":"out"}}`,
		`{"timestamp":"2026-09-01T09:00:07Z","type":"event_msg","payload":{"type":"item_completed","item":{"type":"AgentMessage","id":"dup","text":"should skip"}}}`,
		`{"timestamp":"2026-09-01T09:00:08Z","type":"event_msg","payload":{"type":"turn_aborted"}}`,
	)
	page, err := ReadChatTail("codex", "", legacy, 20, 0)
	if err != nil {
		t.Fatal(err)
	}
	var texts []string
	for _, m := range page.Messages {
		texts = append(texts, m.Role+":"+blockText(m))
	}
	got := strings.Join(texts, " | ")
	wantParts := []string{"user:列出大文件", "reasoning:think", "assistant:", "tool:out", "system:Turn aborted"}
	for _, p := range wantParts {
		if !strings.Contains(got, p) {
			t.Fatalf("legacy missing %q in %s", p, got)
		}
	}
	if strings.Contains(got, "should skip") || strings.Contains(got, "environment_context") || strings.Contains(got, "<skill>") {
		t.Fatalf("legacy kept injected or duplicate %s", got)
	}
	var call ChatBlock
	for _, m := range page.Messages {
		for _, b := range m.Blocks {
			if b.Type == "tool-call" {
				call = b
			}
		}
	}
	if call.Name != "shell" || call.CallID != "c1" || string(call.Input) != `{"z":1,"a":2}` {
		t.Fatalf("call %+v", call)
	}

	paged := filepath.Join(dir, "paged.jsonl")
	writeFile(t, paged,
		`{"type":"session_meta","payload":{"id":"thr","history_mode":"paginated"}}`,
		`{"timestamp":"2026-09-01T09:00:02Z","type":"response_item","payload":{"type":"message","role":"user","content":[{"type":"input_text","text":"legacy dup"}]}}`,
		`{"timestamp":"2026-09-01T09:00:03Z","type":"event_msg","payload":{"type":"item_completed","item":{"id":"m1","type":"UserMessage","content":[{"type":"text","text":"# AGENTS.md instructions\nsecret"},{"type":"text","text":"真正的问题"}]}}}`,
		`{"timestamp":"2026-09-01T09:00:04Z","type":"event_msg","payload":{"type":"item_completed","item":{"id":"m2","type":"CommandExecution","command":["/bin/zsh","-lc","rg retry"],"aggregated_output":"src/retry.go:12"}}}`,
		`{"timestamp":"2026-09-01T09:00:05Z","type":"event_msg","payload":{"type":"user_message","message":"也算"}}`,
	)
	page, err = ReadChatTail("codex", "", paged, 20, 0)
	if err != nil {
		t.Fatal(err)
	}
	texts = nil
	for _, m := range page.Messages {
		texts = append(texts, m.ID+":"+m.Role+":"+blockText(m))
	}
	got = strings.Join(texts, " | ")
	if strings.Contains(got, "legacy dup") || strings.Contains(got, "AGENTS.md") || strings.Contains(got, "secret") {
		t.Fatalf("paged kept filtered text %s", got)
	}
	if !strings.Contains(got, "m1:user:真正的问题") || !strings.Contains(got, "src/retry.go:12") || !strings.Contains(got, "user:也算") {
		t.Fatalf("paged %s", got)
	}
	one, err := ReadChatTail("codex", "", paged, 1, 0)
	if err != nil || !one.HasMore || len(one.Messages) != 1 {
		t.Fatalf("paged tail %+v %v", one, err)
	}
	prev, err := ReadChatTail("codex", "", paged, 1, one.BeforeOffset)
	if err != nil || len(prev.Messages) != 1 || prev.Messages[0].Offset >= one.Messages[0].Offset {
		t.Fatalf("paged prev %+v", prev)
	}
}

func TestChatGrok(t *testing.T) {
	dir := t.TempDir()
	chat := filepath.Join(dir, "chat_history.jsonl")
	writeFile(t, chat,
		`{"type":"system","content":"You are Grok"}`,
		`{"type":"user","content":[{"type":"text","text":"<user_info>\nWorkspace Path: /work/grok\n</user_info>\n<rules>grokrulesinjected</rules>"}]}`,
		`{"type":"user","content":[{"type":"text","text":"<user_query>\n配置 Ghostty 主题\n</user_query>"}],"prompt_index":0}`,
		`{"type":"reasoning","summary":[],"encrypted_content":"xx"}`,
		`{"type":"assistant","model_id":"grok-4.7-build","content":"先读当前配置。","tool_calls":[{"id":"call-a-1","name":"read_file","arguments":"{\"target_file\":\"~/.config/ghostty/config\"}"}]}`,
		`{"type":"tool_result","tool_call_id":"call-a-1","content":"theme = Mocha"}`,
		`{"type":"user","content":[{"type":"text","text":"[Image extracted from tool result above]"},{"type":"image","url":"data:image/png;base64,AA=="}],"synthetic_reason":"system_reminder"}`,
		`{"type":"backend_tool_call","kind":{"tool_type":"web_search","action":{"query":"hive"}}}`,
	)
	writeFile(t, filepath.Join(dir, "updates.jsonl"),
		`{"timestamp":1790000100,"params":{"update":{"sessionUpdate":"user_message_chunk","content":{"text":"配置 Ghostty 主题"},"_meta":{"promptIndex":0}}}}`,
		`{"timestamp":1790000105,"params":{"update":{"sessionUpdate":"agent_message_chunk","content":{"text":"先读当前"}}}}`,
		`{"timestamp":1790000106,"params":{"update":{"sessionUpdate":"agent_message_chunk","content":{"text":"配置。"}}}}`,
		`{"timestamp":1790000107,"params":{"update":{"sessionUpdate":"tool_call","toolCallId":"ws_x_call-a-1"}}}`,
	)
	page, err := ReadChatTail("grok", "", chat, 20, 0)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(blockDump(page), "grokrulesinjected") || strings.Contains(blockDump(page), "You are Grok") {
		t.Fatalf("kept injected %s", blockDump(page))
	}
	if len(page.Messages) != 4 {
		t.Fatalf("count %d %s", len(page.Messages), blockDump(page))
	}
	user, asst, tool, back := page.Messages[0], page.Messages[1], page.Messages[2], page.Messages[3]
	if user.Role != "user" || user.Blocks[0].Text != "配置 Ghostty 主题" || user.Timestamp == nil || *user.Timestamp != 1790000100000 {
		t.Fatalf("user %+v", user)
	}
	if asst.Role != "assistant" || asst.Timestamp == nil || *asst.Timestamp != 1790000105000 || len(asst.Blocks) != 2 ||
		asst.Blocks[1].Type != "tool-call" || asst.Blocks[1].Name != "read_file" || asst.Blocks[1].CallID != "call-a-1" ||
		string(asst.Blocks[1].Input) != `{"target_file":"~/.config/ghostty/config"}` {
		t.Fatalf("assistant %+v", asst)
	}
	if tool.Role != "tool" || tool.Blocks[0].Output != "theme = Mocha" || tool.Blocks[0].CallID != "call-a-1" ||
		tool.Timestamp == nil || *tool.Timestamp != 1790000107000 {
		t.Fatalf("tool %+v", tool)
	}
	if back.Role != "assistant" || back.Blocks[0].Name != "web_search" || string(back.Blocks[0].Input) != `{"text":"hive"}` ||
		back.Timestamp == nil || *back.Timestamp != 1790000107000 {
		t.Fatalf("backend %+v input %s", back, back.Blocks[0].Input)
	}
	last, err := ReadChatTail("grok", "", chat, 1, 0)
	if err != nil || !last.HasMore || last.Messages[0].Blocks[0].Name != "web_search" {
		t.Fatalf("last %+v", last)
	}
	prev, err := ReadChatTail("grok", "", chat, 1, last.BeforeOffset)
	if err != nil || len(prev.Messages) != 1 || prev.Messages[0].Role != "tool" || prev.Messages[0].Offset >= last.Messages[0].Offset {
		t.Fatalf("prev %+v", prev)
	}
}

func leafOf(v any) any {
	m, ok := v.(map[string]any)
	if !ok {
		return v
	}
	return leafOf(m["k"])
}

func chatIDs(page ChatPage) string {
	var s []string
	for _, m := range page.Messages {
		s = append(s, m.ID)
	}
	return strings.Join(s, ",")
}

func blockText(m ChatMessage) string {
	var s []string
	for _, b := range m.Blocks {
		switch b.Type {
		case "text":
			s = append(s, b.Text)
		case "tool-result":
			s = append(s, b.Output)
		case "tool-call":
			s = append(s, b.Name)
		}
	}
	return strings.Join(s, ",")
}

func blockDump(page ChatPage) string {
	var s []string
	for _, m := range page.Messages {
		s = append(s, m.Role+":"+blockText(m))
	}
	return strings.Join(s, " | ")
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var b [8]byte
	i := len(b)
	for n > 0 {
		i--
		b[i] = byte('0' + n%10)
		n /= 10
	}
	return string(b[i:])
}

func TestReadChatTailCursor(t *testing.T) {
	path := filepath.Join(t.TempDir(), "s.jsonl")
	lines := []string{
		`{"role":"user","message":{"content":[{"type":"text","text":"<timestamp>Saturday, Oct 24, 2026, 4:24 PM (UTC+8)</timestamp>\n<user_query>\n看一下这个仓库\n</user_query>"}]}}`,
		`{"role":"assistant","message":{"content":[{"type":"text","text":"先读 README。"},{"type":"tool_use","name":"Read","input":{"path":"README.md"}}]}}`,
		`{"role":"assistant","message":{"content":[{"type":"tool_use","name":"Shell","input":{"command":"ls"}}]}}`,
		`{"type":"turn_ended","status":"success"}`,
	}
	os.WriteFile(path, []byte(strings.Join(lines, "\n")+"\n"), 0o644)
	page, err := ReadChatTail("cursor", "s", path, 40, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(page.Messages) != 3 {
		t.Fatalf("%d messages: %+v", len(page.Messages), page.Messages)
	}
	user := page.Messages[0]
	if user.Role != "user" || user.Blocks[0].Text != "看一下这个仓库" || user.Timestamp == nil {
		t.Fatalf("user %+v", user)
	}
	if got := time.UnixMilli(*user.Timestamp).UTC(); got != time.Date(2026, 10, 24, 8, 24, 0, 0, time.UTC) {
		t.Fatalf("time %v", got)
	}
	a := page.Messages[1]
	if a.Role != "assistant" || len(a.Blocks) != 2 || a.Blocks[1].Type != "tool-call" || a.Blocks[1].Name != "Read" || string(a.Blocks[1].Input) != `{"path":"README.md"}` {
		t.Fatalf("assistant %+v", a)
	}
	if page.Messages[2].Blocks[0].Name != "Shell" {
		t.Fatalf("tool %+v", page.Messages[2])
	}
}
