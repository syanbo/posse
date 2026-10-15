package history

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func writeFile(t *testing.T, path string, lines ...string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(strings.Join(lines, "\n")+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
}

func appendFile(t *testing.T, path string, lines ...string) {
	t.Helper()
	f, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	f.WriteString(strings.Join(lines, "\n") + "\n")
	// mtime 精度不够时追加前后可能相同，显式推后
	later := time.Now().Add(time.Second)
	os.Chtimes(path, later, later)
}

func openIndex(t *testing.T) *Index {
	t.Helper()
	x, err := Open(filepath.Join(t.TempDir(), "history.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { x.Close() })
	return x
}

func update(t *testing.T, x *Index, home string) {
	t.Helper()
	if err := x.Update(context.Background(), Roots{Home: home}, nil); err != nil {
		t.Fatal(err)
	}
}

func search(t *testing.T, x *Index, q string, o SearchOptions) []Hit {
	t.Helper()
	hits, _, err := x.Search(context.Background(), q, o)
	if err != nil {
		t.Fatal(err)
	}
	return hits
}

func ids(hits []Hit) []string {
	var out []string
	for _, h := range hits {
		out = append(out, h.Agent+":"+h.ID)
	}
	return out
}

const claudeSID = "11111111-1111-4111-8111-111111111111"

// fixtures 在 home 下写一份四家 agent 的会话文件，内容取自各自真实记录的结构。
func fixtures(t *testing.T, home string) {
	claude := filepath.Join(home, ".claude", "projects", "-work-shop", claudeSID+".jsonl")
	writeFile(t, claude,
		`{"type":"user","cwd":"/work/shop","gitBranch":"main","sessionId":"`+claudeSID+`","timestamp":"2026-10-01T10:00:00Z","message":{"role":"user","content":"帮我清理磁盘上的缓存 <system-reminder>secret reminder text</system-reminder>"}}`,
		`{"type":"user","isMeta":true,"timestamp":"2026-10-01T10:00:01Z","message":{"role":"user","content":"Base directory for this skill: injectedskillbody"}}`,
		`{"type":"assistant","timestamp":"2026-10-01T10:00:02Z","message":{"role":"assistant","model":"claude-opus-5-5","content":[{"type":"thinking","thinking":"hidden thoughts"},{"type":"text","text":"好的，先看看 Library/Caches 有多大。"},{"type":"tool_use","name":"Bash","input":{"command":"du -sh ~/Library/Caches"}}]}}`,
		`{"type":"user","timestamp":"2026-10-01T10:00:03Z","message":{"role":"user","content":[{"type":"tool_result","content":"12G\t/Users/x/Library/Caches"}]}}`,
		`{"type":"user","timestamp":"2026-10-01T10:00:04Z","message":{"role":"user","content":"<local-command-stdout>compacted output</local-command-stdout>"}}`,
		`{"type":"user","timestamp":"2026-10-01T10:00:05Z","message":{"role":"user","content":"<command-name>/review</command-name>\n<command-message>review</command-message>\n<command-args>branch-x</command-args>"}}`,
		`{"type":"ai-title","aiTitle":"磁盘清理","sessionId":"`+claudeSID+`"}`,
	)
	// 子 agent 的过程记录不算会话
	writeFile(t, filepath.Join(home, ".claude", "projects", "-work-shop", claudeSID, "subagents", "agent-x.jsonl"),
		`{"type":"user","cwd":"/work/shop","sessionId":"`+claudeSID+`","timestamp":"2026-10-01T10:00:00Z","message":{"role":"user","content":"subagentonlyword"}}`)

	codex := filepath.Join(home, ".codex", "sessions", "2026", "10", "02", "rollout-2026-10-02T09-00-00-c0dex.jsonl")
	writeFile(t, codex,
		`{"timestamp":"2026-10-02T09:00:00Z","type":"session_meta","payload":{"id":"c0dex","cwd":"/work/api","timestamp":"2026-10-02T09:00:00Z","source":"cli","thread_source":"user","history_mode":"paginated","git":{"branch":"feat"}}}`,
		`{"timestamp":"2026-10-02T09:00:01Z","type":"response_item","payload":{"type":"message","role":"user","content":[{"type":"input_text","text":"# AGENTS.md instructions\nagentsmdinjected"}]}}`,
		`{"timestamp":"2026-10-02T09:00:02Z","type":"event_msg","payload":{"type":"item_completed","item":{"type":"UserMessage","content":[{"type":"text","text":"把 webhook 的重试改成指数退避"}]}}}`,
		`{"timestamp":"2026-10-02T09:00:03Z","type":"event_msg","payload":{"type":"item_completed","item":{"type":"CommandExecution","command":["/bin/zsh","-lc","rg retry"],"aggregated_output":"src/retry.go:12"}}}`,
		`{"timestamp":"2026-10-02T09:00:04Z","type":"event_msg","payload":{"type":"item_completed","item":{"type":"FileChange","changes":{"/work/api/b.go":{"type":"update","unified_diff":"+backoffB"},"/work/api/a.go":{"type":"update","unified_diff":"+backoffA"}}}}}`,
		`{"timestamp":"2026-10-02T09:00:05Z","type":"event_msg","payload":{"type":"item_completed","item":{"type":"AgentMessage","content":[{"type":"Text","text":"已改为 exponential backoff。"}]}}}`,
	)
	writeFile(t, filepath.Join(home, ".codex", "session_index.jsonl"),
		`{"id":"c0dex","thread_name":"旧名字","updated_at":"2026-10-02T09:00:00Z"}`,
		`{"id":"c0dex","thread_name":"重试退避","updated_at":"2026-10-02T09:10:00Z"}`)
	// 自动审批的子 agent 会话不列出
	writeFile(t, filepath.Join(home, ".codex", "sessions", "2026", "10", "02", "rollout-2026-10-02T09-01-00-guard.jsonl"),
		`{"timestamp":"2026-10-02T09:01:00Z","type":"session_meta","payload":{"id":"guard","cwd":"/work/api","source":{"subagent":{"other":"guardian"}},"thread_source":"guardian_review","history_mode":"paginated"}}`,
		`{"timestamp":"2026-10-02T09:01:01Z","type":"event_msg","payload":{"type":"item_completed","item":{"type":"UserMessage","content":[{"type":"text","text":"guardianonlyword"}]}}}`)

	cursorID := "22222222-2222-4222-8222-222222222222"
	writeFile(t, filepath.Join(home, ".cursor", "projects", "work-web", "agent-transcripts", cursorID, cursorID+".jsonl"),
		`{"role":"user","message":{"content":[{"type":"text","text":"<timestamp>Friday, Sep 25, 2026, 12:17 PM (UTC+8)</timestamp>\n<user_query>\n总结一下这条推文\n</user_query>"}]}}`,
		`{"role":"assistant","message":{"content":[{"type":"text","text":"这是一篇关于 eSIM 的教程。"},{"type":"tool_use","name":"Shell","input":{"command":"curl api.fxtwitter.com"}}]}}`)
	writeFile(t, filepath.Join(home, ".cursor", "chats", "abc", cursorID, "meta.json"),
		`{"schemaVersion":1,"createdAtMs":1790000000000,"title":"eSIM 推文","updatedAtMs":1790500000000,"cwd":"/work/web"}`)

	grokDir := filepath.Join(home, ".grok", "sessions", "%2Fwork%2Fgrok", "33333333-3333-4333-8333-333333333333")
	writeFile(t, filepath.Join(grokDir, "chat_history.jsonl"),
		`{"type":"system","content":"You are Grok"}`,
		`{"type":"user","content":[{"type":"text","text":"<user_info>\nWorkspace Path: /work/grok\n</user_info>\n<rules>grokrulesinjected</rules>"}]}`,
		`{"type":"user","content":[{"type":"text","text":"<user_query>\n配置 Ghostty 主题\n</user_query>"}],"prompt_index":0}`,
		`{"type":"reasoning","summary":[],"encrypted_content":"xx"}`,
		`{"type":"assistant","model_id":"grok-4.7-build","content":"先读当前配置。","tool_calls":[{"id":"call-a-1","name":"read_file","arguments":"{\"target_file\":\"~/.config/ghostty/config\"}"}]}`,
		`{"type":"tool_result","tool_call_id":"call-a-1","content":"theme = Mocha"}`)
	// chat_history 不带时间，时间在 updates.jsonl：回复按开头对上分段输出，工具按调用 ID（可能带前缀）
	writeFile(t, filepath.Join(grokDir, "updates.jsonl"),
		`{"timestamp":1790000100,"params":{"update":{"sessionUpdate":"user_message_chunk","content":{"text":"配置 Ghostty 主题"},"_meta":{"promptIndex":0}}}}`,
		`{"timestamp":1790000105,"params":{"update":{"sessionUpdate":"agent_message_chunk","content":{"text":"先读当前"}}}}`,
		`{"timestamp":1790000106,"params":{"update":{"sessionUpdate":"agent_message_chunk","content":{"text":"配置。"}}}}`,
		`{"timestamp":1790000107,"params":{"update":{"sessionUpdate":"tool_call","toolCallId":"ws_x_call-a-1"}}}`)
	writeFile(t, filepath.Join(grokDir, "summary.json"),
		`{"info":{"id":"33333333-3333-4333-8333-333333333333","cwd":"/work/grok"},"generated_title":"Ghostty 主题","head_branch":"dev","created_at":"2026-09-01T00:00:00Z","updated_at":"2026-09-02T00:00:00Z","last_active_at":"2026-09-03T00:00:00Z"}`)
}

func TestIndexAgents(t *testing.T) {
	home := t.TempDir()
	fixtures(t, home)
	x := openIndex(t)
	update(t, x, home)
	ctx := context.Background()

	ss, err := x.Sessions(ctx, ListOptions{})
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]Session{}
	for _, s := range ss {
		got[s.Agent] = s
	}
	if len(ss) != 4 {
		t.Fatalf("sessions = %+v", ss)
	}
	for agent, want := range map[string]Session{
		"claude": {ID: claudeSID, Title: "磁盘清理", Cwd: "/work/shop", Branch: "main"},
		"codex":  {ID: "c0dex", Title: "重试退避", Cwd: "/work/api", Branch: "feat"},
		"cursor": {ID: "22222222-2222-4222-8222-222222222222", Title: "eSIM 推文", Cwd: "/work/web"},
		"grok":   {ID: "33333333-3333-4333-8333-333333333333", Title: "Ghostty 主题", Cwd: "/work/grok", Branch: "dev"},
	} {
		s := got[agent]
		if s.ID != want.ID || s.Title != want.Title || s.Cwd != want.Cwd || s.Branch != want.Branch {
			t.Errorf("%s session = %+v, want %+v", agent, s, want)
		}
	}
	if got["claude"].Model != "claude-opus-5-5" || got["grok"].Model != "grok-4.7-build" {
		t.Errorf("models: claude %q grok %q", got["claude"].Model, got["grok"].Model)
	}
	if g := got["grok"]; !g.UpdatedAt.Equal(time.Date(2026, 9, 3, 0, 0, 0, 0, time.UTC)) {
		t.Errorf("grok updated = %v", g.UpdatedAt)
	}
	if c := got["cursor"]; c.UpdatedAt.UnixMilli() != 1790500000000 {
		t.Errorf("cursor updated = %v", c.UpdatedAt)
	}

	// 注入的内容、思考过程、子 agent 和自动审批会话都不进索引
	for _, q := range []string{"secret", "injectedskillbody", "hidden", "compacted", "subagentonlyword", "agentsmdinjected", "guardianonlyword", "grokrulesinjected", "Grok"} {
		if hits := search(t, x, q, SearchOptions{}); len(hits) != 0 {
			t.Errorf("search %q = %v, want nothing", q, ids(hits))
		}
	}
	for q, want := range map[string]string{
		"磁盘":                 "claude:" + claudeSID, // 中文子串
		"caches":             "claude:" + claudeSID, // 工具调用和输出
		"/review branch-x":   "claude:" + claudeSID, // 斜杠命令
		"退避":                 "codex:c0dex",
		"backoffA":           "codex:c0dex", // 每个文件的改动单独入库
		"backoffB":           "codex:c0dex",
		"rg retry":           "codex:c0dex",
		"推文":                 "cursor:22222222-2222-4222-8222-222222222222",
		"fxtwitter":          "cursor:22222222-2222-4222-8222-222222222222",
		"mocha":              "grok:33333333-3333-4333-8333-333333333333",
		"ghost":              "grok:33333333-3333-4333-8333-333333333333", // 前缀
		"\"ghostty/config\"": "grok:33333333-3333-4333-8333-333333333333",
		"exponential":        "codex:c0dex",
	} {
		hits := search(t, x, q, SearchOptions{})
		if len(hits) != 1 || ids(hits)[0] != want {
			t.Errorf("search %q = %v, want [%s]", q, ids(hits), want)
		}
	}

	msgs, err := x.Transcript(ctx, got["codex"], TranscriptOptions{})
	if err != nil {
		t.Fatal(err)
	}
	var roles []string
	for _, m := range msgs {
		roles = append(roles, m.Role)
	}
	if strings.Join(roles, ",") != "user,tool,tool,tool,assistant" {
		t.Errorf("codex transcript roles = %v", roles)
	}
	if !strings.Contains(msgs[1].Text, "shell: rg retry") || !strings.Contains(msgs[2].Text, "a.go") {
		t.Errorf("codex transcript = %+v", msgs)
	}

	msgs, err = x.Transcript(ctx, got["grok"], TranscriptOptions{})
	if err != nil {
		t.Fatal(err)
	}
	var times []string
	for _, m := range msgs {
		times = append(times, m.Role+"@"+fmt.Sprint(m.At.Unix()))
	}
	// 用户 100、回复 105（第一段输出）、工具调用沿用回复所在记录的时间、工具结果按调用 ID 107
	if want := "user@1790000100,assistant@1790000105,tool@1790000105,tool@1790000107"; strings.Join(times, ",") != want {
		t.Errorf("grok times = %v, want %s", times, want)
	}
}

func TestSearchRanking(t *testing.T) {
	home := t.TempDir()
	fixtures(t, home)
	x := openIndex(t)
	update(t, x, home)

	// 标题命中优先；指定角色时只看这一类消息
	hits := search(t, x, "ghostty", SearchOptions{})
	if len(hits) != 1 || hits[0].Snippet == "" {
		t.Fatalf("hits = %+v", hits)
	}
	if hits := search(t, x, "mocha", SearchOptions{Role: "user"}); len(hits) != 0 {
		t.Errorf("role filter: %v", ids(hits))
	}
	if hits := search(t, x, "ghostty", SearchOptions{ListOptions: ListOptions{Agent: "claude"}}); len(hits) != 0 {
		t.Errorf("agent filter: %v", ids(hits))
	}
	if hits := search(t, x, "磁盘", SearchOptions{ListOptions: ListOptions{Cwd: "/work"}}); len(hits) != 1 {
		t.Errorf("cwd prefix filter: %v", ids(hits))
	}
	if hits := search(t, x, "磁盘", SearchOptions{ListOptions: ListOptions{Cwd: "/work/sh"}}); len(hits) != 0 {
		t.Errorf("cwd filter must match whole path segments: %v", ids(hits))
	}

	// 没有消息同时含两个词时，退而列出含任一词的
	hits, anyWord, err := x.Search(context.Background(), "磁盘 mocha", SearchOptions{})
	if err != nil || !anyWord || len(hits) != 2 {
		t.Errorf("any-word fallback: any=%v hits=%v err=%v", anyWord, ids(hits), err)
	}
}

func TestIncrementalUpdate(t *testing.T) {
	home := t.TempDir()
	fixtures(t, home)
	x := openIndex(t)
	update(t, x, home)
	ctx := context.Background()
	claude := filepath.Join(home, ".claude", "projects", "-work-shop", claudeSID+".jsonl")

	// 追加的行接着入库，已有的不重复
	appendFile(t, claude, `{"type":"user","timestamp":"2026-10-01T11:00:00Z","message":{"role":"user","content":"再查一下 Xcode DerivedData"}}`)
	update(t, x, home)
	if hits := search(t, x, "deriveddata", SearchOptions{}); len(hits) != 1 {
		t.Fatalf("appended line not indexed: %v", ids(hits))
	}
	if hits := search(t, x, "磁盘", SearchOptions{}); len(hits) != 1 || hits[0].Matches != 1 {
		t.Fatalf("existing lines duplicated: %+v", hits)
	}
	s, err := x.Find(ctx, claudeSID[:8])
	if err != nil {
		t.Fatal(err)
	}
	if want := time.Date(2026, 10, 1, 11, 0, 0, 0, time.UTC); !s.UpdatedAt.Equal(want) {
		t.Errorf("updated = %v, want %v", s.UpdatedAt, want)
	}

	// 写到一半的行先不读，写完后再入库
	f, _ := os.OpenFile(claude, os.O_APPEND|os.O_WRONLY, 0)
	f.WriteString(`{"type":"user","timestamp":"2026-10-01T12:00:00Z","message":{"role":"user","content":"halfwritten`)
	f.Close()
	update(t, x, home)
	if hits := search(t, x, "halfwritten", SearchOptions{}); len(hits) != 0 {
		t.Fatalf("partial line indexed: %v", ids(hits))
	}
	appendFile(t, claude, `line"}}`)
	update(t, x, home)
	if hits := search(t, x, "halfwrittenline", SearchOptions{}); len(hits) != 1 {
		t.Fatalf("completed line not indexed: %v", ids(hits))
	}

	// 文件被改写（内容变短或前文变了）时整份重读
	writeFile(t, claude, `{"type":"user","cwd":"/work/shop","sessionId":"`+claudeSID+`","timestamp":"2026-10-01T10:00:00Z","message":{"role":"user","content":"全新的内容 rewritten"}}`)
	update(t, x, home)
	if hits := search(t, x, "deriveddata", SearchOptions{}); len(hits) != 0 {
		t.Errorf("stale content after rewrite: %v", ids(hits))
	}
	if hits := search(t, x, "rewritten", SearchOptions{}); len(hits) != 1 {
		t.Errorf("rewritten content missing: %v", ids(hits))
	}

	// 删掉的会话从索引里消失
	os.Remove(claude)
	update(t, x, home)
	if hits := search(t, x, "rewritten", SearchOptions{}); len(hits) != 0 {
		t.Errorf("deleted session still found: %v", ids(hits))
	}
	if _, err := x.Find(ctx, claudeSID); err != ErrNotFound {
		t.Errorf("find deleted = %v", err)
	}

	// grok 的元数据变化（改名）不需要聊天记录变化
	summary := filepath.Join(home, ".grok", "sessions", "%2Fwork%2Fgrok", "33333333-3333-4333-8333-333333333333", "summary.json")
	writeFile(t, summary, `{"info":{"id":"33333333-3333-4333-8333-333333333333","cwd":"/work/grok"},"generated_title":"改过的名字","updated_at":"2026-09-05T00:00:00Z"}`)
	later := time.Now().Add(2 * time.Second)
	os.Chtimes(summary, later, later)
	update(t, x, home)
	if hits := search(t, x, "改过", SearchOptions{}); len(hits) != 1 || hits[0].Title != "改过的名字" {
		t.Errorf("renamed grok session: %+v", hits)
	}
}

func TestLongLines(t *testing.T) {
	home := t.TempDir()
	claude := filepath.Join(home, ".claude", "projects", "-p", claudeSID+".jsonl")
	// 超过读缓冲区的行要拼起来读；工具输出按字符截断
	big := strings.Repeat("长", 400<<10)
	writeFile(t, claude,
		`{"type":"user","cwd":"/p","timestamp":"2026-10-01T10:00:00Z","message":{"role":"user","content":[{"type":"tool_result","content":"start `+big+`"}]}}`,
		`{"type":"user","timestamp":"2026-10-01T10:00:01Z","message":{"role":"user","content":"after the long line"}}`)
	x := openIndex(t)
	update(t, x, home)
	if hits := search(t, x, "after", SearchOptions{}); len(hits) != 1 {
		t.Fatalf("line after a long line missing: %v", ids(hits))
	}
	msgs, _ := x.Transcript(context.Background(), Session{Path: claude}, TranscriptOptions{})
	if n := len([]rune(msgs[0].Text)); n != toolClip+1 {
		t.Errorf("tool text clipped to %d runes, want %d", n, toolClip+1)
	}
}

func TestReader(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "history.db")

	// 索引还没建：查询返回空，不报错，也不建表
	r, err := OpenReader(path)
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	if hits, _, err := r.Search(ctx, "x", SearchOptions{}); err != nil || hits != nil {
		t.Fatalf("search before index = %v, %v", hits, err)
	}
	if ss, err := r.Sessions(ctx, ListOptions{}); err != nil || ss != nil {
		t.Fatalf("sessions before index = %v, %v", ss, err)
	}
	if _, err := r.Find(ctx, "x"); err != ErrNotFound {
		t.Fatalf("find before index = %v", err)
	}

	home := t.TempDir()
	fixtures(t, home)
	w, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer w.Close()
	update(t, w, home)
	if hits, _, err := r.Search(ctx, "磁盘", SearchOptions{}); err != nil || len(hits) != 1 {
		t.Fatalf("reader after index = %v, %v", ids(hits), err)
	}

	// 别的版本建的索引：只读方不动它，报 ErrStale
	w.db.Exec(`UPDATE meta SET value='0' WHERE key='schema'`)
	if _, _, err := r.Search(ctx, "磁盘", SearchOptions{}); err != ErrStale {
		t.Fatalf("stale index: %v", err)
	}
	w.db.Exec(`UPDATE meta SET value=? WHERE key='schema'`, schemaVersion)

	// Reset 在原文件上清空，开着的只读连接看到的是空索引
	if err := w.Reset(); err != nil {
		t.Fatal(err)
	}
	if hits, _, err := r.Search(ctx, "磁盘", SearchOptions{}); err != nil || len(hits) != 0 {
		t.Fatalf("after reset = %v, %v", ids(hits), err)
	}
	update(t, w, home)
	if hits, _, _ := r.Search(ctx, "磁盘", SearchOptions{}); len(hits) != 1 {
		t.Fatalf("after rebuild = %v", ids(hits))
	}
}

func TestSnippetPrefersConversation(t *testing.T) {
	home := t.TempDir()
	claude := filepath.Join(home, ".claude", "projects", "-p", claudeSID+".jsonl")
	writeFile(t, claude,
		`{"type":"user","cwd":"/p","timestamp":"2026-10-01T10:00:00Z","message":{"role":"user","content":"整理一下秋千那场戏"}}`,
		`{"type":"user","timestamp":"2026-10-01T10:00:01Z","message":{"role":"user","content":[{"type":"tool_result","content":"秋千 秋千 秋千 秋千 秋千"}]}}`)
	x := openIndex(t)
	update(t, x, home)
	hits := search(t, x, "秋千", SearchOptions{})
	if len(hits) != 1 || hits[0].Role != "user" || hits[0].Matches != 2 {
		t.Fatalf("hits = %+v", hits)
	}
}

// 某一家的解析规则升级后，只重读这一家的文件：文件没变也要重读，会话 ID 不变，其他家不动。
func TestParserVersionReparse(t *testing.T) {
	home := t.TempDir()
	fixtures(t, home)
	grokDir := filepath.Join(home, ".grok", "sessions", "%2Fwork%2Fgrok", "33333333-3333-4333-8333-333333333333")
	updates := filepath.Join(grokDir, "updates.jsonl")
	body, err := os.ReadFile(updates)
	if err != nil {
		t.Fatal(err)
	}
	os.Remove(updates)
	x := openIndex(t)
	update(t, x, home)
	ctx := context.Background()
	grok := func() Session {
		ss, err := x.Sessions(ctx, ListOptions{})
		if err != nil {
			t.Fatal(err)
		}
		for _, s := range ss {
			if s.Agent == "grok" {
				return s
			}
		}
		t.Fatal("grok session missing")
		return Session{}
	}
	before := grok()
	msgs, _ := x.Transcript(ctx, before, TranscriptOptions{})
	if !msgs[0].At.IsZero() {
		t.Fatalf("time without updates.jsonl: %v", msgs[0].At)
	}

	// 补上时间来源；聊天记录本身没变，正常的增量更新不会重读
	os.WriteFile(updates, body, 0o644)
	update(t, x, home)
	if msgs, _ = x.Transcript(ctx, grok(), TranscriptOptions{}); !msgs[0].At.IsZero() {
		t.Fatal("reparsed without a parser change")
	}
	// 索引里记的是旧版解析规则
	x.db.Exec(`UPDATE meta SET value='1' WHERE key='parser:grok'`)
	update(t, x, home)
	after := grok()
	msgs, _ = x.Transcript(ctx, after, TranscriptOptions{})
	if msgs[0].At.Unix() != 1790000100 {
		t.Fatalf("after reparse: %+v", msgs)
	}
	if after.ID != before.ID {
		t.Fatalf("session id changed: %v -> %v", before.ID, after.ID)
	}
}
