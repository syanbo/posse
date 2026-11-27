package app

import (
	"reflect"
	"testing"

	"github.com/egoist/mygo/ui"

	"posse/internal/remote"
	"posse/internal/rpc"
	"posse/locales"
)

func TestHostMonograms(t *testing.T) {
	hosts := []remote.Host{
		{ID: "h1", Name: "Office Mac"},
		{ID: "h2", Name: "Office Mini"},
		{ID: "h3", Name: "打包机"},
		{ID: "h4", Name: "打包测试"},
	}
	got := hostMonograms(hosts)
	if got["h1"] != "OF" {
		t.Fatalf("h1 monogram = %q, want OF", got["h1"])
	}
	if got["h2"] == got["h1"] || got["h2"] != "OM" {
		t.Fatalf("h2 collision monogram = %q, want OM", got["h2"])
	}
	if got["h3"] != "打" {
		t.Fatalf("h3 monogram = %q, want 打", got["h3"])
	}
	if got["h4"] != "打包" {
		t.Fatalf("h4 collision monogram = %q, want 打包", got["h4"])
	}
}

func TestOrderedHostsUsesHostOrder(t *testing.T) {
	v, a, b := hostViewsFixture(t)
	h1, h2, h3 := remoteTestHost("a"), remoteTestHost("b"), remoteTestHost("c")
	h1.Name, h2.Name, h3.Name = "Alpha", "Beta", "Gamma"
	writeHostMenuConfig(t, a, h1, h2, h3)
	a.loadHostMenu()
	a.state.HostOrder = []string{"c", "a"}
	v.copyPreferences(b)
	if len(b.state.HostOrder) != 0 {
		t.Fatalf("HostOrder leaked into remote view state: %v", b.state.HostOrder)
	}
	got := a.orderedHosts()
	var ids []string
	for _, h := range got {
		ids = append(ids, h.ID)
	}
	if !reflect.DeepEqual(ids, []string{"c", "a", "b"}) {
		t.Fatalf("orderedHosts = %v, want [c a b]", ids)
	}
}

func TestHostDialogValidationPasteAndSave(t *testing.T) {
	k, a, _, _, _, _ := connectionFixture(t)
	m := locales.In("zh-Hans")
	existing := remoteTestHost("old")
	existing.Name = "Office"
	writeHostMenuConfig(t, a, existing)
	a.loadHostMenu()

	a.openHostDialog(remote.Host{})
	d := &a.hostDlg
	// 空目标校验
	if _, ok := a.validateHostDialog(m, d); ok || d.fieldErr["target"] == "" {
		t.Fatalf("empty target accepted: %+v", d.fieldErr)
	}
	// 粘贴完整 ssh 命令自动识别端口、用户和名称
	d.target = "ssh -p 2222 -i ~/.ssh/id_ed25519 alice@builder.local"
	a.onHostTargetChanged(m, d)
	if d.target != "alice@builder.local" || d.port != "2222" || d.name != "builder" || !d.advanced || len(d.hints) != 2 {
		t.Fatalf("parsed state = target=%q port=%q name=%q adv=%v hints=%v", d.target, d.port, d.name, d.advanced, d.hints)
	}
	// 重名校验拦住
	d.name = "Office"
	if _, ok := a.validateHostDialog(m, d); ok || d.fieldErr["name"] == "" {
		t.Fatalf("duplicate name accepted: %+v", d.fieldErr)
	}
	// 合法提交写入 hosts.json 并切到新主机（前面可能有异步 ConfigAliases 的 update 排队）
	d.name = "Builder"
	a.submitHostDialog(m, d, false)
	for i := 0; i < 4 && a.hostDlg.saving; i++ {
		awaitHostUpdate(t, a)
	}
	// 等待 dial 完成
	active := k.views.active.Load()
	if active == a {
		t.Fatalf("submit did not activate new remote host (err=%q fieldErr=%v)", a.hostDlg.err, a.hostDlg.fieldErr)
	}
	awaitHostUpdate(t, active)
	saved, err := remote.Load(a.home)
	if err != nil || len(saved) != 2 || saved[1].Name != "Builder" || saved[1].Port != 2222 {
		t.Fatalf("saved hosts = %+v (%v)", saved, err)
	}
}

func TestHostChipVisibilityAndZeroHostOpensDialog(t *testing.T) {
	_, a, b := hostViewsFixture(t)
	// 零主机且在本机：显示「添加 SSH」，点击直接打开添加弹窗
	tt := newTester(func(c *ui.Context) { a.hostChip(c) }, 400, 80)
	settle(tt)
	if !tt.HasText(locales.In("zh-Hans").HostAddShort()) {
		t.Fatalf("zero-host chip missing Add SSH: %v", tt.Texts())
	}
	if err := tt.Click(locales.In("zh-Hans").HostAddItem()); err != nil {
		t.Fatal(err)
	}
	settle(tt)
	if !a.hostDlg.open {
		t.Fatal("clicking zero-host chip did not open add dialog")
	}
	// 已有主机且在本机：隐藏不占位
	writeHostMenuConfig(t, a, remoteTestHost("office"))
	a.loadHostMenu()
	settle(tt)
	if tt.HasText(locales.In("zh-Hans").HostAddShort()) {
		t.Fatalf("local chip should hide when hosts exist: %v", tt.Texts())
	}
	// 远端正常控制时隐藏；查看中显示「查看」；连接中显示阶段文字
	b.host = newHostSession("office", "Posse")
	b.host.current = hostSnapshot{HostID: "office", Connected: true, Controlled: true, Desktop: rpc.DesktopState{InstanceID: "i", Owner: b.host.controllerID}}
	ttB := newTester(func(c *ui.Context) { b.hostChip(c) }, 400, 80)
	settle(ttB)
	if len(ttB.Texts()) != 0 {
		t.Fatalf("controlled remote chip should hide: %v", ttB.Texts())
	}
	b.host.current.Controlled = false
	settle(ttB)
	if !ttB.HasText(locales.In("zh-Hans").HostViewing()) {
		t.Fatalf("viewing remote chip missing Viewing: %v", ttB.Texts())
	}
	b.connecting, b.connectStage, b.connectPct = true, remote.StageUpload, 0.42
	settle(ttB)
	if !ttB.HasText(locales.In("zh-Hans").HostStageUpload(42)) {
		t.Fatalf("uploading chip missing stage text: %v", ttB.Texts())
	}
}

func TestHostDialogEscapeAndMinWindowButtons(t *testing.T) {
	_, a, _ := hostViewsFixture(t)
	m := locales.In("zh-Hans")
	a.hostDlg = hostDialogState{
		open:        true,
		gen:         1,
		advanced:    true,
		aliasesDone: true,
		aliases: []remote.Alias{
			{Name: "mac-1", HostName: "10.0.0.1"},
			{Name: "mac-2", HostName: "10.0.0.2"},
			{Name: "mac-3", HostName: "10.0.0.3"},
			{Name: "mac-4", HostName: "10.0.0.4"},
			{Name: "mac-5", HostName: "10.0.0.5"},
			{Name: "mac-6", HostName: "10.0.0.6"},
		},
		fieldErr: map[string]string{"target": "err", "user": "err", "port": "err", "home": "err", "exe": "err"},
		err:      "save failed",
	}
	tt := newTester(func(c *ui.Context) { a.hostDialog(c) }, 900, 560)
	settle(tt)
	// 最小窗口 900×560 填满候选、高级设置与错误时，底部按钮仍必须在可视区域内且可点
	if !tt.HasText(m.HostAddConnect()) || !tt.HasText(m.CommonCancel()) {
		t.Fatalf("dialog action buttons missing at 900x560: %v", tt.Texts())
	}
	// 保存中按 Escape 不可关窗
	a.hostDlg.saving = true
	settle(tt)
	tt.Key(0, ui.KeyEscape)
	settle(tt)
	if !a.hostDlg.open {
		t.Fatal("Escape closed hostDialog while saving")
	}
	// 非保存状态下按 Escape 正常关闭
	a.hostDlg.saving = false
	settle(tt)
	tt.Key(0, ui.KeyEscape)
	settle(tt)
	if a.hostDlg.open {
		t.Fatal("Escape did not close hostDialog")
	}
	// 重新打开后点击取消按钮也能在 900×560 下点击生效
	a.hostDlg.open = true
	settle(tt)
	if err := tt.Click(m.CommonCancel()); err != nil {
		t.Fatalf("Cancel button not clickable at 900x560: %v", err)
	}
	settle(tt)
	if a.hostDlg.open {
		t.Fatal("clicking Cancel did not close hostDialog")
	}
}

func TestSelectLocalHostFromLocalDevicePageResetsRemoteTarget(t *testing.T) {
	v, a, b := hostViewsFixture(t)
	v.activate(b.viewID)
	// 从远端跳到本机设备页（如设置页）：active 是本机 a，但 target 仍是远端 b
	b.openBoard(settingsBoardID)
	if v.active.Load() != a || v.target != b {
		t.Fatalf("setup unexpected: active=%s target=%s", v.active.Load().viewID, v.target.viewID)
	}
	// 切回本机时必须把 target 也重设为本机，不能误判已在显示本机而跳过
	a.selectLocalHost()
	if v.active.Load() != a || v.target != a {
		t.Fatalf("selectLocalHost left target=%s, want local", v.target.viewID)
	}
}
