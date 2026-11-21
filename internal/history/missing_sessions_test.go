package history

import (
	"context"
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/klauspost/compress/zstd"
)

func writeZst(t *testing.T, path, body string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	f, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	w, err := zstd.NewWriter(f)
	if err != nil {
		f.Close()
		t.Fatal(err)
	}
	if _, err := w.Write([]byte(body)); err != nil {
		t.Fatal(err)
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestCodexCompressedRollout(t *testing.T) {
	home := t.TempDir()
	day := filepath.Join(home, ".codex", "sessions", "2026", "10", "02")
	plain := filepath.Join(day, "rollout-2026-10-02T09-00-00-both.jsonl")
	writeFile(t, plain,
		`{"timestamp":"2026-10-02T09:00:00Z","type":"session_meta","payload":{"id":"both","cwd":"/work/api","source":"cli","thread_source":"user","history_mode":"paginated"}}`,
		`{"timestamp":"2026-10-02T09:00:01Z","type":"event_msg","payload":{"type":"item_completed","item":{"type":"UserMessage","content":[{"type":"text","text":"plainword"}]}}}`)
	writeZst(t, plain+".zst", `{"timestamp":"2026-10-02T09:00:00Z","type":"session_meta","payload":{"id":"both","cwd":"/work/api","source":"cli","thread_source":"user","history_mode":"paginated"}}`+"\n"+
		`{"timestamp":"2026-10-02T09:00:01Z","type":"event_msg","payload":{"type":"item_completed","item":{"type":"UserMessage","content":[{"type":"text","text":"compressedword"}]}}}`+"\n")
	only := filepath.Join(day, "rollout-2026-10-02T09-02-00-only.jsonl.zst")
	writeZst(t, only, `{"timestamp":"2026-10-02T09:02:00Z","type":"session_meta","payload":{"id":"only","cwd":"/work/zst","source":"cli","thread_source":"user","history_mode":"paginated"}}`+"\n"+
		`{"timestamp":"2026-10-02T09:02:01Z","type":"event_msg","payload":{"type":"item_completed","item":{"type":"UserMessage","content":[{"type":"text","text":"zstonlyword"}]}}}`+"\n")

	x := openIndex(t)
	update(t, x, home)
	if hits := search(t, x, "compressedword", SearchOptions{}); len(hits) != 0 {
		t.Fatalf("indexed both encodings: %+v", hits)
	}
	if hits := search(t, x, "plainword", SearchOptions{}); len(hits) != 1 || hits[0].ID != "both" {
		t.Fatalf("plain = %+v", hits)
	}
	hits := search(t, x, "zstonlyword", SearchOptions{})
	if len(hits) != 1 || hits[0].ID != "only" || hits[0].Cwd != "/work/zst" {
		t.Fatalf("zst = %+v", hits)
	}
	s := hits[0].Session
	if err := CheckTrashSession(context.Background(), s); err != nil {
		t.Fatal(err)
	}
	got := TrashPaths(home, s)
	if len(got) != 1 || got[0] != only {
		t.Fatalf("trash zst = %v", got)
	}
	both, err := x.Find(context.Background(), "both")
	if err != nil {
		t.Fatal(err)
	}
	got = TrashPaths(home, both)
	if len(got) != 2 {
		t.Fatalf("trash both encodings = %v", got)
	}
}

func TestAntigravityDesktopAndIDEBrains(t *testing.T) {
	home := t.TempDir()
	const desktop = "44444444-4444-4444-8444-444444444444"
	const ide = "55555555-5555-4555-8555-555555555555"
	line := func(text string) string {
		return `{"step_index":0,"type":"USER_INPUT","source":"USER_EXPLICIT","created_at":"2026-11-18T07:00:00Z","content":"<USER_REQUEST>` + text + `</USER_REQUEST>"}`
	}
	desk := filepath.Join(home, ".gemini", "antigravity", "brain", desktop, ".system_generated", "logs", "transcript.jsonl")
	cli := filepath.Join(home, ".gemini", "antigravity-cli", "brain", desktop, ".system_generated", "logs", "transcript.jsonl")
	idePath := filepath.Join(home, ".gemini", "antigravity-ide", "brain", ide, ".system_generated", "logs", "transcript.jsonl")
	writeFile(t, desk, line("desktopword"))
	writeFile(t, cli, line("oldcliword"))
	writeFile(t, idePath, line("ideword"))
	old := time.Now().Add(-time.Hour)
	if err := os.Chtimes(cli, old, old); err != nil {
		t.Fatal(err)
	}

	x := openIndex(t)
	update(t, x, home)
	ss, err := x.Sessions(context.Background(), ListOptions{Agent: "antigravity"})
	if err != nil {
		t.Fatal(err)
	}
	if len(ss) != 2 {
		t.Fatalf("sessions = %+v", ss)
	}
	if hits := search(t, x, "oldcliword", SearchOptions{}); len(hits) != 0 {
		t.Fatalf("kept older copy: %+v", hits)
	}
	if hits := search(t, x, "desktopword", SearchOptions{}); len(hits) != 1 || hits[0].Path != desk {
		t.Fatalf("desktop = %+v", hits)
	}
	if hits := search(t, x, "ideword", SearchOptions{}); len(hits) != 1 || hits[0].ID != ide {
		t.Fatalf("ide = %+v", hits)
	}
	if got := TrashPaths(home, Session{Agent: "antigravity", ID: desktop, Path: desk}); len(got) != 1 || got[0] != filepath.Dir(filepath.Dir(filepath.Dir(desk))) {
		t.Fatalf("trash desktop = %v", got)
	}
}

func TestCursorIDEChats(t *testing.T) {
	home := t.TempDir()
	dbPath := cursorIDEDatabase(home)
	if err := os.MkdirAll(filepath.Dir(dbPath), 0o755); err != nil {
		t.Fatal(err)
	}
	db, err := sql.Open("sqlite", dbPath)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`CREATE TABLE cursorDiskKV (key TEXT PRIMARY KEY, value TEXT)`); err != nil {
		t.Fatal(err)
	}
	const id = "66666666-6666-4666-8666-666666666666"
	const sub = "77777777-7777-4777-8777-777777777777"
	insert := func(key, value string) {
		t.Helper()
		if _, err := db.Exec(`INSERT INTO cursorDiskKV(key, value) VALUES(?, ?)`, key, value); err != nil {
			t.Fatal(err)
		}
	}
	insert("composerData:"+id, `{"name":"Widget chat","createdAt":1790000000000,"lastUpdatedAt":1790500000000,"workspaceIdentifier":{"uri":{"fsPath":"/work/ide"}},"fullConversationHeadersOnly":[{"bubbleId":"b1","type":1},{"bubbleId":"b2","type":2}]}`)
	insert("bubbleId:"+id+":b1", `{"type":1,"text":"where is ideonlyword"}`)
	insert("bubbleId:"+id+":b2", `{"type":2,"text":"found it","toolFormerData":{"name":"Shell","rawArgs":{"command":"rg widget"},"result":{"output":"panel.go:3"}}}`)
	insert("composerData:"+sub, `{"name":"subtask","subagentInfo":{"parentComposerId":"`+id+`"},"fullConversationHeadersOnly":[{"bubbleId":"s1","type":1}]}`)
	insert("bubbleId:"+sub+":s1", `{"type":1,"text":"subagentonlyide"}`)
	db.Close()

	x := openIndex(t)
	update(t, x, home)
	ss, err := x.Sessions(context.Background(), ListOptions{Agent: "cursor"})
	if err != nil || len(ss) != 1 || ss[0].ID != id || ss[0].Title != "Widget chat" || ss[0].Cwd != "/work/ide" {
		t.Fatalf("sessions = %+v %v", ss, err)
	}
	if hits := search(t, x, "ideonlyword", SearchOptions{}); len(hits) != 1 {
		t.Fatalf("search = %+v", hits)
	}
	if hits := search(t, x, "subagentonlyide", SearchOptions{}); len(hits) != 0 {
		t.Fatalf("subagent listed: %+v", hits)
	}
	if hits := search(t, x, "panel.go", SearchOptions{}); len(hits) != 1 || hits[0].Role != "tool" {
		t.Fatalf("tool = %+v", hits)
	}
	if got := TrashPaths(home, ss[0]); got != nil {
		t.Fatalf("trashed the IDE database: %v", got)
	}

	writeFile(t, filepath.Join(home, ".cursor", "projects", "work-ide", "agent-transcripts", id, id+".jsonl"),
		`{"role":"user","message":{"content":[{"type":"text","text":"transcriptword"}]}}`)
	update(t, x, home)
	ss, err = x.Sessions(context.Background(), ListOptions{Agent: "cursor"})
	if err != nil || len(ss) != 1 || ss[0].Path == cursorIDEPath(home, id) {
		t.Fatalf("transcript should replace the IDE copy: %+v %v", ss, err)
	}
	if hits := search(t, x, "ideonlyword", SearchOptions{}); len(hits) != 0 {
		t.Fatalf("IDE copy kept: %+v", hits)
	}
	if hits := search(t, x, "transcriptword", SearchOptions{}); len(hits) != 1 {
		t.Fatalf("transcript = %+v", hits)
	}
}

// 气泡没读完时不推进戳，已索引的对话还在；库恢复后下一次再读。
func TestCursorIDEReadFailureKeepsIndex(t *testing.T) {
	home := t.TempDir()
	dbPath := cursorIDEDatabase(home)
	if err := os.MkdirAll(filepath.Dir(dbPath), 0o755); err != nil {
		t.Fatal(err)
	}
	db, err := sql.Open("sqlite", dbPath)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`CREATE TABLE cursorDiskKV (key TEXT PRIMARY KEY, value TEXT)`); err != nil {
		t.Fatal(err)
	}
	const id = "88888888-8888-4888-8888-888888888888"
	if _, err := db.Exec(`INSERT INTO cursorDiskKV(key, value) VALUES(?, ?)`, "composerData:"+id,
		`{"name":"Kept","createdAt":1790000000000,"lastUpdatedAt":1790500000000,"fullConversationHeadersOnly":[{"bubbleId":"b1","type":1}]}`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO cursorDiskKV(key, value) VALUES(?, ?)`, "bubbleId:"+id+":b1", `{"type":1,"text":"keepthisword"}`); err != nil {
		t.Fatal(err)
	}
	db.Close()

	x := openIndex(t)
	update(t, x, home)
	var stamp string
	if err := x.db.QueryRow(`SELECT value FROM meta WHERE key='cursor-ide'`).Scan(&stamp); err != nil {
		t.Fatal(err)
	}
	saved, err := os.ReadFile(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(dbPath, []byte("not a database"), 0o644); err != nil {
		t.Fatal(err)
	}
	update(t, x, home)
	var have string
	if err := x.db.QueryRow(`SELECT value FROM meta WHERE key='cursor-ide'`).Scan(&have); err != nil {
		t.Fatal(err)
	}
	if have != stamp {
		t.Fatalf("stamp advanced on failed read: %s -> %s", stamp, have)
	}
	if hits := search(t, x, "keepthisword", SearchOptions{}); len(hits) != 1 {
		t.Fatalf("dropped indexed chat: %+v", hits)
	}
	if err := os.WriteFile(dbPath, saved, 0o644); err != nil {
		t.Fatal(err)
	}
	update(t, x, home)
	if hits := search(t, x, "keepthisword", SearchOptions{}); len(hits) != 1 || hits[0].ID != id {
		t.Fatalf("reread = %+v", hits)
	}
}

// 真实 state.vscdb 的表是 value BLOB，有的 composerData 值是 NULL；它不能让整库的 IDE 对话都收不进来。
func TestCursorIDENullComposer(t *testing.T) {
	home := t.TempDir()
	db := cursorIDEFixture(t, home)
	const id = "99999999-9999-4999-8999-999999999999"
	if _, err := db.Exec(`INSERT INTO cursorDiskKV(key, value) VALUES('composerData:00000000-0000-4000-8000-000000000000', NULL)`); err != nil {
		t.Fatal(err)
	}
	cursorIDEPut(t, db, "composerData:"+id, `{"name":"After null","lastUpdatedAt":1790500000000,"fullConversationHeadersOnly":[{"bubbleId":"b1","type":1},{"bubbleId":"b2","type":2}]}`)
	cursorIDEPut(t, db, "bubbleId:"+id+":b1", `{"type":1,"text":"afternullword"}`)
	if _, err := db.Exec(`INSERT INTO cursorDiskKV(key, value) VALUES(?, NULL)`, "bubbleId:"+id+":b2"); err != nil {
		t.Fatal(err)
	}
	db.Close()

	x := openIndex(t)
	update(t, x, home)
	if hits := search(t, x, "afternullword", SearchOptions{}); len(hits) != 1 || hits[0].ID != id {
		t.Fatalf("search = %+v", hits)
	}
}

// Cursor 开着时库戳一直在变：composerData 没变的对话不重写，变了的重读；解析规则升级时全部重读。
func TestCursorIDEUnchangedNotRewritten(t *testing.T) {
	home := t.TempDir()
	db := cursorIDEFixture(t, home)
	const id = "aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa"
	cursorIDEPut(t, db, "composerData:"+id, `{"name":"Steady","lastUpdatedAt":1790500000000,"fullConversationHeadersOnly":[{"bubbleId":"b1","type":1}]}`)
	cursorIDEPut(t, db, "bubbleId:"+id+":b1", `{"type":1,"text":"steadyword"}`)

	x := openIndex(t)
	update(t, x, home)
	// 先改掉索引里的正文：重写了会变回 steadyword，没重写才留着记号。
	if _, err := x.db.Exec(`UPDATE messages SET text='untouched' WHERE session=(SELECT id FROM sessions WHERE session_id=?)`, id); err != nil {
		t.Fatal(err)
	}
	cursorIDEPut(t, db, "unrelated", `{"x":1}`)
	bumpMtime(t, cursorIDEDatabase(home))
	update(t, x, home)
	var text string
	if err := x.db.QueryRow(`SELECT text FROM messages WHERE session=(SELECT id FROM sessions WHERE session_id=?)`, id).Scan(&text); err != nil {
		t.Fatal(err)
	}
	if text != "untouched" {
		t.Fatalf("unchanged chat rewritten: %q", text)
	}

	cursorIDEPut(t, db, "bubbleId:"+id+":b1", `{"type":1,"text":"reparsedword"}`)
	if _, err := x.db.Exec(`UPDATE meta SET value='0' WHERE key='parser:cursor'`); err != nil {
		t.Fatal(err)
	}
	update(t, x, home)
	if hits := search(t, x, "reparsedword", SearchOptions{}); len(hits) != 1 {
		t.Fatalf("parser bump did not reread IDE chats: %+v", hits)
	}

	cursorIDEPut(t, db, "composerData:"+id, `{"name":"Steady","lastUpdatedAt":1790600000000,"fullConversationHeadersOnly":[{"bubbleId":"b1","type":1},{"bubbleId":"b2","type":2}]}`)
	cursorIDEPut(t, db, "bubbleId:"+id+":b2", `{"type":2,"text":"newreplyword"}`)
	bumpMtime(t, cursorIDEDatabase(home))
	update(t, x, home)
	if hits := search(t, x, "newreplyword", SearchOptions{}); len(hits) != 1 {
		t.Fatalf("changed chat not reread: %+v", hits)
	}
	db.Close()
}

func cursorIDEFixture(t *testing.T, home string) *sql.DB {
	t.Helper()
	dbPath := cursorIDEDatabase(home)
	if err := os.MkdirAll(filepath.Dir(dbPath), 0o755); err != nil {
		t.Fatal(err)
	}
	db, err := sql.Open("sqlite", dbPath)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`CREATE TABLE cursorDiskKV (key TEXT UNIQUE ON CONFLICT REPLACE, value BLOB)`); err != nil {
		t.Fatal(err)
	}
	return db
}

func cursorIDEPut(t *testing.T, db *sql.DB, key, value string) {
	t.Helper()
	if _, err := db.Exec(`INSERT INTO cursorDiskKV(key, value) VALUES(?, ?)`, key, value); err != nil {
		t.Fatal(err)
	}
}

func bumpMtime(t *testing.T, path string) {
	t.Helper()
	at := time.Now().Add(time.Duration(time.Now().UnixNano()%1000+1) * time.Second)
	if err := os.Chtimes(path, at, at); err != nil {
		t.Fatal(err)
	}
}

// 实际 updates.jsonl 的 tool_call_update：进行中只带工具说明，completed / failed 才是结果，content 是数组。
func TestGrokToolResultShapes(t *testing.T) {
	for _, c := range []struct {
		raw  string
		want string
		ok   bool
	}{
		{`{"sessionUpdate":"tool_call_update","content":[{"type":"content","content":{"type":"text","text":"Inspect file"}}]}`, "", false},
		{`{"sessionUpdate":"tool_call_update","status":"completed","content":[{"type":"content","content":{"type":"text","text":"a.go"}},{"type":"content","content":{"type":"text","text":"b.go"}}]}`, "a.go\nb.go", true},
		{`{"sessionUpdate":"tool_call_update","status":"failed","content":[{"type":"content","content":{"type":"text","text":"SSRF blocked"}}],"rawOutput":{"error":"x"}}`, "SSRF blocked", true},
		{`{"sessionUpdate":"tool_call_update","status":"completed","rawOutput":{"type":"ListDir","Content":{"content":"- a/"}}}`, `{"type":"ListDir","Content":{"content":"- a/"}}`, true},
		{`{"sessionUpdate":"tool_call_update","status":"completed","content":{"text":"object form"}}`, "object form", true},
	} {
		var u grokUpdate
		if err := json.Unmarshal([]byte(c.raw), &u); err != nil {
			t.Fatalf("%s: %v", c.raw, err)
		}
		if got, ok := u.result(); got != c.want || ok != c.ok {
			t.Errorf("%s = %q %v, want %q %v", c.raw, got, ok, c.want, c.ok)
		}
	}
}

// 历史索引里 Grok 会话的路径是 updates.jsonl；技能用量、工具和图片扫描仍读同目录的 chat_history.jsonl。
func TestGrokScansUseSnapshot(t *testing.T) {
	home := t.TempDir()
	dir := filepath.Join(home, ".grok", "sessions", "%2Fwork", "bbbbbbbb-bbbb-4bbb-8bbb-bbbbbbbbbbbb")
	chat := filepath.Join(dir, "chat_history.jsonl")
	updates := filepath.Join(dir, "updates.jsonl")
	writeFile(t, chat,
		`{"type":"assistant","tool_calls":[{"id":"g1","name":"use_tool","arguments":"{\"tool_name\":\"cu__js\"}"}]}`,
		`{"type":"tool_result","tool_call_id":"g1","content":"Window: Posse Dev"}`)
	writeFile(t, updates,
		`{"timestamp":1790000100,"params":{"update":{"sessionUpdate":"user_message_chunk","content":{"type":"text","text":"hi"}}}}`)
	if got := TranscriptPath("grok", "", updates); got != chat {
		t.Fatalf("TranscriptPath = %s", got)
	}
	st, err := SkillStats(context.Background(), Roots{Home: home}, "", nil)
	if err != nil {
		t.Fatal(err)
	}
	if m := st.MCP["cu"]; m == nil || m.Count != 1 {
		t.Fatalf("grok MCP use = %+v", m)
	}
}

// 手机历史页按索引里的路径读对话：压过的 rollout 要解开再读，翻页的偏移前后一致。
func TestReadChatTailCompressedCodex(t *testing.T) {
	path := filepath.Join(t.TempDir(), "rollout-2026-10-02T09-02-00-zchat.jsonl.zst")
	writeZst(t, path, `{"timestamp":"2026-10-02T09:02:00Z","type":"session_meta","payload":{"id":"zchat","cwd":"/work/zst","source":"cli","thread_source":"user","history_mode":"paginated"}}`+"\n"+
		`{"timestamp":"2026-10-02T09:02:01Z","type":"event_msg","payload":{"type":"item_completed","item":{"type":"UserMessage","content":[{"type":"text","text":"first zst question"}]}}}`+"\n"+
		`{"timestamp":"2026-10-02T09:02:02Z","type":"event_msg","payload":{"type":"item_completed","item":{"type":"UserMessage","content":[{"type":"text","text":"second zst question"}]}}}`+"\n")
	page, err := ReadChatTail("codex", "", path, 1, 0)
	if err != nil || len(page.Messages) != 1 || !page.HasMore || blockText(page.Messages[0]) != "second zst question" {
		t.Fatalf("tail %+v %v", page, err)
	}
	prev, err := ReadChatTail("codex", "", path, 1, page.BeforeOffset)
	if err != nil || len(prev.Messages) != 1 || blockText(prev.Messages[0]) != "first zst question" {
		t.Fatalf("prev %+v %v", prev, err)
	}
	fi, _ := os.Stat(path)
	if msgs, next, err := ReadChatNew("codex", path, 3); err != nil || len(msgs) != 0 || next != fi.Size() {
		t.Fatalf("new = %+v %d %v", msgs, next, err)
	}
}

// 同一 promptIndex 重发的两次提问在 chat_history 里是两条，流水里也各算一条，不拼成一句。
func TestGrokResentPromptStaysSeparate(t *testing.T) {
	home := t.TempDir()
	dir := filepath.Join(home, ".grok", "sessions", "%2Fwork", "cccccccc-cccc-4ccc-8ccc-cccccccccccc")
	updates := filepath.Join(dir, "updates.jsonl")
	writeFile(t, updates,
		`{"timestamp":1790000100,"params":{"update":{"sessionUpdate":"user_message_chunk","content":{"type":"text","text":"最好补一个 firstask"},"_meta":{"promptIndex":6}}}}`,
		`{"timestamp":1790000111,"params":{"update":{"sessionUpdate":"user_message_chunk","content":{"type":"text","text":"最后补一个 secondask"},"_meta":{"promptIndex":6}}}}`,
		`{"timestamp":1790000113,"params":{"update":{"sessionUpdate":"agent_message_chunk","content":{"type":"text","text":"好的"}}}}`)
	page, err := ReadChatTail("grok", "", updates, 20, 0)
	if err != nil {
		t.Fatal(err)
	}
	if got := blockDump(page); got != "user:最好补一个 firstask | user:最后补一个 secondask | assistant:好的" {
		t.Fatalf("chat = %s", got)
	}
	x := openIndex(t)
	update(t, x, home)
	hits := search(t, x, "secondask", SearchOptions{})
	if len(hits) != 1 {
		t.Fatalf("search = %+v", hits)
	}
	msgs, err := x.Transcript(context.Background(), hits[0].Session, TranscriptOptions{})
	if err != nil || len(msgs) != 3 || msgs[0].Text != "最好补一个 firstask" || msgs[1].Text != "最后补一个 secondask" {
		t.Fatalf("index = %+v %v", msgs, err)
	}
}

// 手机按索引里的路径打开 IDE 对话：从 state.vscdb 读，带气泡时间，翻页前后一致；终端里恢复不了。
func TestCursorIDEChatOnPhone(t *testing.T) {
	home := t.TempDir()
	db := cursorIDEFixture(t, home)
	const id = "dddddddd-dddd-4ddd-8ddd-dddddddddddd"
	cursorIDEPut(t, db, "composerData:"+id, `{"name":"Phone","lastUpdatedAt":1790500000000,"fullConversationHeadersOnly":[{"bubbleId":"b1","type":1},{"bubbleId":"b2","type":2},{"bubbleId":"b3","type":2}]}`)
	cursorIDEPut(t, db, "bubbleId:"+id+":b1", `{"type":1,"text":"where is the widget","createdAt":"2026-09-25T03:55:39.991Z"}`)
	cursorIDEPut(t, db, "bubbleId:"+id+":b2", `{"type":2,"text":"","createdAt":"2026-09-25T03:55:41Z","toolFormerData":{"name":"run_terminal_cmd","rawArgs":"{\"command\":\"rg widget\"}","result":"{\"output\":\"panel.go:3\"}"}}`)
	cursorIDEPut(t, db, "bubbleId:"+id+":b3", `{"type":2,"text":"In panel.go.","createdAt":"2026-09-25T03:55:45Z"}`)
	db.Close()
	path := cursorIDEPath(home, id)
	if !IsCursorIDEChat(path) || IsCursorIDEChat(filepath.Join(home, ".cursor", "projects", "p", "agent-transcripts", id, id+".jsonl")) {
		t.Fatal("IsCursorIDEChat")
	}

	page, err := ReadChatTail("cursor", id, path, 20, 0)
	if err != nil {
		t.Fatal(err)
	}
	if got := blockDump(page); got != "user:where is the widget | assistant:run_terminal_cmd | tool:panel.go:3 | assistant:In panel.go." {
		t.Fatalf("chat = %s", got)
	}
	call := page.Messages[1].Blocks[0]
	asked := time.Date(2026, 9, 25, 3, 55, 39, 991e6, time.UTC).UnixMilli()
	if string(call.Input) != `{"command":"rg widget"}` || page.Messages[0].Timestamp == nil || *page.Messages[0].Timestamp != asked {
		t.Fatalf("call %+v ts %v", call, page.Messages[0].Timestamp)
	}
	var seen []string
	before := int64(0)
	for {
		p, err := ReadChatTail("cursor", id, path, 1, before)
		if err != nil || len(p.Messages) != 1 {
			t.Fatalf("page %+v %v", p, err)
		}
		seen = append([]string{p.Messages[0].ID}, seen...)
		if !p.HasMore {
			break
		}
		before = p.BeforeOffset
	}
	if strings.Join(seen, ",") != chatIDs(page) {
		t.Fatalf("paged %v, want %s", seen, chatIDs(page))
	}
	if _, err := ReadChatTail("cursor", "eeeeeeee", cursorIDEPath(home, "eeeeeeee"), 20, 0); err != ErrChatNotFound {
		t.Fatalf("missing chat err = %v", err)
	}

	x := openIndex(t)
	update(t, x, home)
	msgs, err := x.Transcript(context.Background(), Session{Agent: "cursor", ID: id, Path: path}, TranscriptOptions{})
	if err != nil || len(msgs) != 4 || msgs[0].At.UnixMilli() != asked || msgs[2].Text != "panel.go:3" {
		t.Fatalf("index = %+v %v", msgs, err)
	}
}

// 压过的 Codex rollout 也能统计用量、读上下文和找图片。
func TestCompressedCodexScans(t *testing.T) {
	path := filepath.Join(t.TempDir(), "rollout-2026-10-02T09-02-00-zscan.jsonl.zst")
	url := "data:image/png;base64," + base64.StdEncoding.EncodeToString([]byte("zpixels"))
	writeZst(t, path, strings.Join([]string{
		`{"type":"event_msg","payload":{"type":"item_completed","item":{"type":"McpToolCall","id":"c2","server":"cua","tool":"js","status":"completed"}}}`,
		`{"type":"response_item","payload":{"type":"function_call_output","output":[{"type":"input_image","image_url":"` + url + `"}]}}`,
		`{"type":"event_msg","payload":{"type":"token_count","info":{"last_token_usage":{"total_tokens":134806},"model_context_window":828400}}}`,
	}, "\n")+"\n")

	us := NewUsageScanner("codex", path)
	for range 2 {
		u, err := us.Scan()
		if err != nil || len(u.MCP) != 1 || u.MCP[0].Server != "cua" || u.MCP[0].Count != 1 {
			t.Fatalf("usage %+v %v", u, err)
		}
	}
	if c, ok := ReadContext("codex", path, ""); !ok || c.Window != 828400 {
		t.Fatalf("context %+v %v", c, ok)
	}
	is := NewImageScanner("codex", path)
	imgs, err := is.Scan()
	if err != nil || len(imgs) != 1 {
		t.Fatalf("images %+v %v", imgs, err)
	}
	if b, err := ReadToolImage(path, imgs[0]); err != nil || string(b) != "zpixels" {
		t.Fatalf("read %q %v", b, err)
	}
	if b, err := ReadToolImageContext(context.Background(), path, imgs[0], 1<<20); err != nil || string(b) != "zpixels" {
		t.Fatalf("read ctx %q %v", b, err)
	}
	ctxImgs, err := NewImageScanner("codex", path).ScanContext(context.Background())
	if err != nil || len(ctxImgs) != 1 {
		t.Fatalf("images ctx %+v %v", ctxImgs, err)
	}
	if again, err := is.Scan(); err != nil || len(again) != 1 {
		t.Fatalf("rescan %+v %v", again, err)
	}
}
