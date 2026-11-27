package remote

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"posse/internal/rpc"
)

func TestParseMissingMarker(t *testing.T) {
	_, err := parseProbe([]byte("motd banner\nPOSSE_REMOTE_MISSING:{\"os\":\"Darwin\",\"arch\":\"arm64\",\"home\":\"/Users/test/.posse\"}\n"))
	var missing *RemoteNotInstalledError
	if !errors.As(err, &missing) {
		t.Fatalf("expected RemoteNotInstalledError, got %v", err)
	}
	if missing.OS != "Darwin" || missing.Arch != "arm64" || missing.Home != "/Users/test/.posse" {
		t.Fatalf("missing = %+v", missing)
	}
	// 重复缺失标记或同时出现 probe + missing 均视为歧义错误
	if _, err := parseProbe([]byte(
		"POSSE_REMOTE_MISSING:{\"os\":\"Darwin\",\"arch\":\"arm64\",\"home\":\"/a\"}\n" +
			"POSSE_REMOTE_MISSING:{\"os\":\"Darwin\",\"arch\":\"arm64\",\"home\":\"/b\"}\n",
	)); err == nil || !strings.Contains(err.Error(), "ambiguous") {
		t.Fatalf("duplicate missing marker = %v, want ambiguous error", err)
	}
	if _, err := parseProbe([]byte(
		"POSSE_REMOTE_MISSING:{\"os\":\"Darwin\",\"arch\":\"arm64\",\"home\":\"/a\"}\n" +
			`POSSE_REMOTE_V1:{"protocol":1,"platform":"darwin","home":"/a","socket":"/a/s","running":true}` + "\n",
	)); err == nil || !strings.Contains(err.Error(), "ambiguous") {
		t.Fatalf("contradictory markers = %v, want ambiguous error", err)
	}
}

func TestStageAndCommitCommandSafety(t *testing.T) {
	dir := t.TempDir()
	remoteHome := filepath.Join(dir, "remote ' home")
	pkgDir := t.TempDir()
	// 构造带脚本的测试包，验证 stageAndCommitCommand 会把解压目录放在 versions/ 之外，
	// 正确传递参数给 posse server commit，并在退出时清理临时 staging 目录。
	script := "#!/bin/sh\n" +
		"echo \"ARGS:$*\" >&2\n" +
		"printf 'POSSE_REMOTE_INSTALLED:%s/server/versions/0.1.0-test/posse\\n' \"$POSSE_HOME\"\n"
	archivePath, _ := makeTestServerArchiveBytes(t, pkgDir, "0.1.0", "darwin", "arm64", []byte(script), []byte("fake-lib"))
	pkg, err := inspectAndSnapshot(context.Background(), archivePath, "0.1.0", "darwin", "arm64", "")
	if err != nil {
		t.Fatal(err)
	}
	defer pkg.Close()

	f, err := os.Open(pkg.Path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()

	cmd := exec.Command("/bin/sh", "-c", stageAndCommitCommand(remoteHome, pkg))
	cmd.Stdin = f
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("stageAndCommitCommand failed: %v\n%s", err, out)
	}
	exe, err := parseSingleMarker(out, installedMarker)
	if err != nil {
		t.Fatalf("parseSingleMarker(%q): %v", out, err)
	}
	if !strings.HasSuffix(exe, "/server/versions/0.1.0-test/posse") {
		t.Fatalf("exe = %q", exe)
	}
	// staging 临时目录应被 trap 清理干净
	entries, err := os.ReadDir(filepath.Join(remoteHome, "server", "staging"))
	if err != nil {
		t.Fatalf("read staging dir: %v", err)
	}
	if len(entries) != 0 {
		t.Fatalf("leftover staging entries: %v", entries)
	}
}

func TestConnectionsReuseFallbackOnStoppedDaemon(t *testing.T) {
	c := NewConnections()
	defer c.Close()

	h := Host{ID: "h1", Name: "Mac", Target: "mac.local"}
	dialCalls := 0
	c.dial = func(_ context.Context, h Host, _ Options) (*Session, error) {
		dialCalls++
		done := make(chan struct{})
		return &Session{
			Host:   h,
			Info:   rpc.RemoteInfo{InstanceID: "inst"},
			cancel: func() { close(done) },
			done:   done,
		}, nil
	}
	checkCalls := 0
	c.check = func(_ context.Context, _ *Session, _ Options) error {
		checkCalls++
		return errors.New("daemon stopped")
	}

	if _, err := c.Open(context.Background(), h, Options{Start: true}); err != nil {
		t.Fatalf("first Open: %v", err)
	}
	c.Park()
	// 空闲复用 check 失败且 Start=true 时，应自动丢弃旧隧道并重新 dial 一次成功
	if _, err := c.Open(context.Background(), h, Options{Start: true}); err != nil {
		t.Fatalf("second Open with fallback failed: %v", err)
	}
	if checkCalls != 1 || dialCalls != 2 {
		t.Fatalf("checkCalls=%d dialCalls=%d, want 1 and 2", checkCalls, dialCalls)
	}
}
