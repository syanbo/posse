package app

import (
	"testing"
	"time"

	"github.com/egoist/mygo/ui"

	"hive/internal/history"
)

// 点汇总里的标签和时间线里的名字都复制名字
func TestUsageCopyName(t *testing.T) {
	a := &app{}
	u := history.Usage{
		Skills: []history.UsageItem{{Name: "hive-tasks", Count: 2}},
		MCP:    []history.MCPUsage{{Server: "androidmirror", Count: 1}},
		Events: []history.UsageEvent{{Server: "androidmirror", Tool: "screen_observe", At: time.Now()}},
	}
	tt := ui.NewTester(func(c *ui.Context) {
		ui.Column(c).Children(func() {
			a.usageSummary(c, u)
			a.usageTimeline(c, u.Events)
		})
	}, 360, 400)
	tt.Frame()
	for _, name := range []string{"hive-tasks", "androidmirror", "screen_observe"} {
		if err := tt.Click("复制 " + name); err != nil {
			t.Fatal(err)
		}
		tt.Frame()
		if got := tt.Clipboard(); got != name {
			t.Fatalf("copied %q, want %q", got, name)
		}
	}
	if len(a.toasts) != 3 || a.toasts[2].Title != "已复制 screen_observe" {
		t.Fatalf("toasts %+v", a.toasts)
	}
}

