package remote

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"posse/internal/daemon"
	"posse/internal/rpc"
)

func TestCompareSemver(t *testing.T) {
	cases := []struct {
		a, b string
		want int
	}{
		{"0.1.55", "0.1.54", 1},
		{"0.1.54", "0.1.55", -1},
		{"0.1.54", "0.1.54", 0},
		{"0.2.0", "0.1.99", 1},
		{"1.0.0", "1.0.0-beta.1", 1},
		{"1.0.0-beta.10", "1.0.0-beta.9", 1},
		{"1.0.0-beta.2", "1.0.0-beta.10", -1},
		{"1.0.0-1", "1.0.0-alpha", -1},
		{"v0.1.55", "0.1.54", 1},
	}
	for _, tc := range cases {
		if got := compareSemver(tc.a, tc.b); got != tc.want {
			t.Errorf("compareSemver(%q, %q) = %d, want %d", tc.a, tc.b, got, tc.want)
		}
	}
}

func TestShouldAutoUpgradeHeadless(t *testing.T) {
	baseTime := time.Date(2026, 11, 27, 10, 9, 8, 0, time.UTC)
	newerTime := baseTime.Add(5 * time.Minute)
	olderTime := baseTime.Add(-5 * time.Minute)
	base := rpc.RemoteInfo{
		Version: "0.1.54",
		Build:   "8370eac " + baseTime.Format(time.RFC3339),
		Edition: "headless",
		Managed: true,
		Home:    "/Users/pur/.posse",
	}
	if !shouldAutoUpgradeHeadless(base, "0.1.55", "8370eac", olderTime) {
		t.Fatal("newer semver did not trigger upgrade")
	}
	if shouldAutoUpgradeHeadless(base, "0.1.53", "9999999", newerTime) {
		t.Fatal("older semver attempted downgrade")
	}
	if !shouldAutoUpgradeHeadless(base, "0.1.54", "49f7b8f", newerTime) {
		t.Fatal("same version with strictly newer build time and different commit did not trigger upgrade")
	}
	if shouldAutoUpgradeHeadless(base, "0.1.54", "49f7b8f", olderTime) {
		t.Fatal("same version with older build time triggered backward upgrade")
	}
	if shouldAutoUpgradeHeadless(base, "0.1.54", "49f7b8f", time.Time{}) {
		t.Fatal("same version without builtAt timestamp triggered unverified upgrade")
	}
	if shouldAutoUpgradeHeadless(base, "0.1.54", "8370eac", newerTime) {
		t.Fatal("same version and same commit triggered redundant upgrade")
	}
	// 非托管安装（Managed=false）绝不自动接管升级
	unmanaged := base
	unmanaged.Managed = false
	if shouldAutoUpgradeHeadless(unmanaged, "0.1.55", "49f7b8f", newerTime) {
		t.Fatal("unmanaged headless executable triggered auto-upgrade")
	}
	full := base
	full.Edition = "full"
	if shouldAutoUpgradeHeadless(full, "0.1.55", "49f7b8f", newerTime) {
		t.Fatal("full edition triggered headless auto-upgrade")
	}
}

func startUpgradeMockRPC(t *testing.T, handlers map[string]rpc.Handler) string {
	t.Helper()
	dir, err := os.MkdirTemp("/tmp", "up-*")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })
	sock := filepath.Join(dir, "s")
	ln, err := net.Listen("unix", sock)
	if err != nil {
		t.Fatal(err)
	}
	srv := rpc.NewServer()
	for name, h := range handlers {
		srv.Handle(name, h)
	}
	ctx, cancel := context.WithCancel(context.Background())
	go srv.Serve(ctx, ln)
	t.Cleanup(func() {
		cancel()
		ln.Close()
	})
	return sock
}

func TestCheckServerUpgradeLocalAndRemoteManifestNoTarball(t *testing.T) {
	desktopVer := daemon.Version
	baseInfo := rpc.RemoteInfo{
		Version:  "0.1.0",
		Build:    "8370eac 2026-11-27T10:00:00Z",
		Edition:  "headless",
		Managed:  true,
		Platform: "linux",
		Arch:     "amd64",
		Home:     "/home/dev/.posse",
	}

	// 1. 非托管或完整版节点直接拒绝检查。
	unmanaged := baseInfo
	unmanaged.Managed = false
	if _, err := CheckServerUpgrade(context.Background(), unmanaged, Options{DisableRemoteDownload: true}); err == nil {
		t.Fatal("expected error for unmanaged headless node")
	}

	// 2. 本地目录命中：校验后立即清理临时快照，不触发任何网络访问。
	dir := t.TempDir()
	_, wantSHA := makeTestServerArchive(t, dir, desktopVer, "linux", "amd64")
	res, err := CheckServerUpgrade(context.Background(), baseInfo, Options{
		ServerSearch:          []string{dir},
		DisableRemoteDownload: true,
	})
	if err != nil {
		t.Fatalf("local CheckServerUpgrade failed: %v", err)
	}
	if !res.Available || res.UpToDate || res.Source != "local" || res.TargetVersion != desktopVer {
		t.Fatalf("unexpected local check result: %+v", res)
	}

	// 3. 线上清单检查：只请求签名 JSON，绝不下载 .tar.gz；无 TargetBuiltAt；且不得超过桌面版本。
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	pubB64 := base64.StdEncoding.EncodeToString(pub)
	var manifestHits, bundleHits atomic.Int32
	var manifestVersion string
	var srv *httptest.Server
	srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/server-linux-amd64.json":
			manifestHits.Add(1)
			m := ServerManifest{
				Version: manifestVersion,
				Commit:  "fedcba987654",
				OS:      "linux",
				Arch:    "amd64",
				URL:     srv.URL + "/bundle.tar.gz",
				SHA256:  wantSHA,
			}
			m.Signature = base64.StdEncoding.EncodeToString(ed25519.Sign(priv, serverManifestSignPayload(m)))
			_ = json.NewEncoder(w).Encode(m)
		default:
			bundleHits.Add(1)
			http.Error(w, "tarball should not be downloaded during check", http.StatusInternalServerError)
		}
	}))
	defer srv.Close()

	// 3a. 线上版本 == 桌面版本且 > 远端：可升级。
	manifestVersion = desktopVer
	res, err = CheckServerUpgrade(context.Background(), baseInfo, Options{
		ServerBaseURL: srv.URL,
		ServerPubKey:  pubB64,
		ServerSearch:  []string{t.TempDir()},
	})
	if err != nil {
		t.Fatalf("remote CheckServerUpgrade failed: %v", err)
	}
	if !res.Available || res.DesktopTooOld || res.Source != "remote" || res.TargetVersion != desktopVer || !res.TargetBuiltAt.IsZero() {
		t.Fatalf("unexpected remote check result: %+v", res)
	}
	if manifestHits.Load() != 1 || bundleHits.Load() != 0 {
		t.Fatalf("hits manifest=%d bundle=%d, want 1/0", manifestHits.Load(), bundleHits.Load())
	}

	// 3b. 线上版本 (99.0.0) > 桌面版本：不把远端升到比桌面更新，且 ResolveServerPackage 也在拉压缩包前拒绝。
	manifestVersion = "99.0.0"
	res, err = CheckServerUpgrade(context.Background(), baseInfo, Options{
		ServerBaseURL: srv.URL,
		ServerPubKey:  pubB64,
		ServerSearch:  []string{t.TempDir()},
	})
	if err != nil {
		t.Fatalf("remote CheckServerUpgrade (newer than desktop) failed: %v", err)
	}
	if res.Available || !res.DesktopTooOld || bundleHits.Load() != 0 {
		t.Fatalf("expected DesktopTooOld without tarball download: %+v bundleHits=%d", res, bundleHits.Load())
	}
	_, dlErr := ResolveServerPackage(context.Background(), ServerResolveOptions{
		TargetOS:         "linux",
		TargetArch:       "amd64",
		MaxRemoteVersion: desktopVer,
		BaseURL:          srv.URL,
		PublicKey:        pubB64,
		SearchDirs:       []string{t.TempDir()},
	})
	var tooOld *ServerDesktopTooOldError
	if !errors.As(dlErr, &tooOld) || bundleHits.Load() != 0 {
		t.Fatalf("expected ServerDesktopTooOldError before downloading tarball: err=%v bundleHits=%d", dlErr, bundleHits.Load())
	}
}

func TestUpgradeHeadlessTwoStageCtxAndRecovery(t *testing.T) {
	home := "/home/dev/.posse"
	pkg := ServerPackage{Version: "0.1.55", Tag: "0.1.55-abcdef123456", OS: "linux", Arch: "amd64"}

	// 1. 上传阶段可取消：返回 handedOff=false，不触发 daemon.handoff。
	var handoffCalls atomic.Int32
	sock1 := startUpgradeMockRPC(t, map[string]rpc.Handler{
		"daemon.handoff": func(context.Context, json.RawMessage) (any, error) {
			handoffCalls.Add(1)
			return nil, nil
		},
	})
	s1 := &Session{
		Socket: sock1,
		Info:   rpc.RemoteInfo{InstanceID: "inst-1", Edition: "headless", Managed: true, Home: home, Version: "0.1.54"},
		stageOverride: func(ctx context.Context, _ string, _ ServerPackage, _ func(Stage, float64)) (string, error) {
			<-ctx.Done()
			return "", ctx.Err()
		},
	}
	cctx, ccancel := context.WithCancel(context.Background())
	ccancel()
	if _, handedOff, err := s1.upgradeHeadless(cctx, Options{}, pkg); !errors.Is(err, context.Canceled) || handedOff || handoffCalls.Load() != 0 {
		t.Fatalf("stage 1 cancel failed: handedOff=%v handoffCalls=%d err=%v", handedOff, handoffCalls.Load(), err)
	}

	// 2. 进入 handoff 后调用方取消 ctx：WithoutCancel 保证 handoff 与 activate 继续完成。
	var mu sync.Mutex
	curInst := "inst-1"
	handoffEntered := make(chan struct{})
	releaseHandoff := make(chan struct{})
	var activateCalls atomic.Int32
	sock2 := startUpgradeMockRPC(t, map[string]rpc.Handler{
		"daemon.handoff": func(context.Context, json.RawMessage) (any, error) {
			close(handoffEntered)
			<-releaseHandoff
			mu.Lock()
			curInst = "inst-2"
			mu.Unlock()
			return map[string]any{"ok": true}, nil
		},
		"daemon.info": func(context.Context, json.RawMessage) (any, error) {
			mu.Lock()
			id := curInst
			mu.Unlock()
			return rpc.RemoteInfo{
				InstanceID: id, Edition: "headless", Managed: true, Home: home, Version: "0.1.55",
				Platform: "linux", Arch: "amd64", Capabilities: []string{"posse.remote.v1"},
			}, nil
		},
	})
	s2 := &Session{
		Socket: sock2,
		Info:   rpc.RemoteInfo{InstanceID: "inst-1", Edition: "headless", Managed: true, Home: home, Version: "0.1.54"},
		stageOverride: func(context.Context, string, ServerPackage, func(Stage, float64)) (string, error) {
			return home + "/server/versions/" + pkg.Tag + "/posse", nil
		},
		activateOverride: func(ctx context.Context, _, _, _ string) error {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			activateCalls.Add(1)
			return nil
		},
	}
	ctx2, cancel2 := context.WithCancel(context.Background())
	doneCh := make(chan struct{})
	var gotInfo rpc.RemoteInfo
	var gotHanded bool
	var gotErr error
	go func() {
		defer close(doneCh)
		gotInfo, gotHanded, gotErr = s2.upgradeHeadless(ctx2, Options{Required: []string{"posse.remote.v1"}}, pkg)
	}()
	<-handoffEntered
	cancel2() // 进入 handoff 后取消上层 ctx
	close(releaseHandoff)
	<-doneCh
	if gotErr != nil || !gotHanded || gotInfo.InstanceID != "inst-2" || activateCalls.Load() != 1 {
		t.Fatalf("stage 2 WithoutCancel failed: info=%+v handedOff=%v activate=%d err=%v", gotInfo, gotHanded, activateCalls.Load(), gotErr)
	}

	// 3. handoff 返回瞬时错误、第一次 daemon.info 也瞬时失败、第二次返回匹配目标版本/ServerTag：有限重试后恢复并执行 activate。
	var recActivate atomic.Int32
	var recInfoHits atomic.Int32
	sock3 := startUpgradeMockRPC(t, map[string]rpc.Handler{
		"daemon.handoff": func(context.Context, json.RawMessage) (any, error) {
			return nil, errors.New("connection reset after handoff")
		},
		"daemon.info": func(context.Context, json.RawMessage) (any, error) {
			if recInfoHits.Add(1) == 1 {
				return nil, errors.New("transient socket reset")
			}
			return rpc.RemoteInfo{
				InstanceID: "inst-3", Edition: "headless", Managed: true, Home: home, Version: "0.1.55",
				ServerTag: pkg.Tag, Platform: "linux", Arch: "amd64",
			}, nil
		},
	})
	s3 := &Session{
		Socket: sock3,
		Info:   rpc.RemoteInfo{InstanceID: "inst-1", Edition: "headless", Managed: true, Home: home, Version: "0.1.54"},
		stageOverride: func(context.Context, string, ServerPackage, func(Stage, float64)) (string, error) {
			return home + "/server/versions/" + pkg.Tag + "/posse", nil
		},
		activateOverride: func(context.Context, string, string, string) error {
			recActivate.Add(1)
			return nil
		},
	}
	if info3, handed3, err3 := s3.upgradeHeadless(context.Background(), Options{}, pkg); err3 != nil || !handed3 || info3.InstanceID != "inst-3" || recActivate.Load() != 1 || recInfoHits.Load() < 2 {
		t.Fatalf("handoff recovery failed: info=%+v handed=%v activate=%d hits=%d err=%v", info3, handed3, recActivate.Load(), recInfoHits.Load(), err3)
	}

	// 4. P0-1 回归：上传 0.1.55 但远端实际运行 0.2.0（无论正常回执还是丢回执恢复），绝不激活本次上传包。
	for _, lostReply := range []bool{false, true} {
		var badActivate atomic.Int32
		sockMis := startUpgradeMockRPC(t, map[string]rpc.Handler{
			"daemon.handoff": func(context.Context, json.RawMessage) (any, error) {
				if lostReply {
					return nil, errors.New("broken pipe")
				}
				return map[string]any{"ok": true}, nil
			},
			"daemon.info": func(context.Context, json.RawMessage) (any, error) {
				return rpc.RemoteInfo{
					InstanceID: "inst-other", Edition: "headless", Managed: true, Home: home,
					Version: "0.2.0", ServerTag: "0.2.0-999999999999", Platform: "linux", Arch: "amd64",
				}, nil
			},
		})
		sMis := &Session{
			Socket: sockMis,
			Info:   rpc.RemoteInfo{InstanceID: "inst-1", Edition: "headless", Managed: true, Home: home, Version: "0.1.54"},
			stageOverride: func(context.Context, string, ServerPackage, func(Stage, float64)) (string, error) {
				return home + "/server/versions/" + pkg.Tag + "/posse", nil
			},
			activateOverride: func(context.Context, string, string, string) error {
				badActivate.Add(1)
				return nil
			},
		}
		gotMis, handedMis, errMis := sMis.upgradeHeadless(context.Background(), Options{}, pkg)
		if !errors.Is(errMis, errMismatchedHandoffTarget) || !handedMis || gotMis.Version != "0.2.0" || badActivate.Load() != 0 {
			t.Fatalf("lostReply=%v mismatched target activated old pkg: info=%+v handed=%v act=%d err=%v", lostReply, gotMis, handedMis, badActivate.Load(), errMis)
		}
	}

	// 5. handoff 返回 rpc.CodeBusy 且实例未变：保留 CodeBusy 且 handedOff=false。
	sock4 := startUpgradeMockRPC(t, map[string]rpc.Handler{
		"daemon.handoff": func(context.Context, json.RawMessage) (any, error) {
			return nil, rpc.Errorf(rpc.CodeBusy, "handoff already in progress")
		},
		"daemon.info": func(context.Context, json.RawMessage) (any, error) {
			return rpc.RemoteInfo{InstanceID: "inst-1", Edition: "headless", Managed: true, Home: home, Version: "0.1.54"}, nil
		},
	})
	s4 := &Session{
		Socket: sock4,
		Info:   rpc.RemoteInfo{InstanceID: "inst-1", Edition: "headless", Managed: true, Home: home, Version: "0.1.54"},
		stageOverride: func(context.Context, string, ServerPackage, func(Stage, float64)) (string, error) {
			return home + "/server/versions/" + pkg.Tag + "/posse", nil
		},
	}
	_, handed4, err4 := s4.upgradeHeadless(context.Background(), Options{}, pkg)
	var re *rpc.Error
	if handed4 || !errors.As(err4, &re) || re.Code != rpc.CodeBusy {
		t.Fatalf("expected CodeBusy with handedOff=false, got handed=%v err=%v", handed4, err4)
	}
}

func TestConnectionsPinAndUpgradeActiveMutex(t *testing.T) {
	desktopVer := daemon.Version
	bundleDir := t.TempDir()
	_, _ = makeTestServerArchive(t, bundleDir, desktopVer, "linux", "amd64")

	var mu sync.Mutex
	curInst := "inst-1"
	curVer := "0.1.0"
	sock := startUpgradeMockRPC(t, map[string]rpc.Handler{
		"daemon.info": func(context.Context, json.RawMessage) (any, error) {
			mu.Lock()
			id, ver := curInst, curVer
			mu.Unlock()
			return rpc.RemoteInfo{
				InstanceID: id, Edition: "headless", Managed: true,
				Platform: "linux", Arch: "amd64", Home: "/home/dev/.posse", Version: ver,
			}, nil
		},
		"daemon.handoff": func(context.Context, json.RawMessage) (any, error) {
			mu.Lock()
			curInst, curVer = "inst-2", desktopVer
			mu.Unlock()
			return map[string]any{"ok": true}, nil
		},
	})

	stageStarted := make(chan struct{})
	releaseStage := make(chan struct{})
	closedCh := make(chan struct{})
	s := &Session{
		Host:   Host{ID: "h1", Name: "bwh", Target: "dev@bwh"},
		Socket: sock,
		Info:   rpc.RemoteInfo{InstanceID: "inst-1", Edition: "headless", Managed: true, Platform: "linux", Arch: "amd64", Home: "/home/dev/.posse", Version: "0.1.0"},
		done:   closedCh,
		cancel: func() { close(closedCh) },
		stageOverride: func(context.Context, string, ServerPackage, func(Stage, float64)) (string, error) {
			close(stageStarted)
			<-releaseStage
			return "/home/dev/.posse/server/versions/0.1.55-abc/posse", nil
		},
		activateOverride: func(context.Context, string, string, string) error { return nil },
	}

	p := NewConnections()
	p.ttl = 30 * time.Millisecond
	p.limit = 1
	p.mu.Lock()
	p.active = s
	p.mu.Unlock()

	doneUp := make(chan struct{})
	var upInfo rpc.RemoteInfo
	var upHanded bool
	var upErr error
	go func() {
		defer close(doneUp)
		upInfo, upHanded, upErr = p.UpgradeActive(context.Background(), "h1", Options{
			ServerSearch:          []string{bundleDir},
			DisableRemoteDownload: true,
		})
	}()

	<-stageStarted

	// 并发自动检查遇到 upgradeMu 被占用时直接让路不阻塞；第二个手动升级立即报 CodeBusy。
	if err := s.maybeUpgradeHeadless(context.Background(), s.Host, Options{
		AutoInstall:           true,
		ServerSearch:          []string{bundleDir},
		DisableRemoteDownload: true,
	}); err != nil {
		t.Fatalf("concurrent maybeUpgradeHeadless failed: %v", err)
	}
	if _, _, busyErr := p.UpgradeActive(context.Background(), "h1", Options{
		ServerSearch:          []string{bundleDir},
		DisableRemoteDownload: true,
	}); busyErr == nil {
		t.Fatal("expected second concurrent UpgradeActive to fail fast with busy")
	} else {
		var re *rpc.Error
		if !errors.As(busyErr, &re) || re.Code != rpc.CodeBusy {
			t.Fatalf("expected CodeBusy, got %v", busyErr)
		}
	}

	// Park 后尝试并发 Open 同一主机：不改写 s.Host（无 -race）、不重拨第二条隧道，明确返回 CodeBusy。
	p.Park()
	var dialCalls atomic.Int32
	p.dial = func(context.Context, Host, Options) (*Session, error) {
		dialCalls.Add(1)
		return nil, errors.New("should not dial while pinned")
	}
	openHost := s.Host
	openHost.Name = "renamed-bwh"
	if _, openErr := p.Open(context.Background(), openHost, Options{AutoInstall: true}); openErr == nil {
		t.Fatal("expected Open on pinned upgrading session to return CodeBusy")
	} else {
		var re *rpc.Error
		if !errors.As(openErr, &re) || re.Code != rpc.CodeBusy || dialCalls.Load() != 0 {
			t.Fatalf("expected CodeBusy without dial, got err=%v dials=%d", openErr, dialCalls.Load())
		}
	}

	// TTL 过期期间，被 pin 的 Session 绝不能被提前关闭。
	time.Sleep(60 * time.Millisecond)
	select {
	case <-closedCh:
		t.Fatal("pinned session was closed while UpgradeActive was still running")
	default:
	}

	close(releaseStage)
	<-doneUp
	if upErr != nil || !upHanded || upInfo.InstanceID != "inst-2" {
		t.Fatalf("UpgradeActive failed: info=%+v handed=%v err=%v", upInfo, upHanded, upErr)
	}

	// 解钉后，之前在 TTL 到期时被标记待关闭的 Session 立即回收。
	select {
	case <-closedCh:
	case <-time.After(2 * time.Second):
		t.Fatal("pendingClose session was not closed after unpin")
	}
	p.Close()
}
