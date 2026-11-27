package app

import (
	"reflect"
	"slices"
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
	d.icon = "🖥️"
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
	if err != nil || len(saved) != 2 || saved[1].Name != "Builder" || saved[1].Port != 2222 || saved[1].Icon != "🖥️" {
		t.Fatalf("saved hosts = %+v (%v)", saved, err)
	}
	// 仅改自定义图标不应触发断连重连
	a.openHostDialog(saved[1])
	a.hostDlg.icon = "⚡"
	if a.editingLiveConnectionChanged(&a.hostDlg) {
		t.Fatal("changing only host icon triggered live reconnect")
	}
	// 图标读取拒绝路径穿越且不缓存损坏项
	if img := a.hostIconImage("../escape.png"); img != nil {
		t.Fatal("hostIconImage accepted path traversal")
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
	// 已有主机且在本机正常控制时隐藏；但若本机意外退回只读查看态，仍必须显示查看芯片
	writeHostMenuConfig(t, a, remoteTestHost("office"))
	a.loadHostMenu()
	settle(tt)
	if tt.HasText(locales.In("zh-Hans").HostAddShort()) {
		t.Fatalf("local chip should hide when hosts exist: %v", tt.Texts())
	}
	a.host = newHostSession(localHostID, "Posse")
	a.host.current = hostSnapshot{HostID: localHostID, Connected: true, Controlled: false, Desktop: rpc.DesktopState{InstanceID: "local-i"}}
	settle(tt)
	if !tt.HasText(locales.In("zh-Hans").HostViewing()) {
		t.Fatalf("local viewing chip hidden when remote hosts exist: %v", tt.Texts())
	}
	a.host.current.Controlled = true
	settle(tt)
	if len(tt.Texts()) != 0 {
		t.Fatalf("controlled local chip should hide when remote hosts exist: %v", tt.Texts())
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

func TestHostMenuTakeAndStatusDotStates(t *testing.T) {
	_, a, b := hostViewsFixture(t)
	m := locales.In("zh-Hans")
	a.host = newHostSession(localHostID, "Posse")
	b.host = newHostSession("remote", "Posse")

	hasLabel := func(build func(*popMenu), label string) bool {
		seq := 0
		pm := newPopMenu(&seq, -1)
		build(pm)
		for _, item := range pm.items {
			if item.label == label {
				return true
			}
		}
		return false
	}

	// 本机未连接时不显示禁用的「接管控制」
	a.host.current = hostSnapshot{HostID: localHostID, Connected: false, Controlled: false}
	if hasLabel(a.hostActionsBuild(m, remote.Host{ID: localHostID}, true), m.HostTake()) ||
		hasLabel(a.hostSwitchBuild(m), m.HostTake()) {
		t.Fatal("disconnected local host menu unexpectedly showed Take control")
	}
	// 本机处于已连接只读态时显示「接管控制」，不显示「释放控制」
	a.host.current = hostSnapshot{HostID: localHostID, Connected: true, Controlled: false}
	if !hasLabel(a.hostActionsBuild(m, remote.Host{ID: localHostID}, true), m.HostTake()) ||
		!hasLabel(a.hostSwitchBuild(m), m.HostTake()) {
		t.Fatal("viewing local host menu missing Take control")
	}
	// 本机正常控制态既不显示「释放控制」也不显示「接管控制」
	a.host.current = hostSnapshot{HostID: localHostID, Connected: true, Controlled: true}
	if hasLabel(a.hostActionsBuild(m, remote.Host{ID: localHostID}, true), m.HostRelease()) ||
		hasLabel(a.hostActionsBuild(m, remote.Host{ID: localHostID}, true), m.HostTake()) {
		t.Fatal("controlled local host menu should omit Release and Take")
	}

	// 远端磁贴右下角连接状态枚举核对
	if got := a.remoteHostConnState("remote"); got != hostConnOffline {
		t.Fatalf("offline state = %v, want offline", got)
	}
	b.err = "ssh timeout"
	if got := a.remoteHostConnState("remote"); got != hostConnError {
		t.Fatalf("error state = %v, want error", got)
	}
	b.connecting = true
	if got := a.remoteHostConnState("remote"); got != hostConnConnecting {
		t.Fatalf("connecting state = %v, want connecting", got)
	}
	b.connecting = false
	b.host.current = hostSnapshot{HostID: "remote", Connected: true, Controlled: false}
	if got := a.remoteHostConnState("remote"); got != hostConnViewing {
		t.Fatalf("viewing state = %v, want viewing", got)
	}
	b.host.current.Controlled = true
	if got := a.remoteHostConnState("remote"); got != hostConnOnline {
		t.Fatalf("online state = %v, want online", got)
	}
}

func TestRemoteDisconnectedCardAndStatusBar(t *testing.T) {
	k, a, b, _, base, _ := connectionFixture(t)
	base.Platform = "linux"
	h := remoteTestHost("remote")
	h.Name = "bwh"
	writeHostMenuConfig(t, a, h)
	a.loadHostMenu()

	a.openHost(h)
	awaitHostUpdate(t, b)
	if !b.host.snapshot().Connected || b.targetPlatform() != "linux" {
		t.Fatalf("remote did not capture linux platform: snap=%+v platform=%q", b.host.snapshot(), b.targetPlatform())
	}
	b.loaded = true
	b.daemon.FootprintKB = 43 * 1024
	b.daemon.Running = 1
	b.daemon.Awake = &awakeStatus{Mode: "auto"}
	b.workspaces = []workspace{{ID: "w1", Project: "ai", Label: "ai", Path: "/home/dev/ai", Terminals: []terminalInfo{{ID: "t1", Title: "bash"}}}}
	b.selected = "w1"
	b.activeTerm = map[string]string{"w1": "t1"}

	// 主动断开后仍记住 linux 平台，且主区域直接显示「已断开 bwh」和「重新连接」卡片，不回落到空工作区。
	k.disconnect(h.ID)
	if b.targetPlatform() != "linux" {
		t.Fatalf("targetPlatform lost linux after disconnect: got %q", b.targetPlatform())
	}
	m := locales.In("zh-Hans")
	tt := newTester(func(c *ui.Context) {
		ui.Column(c).Fill().Children(func() {
			if !b.remoteLandingCard(c, &b.workspaces[0]) {
				b.emptyWorkspace(c, &b.workspaces[0])
			}
			b.statusBar(c, &b.workspaces[0])
		})
	}, 900, 600)
	settle(tt)
	if !tt.HasText(m.HostDisconnectedTitle("bwh")) || !tt.HasText(m.HostReconnect()) {
		t.Fatalf("disconnected remote missing card or reconnect button: %v", tt.Texts())
	}
	if tt.HasText(m.EmptyNewTerminal()) {
		t.Fatalf("disconnected remote fell back to empty workspace: %v", tt.Texts())
	}
	if slices.Contains(tt.Texts(), m.AwakeRunningShort()) || tt.HasText("43 MB") {
		t.Fatalf("disconnected remote status bar showed stale telemetry or awake button: %v", tt.Texts())
	}

	// 有已打开的内存文档标签时，保留文档视图不遮挡草稿。
	v := b.filesFor(&b.workspaces[0])
	doc := &fileDoc{path: "/home/dev/ai/draft.md", kind: docText}
	v.docs = []*fileDoc{doc}
	b.activeTerm["w1"] = docKey(doc)
	if !b.hasActiveDocTab(&b.workspaces[0]) {
		t.Fatal("expected active doc tab to be detected")
	}
	b.activeTerm["w1"] = "t1"

	// 点击卡片上的「重新连接」按钮会发起重连。
	if err := tt.Click(m.HostReconnect()); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 4 && b.connecting; i++ {
		awaitHostUpdate(t, b)
	}
	if !b.host.snapshot().Connected || b.isHostManuallyDisconnected(h.ID) {
		t.Fatalf("clicking reconnect on disconnected card did not reconnect: snap=%+v manual=%v", b.host.snapshot(), b.isHostManuallyDisconnected(h.ID))
	}

	// 报错断开时显示「无法连接 bwh」与「重试」。
	k.disconnect(h.ID)
	b.err = "connection refused"
	settle(tt)
	if !tt.HasText(m.HostFailedTitle("bwh")) || !tt.HasText(m.CommonRetry()) {
		t.Fatalf("error disconnected remote missing failure card or retry button: %v", tt.Texts())
	}
}
