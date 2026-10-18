package history

import (
	"context"
	"path/filepath"
	"testing"
)

// Codex 分段的会话合成一个；Cursor 同一个 ID 在两个项目里是两个会话。
func TestSessionParts(t *testing.T) {
	home := t.TempDir()
	day := filepath.Join(home, ".codex", "sessions", "2026", "10", "03")
	meta := func(ts, extra string) string {
		return `{"timestamp":"` + ts + `","type":"session_meta","payload":{"id":"p1","cwd":"/work/api","timestamp":"` + ts + `","source":"cli","thread_source":"user","history_mode":"paginated"` + extra + `}}`
	}
	msg := func(ts, kind, text string) string {
		return `{"timestamp":"` + ts + `","type":"event_msg","payload":{"type":"item_completed","item":{"type":"` + kind + `","content":[{"type":"text","text":"` + text + `"}]}}}`
	}
	first := filepath.Join(day, "rollout-2026-10-03T09-00-00-p1.jsonl")
	second := filepath.Join(day, "rollout-2026-10-03T10-00-00-p1_p2.jsonl")
	writeFile(t, first, meta("2026-10-03T09:00:00Z", ""),
		msg("2026-10-03T09:00:01Z", "UserMessage", "第一段提问"), msg("2026-10-03T09:00:02Z", "AgentMessage", "第一段回答"))
	writeFile(t, second, meta("2026-10-03T10:00:00Z", `,"history_base":{"thread_id":"p1","end_ordinal_exclusive":3}`),
		msg("2026-10-03T10:00:01Z", "UserMessage", "第二段提问 segmentword"), msg("2026-10-03T10:00:02Z", "AgentMessage", "第二段回答"))
	kid := "33333333-3333-4333-8333-333333333333"
	for i, proj := range []string{"proj-a", "proj-b"} {
		writeFile(t, filepath.Join(home, ".cursor", "projects", proj, "agent-transcripts", kid, kid+".jsonl"),
			`{"role":"user","message":{"content":[{"type":"text","text":"<user_query>\ncursor 对话 `+string(rune('A'+i))+`\n</user_query>"}]}}`)
	}
	x := openIndex(t)
	update(t, x, home)
	ctx := context.Background()

	ss, err := x.Sessions(ctx, ListOptions{})
	if err != nil {
		t.Fatal(err)
	}
	var codex, cursor []Session
	for _, s := range ss {
		switch s.Agent {
		case "codex":
			codex = append(codex, s)
		case "cursor":
			cursor = append(cursor, s)
		}
	}
	if len(codex) != 1 || codex[0].Messages != 4 || codex[0].Path != second || codex[0].CreatedAt.UTC().Hour() != 9 || codex[0].Title != "第一段提问" {
		t.Fatalf("codex sessions = %+v", codex)
	}
	if len(cursor) != 2 {
		t.Fatalf("cursor sessions = %+v", cursor)
	}

	s, err := x.Find(ctx, "p1")
	if err != nil || len(s.Parts) != 2 || s.Parts[0] != first || s.Parts[1] != second {
		t.Fatalf("find = %+v, %v", s, err)
	}
	msgs, err := x.Transcript(ctx, s, TranscriptOptions{})
	if err != nil {
		t.Fatal(err)
	}
	var texts []string
	for i, m := range msgs {
		if i > 0 && m.Seq <= msgs[i-1].Seq {
			t.Fatalf("seq not increasing: %+v", msgs)
		}
		texts = append(texts, m.Text)
	}
	if len(texts) != 4 || texts[0] != "第一段提问" || texts[3] != "第二段回答" {
		t.Fatalf("transcript = %q", texts)
	}
	tail, _ := x.Transcript(ctx, s, TranscriptOptions{Tail: 1})
	if len(tail) != 1 || tail[0].Text != "第二段回答" {
		t.Fatalf("tail = %+v", tail)
	}

	hits := search(t, x, "segmentword", SearchOptions{})
	if len(hits) != 1 || hits[0].Messages != 4 || hits[0].Seq != msgs[2].Seq {
		t.Fatalf("hits = %+v, want seq %d", hits, msgs[2].Seq)
	}
	if hits := search(t, x, "段", SearchOptions{}); len(hits) != 1 {
		t.Fatalf("two parts should be one hit: %+v", hits)
	}

	// 按文件找：cursor 的两个会话各是各的，codex 的任何一段都找到整个会话
	b, err := x.FindPath(ctx, filepath.Join(home, ".cursor", "projects", "proj-b", "agent-transcripts", kid, kid+".jsonl"))
	if err != nil || b.Cwd == "" && b.Title == "" || !filepath.IsAbs(b.Path) || filepath.Base(filepath.Dir(filepath.Dir(filepath.Dir(b.Path)))) != "proj-b" {
		t.Fatalf("find path cursor = %+v, %v", b, err)
	}
	p, err := x.FindPath(ctx, first)
	if err != nil || p.Path != second || len(p.Parts) != 2 {
		t.Fatalf("find path codex = %+v, %v", p, err)
	}
	st, _ := x.Stats(ctx)
	if st.ByAgent["codex"] != 1 || st.ByAgent["cursor"] != 2 {
		t.Fatalf("stats = %+v", st)
	}
}
