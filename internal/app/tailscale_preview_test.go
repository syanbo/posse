package app

import (
	"os"
	"testing"
	"time"

	"posse/internal/tslink"
)

// TestPreviewTailscale 画手机页「内置 Tailscale」卡片的几种状态：关闭、需登录、已连接、展开高级设置。
// 和 TestPreview 一样设 POSSE_PREVIEW_DIR 才画。
func TestPreviewTailscale(t *testing.T) {
	if os.Getenv("POSSE_PREVIEW_DIR") == "" {
		t.Skip("set POSSE_PREVIEW_DIR to render previews")
	}
	cases := []struct {
		name string
		ts   tailscaleView
	}{
		{"disabled", tailscaleView{
			loaded: true,
			st:     tslink.Status{Enabled: false, State: tslink.StateDisabled},
		}},
		{"needs-login", tailscaleView{
			loaded: true,
			st: tslink.Status{
				Enabled: true,
				State:   tslink.StateNeedsLogin,
				AuthURL: "https://login.tailscale.com/a/example",
			},
		}},
		{"running", tailscaleView{
			loaded: true,
			st: tslink.Status{
				Enabled:  true,
				State:    tslink.StateRunning,
				IP:       "100.64.0.12",
				Hostname: "posse-mac",
				DNSName:  "posse-mac.tail1234.ts.net",
			},
		}},
		{"advanced", tailscaleView{
			loaded:     true,
			advanced:   true,
			draftInit:  true,
			hostname:   "posse-mac",
			controlURL: "https://headscale.example.com",
			st: tslink.Status{
				Enabled:    true,
				State:      tslink.StateRunning,
				IP:         "100.64.0.12",
				Hostname:   "posse-mac",
				ControlURL: "https://headscale.example.com",
			},
		}},
	}
	for _, dark := range []bool{true, false} {
		suffix := map[bool]string{true: "dark", false: "light"}[dark]
		for _, tc := range cases {
			a := previewApp(t)
			a.selected = mobileBoardID
			now := time.Now()
			tc.ts.loadedAt = now
			a.mobile = mobileView{
				shown:     true,
				loadedAt:  now,
				endpoint:  "ws://100.64.0.12:6789",
				tailscale: tc.ts,
				relay:     relayView{loaded: true, loadedAt: now, st: relayStatus{State: "none"}},
			}
			tt := newPreviewTester(a, dark)
			tt.Frame()
			tt.Scroll(800, 500, 0, 260)
			pass(tt, 300*time.Millisecond)
			tt.Frame()
			savePNG(t, tt, "mobile-tailscale-"+tc.name+"-"+suffix+".png")
		}
	}
}
