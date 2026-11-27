package mobile

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"testing"
)

type metaFakeHost struct {
	*fakeHost
	meta HostMeta
}

func (m *metaFakeHost) HostMeta() HostMeta { return m.meta }

func TestStatusReportsHostMetaAndFallback(t *testing.T) {
	// 1. 未实现 HostMetaProvider 时按当前运行平台回退，不再写死 darwin。
	srvFallback, err := New(t.TempDir(), &fakeHost{}, 0)
	if err != nil {
		t.Fatal(err)
	}
	st := srvFallback.status()
	if got := st["hostPlatform"]; got != runtime.GOOS {
		t.Fatalf("fallback hostPlatform = %v, want %q", got, runtime.GOOS)
	}
	if got := st["hostArch"]; got != runtime.GOARCH {
		t.Fatalf("fallback hostArch = %v, want %q", got, runtime.GOARCH)
	}
	if got := st["appVersion"]; got != "posse" {
		t.Fatalf("appVersion = %v, want posse", got)
	}

	// 2. 实现 HostMetaProvider 时透传真实 Linux 无头节点信息与能力集。
	host := &metaFakeHost{
		fakeHost: &fakeHost{},
		meta: HostMeta{
			Platform:     "linux",
			Arch:         "amd64",
			Edition:      "headless",
			Managed:      true,
			Version:      "0.1.55",
			Commit:       "abc1234",
			Capabilities: []string{"memory", "autostart", "autostartWrite"},
		},
	}
	srv, err := New(t.TempDir(), host, 0)
	if err != nil {
		t.Fatal(err)
	}
	st = srv.status()
	if st["hostPlatform"] != "linux" || st["hostArch"] != "amd64" || st["hostEdition"] != "headless" {
		t.Fatalf("unexpected status platform/edition: %+v", st)
	}
	if st["hostManaged"] != true || st["posseVersion"] != "0.1.55" || st["posseCommit"] != "abc1234" {
		t.Fatalf("unexpected status managed/version: %+v", st)
	}
	gotCaps, ok := st["hostCapabilities"].([]string)
	if !ok || !reflect.DeepEqual(gotCaps, []string{"memory", "autostart", "autostartWrite"}) {
		t.Fatalf("hostCapabilities = %#v, want [memory autostart autostartWrite]", st["hostCapabilities"])
	}
}

func TestPairInfoIncludesLocalEndpoints(t *testing.T) {
	srv, err := New(t.TempDir(), &fakeHost{}, 0)
	if err != nil {
		t.Fatal(err)
	}
	srv.port = 0 // 测试使用随机空闲端口，避免撞本机常驻进程默认 6788
	defer srv.Close()
	info, err := srv.Pair(false, "127.0.0.1:6788")
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(info.Locals, LocalEndpoints()) {
		t.Fatalf("PairInfo.Locals = %v, want %v", info.Locals, LocalEndpoints())
	}
}

func TestStatusEmptyCapabilitiesEncodesAsArrayNotNull(t *testing.T) {
	hostEmpty := &metaFakeHost{
		fakeHost: &fakeHost{},
		meta: HostMeta{
			Platform:     "linux",
			Edition:      "headless",
			Capabilities: []string{},
		},
	}
	srvEmpty, err := New(t.TempDir(), hostEmpty, 0)
	if err != nil {
		t.Fatal(err)
	}
	b, err := json.Marshal(srvEmpty.status())
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(b), `"hostCapabilities":[]`) {
		t.Fatalf("expected explicit empty JSON array [], got %s", b)
	}

	hostNil := &metaFakeHost{
		fakeHost: &fakeHost{},
		meta:     HostMeta{Platform: "darwin"},
	}
	srvNil, err := New(t.TempDir(), hostNil, 0)
	if err != nil {
		t.Fatal(err)
	}
	bNil, err := json.Marshal(srvNil.status())
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(bNil), `"hostCapabilities"`) {
		t.Fatalf("expected hostCapabilities omitted when nil, got %s", bNil)
	}
}

func TestRemoveDeviceReturnsPersistErrorAndKeepsState(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("chmod read-only dir does not block root")
	}
	dir := t.TempDir()
	srv, err := New(dir, &fakeHost{}, 0)
	if err != nil {
		t.Fatal(err)
	}
	srv.port = 0
	defer srv.Close()
	dev, err := srv.pair.pending(false)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := srv.pair.auth(dev.Token); !ok {
		t.Fatal("expected auth to succeed")
	}
	if len(srv.Devices()) != 1 {
		t.Fatalf("expected 1 paired device, got %d", len(srv.Devices()))
	}

	// 将 home 目录设为只读，使原子持久化临时文件创建失败：RemoveDevice 必须返回错误并保持内存与磁盘一致。
	if err := os.Chmod(dir, 0o500); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(dir, 0o700) })

	removed, err := srv.RemoveDevice(dev.ID)
	if err == nil || removed {
		t.Fatalf("expected persist error on read-only dir, got removed=%v err=%v", removed, err)
	}
	if !srv.pair.valid(dev.Token) || len(srv.Devices()) != 1 {
		t.Fatal("device state mutated despite persistence failure")
	}

	// 恢复目录权限后重新移除应成功，且重载 mobile.json 后不再承认旧令牌。
	if err := os.Chmod(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	removed, err = srv.RemoveDevice(dev.ID)
	if err != nil || !removed {
		t.Fatalf("expected RemoveDevice to succeed after restore, got removed=%v err=%v", removed, err)
	}
	reloaded, err := openPairing(dir)
	if err != nil {
		t.Fatal(err)
	}
	if reloaded.valid(dev.Token) || len(reloaded.devices()) != 0 {
		t.Fatalf("reloaded pairing file %s still accepts removed token", filepath.Join(dir, "mobile.json"))
	}
}
