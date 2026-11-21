package history

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestNestSpawnedSessions(t *testing.T) {
	home := t.TempDir()
	parentID := "aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa"
	childID := "bbbbbbbb-bbbb-4bbb-8bbb-bbbbbbbbbbbb"
	writeFile(t, filepath.Join(home, ".codex", "sessions", "2026", "10", "02", "rollout-parent.jsonl"),
		`{"timestamp":"2026-10-02T09:00:00Z","type":"session_meta","payload":{"id":"parent1","cwd":"/work/api","source":"cli","thread_source":"user"}}`,
		`{"timestamp":"2026-10-02T09:00:01Z","type":"response_item","payload":{"type":"message","role":"user","content":[{"type":"input_text","text":"parentonlyword"}]}}`,
		`{"timestamp":"2026-10-02T09:00:02Z","type":"response_item","payload":{"type":"function_call_output","output":"{\"task_name\":\"/root/review_issue17\"}"}}`,
	)
	writeFile(t, filepath.Join(home, ".codex", "sessions", "2026", "10", "02", "rollout-child.jsonl"),
		`{"timestamp":"2026-10-02T09:01:00Z","type":"session_meta","payload":{"id":"child1","cwd":"/work/api","source":{"subagent":{"thread_spawn":{"parent_thread_id":"parent1","agent_path":"/root/review_issue17","agent_nickname":"Wegener"}}},"thread_source":"subagent","subagent_history_start_ordinal":2}}`,
		`{"timestamp":"2026-10-02T09:01:01Z","ordinal":1,"type":"response_item","payload":{"type":"message","role":"user","content":[{"type":"input_text","text":"inheritedonlyword"}]}}`,
		`{"timestamp":"2026-10-02T09:01:02Z","ordinal":2,"type":"response_item","payload":{"type":"message","role":"user","content":[{"type":"input_text","text":"childonlyword"}]}}`,
	)
	writeFile(t, filepath.Join(home, ".cursor", "projects", "work", "agent-transcripts", parentID, parentID+".jsonl"),
		`{"role":"user","message":{"content":[{"type":"text","text":"<user_query>cursorparentonly</user_query>"}]}}`,
		`{"role":"assistant","message":{"content":[{"type":"tool_use","name":"Task","input":{"prompt":"look `+childID+`"}}]}}`,
	)
	writeFile(t, filepath.Join(home, ".cursor", "projects", "work", "agent-transcripts", parentID, "subagents", childID+".jsonl"),
		`{"role":"user","message":{"content":[{"type":"text","text":"<user_query>cursorchildonly</user_query>"}]}}`,
	)
	grokParent := filepath.Join(home, ".grok", "sessions", "proj", "grokparent")
	grokChild := filepath.Join(home, ".grok", "sessions", "proj", "grokchild")
	writeFile(t, filepath.Join(grokParent, "updates.jsonl"),
		`{"timestamp":1790000000000,"params":{"update":{"sessionUpdate":"user_message_chunk","content":{"type":"text","text":"grokparentonly"}}}}`)
	writeFile(t, filepath.Join(grokParent, "summary.json"),
		`{"info":{"id":"grokparent","cwd":"/work/grok"},"generated_title":"Parent plan"}`)
	writeFile(t, filepath.Join(grokParent, "subagents", "one", "meta.json"),
		`{"parent_session_id":"grokparent","child_session_id":"grokchild"}`)
	writeFile(t, filepath.Join(grokChild, "updates.jsonl"),
		`{"timestamp":1790000001000,"params":{"update":{"sessionUpdate":"user_message_chunk","content":{"type":"text","text":"grokchildonly"}}}}`)
	writeFile(t, filepath.Join(grokChild, "summary.json"),
		`{"info":{"id":"grokchild","cwd":"/work/grok"},"generated_title":"Child task"}`)

	x := openIndex(t)
	update(t, x, home)
	ctx := context.Background()
	ss, err := x.Sessions(ctx, ListOptions{})
	if err != nil {
		t.Fatal(err)
	}
	byAgent := map[string]Session{}
	for _, s := range ss {
		byAgent[s.Agent] = s
	}
	if len(ss) != 3 {
		t.Fatalf("roots = %+v", ss)
	}
	codex := byAgent["codex"]
	if codex.ID != "parent1" || len(codex.Children) != 1 || codex.Children[0].ID != "child1" || codex.Children[0].Title != "review_issue17" {
		t.Fatalf("codex = %+v", codex)
	}
	cursor := byAgent["cursor"]
	if cursor.ID != parentID || len(cursor.Children) != 1 || cursor.Children[0].ID != childID {
		t.Fatalf("cursor = %+v", cursor)
	}
	grok := byAgent["grok"]
	if grok.ID != "grokparent" || len(grok.Children) != 1 || grok.Children[0].ID != "grokchild" || grok.Children[0].Title != "Child task" {
		t.Fatalf("grok = %+v", grok)
	}
	if hits := search(t, x, "inheritedonlyword", SearchOptions{}); len(hits) != 0 {
		t.Fatalf("copied parent history indexed: %+v", hits)
	}
	if hits := search(t, x, "childonlyword", SearchOptions{}); len(hits) != 1 || hits[0].ID != "child1" {
		t.Fatalf("child search = %+v", hits)
	}
	if hits := search(t, x, "cursorchildonly", SearchOptions{}); len(hits) != 1 || hits[0].Parent != parentID {
		t.Fatalf("cursor child search = %+v", hits)
	}
	msgs, err := x.Transcript(ctx, codex, TranscriptOptions{})
	if err != nil {
		t.Fatal(err)
	}
	var linked bool
	for _, m := range msgs {
		if m.Child == "child1" && m.ChildPath != "" {
			linked = true
		}
	}
	if !linked {
		t.Fatalf("spawn output not linked: %+v", msgs)
	}
	msgs, err = x.Transcript(ctx, cursor, TranscriptOptions{})
	if err != nil {
		t.Fatal(err)
	}
	linked = false
	for _, m := range msgs {
		if m.Child == childID {
			linked = true
		}
	}
	if !linked {
		t.Fatalf("cursor task not linked: %+v", msgs)
	}
}

// Codex 子线程续写出第二段，续写的 session_meta 不带 thread_spawn：合并后仍挂在父线程下，统计不算成顶层。
func TestNestCodexContinuation(t *testing.T) {
	home := t.TempDir()
	day := filepath.Join(home, ".codex", "sessions", "2026", "10", "04")
	writeFile(t, filepath.Join(day, "rollout-2026-10-04T08-00-00-parent2.jsonl"),
		`{"timestamp":"2026-10-04T08:00:00Z","type":"session_meta","payload":{"id":"parent2","cwd":"/work/api","source":"cli","thread_source":"user"}}`,
		`{"timestamp":"2026-10-04T08:00:01Z","type":"response_item","payload":{"type":"message","role":"user","content":[{"type":"input_text","text":"parenttask"}]}}`,
		`{"timestamp":"2026-10-04T08:00:02Z","type":"response_item","payload":{"type":"function_call_output","output":"{\"task_name\":\"/root/fix_cache\"}"}}`,
	)
	meta := func(ts, source, extra string) string {
		return `{"timestamp":"` + ts + `","type":"session_meta","payload":{"id":"kid2","cwd":"/work/api","timestamp":"` + ts + `","source":` + source + `,"history_mode":"paginated"` + extra + `}}`
	}
	msg := func(ts, text string) string {
		return `{"timestamp":"` + ts + `","type":"event_msg","payload":{"type":"item_completed","item":{"type":"UserMessage","content":[{"type":"text","text":"` + text + `"}]}}}`
	}
	first := filepath.Join(day, "rollout-2026-10-04T08-01-00-kid2.jsonl")
	second := filepath.Join(day, "rollout-2026-10-04T09-00-00-kid2_p2.jsonl")
	writeFile(t, first,
		meta("2026-10-04T08:01:00Z", `{"subagent":{"thread_spawn":{"parent_thread_id":"parent2","agent_path":"/root/fix_cache","agent_nickname":"Noether"}}},"thread_source":"subagent"`, ""),
		msg("2026-10-04T08:01:01Z", "kidfirstpart"))
	writeFile(t, second,
		meta("2026-10-04T09:00:00Z", `"cli","thread_source":"user"`, `,"history_base":{"thread_id":"kid2","end_ordinal_exclusive":2}`),
		msg("2026-10-04T09:00:01Z", "kidsecondpart"))

	x := openIndex(t)
	update(t, x, home)
	ctx := context.Background()
	ss, err := x.Sessions(ctx, ListOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if len(ss) != 1 || ss[0].ID != "parent2" || len(ss[0].Children) != 1 {
		t.Fatalf("roots = %+v", ss)
	}
	kid := ss[0].Children[0]
	if kid.ID != "kid2" || kid.Path != second || kid.Messages != 2 || kid.Parent != "parent2" {
		t.Fatalf("child = %+v", kid)
	}
	if st, _ := x.Stats(ctx); st.ByAgent["codex"] != 1 || st.Sessions != 1 {
		t.Fatalf("stats = %+v", st)
	}
	if hits := search(t, x, "kidsecondpart", SearchOptions{}); len(hits) != 1 || hits[0].ID != "kid2" || hits[0].Parent != "parent2" {
		t.Fatalf("search = %+v", hits)
	}
	msgs, err := x.Transcript(ctx, ss[0], TranscriptOptions{})
	if err != nil {
		t.Fatal(err)
	}
	var linked bool
	for _, m := range msgs {
		if m.Child == "kid2" && m.ChildPath == second {
			linked = true
		}
	}
	if !linked {
		t.Fatalf("spawn output not linked to the latest part: %+v", msgs)
	}
}

// Cursor 同一个会话 ID 出现在两个项目里：subagents 下的子会话各挂在自己目录的父会话下。
func TestNestCursorSameIDTwoProjects(t *testing.T) {
	home := t.TempDir()
	parentID := "cccccccc-cccc-4ccc-8ccc-cccccccccccc"
	kids := map[string]string{"proj-a": "dddddddd-dddd-4ddd-8ddd-dddddddddddd", "proj-b": "eeeeeeee-eeee-4eee-8eee-eeeeeeeeeeee"}
	base := time.Date(2026, 10, 5, 9, 0, 0, 0, time.UTC)
	for i, proj := range []string{"proj-a", "proj-b"} {
		dir := filepath.Join(home, ".cursor", "projects", proj, "agent-transcripts", parentID)
		parent := filepath.Join(dir, parentID+".jsonl")
		kid := filepath.Join(dir, "subagents", kids[proj]+".jsonl")
		writeFile(t, parent,
			`{"role":"user","message":{"content":[{"type":"text","text":"<user_query>parent `+proj+`</user_query>"}]}}`,
			`{"role":"assistant","message":{"content":[{"type":"tool_use","name":"Task","input":{"prompt":"look `+kids[proj]+`"}}]}}`)
		writeFile(t, kid, `{"role":"user","message":{"content":[{"type":"text","text":"<user_query>child `+proj+`</user_query>"}]}}`)
		// proj-b 的父会话更新得更近，只按 ID 找父会话时两个子会话都会挂到它下面
		at := base.Add(time.Duration(i) * time.Hour)
		for _, p := range []string{kid, parent} {
			if err := os.Chtimes(p, at, at); err != nil {
				t.Fatal(err)
			}
		}
	}
	x := openIndex(t)
	update(t, x, home)
	ctx := context.Background()
	ss, err := x.Sessions(ctx, ListOptions{Agent: "cursor"})
	if err != nil {
		t.Fatal(err)
	}
	if len(ss) != 2 {
		t.Fatalf("roots = %+v", ss)
	}
	for _, s := range ss {
		proj := filepath.Base(filepath.Dir(filepath.Dir(filepath.Dir(s.Path))))
		if s.ID != parentID || len(s.Children) != 1 || s.Children[0].ID != kids[proj] {
			t.Fatalf("%s: %+v", proj, s)
		}
		msgs, err := x.Transcript(ctx, s, TranscriptOptions{})
		if err != nil {
			t.Fatal(err)
		}
		for _, m := range msgs {
			if m.Child != "" && m.Child != kids[proj] {
				t.Fatalf("%s linked %s", proj, m.Child)
			}
		}
	}
	if st, _ := x.Stats(ctx); st.ByAgent["cursor"] != 2 {
		t.Fatalf("stats = %+v", st)
	}
}

// 父会话很久没动、子会话刚更新：顶层按整棵树最近的时间排，Limit 截的是顶层，行上的时间仍是各自的。
func TestNestLatestOrdering(t *testing.T) {
	home := t.TempDir()
	proj := filepath.Join(home, ".claude", "projects", "-work-app")
	parentID := "11111111-aaaa-4aaa-8aaa-aaaaaaaaaaaa"
	otherID := "22222222-bbbb-4bbb-8bbb-bbbbbbbbbbbb"
	line := func(sid, ts, text, extra string) string {
		return `{"type":"user","cwd":"/work/app","sessionId":"` + sid + `","timestamp":"` + ts + `"` + extra + `,"message":{"role":"user","content":"` + text + `"}}`
	}
	writeFile(t, filepath.Join(proj, parentID+".jsonl"), line(parentID, "2026-10-01T10:00:00Z", "old parent", ""))
	writeFile(t, filepath.Join(proj, otherID+".jsonl"), line(otherID, "2026-10-05T10:00:00Z", "unrelated", ""))
	writeFile(t, filepath.Join(proj, parentID, "subagents", "agent-a1b2c3.jsonl"),
		line(parentID, "2026-10-09T10:00:00Z", "fresh child", `,"isSidechain":true,"agentId":"a1b2c3"`))

	x := openIndex(t)
	update(t, x, home)
	ctx := context.Background()
	ss, err := x.Sessions(ctx, ListOptions{Limit: 1})
	if err != nil {
		t.Fatal(err)
	}
	if len(ss) != 1 || ss[0].ID != parentID || len(ss[0].Children) != 1 || ss[0].Children[0].ID != "a1b2c3" {
		t.Fatalf("limit 1 = %+v", ss)
	}
	if got := ss[0].UpdatedAt.UTC().Day(); got != 1 {
		t.Fatalf("parent shows its own time, got day %d", got)
	}
	if got := ss[0].Latest().UTC().Day(); got != 9 {
		t.Fatalf("latest = day %d", got)
	}
	ss, err = x.Sessions(ctx, ListOptions{Limit: 2})
	if err != nil {
		t.Fatal(err)
	}
	if len(ss) != 2 || ss[0].ID != parentID || ss[1].ID != otherID || len(ss[1].Children) != 0 {
		t.Fatalf("limit 2 = %+v", ss)
	}
	if st, _ := x.Stats(ctx); st.Sessions != 2 {
		t.Fatalf("stats = %+v", st)
	}
}
