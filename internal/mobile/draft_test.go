package mobile

import (
	"compress/gzip"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"posse/internal/history"
	"posse/internal/host"
	"posse/internal/term"
)

// draftRecording 是真实 agent 跑一轮时定时读到的屏幕（只留变了的帧），
// 以及这一轮写进会话文件的助手正文和写入时刻。before 是录制前会话里最后一条回复。
// 录法见 docs/plans/chat-draft.md；TUI 改版后重录，这个测试就是为了发现规则失效。
type draftRecording struct {
	Agent    string `json:"agent"`
	Cols     int    `json:"cols"`
	Before   string `json:"before"`
	Prompt   string `json:"prompt"`
	Messages []struct {
		T         float64 `json:"t"`
		Text      string  `json:"text"`
		DraftTail string  `json:"draftTail"`
	} `json:"messages"`
	Frames []struct {
		T       float64  `json:"t"`
		Lines   []string `json:"lines"`
		Working *bool    `json:"working"`
	} `json:"frames"`
}

func loadDraftRecording(t *testing.T, path string) draftRecording {
	t.Helper()
	f, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	z, err := gzip.NewReader(f)
	if err != nil {
		t.Fatal(err)
	}
	var rec draftRecording
	if err := json.NewDecoder(z).Decode(&rec); err != nil {
		t.Fatal(err)
	}
	return rec
}

func assistantText(text string) []history.ChatMessage {
	return []history.ChatMessage{{Role: "assistant", Blocks: []history.ChatBlock{{Type: "text", Text: text}}}}
}

func (rec draftRecording) drafter() *drafter {
	d := newDrafter(rec.Agent)
	d.note(assistantText(rec.Before))
	if rec.Prompt != "" {
		d.note([]history.ChatMessage{{Role: "user", Blocks: []history.ChatBlock{{Type: "text", Text: rec.Prompt}}}})
	}
	return d
}

func TestDraftReplay(t *testing.T) {
	files, _ := filepath.Glob("testdata/draft/*.json.gz")
	if len(files) == 0 {
		t.Fatal("no recordings")
	}
	for _, path := range files {
		t.Run(strings.TrimSuffix(filepath.Base(path), ".json.gz"), func(t *testing.T) {
			rec := loadDraftRecording(t, path)
			d := rec.drafter()
			norm := func(text string, screen bool) string {
				if rec.Agent == "antigravity" {
					return antigravityDraftNorm(text, screen)
				}
				return draftNorm(text)
			}
			var all []string
			for _, m := range rec.Messages {
				all = append(all, norm(m.Text, false))
			}
			next, last, shown := 0, "", 0
			for _, fr := range rec.Frames {
				// 会话文件在这一帧之前写进的正文
				for next < len(rec.Messages) && rec.Messages[next].T <= fr.T {
					want, got := all[next], norm(last, true)
					complete := len([]rune(got)) >= len([]rune(want))*8/10
					// 一次更新可能同时补上多行并结束，按落盘前最后采到的正文末尾比较。
					if tail := rec.Messages[next].DraftTail; tail != "" {
						complete = strings.HasSuffix(got, norm(tail, true))
					}
					if last != "" && (!strings.HasPrefix(want, got) || !complete) {
						t.Errorf("%.2fs: last draft before transcript differs\n draft: %s\n final: %s", fr.T, got, want)
					}
					d.note(assistantText(rec.Messages[next].Text))
					next++
				}
				text, changed := d.next(fr.Lines, rec.Cols, fr.Working == nil || *fr.Working)
				if !changed {
					continue
				}
				n := norm(text, true)
				if text != "" {
					shown++
					// 草稿里只有正文：工具、提示、状态行都不进来
					if !containsAny(all, n) {
						t.Errorf("%.2fs: draft not part of any reply:\n%s", fr.T, text)
					}
					// 写进文件后同一段不再推
					for _, k := range all[:next] {
						if strings.Contains(k, n) {
							t.Errorf("%.2fs: draft already in transcript:\n%s", fr.T, text)
						}
					}
				}
				// 同一块里只增不减
				old := norm(last, true)
				if last != "" && text != "" && !strings.HasPrefix(n, old) && sameBlock(all, old, n) {
					t.Errorf("%.2fs: draft shrank\n before: %s\n after:  %s", fr.T, old, n)
				}
				last = text
			}
			if shown == 0 {
				t.Error("no draft shown")
			}
			if text, _ := d.next(rec.Frames[len(rec.Frames)-1].Lines, rec.Cols, false); text != "" {
				t.Errorf("draft after agent stopped: %q", text)
			}
		})
	}
}

func containsAny(all []string, n string) bool {
	for _, a := range all {
		if strings.Contains(a, n) {
			return true
		}
	}
	return false
}

// sameBlock 判断两份草稿出自同一条正文
func sameBlock(all []string, a, b string) bool {
	for _, k := range all {
		if strings.Contains(k, a) && strings.Contains(k, b) {
			return true
		}
	}
	return false
}

// 声明了 posse.draft.v1 的手机在对话流里收到草稿，会话文件写进同一段后收到空的；没声明的收不到。
func TestChatDraftFrames(t *testing.T) {
	tm, err := term.Start(term.Options{ID: "t_1", Dir: t.TempDir(), Command: []string{"/bin/sh"}, Cols: 80, Rows: 24})
	if err != nil {
		t.Fatal(err)
	}
	defer tm.Close(time.Second)
	tm.SetAgent(term.AgentState{Status: "working", SessionID: "sid"})
	host := &fakeHost{terms: map[string]*term.Terminal{"t_1": tm}, order: []string{"t_1"}}
	srv, err := New(t.TempDir(), host, -1)
	if err != nil {
		t.Fatal(err)
	}
	srv.port = 0
	defer srv.Close()
	transcript := filepath.Join(t.TempDir(), "sid.jsonl")
	if err := os.WriteFile(transcript, nil, 0o644); err != nil {
		t.Fatal(err)
	}
	subscribe := func(caps []string, id string) (*phone, string) {
		info, err := srv.Pair(false, "127.0.0.1")
		if err != nil {
			t.Fatal(err)
		}
		p := dialPhone(t, srv, info)
		if caps != nil {
			result(t, p.next(p.call("runtime.clientCapabilities.update", map[string]any{
				"client": "posse-flutter", "version": 1, "capabilities": caps,
			})))
		}
		sub := p.call("nativeChat.subscribe", map[string]any{
			"agent": "claude", "sessionId": "sid", "transcriptPath": transcript, "subscriptionId": id, "limit": 5,
		})
		if result(t, p.next(sub))["type"] != "snapshot" {
			t.Fatal("missing snapshot")
		}
		return p, sub
	}
	orca, osub := subscribe(nil, "o")
	p, sub := subscribe([]string{"posse.draft.v1"}, "h")

	// 画一块 Claude 的回复和下面的输入框。printf 的八进制转义：⏺、─、❯
	tm.Send([]byte(`clear; printf '\342\217\272 hello draft\n\n\342\224\200\342\224\200\n\342\235\257 \n'` + "\r"))
	waitDraft := func(want string) {
		t.Helper()
		deadline := time.Now().Add(5 * time.Second)
		for time.Now().Before(deadline) {
			m, ok := p.nextWithin(sub, time.Until(deadline))
			if !ok {
				break
			}
			if f := result(t, m); f["type"] == "draft" && f["text"] == want {
				return
			}
		}
		t.Fatalf("no draft %q", want)
	}
	waitDraft("hello draft")
	line := `{"type":"assistant","uuid":"a1","timestamp":"2026-10-29T00:00:00Z","message":{"role":"assistant","content":[{"type":"text","text":"hello **draft**"}]}}` + "\n"
	if err := os.WriteFile(transcript, []byte(line), 0o644); err != nil {
		t.Fatal(err)
	}
	waitDraft("")

	for {
		m, ok := orca.nextWithin(osub, 300*time.Millisecond)
		if !ok {
			break
		}
		if f := result(t, m); f["type"] == "draft" {
			t.Fatalf("orca connection got %v", f)
		}
	}
}

type draftScreenTerminal struct {
	host.Terminal
	screen func() []string
}

func (t draftScreenTerminal) Screen() []string { return t.screen() }

type antigravityDraftHost struct {
	*fakeHost
	terminal host.Terminal
	switched atomic.Bool
}

func (h *antigravityDraftHost) SessionTerminal(agent, id string) host.Terminal {
	if agent == "antigravity" && id == "sid" {
		return h.terminal
	}
	return nil
}

func (h *antigravityDraftHost) TerminalSessionID(string) string {
	if h.switched.Load() {
		return "other-session"
	}
	return "sid"
}

func TestAntigravityChatDraftFrames(t *testing.T) {
	tm, err := term.Start(term.Options{ID: "t_1", Dir: t.TempDir(), Command: []string{"/bin/sh"}, Cols: 80, Rows: 24})
	if err != nil {
		t.Fatal(err)
	}
	defer tm.Close(time.Second)
	tm.SetAgent(term.AgentState{Status: "working", SessionID: "sid"})
	h := &antigravityDraftHost{fakeHost: &fakeHost{terms: map[string]*term.Terminal{"t_1": tm}}}
	var stage atomic.Int32
	h.terminal = draftScreenTerminal{Terminal: host.Wrap(tm), screen: func() []string {
		body := "  hello draft"
		if stage.Load() == 2 {
			// 模拟读屏期间真正的会话换了、标签上的 UUID 还没更新。
			h.switched.Store(true)
			body = "  foreign reply"
		}
		return []string{"> Test prompt", "  second line", "", body, "⣾  Generating...", "────", ">", "────", "esc to cancel"}
	}}
	transcript := filepath.Join(t.TempDir(), "transcript.jsonl")
	user := `{"step_index":0,"type":"USER_INPUT","content":"Test prompt\nsecond line"}` + "\n"
	appendLine := func(content string) {
		t.Helper()
		f, err := os.OpenFile(transcript, os.O_WRONLY|os.O_CREATE|os.O_APPEND, 0o600)
		if err != nil {
			t.Fatal(err)
		}
		defer f.Close()
		if _, err := f.WriteString(content); err != nil {
			t.Fatal(err)
		}
	}
	appendLine(user)
	srv, err := New(t.TempDir(), h, -1)
	if err != nil {
		t.Fatal(err)
	}
	srv.port = 0
	defer srv.Close()
	info, err := srv.Pair(false, "127.0.0.1")
	if err != nil {
		t.Fatal(err)
	}
	p := dialPhone(t, srv, info)
	result(t, p.next(p.call("runtime.clientCapabilities.update", map[string]any{
		"client": "posse-flutter", "version": 1, "capabilities": []string{"posse.draft.v1"},
	})))
	sub := p.call("nativeChat.subscribe", map[string]any{
		"agent": "antigravity", "sessionId": "sid", "transcriptPath": transcript, "limit": 5,
	})
	if result(t, p.next(sub))["type"] != "snapshot" {
		t.Fatal("missing snapshot")
	}
	waitDraft := func(want string) {
		t.Helper()
		for {
			m, ok := p.nextWithin(sub, 3*time.Second)
			if !ok {
				t.Fatalf("no draft %q", want)
			}
			if f := result(t, m); f["type"] == "draft" {
				if f["text"] != want {
					t.Fatalf("draft = %v, want %q", f, want)
				}
				return
			}
		}
	}
	waitDraft("hello draft")
	appendLine(`{"step_index":1,"type":"PLANNER_RESPONSE","content":"hello **draft**"}` + "\n")
	waitDraft("")
	appendLine(`{"step_index":2,"type":"USER_INPUT","content":"Test prompt\nsecond line"}` + "\n")
	waitDraft("hello draft")
	stage.Store(2)
	waitDraft("")
	if m, ok := p.nextWithin(sub, 600*time.Millisecond); ok {
		t.Fatalf("unexpected frame after session switched: %v", result(t, m))
	}
}

func TestAntigravityDraftRequiresReplyBoundary(t *testing.T) {
	for _, tc := range []struct {
		name   string
		screen []string
		want   string
	}{
		{"multiline user input", []string{"> First line", "  wraps here", "", "  last paragraph"}, ""},
		{"indented lines on subscribe", []string{"  wraps here", "", "  last paragraph"}, ""},
		{"expanded tool still running", []string{"● Bash(printf example) (ctrl+o to collapse)", "  ⎿  tool output"}, ""},
		{"expanded tool scrolled", []string{"     tool output", "     more content", "     last content (ctrl+o to collapse)"}, ""},
		{"tool marker in user text", []string{"> First line", "  (ctrl+o to collapse)", "", "  last paragraph"}, ""},
		{"thinking only", []string{"○ Thought for 19.8s (internal summary…)", "  summary continuation"}, ""},
		{"reply after summarized thought", []string{"○ Thought for 19.8s (internal summary…)", "  summary continuation", "", "  The channel begins."}, "The channel begins."},
		{"reply after collapsed thought", []string{"● Ran (printf example) (ctrl+o to expand)", "", "▸ Thought for 7s, 543 tokens", "", "  The channel begins."}, "The channel begins."},
	} {
		t.Run(tc.name, func(t *testing.T) {
			d := draftRecording{Agent: "antigravity", Prompt: "First line wraps here\n\nlast paragraph"}.drafter()
			screen := append(tc.screen, "⣷  Analyzing the prompt...", "└ Tip: /help", "────", "> Unsent input", "────", "esc to cancel")
			if text, changed := d.next(screen, 80, true); text != tc.want || changed != (tc.want != "") {
				t.Fatalf("draft = %q, changed = %v, want %q", text, changed, tc.want)
			}
		})
	}
}

// 手机照帧拼出来的草稿要和电脑算出的一样；接着长的那些帧只带新加的部分。
func TestDraftFrameAppend(t *testing.T) {
	files, _ := filepath.Glob("testdata/draft/*.json.gz")
	for _, path := range files {
		t.Run(strings.TrimSuffix(filepath.Base(path), ".json.gz"), func(t *testing.T) {
			rec := loadDraftRecording(t, path)
			d := rec.drafter()
			next, phone, appends, full, sent := 0, "", 0, 0, 0
			for _, fr := range rec.Frames {
				for next < len(rec.Messages) && rec.Messages[next].T <= fr.T {
					d.note(assistantText(rec.Messages[next].Text))
					next++
				}
				text, changed := d.next(fr.Lines, rec.Cols, fr.Working == nil || *fr.Working)
				if !changed {
					continue
				}
				f := d.frame(text)
				if s, ok := f["append"].(string); ok {
					appends++
					sent += len(s)
					phone += s
				} else {
					full += len(text)
					sent += len(text)
					phone = f["text"].(string)
				}
				if phone != text {
					t.Fatalf("%.2fs: phone has\n%q\nwant\n%q", fr.T, phone, text)
				}
			}
			if appends == 0 {
				t.Error("no append frames")
			}
			t.Logf("appends=%d bytes=%d (full text frames %d bytes)", appends, sent, full)
		})
	}
}
