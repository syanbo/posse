package tslink

import (
	"context"
	"net/netip"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"tailscale.com/ipn/ipnstate"
	"tailscale.com/types/key"
)

func TestConfigLoadSave(t *testing.T) {
	home := t.TempDir()

	got, err := LoadConfig(home)
	if err != nil {
		t.Fatalf("LoadConfig missing: %v", err)
	}
	if got.Enabled || got.Hostname != "" || got.ControlURL != "" {
		t.Fatalf("expected zero config, got %+v", got)
	}

	want := Config{
		Enabled:              true,
		Hostname:             "posse-mac",
		ControlURL:           "https://hs.example.com",
		DisableBindInterface: true,
	}
	if err := SaveConfig(home, want); err != nil {
		t.Fatalf("SaveConfig: %v", err)
	}
	info, err := os.Stat(ConfigFile(home))
	if err != nil {
		t.Fatalf("stat config: %v", err)
	}
	if perm := info.Mode().Perm(); perm != 0o600 {
		t.Fatalf("config perm = %o, want 0600", perm)
	}
	got, err = LoadConfig(home)
	if err != nil {
		t.Fatalf("LoadConfig saved: %v", err)
	}
	if got != want {
		t.Fatalf("got %+v, want %+v", got, want)
	}

	// 非法 ControlURL 应被 SaveConfig 拒绝
	bad := Config{Enabled: true, ControlURL: "ftp://bad.example.com"}
	if err := SaveConfig(home, bad); err == nil {
		t.Fatal("expected error for invalid control URL")
	}

	// 手工写入非法 ControlURL 时 LoadConfig 也应报错
	if err := os.WriteFile(ConfigFile(home), []byte(`{"enabled":true,"controlUrl":"https://hs.example.com:65536"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadConfig(home); err == nil {
		t.Fatal("expected LoadConfig to reject invalid control URL")
	}
}

func TestConfigConcurrentSave(t *testing.T) {
	home := t.TempDir()
	var wg sync.WaitGroup
	errs := make(chan error, 32)
	for i := range 16 {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			for j := range 5 {
				c := Config{
					Enabled:  (i+j)%2 == 0,
					Hostname: "posse-mac",
				}
				if err := SaveConfig(home, c); err != nil {
					errs <- err
					return
				}
			}
		}(i)
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Fatalf("concurrent SaveConfig failed: %v", err)
	}
	if _, err := LoadConfig(home); err != nil {
		t.Fatalf("LoadConfig after concurrent save: %v", err)
	}
}

func TestValidateControlURL(t *testing.T) {
	for _, ok := range []string{
		"",
		"https://hs.example.com",
		"http://127.0.0.1:8080",
		"https://hs.example.com:8443/prefix",
		"https://hs.example.com/a%23b",
		"https://[::1]:8443",
	} {
		if err := ValidateControlURL(ok); err != nil {
			t.Errorf("ValidateControlURL(%q) unexpected err: %v", ok, err)
		}
	}
	for _, bad := range []string{
		"hs.example.com",
		"ftp://hs.example.com",
		"https://",
		"https://user:pass@hs.example.com",
		"https://hs.example.com:0",
		"https://hs.example.com:65536",
		"https://hs.example.com:",
		"https://hs.example.com:080",
		"https://hs.example.com?foo=1",
		"https://hs.example.com?",
		"https://hs.example.com#frag",
		"https://localhost#",
	} {
		if err := ValidateControlURL(bad); err == nil {
			t.Errorf("ValidateControlURL(%q) expected error", bad)
		}
	}
}

func TestValidateStateDirAndResetPreservesUnrelatedFiles(t *testing.T) {
	root := t.TempDir()
	rawDotDot := root + "/config/tailscale/.."
	// 含 ".."、相对路径、根目录必须被拒绝
	for _, bad := range []string{"", "relative/dir", "/", rawDotDot} {
		if _, err := ValidateStateDir(bad); err == nil {
			t.Errorf("ValidateStateDir(%q) expected error", bad)
		}
	}

	// 传入含 ".." 路径构造未启动 Node 时，Reset 不应删除父目录中的无关文件
	parent := filepath.Join(root, "config")
	if err := os.MkdirAll(parent, 0o700); err != nil {
		t.Fatal(err)
	}
	unrelated := filepath.Join(parent, "unrelated.json")
	if err := os.WriteFile(unrelated, []byte(`{"keep":true}`), 0o600); err != nil {
		t.Fatal(err)
	}
	badNode := New(Options{StateDir: rawDotDot})
	if _, err := badNode.Reset(); err == nil {
		t.Fatal("expected Reset on invalid ownedDir to fail")
	}
	if _, err := os.Stat(unrelated); err != nil {
		t.Fatalf("unrelated file in parent dir was deleted: %v", err)
	}

	// 即便在合法目录中存在无关文件/同名 files 子目录，Reset 也只清 Tailscale 实际落盘项（含 profile-data/netmap-cache）并保留无关内容
	stateDir := filepath.Join(root, "state")
	netmapDir := filepath.Join(stateDir, "profile-data", "p1234", "netmap-cache")
	unrelatedDir := filepath.Join(stateDir, "files")
	if err := os.MkdirAll(netmapDir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(unrelatedDir, 0o700); err != nil {
		t.Fatal(err)
	}
	tsFile := filepath.Join(stateDir, "tailscaled.state")
	netmapFile := filepath.Join(netmapDir, "cache.json")
	keepFile := filepath.Join(unrelatedDir, "keep.txt")
	if err := os.WriteFile(tsFile, []byte("state"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(netmapFile, []byte("netmap"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(keepFile, []byte("keep"), 0o600); err != nil {
		t.Fatal(err)
	}
	n := New(Options{StateDir: stateDir})
	res, err := n.Reset()
	if err != nil || !res.LocalCleared {
		t.Fatalf("Reset: res=%+v err=%v", res, err)
	}
	if _, err := os.Stat(tsFile); !os.IsNotExist(err) {
		t.Fatalf("expected tailscaled.state removed, err=%v", err)
	}
	if _, err := os.Stat(netmapFile); !os.IsNotExist(err) {
		t.Fatalf("expected profile-data netmap-cache removed, err=%v", err)
	}
	if _, err := os.Stat(keepFile); err != nil {
		t.Fatalf("expected unrelated files/keep.txt kept, err=%v", err)
	}
}

func TestSanitizeHostname(t *testing.T) {
	cases := []struct {
		in, fallback, want string
	}{
		{"My MacBook Pro.local", "posse", "my-macbook-pro"},
		{"  Posse_Dev--01  ", "posse", "posse-dev-01"},
		{"---", "posse-mac", "posse-mac"},
		{"!!!", "posse-mac", "posse-mac"},
		{"", "", "posse"},
		{strings.Repeat("a", 80), "posse", strings.Repeat("a", 63)},
	}
	for _, tc := range cases {
		if got := SanitizeHostname(tc.in, tc.fallback); got != tc.want {
			t.Errorf("SanitizeHostname(%q, %q) = %q, want %q", tc.in, tc.fallback, got, tc.want)
		}
	}
}

func TestIsTailscaleIP(t *testing.T) {
	for _, yes := range []string{"100.64.0.1", "100.100.100.100", "100.127.255.254", "::ffff:100.64.0.1"} {
		if !IsTailscaleIPString(yes) {
			t.Errorf("expected %s to be Tailscale IP", yes)
		}
	}
	for _, no := range []string{"100.63.255.255", "100.128.0.1", "192.168.1.10", "127.0.0.1", "fd7a:115c:a1e0::1", "not-an-ip"} {
		if IsTailscaleIPString(no) {
			t.Errorf("expected %s not to be Tailscale IP", no)
		}
	}
}

func TestClassifyPeerRoute(t *testing.T) {
	k1 := key.NewNode().Public()
	k2 := key.NewNode().Public()
	k3 := key.NewNode().Public()
	k4 := key.NewNode().Public()
	k5 := key.NewNode().Public()
	ip1 := netip.MustParseAddr("100.64.0.10")
	ip2 := netip.MustParseAddr("100.64.0.20")
	ip3 := netip.MustParseAddr("100.64.0.30")
	ip4 := netip.MustParseAddr("100.64.0.40")
	ip5 := netip.MustParseAddr("100.64.0.50")

	st := &ipnstate.Status{
		Peer: map[key.NodePublic]*ipnstate.PeerStatus{
			k1: {TailscaleIPs: []netip.Addr{ip1}, CurAddr: "203.0.113.5:41641", Relay: "hkg", Active: true},
			k2: {TailscaleIPs: []netip.Addr{ip2}, CurAddr: "", Relay: "sin", Active: true},
			// 仅有历史握手/字节数但 Active=false，不应误判为当前 DERP
			k3: {TailscaleIPs: []netip.Addr{ip3}, CurAddr: "", Relay: "tok", Active: false, LastHandshake: time.Now().Add(-24 * time.Hour), TxBytes: 1024},
			k4: {TailscaleIPs: []netip.Addr{ip4}, CurAddr: "", PeerRelay: "198.51.100.2:40000:1", Relay: "hkg", Active: true},
			k5: {TailscaleIPs: []netip.Addr{netip.MustParseAddr("::ffff:100.64.0.50")}, Active: true, Relay: "sjc"},
		},
	}

	if got := ClassifyPeerRoute(st, ip1); got.Kind != RouteDirect || got.Addr != "203.0.113.5:41641" || got.IsDERP() {
		t.Fatalf("peer1 route = %+v, want direct", got)
	}
	if got := ClassifyPeerRoute(st, ip2); got.Kind != RouteDERP || got.Relay != "sin" || !got.IsDERP() {
		t.Fatalf("peer2 route = %+v, want derp(sin)", got)
	}
	if got := ClassifyPeerRoute(st, ip3); got.Kind != RouteUnknown || got.IsDERP() {
		t.Fatalf("peer3 idle historical peer route = %+v, want unknown", got)
	}
	if got := ClassifyPeerRoute(st, ip4); got.Kind != RoutePeerRelay || got.PeerRelay == "" || got.IsDERP() {
		t.Fatalf("peer4 route = %+v, want peer_relay", got)
	}
	if got := ClassifyPeerRoute(st, ip5); got.Kind != RouteDERP || got.Relay != "sjc" {
		t.Fatalf("peer5 mapped IPv4 route = %+v, want derp(sjc)", got)
	}
	if got := ClassifyPeerRoute(st, netip.MustParseAddr("100.64.0.99")); got.Kind != RouteUnknown {
		t.Fatalf("missing peer route = %+v, want unknown", got)
	}
}

func TestNodeStoppedOperationsAndValidation(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "ts-state")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "tailscaled.state"), []byte("dummy"), 0o600); err != nil {
		t.Fatal(err)
	}

	n := New(Options{
		StateDir: dir,
		Hostname: "posse-test",
		AuthKey:  "tskey-auth-should-not-stick",
	})
	if n.opts.AuthKey != "" {
		t.Fatal("New must not retain AuthKey")
	}
	if n.IsRunning() {
		t.Fatal("new node should not be running")
	}
	if _, ok := n.IPv4(); ok {
		t.Fatal("new node should not have IPv4")
	}

	// 非法端口校验
	for _, badPort := range []int{-1, 65536, 100000} {
		if _, err := n.ListenPacket(badPort); err != ErrInvalidPort {
			t.Fatalf("ListenPacket(%d) err = %v, want ErrInvalidPort", badPort, err)
		}
		if _, err := n.ListenTCP(badPort); err != ErrInvalidPort {
			t.Fatalf("ListenTCP(%d) err = %v, want ErrInvalidPort", badPort, err)
		}
	}
	if _, err := n.ListenPacket(0); err != ErrNotRunning {
		t.Fatalf("ListenPacket(0) err = %v, want ErrNotRunning", err)
	}
	if _, err := n.ListenTCP(0); err != ErrNotRunning {
		t.Fatalf("ListenTCP(0) err = %v, want ErrNotRunning", err)
	}

	// DialUDP 拒绝非 Tailscale IP 与非法端口
	for _, badAddr := range []string{"192.168.1.1:6788", "8.8.8.8:53", "100.64.0.1:0", "not-an-addr", "[fd7a:115c:a1e0::1]:6788"} {
		if _, err := n.DialUDP(context.Background(), badAddr); err != ErrInvalidTarget {
			t.Fatalf("DialUDP(%q) err = %v, want ErrInvalidTarget", badAddr, err)
		}
	}
	if _, err := n.DialUDP(context.Background(), "100.64.0.1:6788"); err != ErrNotRunning {
		t.Fatalf("DialUDP valid target err = %v, want ErrNotRunning", err)
	}
	if err := n.StartLoginInteractive(context.Background()); err != ErrNotRunning {
		t.Fatalf("StartLoginInteractive err = %v, want ErrNotRunning", err)
	}
	n.InjectLinkChange()

	res, err := n.Reset()
	if err != nil {
		t.Fatalf("Reset: %v", err)
	}
	if !res.LocalCleared || res.LoggedOut {
		t.Fatalf("unexpected ResetResult: %+v", res)
	}
	if _, err := os.Stat(dir); !os.IsNotExist(err) {
		t.Fatalf("expected empty state dir removed, stat err = %v", err)
	}
	if st := n.Status(); st.State != StateDisabled {
		t.Fatalf("status after Reset = %+v, want disabled", st)
	}
}

func TestCommitStatusOrderingAndReentrancy(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "ts-state")
	n := New(Options{StateDir: dir, Hostname: "posse-test"})

	// 测试乱序刷新不会把 Running 回退成 NeedsLogin
	n.mu.Lock()
	n.gen = 1
	n.mu.Unlock()
	n.commitStatus(1, 2, Status{Enabled: true, State: StateRunning, IP: "100.64.0.2"})
	n.commitStatus(1, 1, Status{Enabled: true, State: StateNeedsLogin, AuthURL: "https://login.tailscale.com/a/old"})
	if got := n.Status(); got.State != StateRunning || got.AuthURL != "" {
		t.Fatalf("out-of-order refresh regressed status: %+v", got)
	}

	// 旧代次提交不能覆盖新代次
	n.mu.Lock()
	n.gen = 2
	n.mu.Unlock()
	n.commitStatus(1, 99, Status{Enabled: true, State: StateError, Error: "stale"})
	if got := n.Status(); got.State != StateRunning {
		t.Fatalf("stale generation overwrote status: %+v", got)
	}

	// 回调里重入 Start(带 OnChange) 和 Stop 不死锁，且外部 SetOnChange(nil) 会等待在途回调退出
	inCallback := make(chan struct{})
	release := make(chan struct{})
	reenterDone := make(chan struct{})

	n.SetOnChange(func(s Status) {
		if s.State == StateNeedsLogin {
			close(inCallback)
			<-release
			// 带 OnChange 的 Start 重入不能与 cbMu 形成死锁
			_ = n.Start(Options{
				StateDir:   filepath.Join(t.TempDir(), "reenter"),
				Hostname:   "posse-reenter",
				ControlURL: "http://127.0.0.1:1",
				OnChange:   func(Status) {},
			})
			_ = n.Stop()
			close(reenterDone)
		}
	})
	n.commitStatus(2, 0, Status{Enabled: true, State: StateNeedsLogin, AuthURL: "https://login.tailscale.com/a/new"})

	select {
	case <-inCallback:
	case <-time.After(2 * time.Second):
		t.Fatal("callback did not fire")
	}

	clearDone := make(chan struct{})
	go func() {
		n.SetOnChange(nil)
		close(clearDone)
	}()

	// 回调尚未返回，SetOnChange(nil) 应仍在等待
	select {
	case <-clearDone:
		t.Fatal("SetOnChange(nil) returned while callback still running")
	case <-time.After(30 * time.Millisecond):
	}

	close(release)
	select {
	case <-reenterDone:
	case <-time.After(5 * time.Second):
		t.Fatal("reentrant Start(with OnChange)+Stop inside callback deadlocked")
	}
	select {
	case <-clearDone:
	case <-time.After(5 * time.Second):
		t.Fatal("SetOnChange(nil) did not finish after callback returned")
	}
}

func TestNodeRealStartIdempotentAndFailedStartKeepsOwnedDir(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "ts-real")
	n := New(Options{
		StateDir:     dir,
		Hostname:     "posse-test-real",
		PollInterval: 50 * time.Millisecond,
	})
	n.SetOnChange(func(s Status) {
		// 即使在 Starting 回调里立刻调 IPv4()，也不能 panic
		_, _ = n.IPv4()
	})
	opts := Options{
		StateDir:     dir,
		Hostname:     "posse-test-real",
		ControlURL:   "http://127.0.0.1:1",
		AuthKey:      "tskey-auth-ephemeral-test",
		PollInterval: 50 * time.Millisecond,
	}
	if err := n.Start(opts); err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer func() { _ = n.Stop() }()

	// srv.AuthKey 启动后应已清空
	n.mu.Lock()
	srv1 := n.srv
	authKeyAfter := ""
	if srv1 != nil {
		authKeyAfter = srv1.AuthKey
	}
	n.mu.Unlock()
	if srv1 == nil {
		t.Fatal("expected srv initialized after Start")
	}
	if authKeyAfter != "" {
		t.Fatalf("expected srv.AuthKey cleared after Start, got %q", authKeyAfter)
	}

	// 同配置且无新 AuthKey 重复 Start 应幂等复用同一个 srv
	same := opts
	same.AuthKey = ""
	if err := n.Start(same); err != nil {
		t.Fatalf("idempotent Start: %v", err)
	}
	n.mu.Lock()
	srv2 := n.srv
	n.mu.Unlock()
	if srv1 != srv2 {
		t.Fatal("expected idempotent Start to reuse existing server")
	}

	// 启动失败（目标路径是普通文件而非目录）不应改写 ownedDir
	blocker := filepath.Join(t.TempDir(), "not-a-dir")
	if err := os.WriteFile(blocker, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := n.Start(Options{StateDir: blocker, Hostname: "posse-fail"}); err == nil {
		t.Fatal("expected Start with file-as-dir to fail")
	}
	n.mu.Lock()
	owned := n.ownedDir
	n.mu.Unlock()
	if owned != dir {
		t.Fatalf("failed Start corrupted ownedDir: got %q, want %q", owned, dir)
	}
}

func TestBuildTagsPreserveRequiredFeatures(t *testing.T) {
	for name, tags := range map[string]string{"darwin": BuildTagsDarwin, "android": BuildTagsAndroid} {
		// 必须裁掉 logtail，防止向 Tailscale 上传日志
		if !strings.Contains(tags, "ts_omit_logtail") {
			t.Errorf("%s tags missing ts_omit_logtail", name)
		}
		// 必须保留 nattraversal、cachenetmap、useproxy、netstack
		for _, mustKeep := range []string{"ts_omit_nattraversal", "ts_omit_cachenetmap", "ts_omit_useproxy", "ts_omit_netstack"} {
			if strings.Contains(tags, mustKeep) {
				t.Errorf("%s tags must not contain %s", name, mustKeep)
			}
		}
	}
	if strings.Contains(BuildTagsAndroid, "ts_omit_androidbin") {
		t.Error("android tags must not omit androidbin")
	}
	if !strings.Contains(BuildTagsDarwin, "ts_omit_androidbin") {
		t.Error("darwin tags should omit androidbin")
	}
}
