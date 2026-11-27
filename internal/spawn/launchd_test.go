package spawn

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"testing"
)

func TestLaunchAgentLabel(t *testing.T) {
	userHome := t.TempDir()
	defaultHome := filepath.Join(userHome, ".posse")
	if got := LaunchAgentLabel(defaultHome, userHome); got != defaultLaunchAgentLabel {
		t.Fatalf("default label = %q, want %q", got, defaultLaunchAgentLabel)
	}
	customHome := filepath.Join(userHome, ".posse-dev")
	got := LaunchAgentLabel(customHome, userHome)
	if !strings.HasPrefix(got, defaultLaunchAgentLabel+".") || len(got) != len(defaultLaunchAgentLabel)+9 {
		t.Fatalf("custom label = %q", got)
	}
	if got == LaunchAgentLabel(filepath.Join(userHome, ".posse-other"), userHome) {
		t.Fatal("different custom homes collided on label")
	}
}

func TestInstallQueryUninstallLaunchAgent(t *testing.T) {
	userHome := t.TempDir()
	home := filepath.Join(userHome, ".posse")
	agentsDir := filepath.Join(userHome, "Library", "LaunchAgents")
	fakeExe := filepath.Join(userHome, "posse-bin")
	if err := os.WriteFile(fakeExe, []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}

	var calls [][]string
	loaded := false
	runner := func(_ context.Context, args ...string) ([]byte, error) {
		calls = append(calls, append([]string(nil), args...))
		switch args[0] {
		case "bootout":
			loaded = false
			return nil, nil
		case "bootstrap":
			loaded = true
			return nil, nil
		case "print":
			if loaded {
				return []byte("state = running\n"), nil
			}
			return []byte("Could not find service"), errors.New("not loaded")
		default:
			return nil, nil
		}
	}

	st, err := InstallLaunchAgent(context.Background(), LaunchAgentOptions{
		Home:         home,
		UserHome:     userHome,
		AgentsDir:    agentsDir,
		Executable:   fakeExe,
		Domain:       "gui/501",
		RunLaunchctl: runner,
	})
	if err != nil {
		t.Fatalf("InstallLaunchAgent failed: %v", err)
	}
	if !st.Installed || !st.Loaded || st.Label != defaultLaunchAgentLabel || st.Interval != 60 {
		t.Fatalf("unexpected status: %+v", st)
	}
	// 确认 $POSSE_HOME/bin/posse 链接已创建，且 plist 指向该稳定路径而非副本
	wantLink := filepath.Join(canonicalPath(home), "bin", "posse")
	if st.Executable != wantLink {
		t.Fatalf("executable = %q, want %q", st.Executable, wantLink)
	}
	if _, err := os.Stat(wantLink); err != nil {
		t.Fatalf("posse link missing: %v", err)
	}
	raw, err := os.ReadFile(st.Plist)
	if err != nil {
		t.Fatal(err)
	}
	plistText := string(raw)
	for _, want := range []string{
		"<string>" + defaultLaunchAgentLabel + "</string>",
		"<string>" + wantLink + "</string>",
		"<string>daemon</string>",
		"<string>start</string>",
		"<key>POSSE_HOME</key>",
		"<key>RunAtLoad</key>",
		"<key>StartInterval</key>",
		"<integer>60</integer>",
		"<string>Aqua</string>",
		"<string>Interactive</string>",
	} {
		if !strings.Contains(plistText, want) {
			t.Fatalf("plist missing %q:\n%s", want, plistText)
		}
	}
	if strings.Contains(plistText, "KeepAlive") {
		t.Fatalf("plist must not set KeepAlive:\n%s", plistText)
	}
	if runtime.GOOS == "darwin" {
		if out, err := exec.Command("/usr/bin/plutil", "-lint", st.Plist).CombinedOutput(); err != nil {
			t.Fatalf("plutil -lint failed: %v: %s", err, string(out))
		}
	}

	wantCalls := [][]string{
		{"bootout", "gui/501/" + defaultLaunchAgentLabel},
		{"bootstrap", "gui/501", st.Plist},
	}
	if !reflect.DeepEqual(calls, wantCalls) {
		t.Fatalf("launchctl calls = %v, want %v", calls, wantCalls)
	}

	q, err := QueryLaunchAgent(context.Background(), LaunchAgentOptions{
		Home:         home,
		UserHome:     userHome,
		AgentsDir:    agentsDir,
		Domain:       "gui/501",
		RunLaunchctl: runner,
	})
	if err != nil || !q.Installed || !q.Loaded {
		t.Fatalf("QueryLaunchAgent = %+v (%v)", q, err)
	}

	u, err := UninstallLaunchAgent(context.Background(), LaunchAgentOptions{
		Home:         home,
		UserHome:     userHome,
		AgentsDir:    agentsDir,
		Domain:       "gui/501",
		RunLaunchctl: runner,
	})
	if err != nil || u.Installed || u.Loaded {
		t.Fatalf("UninstallLaunchAgent = %+v (%v)", u, err)
	}
	if _, err := os.Stat(st.Plist); !os.IsNotExist(err) {
		t.Fatalf("plist still exists after uninstall: %v", err)
	}
}

func TestInstallLaunchAgentWithoutAquaSessionKeepsPlist(t *testing.T) {
	userHome := t.TempDir()
	// 包含 XML 特殊字符的目录名也必须生成合法 plist
	home := filepath.Join(userHome, "posse&home")
	agentsDir := filepath.Join(userHome, "Library", "LaunchAgents")
	fakeExe := filepath.Join(userHome, "posse-bin")
	if err := os.WriteFile(fakeExe, []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}

	noAqua := func(_ context.Context, args ...string) ([]byte, error) {
		switch args[0] {
		case "bootstrap":
			return []byte("Bootstrap failed: 5: Input/output error"), errors.New("exit status 5")
		case "print-disabled":
			return []byte("Could not find domain"), errors.New("exit status 113")
		default:
			return nil, nil
		}
	}
	st, err := InstallLaunchAgent(context.Background(), LaunchAgentOptions{
		Home:         home,
		UserHome:     userHome,
		AgentsDir:    agentsDir,
		Executable:   fakeExe,
		Domain:       "gui/501",
		RunLaunchctl: noAqua,
	})
	if err != nil {
		t.Fatalf("InstallLaunchAgent without Aqua should succeed softly: %v", err)
	}
	if !st.Installed || st.Loaded || st.Note == "" {
		t.Fatalf("unexpected non-Aqua status: %+v", st)
	}
	if runtime.GOOS == "darwin" {
		if out, err := exec.Command("/usr/bin/plutil", "-lint", st.Plist).CombinedOutput(); err != nil {
			t.Fatalf("XML-escaped plist failed plutil -lint: %v: %s", err, string(out))
		}
	}

	// Aqua 域存在但 bootstrap 仍失败时必须报错
	realFail := func(_ context.Context, args ...string) ([]byte, error) {
		switch args[0] {
		case "bootstrap":
			return []byte("Bootstrap failed: 125: Domain does not support specified action"), errors.New("exit status 125")
		case "print-disabled":
			return []byte("disabled services = {}"), nil
		default:
			return nil, nil
		}
	}
	if _, err := InstallLaunchAgent(context.Background(), LaunchAgentOptions{
		Home:         home,
		UserHome:     userHome,
		AgentsDir:    agentsDir,
		Executable:   fakeExe,
		Domain:       "gui/501",
		RunLaunchctl: realFail,
	}); err == nil {
		t.Fatal("expected error when Aqua domain exists but bootstrap failed")
	}
}

func TestLaunchAgentHeadlessCurrentPriorityAndErrorSafety(t *testing.T) {
	userHome := t.TempDir()
	home := filepath.Join(userHome, ".posse")
	agentsDir := filepath.Join(userHome, "Library", "LaunchAgents")

	// 相对路径必须被拒绝
	if _, err := InstallLaunchAgent(context.Background(), LaunchAgentOptions{
		Home:      "relative-home",
		UserHome:  userHome,
		AgentsDir: agentsDir,
	}); err == nil {
		t.Fatal("relative home was accepted")
	}

	// 先放一个旧的 bin/posse 链接，再建托管无头版本 server/current/posse，LaunchAgent 必须优先指向 server/current/posse
	oldBin := filepath.Join(userHome, "old-posse")
	if err := os.WriteFile(oldBin, []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(home, "bin"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(oldBin, filepath.Join(home, "bin", "posse")); err != nil {
		t.Fatal(err)
	}
	verDir := filepath.Join(home, "server", "versions", "0.9.0-abc123")
	if err := os.MkdirAll(verDir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(verDir, "posse"), []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join("versions", "0.9.0-abc123"), filepath.Join(home, "server", "current")); err != nil {
		t.Fatal(err)
	}

	okRunner := func(_ context.Context, args ...string) ([]byte, error) { return nil, nil }
	st, err := InstallLaunchAgent(context.Background(), LaunchAgentOptions{
		Home:         home,
		UserHome:     userHome,
		AgentsDir:    agentsDir,
		Domain:       "gui/501",
		RunLaunchctl: okRunner,
	})
	if err != nil {
		t.Fatalf("InstallLaunchAgent failed: %v", err)
	}
	wantCurrent := filepath.Join(canonicalPath(home), "server", "current", "posse")
	if st.Executable != wantCurrent {
		t.Fatalf("headless LaunchAgent executable = %q, want %q", st.Executable, wantCurrent)
	}
	origPlist, err := os.ReadFile(st.Plist)
	if err != nil {
		t.Fatal(err)
	}

	// 权限失败（非 domain-not-found）绝不能伪报待登录，且应恢复旧 plist
	permFail := func(_ context.Context, args ...string) ([]byte, error) {
		switch args[0] {
		case "bootstrap":
			return []byte("Operation not permitted"), errors.New("exit status 1")
		case "print-disabled":
			return []byte("Operation not permitted"), errors.New("exit status 1")
		default:
			return nil, nil
		}
	}
	if _, err := InstallLaunchAgent(context.Background(), LaunchAgentOptions{
		Home:         home,
		UserHome:     userHome,
		AgentsDir:    agentsDir,
		Interval:     120,
		Domain:       "gui/501",
		RunLaunchctl: permFail,
	}); err == nil {
		t.Fatal("permission failure was misreported as non-Aqua success")
	}
	afterPlist, err := os.ReadFile(st.Plist)
	if err != nil || string(afterPlist) != string(origPlist) {
		t.Fatal("failed update did not restore previous plist")
	}

	// bootout 失败且 print 显示作业仍在加载时，卸载必须报错且保留 plist
	stillLoaded := func(_ context.Context, args ...string) ([]byte, error) {
		switch args[0] {
		case "bootout":
			return []byte("Operation not permitted"), errors.New("exit status 1")
		case "print":
			return []byte("state = running"), nil
		default:
			return nil, nil
		}
	}
	if _, err := UninstallLaunchAgent(context.Background(), LaunchAgentOptions{
		Home:         home,
		UserHome:     userHome,
		AgentsDir:    agentsDir,
		Domain:       "gui/501",
		RunLaunchctl: stillLoaded,
	}); err == nil {
		t.Fatal("UninstallLaunchAgent reported success while service stayed loaded")
	}
	if _, err := os.Stat(st.Plist); err != nil {
		t.Fatal("UninstallLaunchAgent deleted plist while service stayed loaded")
	}

	// QueryLaunchAgent 遇到权限/工具异常必须报错，不能报成未加载
	queryFail := func(_ context.Context, args ...string) ([]byte, error) {
		return []byte("Operation not permitted"), errors.New("exit status 1")
	}
	if _, err := QueryLaunchAgent(context.Background(), LaunchAgentOptions{
		Home:         home,
		UserHome:     userHome,
		AgentsDir:    agentsDir,
		Domain:       "gui/501",
		RunLaunchctl: queryFail,
	}); err == nil {
		t.Fatal("QueryLaunchAgent masked launchctl permission error as unloaded")
	}
}
