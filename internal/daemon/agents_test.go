package daemon

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"posse/internal/agent"
)

// 设置里关掉的 agent：新建终端、建工作区时同时启动都被拒绝，手机的列表里也没有它。
func TestDisabledAgent(t *testing.T) {
	home := t.TempDir()
	d := &Daemon{cfg: Config{Home: home}}
	d.agents.installed = []string{"claude", "codex"}
	if err := agent.SetEnabled(home, "codex", false); err != nil {
		t.Fatal(err)
	}
	if got := (mobileHost{d}).Agents(); len(got) != 1 || got[0] != "claude" {
		t.Fatalf("mobile agents = %v", got)
	}
	if err := d.checkEnabled("claude"); err != nil {
		t.Fatal(err)
	}
	raw, _ := json.Marshal(map[string]string{"project": "p", "name": "x", "agent": "codex"})
	if _, err := d.workspaceCreate(context.Background(), raw); err == nil || !strings.Contains(err.Error(), "turned off") {
		t.Fatalf("workspace create with disabled agent: %v", err)
	}
}
