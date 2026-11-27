package daemon

import (
	"context"
	"encoding/json"
	"reflect"
	"strings"
	"testing"

	"posse/internal/rpc"
	"posse/internal/tslink"
)

func TestBuildMobileHostMetaAndDeskSupport(t *testing.T) {
	fullMac := buildMobileHostMeta("darwin", "arm64", "full", false, "0.1.54", "690628d")
	if fullMac.Platform != "darwin" || fullMac.Edition != "full" || fullMac.Managed {
		t.Fatalf("unexpected fullMac meta: %+v", fullMac)
	}
	if !reflect.DeepEqual(fullMac.Capabilities, []string{"desk", "memory", "autostart"}) {
		t.Fatalf("fullMac caps = %v", fullMac.Capabilities)
	}
	if !deskHostSupported("darwin", "full") {
		t.Fatal("expected darwin full to support desk")
	}

	headlessMac := buildMobileHostMeta("darwin", "arm64", "headless", true, "0.1.54", "690628d")
	if !headlessMac.Managed || !reflect.DeepEqual(headlessMac.Capabilities, []string{"memory", "autostart", "autostartWrite"}) {
		t.Fatalf("headlessMac meta = %+v", headlessMac)
	}
	if deskHostSupported("darwin", "headless") {
		t.Fatal("expected darwin headless not to support desk")
	}

	headlessLinux := buildMobileHostMeta("linux", "amd64", "headless", true, "0.1.54", "690628d")
	if !headlessLinux.Managed || !reflect.DeepEqual(headlessLinux.Capabilities, []string{"memory", "autostart", "autostartWrite"}) {
		t.Fatalf("headlessLinux meta = %+v", headlessLinux)
	}
	if deskHostSupported("linux", "headless") || deskHostSupported("linux", "full") {
		t.Fatal("expected linux not to support desk")
	}

	unmanagedLinux := buildMobileHostMeta("linux", "amd64", "headless", false, "0.1.54", "690628d")
	if unmanagedLinux.Managed || !reflect.DeepEqual(unmanagedLinux.Capabilities, []string{"memory", "autostart"}) {
		t.Fatalf("unmanagedLinux meta = %+v", unmanagedLinux)
	}
}

func TestDesktopCallMobilePairNeverCachesOrReplaysSecret(t *testing.T) {
	d := &Daemon{instanceID: "inst-secret"}
	t.Cleanup(d.desktop.shutdown)
	ctrlA := desktopTakeTest(t, d, "A")

	srv := rpc.NewServer()
	srv.Handle("mobile.pair", func(ctx context.Context, raw json.RawMessage) (any, error) {
		return map[string]any{
			"url":      "orca://pair?code=DUMMY_SECRET_TOKEN_XYZ",
			"code":     "DUMMY_SECRET_TOKEN_XYZ",
			"endpoint": "ws://100.64.0.9:6788",
		}, nil
	})

	req := rpc.DesktopCall{Desktop: ctrlA, Sequence: 1, Method: "mobile.pair", Params: json.RawMessage(`{"rotate":false}`)}
	res, err := desktopRequestCall(d, srv, context.Background(), req)
	if err != nil {
		t.Fatalf("first mobile.pair failed: %v", err)
	}
	raw, _ := json.Marshal(res)
	if !strings.Contains(string(raw), "DUMMY_SECRET_TOKEN_XYZ") {
		t.Fatalf("original caller did not receive pairing code: %s", raw)
	}

	// 查看端或任意客户端查 desktop.operation 绝不能读到缓存的配对密文。
	op := desktopOperationGet(t, d, req)
	if op.State != "done" || len(op.Result) != 0 {
		t.Fatalf("desktop.operation leaked secret result: state=%q result=%s", op.State, op.Result)
	}
	opsRaw, err := d.desktopOperations(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	opsJSON, _ := json.Marshal(opsRaw)
	if strings.Contains(string(opsJSON), "DUMMY_SECRET_TOKEN_XYZ") {
		t.Fatalf("desktop.operations leaked secret: %s", opsJSON)
	}

	// 相同序号重放：即使控制权仍在也禁止回放密文。
	_, err = desktopRequestCall(d, srv, context.Background(), req)
	desktopErrorCode(t, err, rpc.CodeOutcomeUnknown)

	// 控制权转移给 B 后，A 用旧身份重放直接被门禁拒绝（CodeReadOnly）。
	_ = desktopTakeTest(t, d, "B")
	_, err = desktopRequestCall(d, srv, context.Background(), req)
	desktopErrorCode(t, err, rpc.CodeReadOnly)
}

func TestDesktopMobileSurfaceAndTailscaleAuthURLRedacted(t *testing.T) {
	// 一期不开放无界面入口的远端写方法。
	for _, blocked := range []string{"relay.set", "relay.clear", "relay.invite", "tailscale.set", "tailscale.login", "tailscale.reset", "desk.stop"} {
		if _, allowed := rpc.DesktopMethod(blocked); allowed {
			t.Fatalf("method %q should not be exposed over desktop.call in Phase 1", blocked)
		}
	}
	// tailscale.status 经 desktop.call 读取时必须剥离 AuthURL。
	d := &Daemon{cfg: Config{Home: t.TempDir()}}
	ctx := context.WithValue(context.Background(), desktopCallContext{}, desktopRequestContext{readOnly: true})
	v, err := d.tailscaleStatus(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	st := v.(tslink.Status)
	if st.AuthURL != "" {
		t.Fatalf("expected AuthURL redacted on desktop.call, got %q", st.AuthURL)
	}
}
