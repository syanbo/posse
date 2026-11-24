package daemon

import (
	"context"
	"encoding/json"
	"os"
	"strings"
	"testing"

	"posse/internal/tslink"
)

func TestDaemonTailscaleRPCsAndLockGate(t *testing.T) {
	home := t.TempDir()
	d := &Daemon{cfg: Config{Home: home}}
	d.newTailscale()
	defer d.stopTailscale()

	// 尚未拿到 possed.lock（tsAllowed=false）时，set/login/reset 必须被拒绝
	if _, err := d.tailscaleSet(context.Background(), json.RawMessage(`{"enabled":true}`)); err == nil {
		t.Fatal("expected tailscale.set rejected before lock acquired")
	}
	if _, err := d.tailscaleLogin(context.Background(), nil); err == nil {
		t.Fatal("expected tailscale.login rejected before lock acquired")
	}
	if _, err := d.tailscaleReset(context.Background(), nil); err == nil {
		t.Fatal("expected tailscale.reset rejected before lock acquired")
	}

	// 拿到锁启动后默认仍为关闭（懒启动）
	d.startTailscale()
	rawSt, err := d.tailscaleStatus(context.Background(), nil)
	if err != nil {
		t.Fatalf("tailscaleStatus: %v", err)
	}
	st := rawSt.(tslink.Status)
	if st.Enabled || st.State != tslink.StateDisabled {
		t.Fatalf("initial status = %+v, want disabled", st)
	}

	// 非法 ControlURL 应返回错误且不写配置
	if _, err := d.tailscaleSet(context.Background(), json.RawMessage(`{"enabled":true,"controlUrl":"ftp://bad"}`)); err == nil {
		t.Fatal("expected invalid controlUrl rejected")
	}

	// 使用本地不可达 ControlURL 开启，不触发外网注册，验证配置落盘不含 AuthKey 且 0600
	rawSt, err = d.tailscaleSet(context.Background(), json.RawMessage(`{
		"enabled": true,
		"hostname": "posse-daemon-test",
		"controlUrl": "http://127.0.0.1:1",
		"authKey": "tskey-auth-secret-never-write",
		"disableBindInterface": true
	}`))
	if err != nil {
		t.Fatalf("tailscaleSet on: %v", err)
	}
	st = rawSt.(tslink.Status)
	if !st.Enabled || st.Hostname != "posse-daemon-test" {
		t.Fatalf("status after enable = %+v", st)
	}
	rawCfg, err := os.ReadFile(tslink.ConfigFile(home))
	if err != nil {
		t.Fatalf("read tailscale.json: %v", err)
	}
	if strings.Contains(string(rawCfg), "tskey-auth-secret-never-write") {
		t.Fatal("AuthKey must never be persisted to tailscale.json")
	}

	// 关闭节点
	rawSt, err = d.tailscaleSet(context.Background(), json.RawMessage(`{"enabled":false}`))
	if err != nil {
		t.Fatalf("tailscaleSet off: %v", err)
	}
	if st := rawSt.(tslink.Status); st.Enabled || st.State != tslink.StateDisabled {
		t.Fatalf("status after disable = %+v, want disabled", st)
	}

	// 关闭状态下保存高级设置（不开启节点），返回值必须立刻回显磁盘新配置而非旧 runtime opts
	rawSt, err = d.tailscaleSet(context.Background(), json.RawMessage(`{
		"hostname": "posse-disabled-edit",
		"controlUrl": "https://hs.example.test",
		"disableBindInterface": true
	}`))
	if err != nil {
		t.Fatalf("tailscaleSet disabled edit: %v", err)
	}
	st = rawSt.(tslink.Status)
	if st.Enabled || st.State != tslink.StateDisabled || st.Hostname != "posse-disabled-edit" ||
		st.ControlURL != "https://hs.example.test" || !st.DisableBindInterface {
		t.Fatalf("disabled set did not return persisted config: %+v", st)
	}

	// 重置节点应清理本地状态目录并将配置置为 disabled，同时回显保留的磁盘配置
	rawReset, err := d.tailscaleReset(context.Background(), nil)
	if err != nil {
		t.Fatalf("tailscaleReset: %v", err)
	}
	resetRes := rawReset.(tailscaleResetResult)
	if !resetRes.LocalCleared || resetRes.Status.Enabled ||
		resetRes.Status.Hostname != "posse-disabled-edit" ||
		resetRes.Status.ControlURL != "https://hs.example.test" ||
		!resetRes.Status.DisableBindInterface {
		t.Fatalf("unexpected reset result: %+v", resetRes)
	}

	// stopTailscale 后即便延迟调用 startTailscale（如接班进程关停时等锁协程晚到），也不得再开门禁或启动节点
	if err := tslink.SaveConfig(home, tslink.Config{Enabled: true, Hostname: "late", ControlURL: "http://127.0.0.1:1"}); err != nil {
		t.Fatal(err)
	}
	d.stopTailscale()
	d.startTailscale()
	d.tsMu.Lock()
	allowedAfterStop := d.tsAllowed
	d.tsMu.Unlock()
	if allowedAfterStop || d.ts.Status().State != tslink.StateDisabled {
		t.Fatalf("late startTailscale bypassed stop: allowed=%v status=%+v", allowedAfterStop, d.ts.Status())
	}
}
