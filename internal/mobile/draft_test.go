package mobile

import (
	"compress/gzip"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"posse/internal/history"
	"posse/internal/term"
)

// draftRecording 是真实 Claude、Codex 跑一轮时每 200ms 读一次的屏幕（只留变了的帧），
// 以及这一轮写进会话文件的助手正文和写入时刻。before 是录制前会话里最后一条回复。
// 录法见 docs/plans/chat-draft.md；TUI 改版后重录，这个测试就是为了发现规则失效。
type draftRecording struct {
	Agent    string `json:"agent"`
	Cols     int    `json:"cols"`
	Before   string `json:"before"`
	Messages []struct {
		T    float64 `json:"t"`
		Text string  `json:"text"`
	} `json:"messages"`
	Frames []struct {
		T     float64  `json:"t"`
		Lines []string `json:"lines"`
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

func TestDraftReplay(t *testing.T) {
	files, _ := filepath.Glob("testdata/draft/*.json.gz")
	if len(files) == 0 {
		t.Fatal("no recordings")
	}
	for _, path := range files {
		t.Run(strings.TrimSuffix(filepath.Base(path), ".json.gz"), func(t *testing.T) {
			rec := loadDraftRecording(t, path)
			d := newDrafter(rec.Agent)
			d.note(assistantText(rec.Before))
			var all []string
			for _, m := range rec.Messages {
				all = append(all, draftNorm(m.Text))
			}
			next, last, shown := 0, "", 0
			for _, fr := range rec.Frames {
				// 会话文件在这一帧之前写进的正文
				for next < len(rec.Messages) && rec.Messages[next].T <= fr.T {
					// 写进文件前最后推的草稿就是这段正文。会话文件可能比屏幕上最后一两行先到
					if want, got := all[next], draftNorm(last); last != "" &&
						(!strings.HasPrefix(want, got) || len([]rune(got)) < len([]rune(want))*8/10) {
						t.Errorf("%.2fs: last draft before transcript differs\n draft: %s\n final: %s", fr.T, got, want)
					}
					d.note(assistantText(rec.Messages[next].Text))
					next++
				}
				text, changed := d.next(fr.Lines, rec.Cols, true)
				if !changed {
					continue
				}
				n := draftNorm(text)
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
				if last != "" && text != "" && !strings.HasPrefix(n, draftNorm(last)) && sameBlock(all, last, text) {
					t.Errorf("%.2fs: draft shrank\n before: %s\n after:  %s", fr.T, draftNorm(last), n)
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
	na, nb := draftNorm(a), draftNorm(b)
	for _, k := range all {
		if strings.Contains(k, na) && strings.Contains(k, nb) {
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

// 手机照帧拼出来的草稿要和电脑算出的一样；接着长的那些帧只带新加的部分。
func TestDraftFrameAppend(t *testing.T) {
	files, _ := filepath.Glob("testdata/draft/*.json.gz")
	for _, path := range files {
		t.Run(strings.TrimSuffix(filepath.Base(path), ".json.gz"), func(t *testing.T) {
			rec := loadDraftRecording(t, path)
			d := newDrafter(rec.Agent)
			d.note(assistantText(rec.Before))
			next, phone, appends, full, sent := 0, "", 0, 0, 0
			for _, fr := range rec.Frames {
				for next < len(rec.Messages) && rec.Messages[next].T <= fr.T {
					d.note(assistantText(rec.Messages[next].Text))
					next++
				}
				text, changed := d.next(fr.Lines, rec.Cols, true)
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
