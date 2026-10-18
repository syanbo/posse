package mobile

import (
	"testing"
	"time"

	"hive/internal/term"
)

func TestAgentStatus(t *testing.T) {
	for _, c := range []struct {
		screen []string
		want   string
	}{
		{[]string{"⏺ 好的", "", "✽ Puzzling… (1m 54s · ↓ 9.5k tokens · thought for 1s)", "", "> "}, "Puzzling… (1m 54s · ↓ 9.5k tokens)"},
		{[]string{"· Puzzling… (1m 55s · ↓ 9.5k tokens)"}, "Puzzling… (1m 55s · ↓ 9.5k tokens)"},
		{[]string{"✻ Thinking… (3s · esc to interrupt)"}, "Thinking… (3s)"},
		{[]string{"✢ Thinking…"}, "Thinking…"},
		{[]string{"• Working (12s • esc to interrupt)"}, "Working (12s)"},
		// 正文有缩进；干完之后的总结没有省略号；没有按键提示的圆点列表不是 Codex 的状态
		{[]string{"  · Puzzling… (3s)", "✻ Worked for 1m 2s", "• Fixed (the bug)"}, ""},
		{nil, ""},
	} {
		if got := agentStatus(c.screen); got != c.want {
			t.Errorf("agentStatus(%q) = %q, want %q", c.screen, got, c.want)
		}
	}
}

// 声明了 hive.status.v1 的手机在对话流里收到状态行，状态行消失时收到空的；没声明的收不到。
func TestChatStatusFrames(t *testing.T) {
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
	info, err := srv.Pair(false, "127.0.0.1")
	if err != nil {
		t.Fatal(err)
	}
	orca := dialPhone(t, srv, info)
	osub := orca.call("nativeChat.subscribe", map[string]any{"agent": "claude", "sessionId": "sid", "subscriptionId": "o", "limit": 5})
	if result(t, orca.next(osub))["type"] != "snapshot" {
		t.Fatal("missing snapshot")
	}

	info, err = srv.Pair(false, "127.0.0.1")
	if err != nil {
		t.Fatal(err)
	}
	p := dialPhone(t, srv, info)
	result(t, p.next(p.call("runtime.clientCapabilities.update", map[string]any{
		"client": "hive-flutter", "version": 1, "capabilities": []string{"hive.status.v1"},
	})))
	sub := p.call("nativeChat.subscribe", map[string]any{"agent": "claude", "sessionId": "sid", "subscriptionId": "h", "limit": 5})
	if result(t, p.next(sub))["type"] != "snapshot" {
		t.Fatal("missing snapshot")
	}

	// printf 的八进制转义：✻ 和 … 和 ·
	tm.Send([]byte(`printf '\342\234\273 Puzzling\342\200\246 (3s \302\267 esc to interrupt)\n'` + "\r"))
	waitStatus := func(want string) {
		t.Helper()
		deadline := time.Now().Add(5 * time.Second)
		for time.Now().Before(deadline) {
			m, ok := p.nextWithin(sub, time.Until(deadline))
			if !ok {
				break
			}
			if f := result(t, m); f["type"] == "status" && f["text"] == want {
				return
			}
		}
		t.Fatalf("no status %q", want)
	}
	waitStatus("Puzzling… (3s)")
	tm.Send([]byte("clear\r"))
	waitStatus("")

	for {
		m, ok := orca.nextWithin(osub, 300*time.Millisecond)
		if !ok {
			break
		}
		if f := result(t, m); f["type"] == "status" {
			t.Fatalf("orca connection got %v", f)
		}
	}
}
