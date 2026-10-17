package history

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

// unfiltered 去掉 parser 的粗筛，其余照旧。
type unfiltered struct{ p parser }

func (u unfiltered) line(raw []byte, st *fileState) []Message { return u.p.line(raw, st) }

// legacyCodex 写一份旧格式（没有 history_mode）的 codex 会话：消息在 response_item 里。
func legacyCodex(t *testing.T, home string) {
	writeFile(t, filepath.Join(home, ".codex", "sessions", "2026", "09", "01", "rollout-2026-09-01T09-00-00-legacy.jsonl"),
		`{"timestamp":"2026-09-01T09:00:00Z","type":"session_meta","payload":{"id":"legacy","cwd":"/work/old","source":"cli"}}`,
		`{"timestamp":"2026-09-01T09:00:01Z","type":"turn_context","payload":{"cwd":"/work/old","model":"gpt-6"}}`,
		`{"timestamp":"2026-09-01T09:00:02Z","type":"response_item","payload":{"type":"message","role":"user","content":[{"type":"input_text","text":"列出大文件"}]}}`,
		`{"timestamp":"2026-09-01T09:00:03Z","type":"event_msg","payload":{"type":"agent_reasoning","text":"thinking"}}`,
		`{"timestamp":"2026-09-01T09:00:04Z","type":"response_item","payload":{"type":"function_call","name":"shell","arguments":"{\"command\":[\"bash\",\"-lc\",\"du -sh *\"]}"}}`,
		`{"timestamp":"2026-09-01T09:00:05Z","type":"response_item","payload":{"type":"function_call_output","output":"{\"output\":\"4G big\"}"}}`,
		`{"timestamp":"2026-09-01T09:00:06Z","type":"response_item","payload":{"type":"message","role":"assistant","content":[{"type":"output_text","text":"big 最大。"}]}}`)
}

// spacedCopy 把 src 下的会话文件复制到 dst，每行改写成冒号后带空格的 JSON。
func spacedCopy(t *testing.T, src, dst string) {
	t.Helper()
	err := filepath.WalkDir(src, func(path string, d os.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		b, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(src, path)
		var out []string
		for _, line := range strings.Split(strings.TrimRight(string(b), "\n"), "\n") {
			var buf bytes.Buffer
			if json.Indent(&buf, []byte(line), "", "") != nil {
				out = append(out, line)
				continue
			}
			// 字符串里的换行都已转义，剩下的换行只是排版
			out = append(out, strings.ReplaceAll(buf.String(), "\n", " "))
		}
		writeFile(t, filepath.Join(dst, rel), out...)
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}

func parseAll(t *testing.T, src source, p parser) ([]Message, fileState) {
	t.Helper()
	f, err := os.Open(src.transcript)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	var st fileState
	if pr, ok := src.parser.(preparer); ok {
		pr.prepare(src.transcript, &st)
	}
	msgs, _, _, err := readLines(f, p, &st)
	if err != nil {
		t.Fatal(err)
	}
	return msgs, st
}

// 粗筛只省掉解析，不改变结果：有无粗筛、冒号后有无空格，得到的消息和元数据都一样。
func TestLineFilterSameResult(t *testing.T) {
	home := t.TempDir()
	fixtures(t, home)
	legacyCodex(t, home)
	spaced := t.TempDir()
	spacedCopy(t, home, spaced)

	compact := map[string][]Message{}
	for _, src := range discover(Roots{Home: home}) {
		if _, ok := src.parser.(lineFilter); !ok {
			continue
		}
		want, wantSt := parseAll(t, src, unfiltered{src.parser})
		got, gotSt := parseAll(t, src, src.parser)
		if len(want) == 0 && !wantSt.Skip {
			t.Errorf("%s: fixture produced no messages", src.transcript)
		}
		if !reflect.DeepEqual(got, want) || !reflect.DeepEqual(gotSt, wantSt) {
			t.Errorf("%s: filtered result differs\n got %+v %+v\nwant %+v %+v", src.transcript, got, gotSt, want, wantSt)
		}
		rel, _ := filepath.Rel(home, src.transcript)
		compact[rel] = want
	}
	n := 0
	for _, src := range discover(Roots{Home: spaced}) {
		if _, ok := src.parser.(lineFilter); !ok {
			continue
		}
		got, _ := parseAll(t, src, src.parser)
		rel, _ := filepath.Rel(spaced, src.transcript)
		if !reflect.DeepEqual(got, compact[rel]) {
			t.Errorf("%s with spaces: got %+v\nwant %+v", rel, got, compact[rel])
		}
		n++
	}
	// claude、两份 codex（含被跳过的 guardian）、旧格式 codex、grok
	if n != 5 || len(compact) != 5 {
		t.Fatalf("checked %d spaced and %d compact files", n, len(compact))
	}
}

func TestClaudeCommandArgs(t *testing.T) {
	home := t.TempDir()
	fixtures(t, home)
	x := openIndex(t)
	update(t, x, home)
	msgs, err := x.Transcript(context.Background(), Session{Path: filepath.Join(home, ".claude", "projects", "-work-shop", claudeSID+".jsonl")},
		TranscriptOptions{})
	if err != nil {
		t.Fatal(err)
	}
	var found bool
	for _, m := range msgs {
		if strings.HasPrefix(m.Text, "/review") {
			found = true
			if m.Text != "/review branch-x" {
				t.Errorf("slash command = %q, want %q", m.Text, "/review branch-x")
			}
		}
	}
	if !found {
		t.Fatalf("slash command missing: %+v", msgs)
	}
	if got := claudeUserText(&fileState{}, "<command-message>x</command-message>\n<command-name>/x</command-name>"); got != "/x" {
		t.Errorf("command without args = %q", got)
	}
}

func TestTranscriptOptions(t *testing.T) {
	home := t.TempDir()
	fixtures(t, home)
	x := openIndex(t)
	update(t, x, home)
	ctx := context.Background()
	ss, err := x.Sessions(ctx, ListOptions{Agent: "codex"})
	if err != nil || len(ss) != 1 {
		t.Fatalf("sessions = %v, %v", ss, err)
	}
	roles := func(o TranscriptOptions) string {
		msgs, err := x.Transcript(ctx, ss[0], o)
		if err != nil {
			t.Fatal(err)
		}
		var out []string
		for _, m := range msgs {
			out = append(out, fmt.Sprintf("%s%d", m.Role, m.Seq))
		}
		return strings.Join(out, ",")
	}
	for o, want := range map[TranscriptOptions]string{
		{}:                       "user0,tool1,tool2,tool3,assistant4",
		{Tail: 2}:                "tool3,assistant4",
		{NoTools: true}:          "user0,assistant4",
		{Tail: 1, NoTools: true}: "assistant4",
		{Tail: 9}:                "user0,tool1,tool2,tool3,assistant4",
	} {
		if got := roles(o); got != want {
			t.Errorf("%+v: %s, want %s", o, got, want)
		}
	}
}

// grok 增量读取：新的回复不能对上之前回复用过的分段，结果和整份重读一致。
func TestGrokIncrementalTimes(t *testing.T) {
	home := t.TempDir()
	dir := filepath.Join(home, ".grok", "sessions", "%2Fwork%2Fg", "44444444-4444-4444-8444-444444444444")
	chat, updates := filepath.Join(dir, "chat_history.jsonl"), filepath.Join(dir, "updates.jsonl")
	writeFile(t, chat,
		`{"type":"user","content":[{"type":"text","text":"<user_query>看看配置</user_query>"}],"prompt_index":0}`,
		`{"type":"assistant","content":"好的，先看看配置。"}`)
	writeFile(t, updates,
		`{"timestamp":1790000100,"params":{"update":{"sessionUpdate":"user_message_chunk","_meta":{"promptIndex":0}}}}`,
		`{"timestamp":1790000105,"params":{"update":{"sessionUpdate":"agent_message_chunk","content":{"text":"好的"}}}}`,
		`{"timestamp":1790000106,"params":{"update":{"sessionUpdate":"agent_message_chunk","content":{"text":"，先看看配置。"}}}}`)
	x := openIndex(t)
	update(t, x, home)

	appendFile(t, updates,
		`{"timestamp":1790000150,"params":{"update":{"sessionUpdate":"user_message_chunk","_meta":{"promptIndex":1}}}}`,
		`{"timestamp":1790000200,"params":{"update":{"sessionUpdate":"agent_message_chunk","content":{"text":"好的"}}}}`,
		`{"timestamp":1790000201,"params":{"update":{"sessionUpdate":"agent_message_chunk","content":{"text":"，已经改好了。"}}}}`)
	appendFile(t, chat,
		`{"type":"user","content":[{"type":"text","text":"<user_query>改成浅色</user_query>"}],"prompt_index":1}`,
		`{"type":"assistant","content":"好的，已经改好了。"}`)
	update(t, x, home)

	times := func(x *Index) string {
		msgs, err := x.Transcript(context.Background(), Session{Path: chat}, TranscriptOptions{})
		if err != nil {
			t.Fatal(err)
		}
		var out []string
		for _, m := range msgs {
			out = append(out, fmt.Sprintf("%s@%d", m.Role, m.At.Unix()))
		}
		return strings.Join(out, ",")
	}
	want := "user@1790000100,assistant@1790000105,user@1790000150,assistant@1790000200"
	if got := times(x); got != want {
		t.Errorf("incremental times = %s, want %s", got, want)
	}
	full := openIndex(t)
	update(t, full, home)
	if got := times(full); got != want {
		t.Errorf("full reparse times = %s, want %s", got, want)
	}
}

// codex 的会话名来自 session_index.jsonl：会话追加内容时名字不丢，只改名时也能更新。
func TestCodexTitles(t *testing.T) {
	home := t.TempDir()
	fixtures(t, home)
	x := openIndex(t)
	update(t, x, home)
	ctx := context.Background()
	title := func() string {
		s, err := x.Find(ctx, "c0dex")
		if err != nil {
			t.Fatal(err)
		}
		return s.Title
	}
	codex := filepath.Join(home, ".codex", "sessions", "2026", "10", "02", "rollout-2026-10-02T09-00-00-c0dex.jsonl")
	appendFile(t, codex, `{"timestamp":"2026-10-02T09:01:00Z","type":"event_msg","payload":{"type":"item_completed","item":{"type":"AgentMessage","content":[{"type":"Text","text":"再补一句。"}]}}}`)
	// 只更新会话文件，不经过 applyCodexTitles
	known, err := x.knownFiles(ctx)
	if err != nil {
		t.Fatal(err)
	}
	src := source{agent: "codex", transcript: codex, parser: codexParser{}}
	if err := x.updateFile(ctx, src, known[codex], Roots{Home: home}); err != nil {
		t.Fatal(err)
	}
	if got := title(); got != "重试退避" {
		t.Fatalf("title after append = %q", got)
	}
	if hits := search(t, x, "重试", SearchOptions{}); len(hits) != 1 {
		t.Fatalf("title search after append = %v", ids(hits))
	}

	appendFile(t, filepath.Join(home, ".codex", "session_index.jsonl"), `{"id":"c0dex","thread_name":"指数退避改名","updated_at":"2026-10-02T10:00:00Z"}`)
	update(t, x, home)
	if got := title(); got != "指数退避改名" {
		t.Fatalf("title after rename = %q", got)
	}
	if hits := search(t, x, "改名", SearchOptions{}); len(hits) != 1 {
		t.Fatalf("renamed title not searchable: %v", ids(hits))
	}
}

// 工作中插进来的话（queued_command，来源是 human）算用户消息；后台任务通知不算。
func TestClaudeQueuedPrompt(t *testing.T) {
	home := t.TempDir()
	fixtures(t, home)
	x := openIndex(t)
	update(t, x, home)
	path := filepath.Join(home, ".claude", "projects", "-work-shop", claudeSID+".jsonl")
	msgs, err := x.Transcript(context.Background(), Session{Path: path}, TranscriptOptions{})
	if err != nil {
		t.Fatal(err)
	}
	var users []string
	for _, m := range msgs {
		if m.Role == "user" {
			users = append(users, m.Text)
		}
		if strings.Contains(m.Text, "bgtasknote") {
			t.Fatalf("task notification indexed: %q", m.Text)
		}
	}
	want := []string{"顺便看看 Downloads", "[Image #1] 这个也删"}
	if len(users) < 2 || !reflect.DeepEqual(users[len(users)-2:], want) {
		t.Fatalf("user messages %q", users)
	}

	page, err := ReadChatTail("claude", "", path, 40, 0)
	if err != nil {
		t.Fatal(err)
	}
	var chat []string
	for _, m := range page.Messages {
		if m.Role != "user" {
			continue
		}
		for _, b := range m.Blocks {
			if b.Type == "text" {
				chat = append(chat, b.Text)
			}
		}
		if strings.Contains(fmt.Sprint(m.Blocks), "bgtasknote") {
			t.Fatal("task notification in chat")
		}
	}
	if len(chat) < 2 || !reflect.DeepEqual(chat[len(chat)-2:], want) {
		t.Fatalf("chat user messages %q", chat)
	}
}
