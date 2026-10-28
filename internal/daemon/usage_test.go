package daemon

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"posse/internal/usage"
)

// 关掉套餐用量后，statusLine 报上来的读数不记、也不给界面；重新打开后照常记。
func TestUsageOff(t *testing.T) {
	// 重新打开时会像启动时一样读各家的本地记录：都指到空目录，Cursor、Codex 的联网查询关掉
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("CLAUDE_CONFIG_DIR", home)
	t.Setenv("CODEX_HOME", home)
	t.Setenv("GROK_HOME", home)
	d := &Daemon{}
	d.usage.store = usage.Open(home)
	d.usage.store.SetCursorOff(true)
	d.usage.store.SetCodexOff(true)
	d.usage.grok = usage.NewGrokLog(usage.GrokLogPath())
	d.usage.codexAt = map[string]time.Time{}
	report := func() bool {
		raw, _ := json.Marshal(usageReportParams{Agent: "claude", Payload: json.RawMessage(`{"rate_limits":{"five_hour":{"used_percentage":15,"resets_at":4102444800}}}`)})
		res, err := d.usageReport(context.Background(), raw)
		if err != nil {
			t.Fatal(err)
		}
		return res.(map[string]bool)["updated"]
	}
	set := func(off bool) {
		raw, _ := json.Marshal(map[string]bool{"off": off})
		if _, err := d.usageSet(context.Background(), raw); err != nil {
			t.Fatal(err)
		}
	}

	set(true)
	if report() {
		t.Fatal("report recorded while off")
	}
	if st := d.usageStatus(); !st.Off || len(st.Usages) != 0 {
		t.Fatalf("status while off: %+v", st)
	}
	if st, err := d.usageRefresh(context.Background(), nil); err != nil || !st.(*usageStatus).Off {
		t.Fatalf("refresh while off: %+v %v", st, err)
	}

	set(false)
	if !report() {
		t.Fatal("report not recorded after turning back on")
	}
	if st := d.usageStatus(); st.Off || len(st.Usages) != 1 {
		t.Fatalf("status after on: %+v", st)
	}
}
