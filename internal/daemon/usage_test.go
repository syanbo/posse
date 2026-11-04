package daemon

import (
	"context"
	"encoding/json"
	"slices"
	"testing"
	"time"

	"posse/internal/agent"
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

// 设置里关掉的 agent：它的读数不记、也不给界面；重新打开后照常记，之前记下的也重新给出。
func TestUsageAgentDisabled(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("CLAUDE_CONFIG_DIR", home)
	t.Setenv("CODEX_HOME", home)
	t.Setenv("GROK_HOME", home)
	d := &Daemon{}
	d.cfg.Home = home
	d.usage.store = usage.Open(home)
	d.usage.store.SetCursorOff(true)
	d.usage.store.SetCodexOff(true)
	d.usage.grok = usage.NewGrokLog(usage.GrokLogPath())
	d.usage.codexAt = map[string]time.Time{}
	d.usage.store.Update(usage.Usage{Agent: "codex", Windows: []usage.Window{{Percent: 3, Minutes: 300}}, UpdatedAt: time.Now().UnixMilli()})
	report := func() bool {
		raw, _ := json.Marshal(usageReportParams{Agent: "claude", Payload: json.RawMessage(`{"rate_limits":{"five_hour":{"used_percentage":15,"resets_at":4102444800}}}`)})
		res, err := d.usageReport(context.Background(), raw)
		if err != nil {
			t.Fatal(err)
		}
		return res.(map[string]bool)["updated"]
	}
	agents := func(st *usageStatus) []string {
		var out []string
		for _, u := range st.Usages {
			out = append(out, u.Agent)
		}
		return out
	}

	for _, n := range []string{"claude", "codex"} {
		if err := agent.SetEnabled(home, n, false); err != nil {
			t.Fatal(err)
		}
	}
	if report() {
		t.Fatal("report recorded while claude is disabled")
	}
	if got := agents(d.usageStatus()); len(got) != 0 {
		t.Fatalf("status while disabled: %v", got)
	}

	if err := agent.SetEnabled(home, "claude", true); err != nil {
		t.Fatal(err)
	}
	if !report() {
		t.Fatal("report not recorded after enabling claude")
	}
	if got := agents(d.usageStatus()); !slices.Equal(got, []string{"claude"}) {
		t.Fatalf("status with codex disabled: %v", got)
	}
	if err := agent.SetEnabled(home, "codex", true); err != nil {
		t.Fatal(err)
	}
	if got := agents(d.usageStatus()); len(got) != 2 {
		t.Fatalf("status after enabling codex: %v", got)
	}
}
