package app

import (
	"os"
	"testing"
	"time"
)

// TestPreviewRelay 画手机页「外网中转」一栏的几种样子：没配、配好了、管理员生成了邀请码。
// 和 TestPreview 一样设 POSSE_PREVIEW_DIR 才画。
func TestPreviewRelay(t *testing.T) {
	if os.Getenv("POSSE_PREVIEW_DIR") == "" {
		t.Skip("set POSSE_PREVIEW_DIR to render previews")
	}
	cases := []struct {
		name  string
		relay relayView
	}{
		{"none", relayView{loaded: true, st: relayStatus{State: "none"}, err: "不是有效的中转邀请码"}},
		{"ready", relayView{loaded: true, st: relayStatus{Configured: true, Name: "alice", State: "ready"}}},
		{"unreachable", relayView{loaded: true, st: relayStatus{Configured: true, Name: "alice", State: "unreachable"}}},
		{"admin", relayView{loaded: true, st: relayStatus{Configured: true, Admin: true, State: "ready"},
			invite: "posse-relay:AQEPMjAzLjAuMTEzLjEwOjQ0Mwlsdnhpb2hhaQ7Kz3VxN2mWq9TfYhRb0cLpGa1EuJk4sXo"}},
	}
	for _, dark := range []bool{true, false} {
		suffix := map[bool]string{true: "dark", false: "light"}[dark]
		for _, tc := range cases {
			a := previewApp(t)
			a.selected = mobileBoardID
			// 每张图现取时间：隔久了手机页会去常驻进程重取配对码
			now := time.Now()
			tc.relay.loadedAt = now
			a.mobile = mobileView{shown: true, loadedAt: now, endpoint: "wss://orca.example.uk/v11v7w0", relay: tc.relay}
			tt := newPreviewTester(a, dark)
			tt.Frame()
			tt.Scroll(800, 500, 0, 400)
			pass(tt, 300*time.Millisecond)
			tt.Frame()
			savePNG(t, tt, "mobile-relay-"+tc.name+"-"+suffix+".png")
		}
	}
}
