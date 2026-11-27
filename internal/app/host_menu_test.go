package app

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/egoist/mygo/ui"

	"posse/internal/remote"
	"posse/internal/rpc"
	"posse/locales"
)

func hostMenuLocalFixture(t *testing.T) (*app, *hostControlFixture) {
	t.Helper()
	a, f := localDesktopFixture(t)
	t.Cleanup(func() {
		// 切主机会异步存 UI；同步写入新序号，避免旧保存晚于临时目录清理。
		for _, view := range a.views.all {
			if err := saveUIState(view.home, view.state); err != nil {
				t.Error(err)
			}
		}
	})
	return a, f
}

func writeHostMenuConfig(t *testing.T, a *app, hosts ...remote.Host) {
	t.Helper()
	data, err := json.Marshal(map[string]any{"version": 1, "hosts": hosts})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(a.windowOwner().home, "hosts.json"), data, 0600); err != nil {
		t.Fatal(err)
	}
}

func hostMenuLabels(a *app) []string {
	seq := 0
	m := newPopMenu(&seq, -1)
	a.hostSwitchBuild(appL())(m)
	var labels []string
	for _, item := range m.items {
		if item.sep {
			break
		}
		labels = append(labels, item.label)
	}
	return labels
}

func hostMenuChoose(t *testing.T, a *app, label string) {
	t.Helper()
	seq := 0
	m := newPopMenu(&seq, -1)
	a.hostSwitchBuild(appL())(m)
	for _, item := range m.items {
		if item.label == label && !item.disabled {
			seq = 0
			a.hostSwitchBuild(appL())(newPopMenu(&seq, item.seq))
			return
		}
	}
	t.Fatalf("enabled menu item %q not found", label)
}

func TestHostMenuConfiguration(t *testing.T) {
	_, a, _ := hostViewsFixture(t)
	h1, h2 := remoteTestHost("second"), remoteTestHost("first")
	h1.Name, h2.Name = "Studio Mac", "Office Mac"
	writeHostMenuConfig(t, a, h1, h2)
	tt := newTester(func(c *ui.Context) {
		ui.Row(c).Fill().Children(func() { a.navigation(c) })
	}, 600, 500)
	settleMenu(tt)
	a.openHostMenu()
	settleMenu(tt)
	want := []string{appL().HostLocal(), "Studio Mac", "Office Mac"}
	if got := hostMenuLabels(a); !reflect.DeepEqual(got, want) {
		t.Fatalf("menu = %v, want %v", got, want)
	}
	if err := os.WriteFile(filepath.Join(a.home, "hosts.json"), []byte("broken"), 0600); err != nil {
		t.Fatal(err)
	}
	// 关闭再打开才重读；配置损坏仍留下本机入口，并沿用现有错误条。
	tt.Key(0, ui.KeyEscape)
	settleMenu(tt)
	a.openHostMenu()
	settleMenu(tt)
	if got := hostMenuLabels(a); !reflect.DeepEqual(got, want[:1]) || a.err == "" {
		t.Fatalf("invalid configuration menu=%v error=%q", got, a.err)
	}
	if tt.HasText("local") {
		t.Fatal("local host exposed internal ID")
	}
}

func TestHostMenuLocalSelectionKeepsPage(t *testing.T) {
	for _, selected := range []string{settingsBoardID, "current-workspace"} {
		t.Run(selected, func(t *testing.T) {
			v, a, _ := hostViewsFixture(t)
			a.workspaces = []workspace{{ID: "previous-workspace"}, {ID: "current-workspace"}}
			a.state.LastWorkspace = "previous-workspace"
			a.selected = selected
			hostMenuChoose(t, a, appL().HostLocal())
			if a.selected != selected || v.active.Load() != a {
				t.Fatalf("selecting the displayed local host changed page from %q to %q", selected, a.selected)
			}
		})
	}
}

func TestHostMenuRemoteLifecycle(t *testing.T) {
	k, a, b, f, _, _ := connectionFixture(t)
	h := remoteTestHost("remote")
	writeHostMenuConfig(t, a, h)
	a.loadHostMenu()
	hostMenuChoose(t, a, h.Name)
	if !b.connecting || b.hostStatus(appL()) != appL().HostStageAuth() {
		t.Fatalf("connection in progress was not shown: connecting=%v status=%q", b.connecting, b.hostStatus(appL()))
	}
	awaitHostUpdate(t, b)
	// 无人控制的远端主机会自动接管
	if !b.host.snapshot().Connected || !b.host.snapshot().Controlled || b.hostStatus(appL()) != "" {
		t.Fatalf("unowned remote connection did not auto-take control: snap=%+v", b.host.snapshot())
	}
	old := b.host.snapshot()
	b.releaseHost(old)
	viewer := b.host.snapshot()
	if !viewer.Connected || viewer.Controlled || b.host.matches(old) || k.active == nil {
		t.Fatal("release did not immediately revoke control while keeping the connection")
	}
	awaitHostUpdate(t, b)
	f.mu.Lock()
	owner, takes := f.state.Owner, f.takes
	f.mu.Unlock()
	if owner != "" || takes != 1 {
		t.Fatalf("release waited for expiry or retook control: owner=%q takes=%d", owner, takes)
	}
	for range 3 {
		if !k.heartbeat(b) || b.host.snapshot().Controlled {
			t.Fatal("viewing refresh restored control")
		}
	}
	// 主动释放后断开再重连，同一个实例应保持查看（blockedInstance 生效）
	hostMenuChoose(t, b, appL().HostDisconnect())
	if b.host.snapshot().Connected || b.hostStatus(appL()) != appL().HostDisconnected() || k.views.active.Load() != b {
		t.Fatal("disconnect did not retain the disconnected remote view")
	}
	hostMenuChoose(t, b, appL().HostReconnect())
	awaitHostUpdate(t, b)
	if !b.host.snapshot().Connected || b.host.snapshot().Controlled {
		t.Fatal("reconnect after explicit release retook control")
	}
	// 显式接管后再断开重连，无人控制会重新自动接管
	hostMenuChoose(t, b, appL().HostTake())
	awaitHostUpdate(t, b)
	if !b.host.snapshot().Controlled {
		t.Fatal("explicit takeover did not obtain control")
	}
	hostMenuChoose(t, b, appL().HostDisconnect())
	k.jobs.Wait()
	b.runPending()
	f.mu.Lock()
	owner = f.state.Owner
	f.mu.Unlock()
	if owner != "" {
		t.Fatal("disconnect left control held")
	}
	hostMenuChoose(t, b, appL().HostReconnect())
	awaitHostUpdate(t, b)
	if !b.host.snapshot().Connected || !b.host.snapshot().Controlled {
		t.Fatal("reconnect after explicit takeover did not auto-take unowned remote")
	}
}

func TestHostMenuTakeConfirmation(t *testing.T) {
	for _, mode := range []string{"cancel", "confirm", "generation", "switch", "instance"} {
		t.Run(mode, func(t *testing.T) {
			k, _, b, f, base, _ := connectionFixture(t)
			f.mu.Lock()
			f.state.Owner, f.state.Label = "other", "Another Mac"
			f.mu.Unlock()
			k.connectRemote(remoteTestHost("remote"), false)
			awaitHostUpdate(t, b)
			b.takeHost(appL(), b.host.snapshot())
			if !b.confirming || b.hostStatus(appL()) != appL().HostViewingOwner("Another Mac") {
				t.Fatal("other controller did not require confirmation or display its label")
			}
			confirm := b.confirmDo
			switch mode {
			case "cancel":
				tt := newTester(b.confirmDialog, 700, 500)
				settleMenu(tt)
				if err := tt.Click(locales.In("zh-Hans").CommonCancel()); err != nil {
					t.Fatal(err)
				}
				settleMenu(tt)
				if b.confirming {
					t.Fatal("cancel did not close confirmation")
				}
			case "generation":
				b.host.detach()
				if err := b.host.connect(context.Background(), base); err != nil {
					t.Fatal(err)
				}
			case "switch":
				b.selectLocalHost()
			case "instance":
				f.mu.Lock()
				f.state = rpc.DesktopState{InstanceID: "replacement", Epoch: 1}
				f.mu.Unlock()
				if err := b.host.refresh(context.Background()); err != nil {
					t.Fatal(err)
				}
			}
			if mode != "cancel" {
				confirm()
				b.runPending()
				if mode == "confirm" {
					awaitHostUpdate(t, b)
				}
			}
			f.mu.Lock()
			takes := f.takes
			f.mu.Unlock()
			want := 0
			if mode == "confirm" {
				want = 1
			}
			if takes != want || b.host.snapshot().Controlled != (mode == "confirm") {
				t.Fatalf("confirmation %s: takes=%d, want=%d", mode, takes, want)
			}
		})
	}
}

func TestHostMenuRemoteNoRetries(t *testing.T) {
	for _, mode := range []string{"disconnect", "dial-failure", "ssh-exit"} {
		t.Run(mode, func(t *testing.T) {
			k, a, b, f, base, closed := connectionFixture(t)
			var dials atomic.Int32
			k.dial = func(context.Context, remote.Host, remote.Options) (hostTransport, error) {
				n := dials.Add(1)
				if mode == "dial-failure" && n == 1 {
					return hostTransport{}, errors.New("test connection failure")
				}
				if n == 1 {
					return hostTransport{base, closed}, nil
				}
				return hostTransport{base, make(chan struct{})}, nil
			}
			a.selectRemoteHost(remoteTestHost("remote"))
			awaitHostUpdate(t, b)
			switch mode {
			case "disconnect":
				k.disconnect("remote")
			case "ssh-exit":
				close(closed)
				awaitHostUpdate(t, b)
			}
			for range 3 {
				if k.heartbeat(b) {
					t.Fatal("disconnected host reported connected")
				}
				b.runPending()
			}
			if dials.Load() != 1 || b.connecting || b.hostStatus(appL()) != appL().HostDisconnected() {
				t.Fatalf("automatic retry after %s: dials=%d", mode, dials.Load())
			}
			if mode != "disconnect" && b.err == "" {
				t.Fatal("connection failure omitted error")
			}
			b.selectRemoteHost(remoteTestHost("remote"))
			for i := 0; i < 4 && b.connecting; i++ {
				awaitHostUpdate(t, b)
			}
			f.mu.Lock()
			takes := f.takes
			f.mu.Unlock()
			if dials.Load() != 2 || !b.host.snapshot().Connected || !b.host.snapshot().Controlled || takes == 0 {
				t.Fatalf("explicit reconnect did not establish an auto-taken session: dials=%d snap=%+v takes=%d", dials.Load(), b.host.snapshot(), takes)
			}
		})
	}
}

func TestHostMenuLocalReturn(t *testing.T) {
	for _, mode := range []string{"controlled", "released", "heartbeat"} {
		t.Run(mode, func(t *testing.T) {
			a, f := hostMenuLocalFixture(t)
			connectLocalDesktop(t, a)
			h := remoteTestHost("remote")
			_, base := newHostControlFixture(t)
			base.Remote = true
			a.hosts.dial = func(context.Context, remote.Host, remote.Options) (hostTransport, error) {
				return hostTransport{base, make(chan struct{})}, nil
			}
			if mode == "released" {
				a.releaseHost(a.host.snapshot())
				if !a.host.snapshot().Connected || a.host.snapshot().Controlled {
					t.Fatal("local release dropped connection or retained control")
				}
				awaitHostUpdate(t, a)
				if err := a.host.refresh(context.Background()); err != nil || a.host.snapshot().Controlled {
					t.Fatalf("local release was automatically reversed: %v", err)
				}
			} else if mode == "heartbeat" {
				f.mu.Lock()
				f.heartbeatFail = true
				f.mu.Unlock()
				if !a.hosts.heartbeat(a) || a.host.snapshot().Controlled {
					t.Fatal("heartbeat failure retained control")
				}
				f.mu.Lock()
				f.state.Owner, f.heartbeatFail = "", false
				f.state.Epoch++
				f.mu.Unlock()
			}
			a.selectRemoteHost(h)
			b := a.views.active.Load()
			awaitHostUpdate(t, b)
			b.selectLocalHost()
			a.connectLocal(context.Background())
			if a.displayed() != a || a.host.snapshot().Controlled != (mode == "controlled") {
				t.Fatalf("local return %s: %+v", mode, a.host.snapshot())
			}
			f.mu.Lock()
			takes := f.takes
			f.mu.Unlock()
			want := 1
			if mode == "controlled" {
				want = 2
			}
			if takes != want {
				t.Fatalf("local return %s takes=%d, want=%d", mode, takes, want)
			}
			if mode == "released" {
				f.mu.Lock()
				f.state = rpc.DesktopState{InstanceID: "replacement", Epoch: 1}
				f.mu.Unlock()
				if err := a.host.refresh(context.Background()); err != nil || !a.host.snapshot().Controlled {
					t.Fatalf("empty replacement instance did not allow automatic control: %v", err)
				}
			}
		})
	}
}

func TestHostMenuLocalLateRelease(t *testing.T) {
	a, f := hostMenuLocalFixture(t)
	connectLocalDesktop(t, a)
	entered, finish := make(chan struct{}), make(chan struct{})
	var once sync.Once
	defer once.Do(func() { close(finish) })
	var releases atomic.Int32
	f.mu.Lock()
	f.beforeRelease = func() {
		if releases.Add(1) == 1 {
			close(entered)
			<-finish
		}
	}
	f.mu.Unlock()
	b := a.views.forHost(newHostSession("remote", "Remote"), a.c)
	a.views.activate("remote")
	waitConnectionEvent(t, entered)
	b.selectLocalHost()
	a.connectLocal(context.Background())
	if !a.host.snapshot().Controlled {
		t.Fatal("local return did not obtain new control")
	}
	once.Do(func() { close(finish) })
	a.hosts.jobs.Wait()
	if err := a.host.refresh(context.Background()); err != nil || !a.host.snapshot().Controlled {
		t.Fatalf("late release revoked new control: %v", err)
	}
}

func TestHostMenuRemoteHeartbeatAndInstance(t *testing.T) {
	k, a, b, f, _, _ := connectionFixture(t)
	a.selectRemoteHost(remoteTestHost("remote"))
	awaitHostUpdate(t, b)
	if !b.host.snapshot().Controlled {
		t.Fatal("unowned remote did not auto-take control")
	}
	f.mu.Lock()
	f.heartbeatFail = true
	f.mu.Unlock()
	if !k.heartbeat(b) || b.host.snapshot().Controlled {
		t.Fatal("failed heartbeat retained write access")
	}
	if !k.heartbeat(b) || b.host.snapshot().Controlled {
		t.Fatal("query of our old owner restored write access")
	}
	f.mu.Lock()
	f.state = rpc.DesktopState{InstanceID: "replacement", Epoch: 1}
	f.mu.Unlock()
	if !k.heartbeat(b) || b.host.snapshot().Controlled {
		t.Fatal("remote instance change took control")
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.takes != 1 {
		t.Fatal("heartbeat or instance change repeated takeover")
	}
}

func TestHostMenuDiscussionAndGitGuards(t *testing.T) {
	_, a, b, _, _, _ := connectionFixture(t)
	local, base := startFakeDaemon(t, map[string]rpc.Handler{
		"discussion.list": func(context.Context, json.RawMessage) (any, error) { return discList{}, nil },
	})
	a.c = base
	a.selectRemoteHost(remoteTestHost("remote"))
	awaitHostUpdate(t, b)
	r := b.discussionRequest()
	if r.client != base || r.client.Desktop != nil || r.host != nil {
		t.Fatal("remote view changed discussion base identity")
	}
	if err := r.invoke(context.Background(), "discussion.list", nil, nil); err != nil {
		t.Fatal(err)
	}
	if _, ok := local.find("discussion.list"); !ok {
		t.Fatal("discussion did not reach local server")
	}
	for _, controlled := range []bool{true, false} {
		if !controlled {
			b.releaseHost(b.host.snapshot())
			awaitHostUpdate(t, b)
		}
		v := &filesView{root: t.TempDir(), remote: true}
		g := &gitState{v: v, repo: v.root}
		var ran atomic.Bool
		op := gitOp{m: appL(), what: "forbidden", fn: func(context.Context, string) error { ran.Store(true); return nil }}
		b.gitStart(g, op)
		b.gitRun(g, op)
		assertRemoteFileUnsupported(t, b.err, "git.write")
		if ran.Load() || len(g.queue) != 0 || g.busy != "" {
			t.Fatal("host menu enabled an unmigrated Git write")
		}
		b.autoFetch(nil, &workspace{Path: v.root}, v.root, &trackInfo{})
		if len(b.fetchAt) != 0 {
			t.Fatal("host menu enabled automatic Fetch")
		}
	}
}

func TestHostMenuReleaseThenDisconnect(t *testing.T) {
	k, a, b, f, _, _ := connectionFixture(t)
	a.selectRemoteHost(remoteTestHost("remote"))
	awaitHostUpdate(t, b)
	if !b.host.snapshot().Controlled {
		t.Fatal("unowned remote did not auto-take control")
	}
	entered, finish := make(chan struct{}), make(chan struct{})
	var signal, unblock sync.Once
	defer unblock.Do(func() { close(finish) })
	f.mu.Lock()
	f.beforeRelease = func() { signal.Do(func() { close(entered) }); <-finish }
	f.mu.Unlock()
	pool := &observedHostCleanup{k.pool, make(chan struct{}, 8), make(chan struct{}, 8)}
	k.pool = pool
	b.releaseHost(b.host.snapshot())
	waitConnectionEvent(t, entered)
	k.disconnect(b.viewID)
	if b.host.snapshot().Connected {
		t.Fatal("disconnect did not revoke the visible connection immediately")
	}
	select {
	case <-pool.disconnected:
		t.Fatal("disconnect closed SSH while the explicit release was still pending")
	default:
	}
	unblock.Do(func() { close(finish) })
	k.jobs.Wait()
	b.runPending()
	waitConnectionEvent(t, pool.disconnected)
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.state.Owner != "" {
		t.Fatal("release followed by disconnect retained the server lease")
	}
}

func TestHostMenuLocalReturnAfterOtherController(t *testing.T) {
	a, f := hostMenuLocalFixture(t)
	f.mu.Lock()
	f.state.Owner, f.state.Label = "other", "Another Mac"
	f.mu.Unlock()
	a.connectLocal(context.Background())
	if !a.host.snapshot().Connected || a.host.snapshot().Controlled {
		t.Fatal("occupied local instance did not start as a viewer")
	}
	b := a.views.forHost(newHostSession("remote", "Remote"), a.c)
	a.views.activate("remote")
	f.mu.Lock()
	f.state.Owner = ""
	f.state.Epoch++
	f.mu.Unlock()
	b.selectLocalHost()
	a.connectLocal(context.Background())
	if !a.host.snapshot().Controlled {
		t.Fatal("explicit return to a now-empty local instance did not take control")
	}
}

func TestHostMenuLocalUnknownTakeDoesNotRetryOnReturn(t *testing.T) {
	a, f := hostMenuLocalFixture(t)
	entered, finish := make(chan struct{}), make(chan struct{})
	var unblock sync.Once
	defer unblock.Do(func() { close(finish) })
	f.mu.Lock()
	f.afterTake = func() { close(entered); <-finish }
	f.mu.Unlock()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- a.host.connect(ctx, a.c) }()
	waitConnectionEvent(t, entered)
	b := a.views.forHost(newHostSession("remote", "Remote"), a.c)
	a.views.activate("remote")
	cancel()
	if err := <-done; err == nil {
		t.Fatal("lost take reply unexpectedly succeeded")
	}
	unblock.Do(func() { close(finish) })
	f.mu.Lock()
	f.afterTake = nil
	f.mu.Unlock()
	b.selectLocalHost()
	a.connectLocal(context.Background())
	if !a.host.snapshot().Connected || a.host.snapshot().Controlled {
		t.Fatal("local return repeated an uncertain takeover")
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.takes != 1 || f.state.Owner != "" {
		t.Fatal("uncertain takeover was replayed or its lease was retained")
	}
}

func TestHostMenuFrameKeepsActionNumbers(t *testing.T) {
	k, _, b, _, _, _ := connectionFixture(t)
	k.connectRemote(remoteTestHost("remote"), false)
	awaitHostUpdate(t, b)
	build := b.hostSwitchBuild(appL())
	actions := b.hostActionsBuild(appL(), remoteTestHost("remote"), true)
	seq := 0
	before := newPopMenu(&seq, -1)
	build(before)
	seq = 0
	beforeActions := newPopMenu(&seq, -1)
	actions(beforeActions)
	k.disconnect("remote")
	seq = 0
	after := newPopMenu(&seq, -1)
	build(after)
	seq = 0
	afterActions := newPopMenu(&seq, -1)
	actions(afterActions)
	if len(before.items) != len(after.items) || len(beforeActions.items) != len(afterActions.items) {
		t.Fatal("connection state changed menu numbering within the same frame")
	}
	for i, item := range before.items {
		if item.label != after.items[i].label || item.disabled != after.items[i].disabled {
			t.Fatal("connection state replaced a captured menu action")
		}
	}
	for i, item := range beforeActions.items {
		if item.label != afterActions.items[i].label || item.disabled != afterActions.items[i].disabled {
			t.Fatal("connection state replaced a captured host action item")
		}
	}
}

func TestHostOpenRespectsManualDisconnect(t *testing.T) {
	k, a, b, _, base, _ := connectionFixture(t)
	h := remoteTestHost("remote")
	writeHostMenuConfig(t, a, h)
	a.loadHostMenu()

	var dials atomic.Int32
	blockDial := make(chan struct{})
	var holdNext atomic.Bool
	k.dial = func(ctx context.Context, host remote.Host, opts remote.Options) (hostTransport, error) {
		dials.Add(1)
		if holdNext.Load() {
			select {
			case <-ctx.Done():
				return hostTransport{}, ctx.Err()
			case <-blockDial:
			}
		}
		return hostTransport{base, make(chan struct{})}, nil
	}

	// 首次点击远端磁贴正常建连，正常切回本机再切回远端应自动重连。
	a.openHost(h)
	awaitHostUpdate(t, b)
	if !b.host.snapshot().Connected || dials.Load() != 1 {
		t.Fatalf("initial openHost did not connect: snap=%+v dials=%d", b.host.snapshot(), dials.Load())
	}
	b.selectLocalHost()
	a.openHost(h)
	awaitHostUpdate(t, b)
	if !b.host.snapshot().Connected || dials.Load() != 2 {
		t.Fatalf("normal host switch did not auto-reconnect: snap=%+v dials=%d", b.host.snapshot(), dials.Load())
	}

	// 主动点击「断开连接」后切回本机，再点击远端磁贴只切入已断开视图，不自动重连。
	k.disconnect(h.ID)
	if !b.manuallyDisconnected || b.host.snapshot().Connected {
		t.Fatalf("manual disconnect did not mark disconnected state: manual=%v snap=%+v", b.manuallyDisconnected, b.host.snapshot())
	}
	b.selectLocalHost()
	a.openHost(h)
	if k.views.active.Load() != b || b.connecting || b.host.snapshot().Connected || dials.Load() != 2 {
		t.Fatalf("openHost auto-reconnected manually disconnected host: active=%s connecting=%v snap=%+v dials=%d", k.views.active.Load().viewID, b.connecting, b.host.snapshot(), dials.Load())
	}

	// 显式「重新连接」会清除主动断开标记并重新连上，之后正常切换恢复无感重连。
	b.selectRemoteHost(h)
	awaitHostUpdate(t, b)
	if b.manuallyDisconnected || !b.host.snapshot().Connected || dials.Load() != 3 {
		t.Fatalf("explicit reconnect failed or kept manual flag: manual=%v snap=%+v dials=%d", b.manuallyDisconnected, b.host.snapshot(), dials.Load())
	}
	b.selectLocalHost()
	a.openHost(h)
	awaitHostUpdate(t, b)
	if !b.host.snapshot().Connected || dials.Load() != 4 {
		t.Fatalf("switch after explicit reconnect did not resume auto-reconnect: snap=%+v dials=%d", b.host.snapshot(), dials.Load())
	}

	// 取消尚未连上的拨号不记为主动断开，切走后再次点击磁贴仍可重新尝试建连。
	b.selectLocalHost()
	holdNext.Store(true)
	a.openHost(h)
	if !b.connecting {
		t.Fatal("expected dial to stay in connecting state")
	}
	k.disconnect(h.ID)
	if b.manuallyDisconnected {
		t.Fatal("canceling an in-progress dial unexpectedly set manuallyDisconnected")
	}
	holdNext.Store(false)
	close(blockDial)
	b.selectLocalHost()
	a.openHost(h)
	for i := 0; i < 4 && b.connecting; i++ {
		awaitHostUpdate(t, b)
	}
	if !b.host.snapshot().Connected {
		t.Fatalf("openHost after canceled dial did not retry connecting: snap=%+v", b.host.snapshot())
	}
}
