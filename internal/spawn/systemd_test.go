package spawn

import (
	"context"
	"errors"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"
)

func TestSystemdUnitBase(t *testing.T) {
	userHome := t.TempDir()
	def := filepath.Join(userHome, ".posse")
	if got := SystemdUnitBase(def, userHome); got != "posse-server" {
		t.Fatalf("default unit = %q, want posse-server", got)
	}
	custom := filepath.Join(userHome, ".posse-custom")
	got := SystemdUnitBase(custom, userHome)
	if !strings.HasPrefix(got, "posse-server-") || len(got) != len("posse-server-")+8 {
		t.Fatalf("custom unit = %q", got)
	}
}

func TestSystemdUserInstallQueryUninstall(t *testing.T) {
	userHome := t.TempDir()
	posseHome := filepath.Join(userHome, ".posse")
	unitsDir := filepath.Join(userHome, ".config", "systemd", "user")
	curDir := filepath.Join(posseHome, "server", "current")
	if err := os.MkdirAll(curDir, 0o755); err != nil {
		t.Fatal(err)
	}
	exePath := filepath.Join(curDir, "posse")
	if err := os.WriteFile(exePath, []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}

	var calls [][]string
	enabled := false
	active := false
	fakeRun := func(_ context.Context, args ...string) ([]byte, error) {
		calls = append(calls, append([]string(nil), args...))
		if len(args) >= 2 && args[0] == "--user" {
			switch args[1] {
			case "daemon-reload":
				return nil, nil
			case "enable":
				enabled = true
				return nil, nil
			case "start":
				active = true
				return nil, nil
			case "stop":
				active = false
				return nil, nil
			case "disable":
				enabled, active = false, false
				return nil, nil
			case "is-enabled":
				if enabled {
					return []byte("enabled\n"), nil
				}
				return []byte("disabled\n"), errors.New("exit status 1")
			case "is-active":
				if active {
					return []byte("active\n"), nil
				}
				return []byte("inactive\n"), errors.New("exit status 3")
			}
		}
		return nil, errors.New("unexpected call")
	}
	fakeLoginctl := func(context.Context, ...string) ([]byte, error) {
		return []byte("no\n"), nil
	}

	st, err := InstallSystemdUser(context.Background(), SystemdUserOptions{
		Home:         posseHome,
		UserHome:     userHome,
		UnitsDir:     unitsDir,
		Interval:     45,
		RunSystemctl: fakeRun,
		RunLoginctl:  fakeLoginctl,
	})
	if err != nil {
		t.Fatalf("InstallSystemdUser: %v", err)
	}
	if !st.Installed || !st.Enabled || !st.Active || st.Unit != "posse-server.service" || st.Linger != "no" || !strings.Contains(st.Note, "enable-linger") {
		t.Fatalf("install status = %+v", st)
	}
	svcRaw, err := os.ReadFile(st.ServicePath)
	if err != nil {
		t.Fatal(err)
	}
	svcText := string(svcRaw)
	if !strings.Contains(svcText, "Type=oneshot") || !strings.Contains(svcText, "KillMode=process") || !strings.Contains(svcText, "daemon start") {
		t.Fatalf("unexpected service file:\n%s", svcText)
	}
	timerRaw, err := os.ReadFile(st.TimerPath)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(timerRaw), "OnUnitActiveSec=45s") {
		t.Fatalf("unexpected timer file:\n%s", string(timerRaw))
	}

	q, err := QuerySystemdUser(context.Background(), SystemdUserOptions{
		Home:         posseHome,
		UserHome:     userHome,
		UnitsDir:     unitsDir,
		RunSystemctl: fakeRun,
		RunLoginctl:  fakeLoginctl,
	})
	if err != nil || !q.Installed || !q.Enabled || !q.Active || q.Linger != "no" {
		t.Fatalf("QuerySystemdUser = %+v, err=%v", q, err)
	}

	u, err := UninstallSystemdUser(context.Background(), SystemdUserOptions{
		Home:         posseHome,
		UserHome:     userHome,
		UnitsDir:     unitsDir,
		RunSystemctl: fakeRun,
	})
	if err != nil || u.Installed || u.Enabled || u.Active {
		t.Fatalf("UninstallSystemdUser = %+v, err=%v", u, err)
	}
	if _, err := os.Stat(st.ServicePath); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("service file still exists: %v", err)
	}
	if _, err := os.Stat(st.TimerPath); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("timer file still exists: %v", err)
	}

	// NoStart 只启用单元，不触发 start
	calls = nil
	stNoStart, err := InstallSystemdUser(context.Background(), SystemdUserOptions{
		Home:         posseHome,
		UserHome:     userHome,
		UnitsDir:     unitsDir,
		NoStart:      true,
		RunSystemctl: fakeRun,
		RunLoginctl:  func(context.Context, ...string) ([]byte, error) { return []byte("yes\n"), nil },
	})
	if err != nil || !stNoStart.Enabled || stNoStart.Active || stNoStart.Linger != "yes" || stNoStart.Note != "" {
		t.Fatalf("NoStart status = %+v, err=%v", stNoStart, err)
	}
	for _, c := range calls {
		if slices.Contains(c, "start") {
			t.Fatalf("NoStart unexpectedly called start: %v", c)
		}
	}
}

func TestSystemdUserInstallBusUnavailableAndRollback(t *testing.T) {
	userHome := t.TempDir()
	posseHome := filepath.Join(userHome, ".posse")
	unitsDir := filepath.Join(userHome, ".config", "systemd", "user")
	curDir := filepath.Join(posseHome, "server", "current")
	if err := os.MkdirAll(curDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(curDir, "posse"), []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}

	// 无用户总线时保留 unit 文件并在 note 中提示开启 linger
	st, err := InstallSystemdUser(context.Background(), SystemdUserOptions{
		Home:     posseHome,
		UserHome: userHome,
		UnitsDir: unitsDir,
		RunSystemctl: func(context.Context, ...string) ([]byte, error) {
			return []byte("Failed to connect to bus: No medium found\n"), errors.New("exit status 1")
		},
	})
	if err != nil {
		t.Fatalf("bus unavailable returned error: %v", err)
	}
	if !st.Installed || st.Enabled || st.Active || !strings.Contains(st.Note, "linger") {
		t.Fatalf("unexpected bus-unavailable status: %+v", st)
	}

	// enable 成功后 start 失败：必须回滚 unit 文件并 disable 刚创建的启用链接
	userHome3 := t.TempDir()
	posseHome3 := filepath.Join(userHome3, ".posse")
	unitsDir3 := filepath.Join(userHome3, ".config", "systemd", "user")
	curDir3 := filepath.Join(posseHome3, "server", "current")
	if err := os.MkdirAll(curDir3, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(curDir3, "posse"), []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	disabledOnRollback := false
	st3, err := InstallSystemdUser(context.Background(), SystemdUserOptions{
		Home:     posseHome3,
		UserHome: userHome3,
		UnitsDir: unitsDir3,
		RunSystemctl: func(_ context.Context, args ...string) ([]byte, error) {
			if len(args) >= 2 && args[0] == "--user" {
				switch args[1] {
				case "is-enabled":
					return []byte("disabled\n"), errors.New("exit 1")
				case "is-active":
					return []byte("inactive\n"), errors.New("exit 3")
				case "daemon-reload", "enable":
					return nil, nil
				case "start":
					return []byte("Job failed"), errors.New("exit 1")
				case "disable":
					disabledOnRollback = true
					return nil, nil
				}
			}
			return nil, errors.New("unexpected")
		},
	})
	if err == nil || st3.Installed || st3.Enabled || !disabledOnRollback {
		t.Fatalf("start failure rollback incomplete: st=%+v disabled=%v err=%v", st3, disabledOnRollback, err)
	}
	if _, serr := os.Stat(st3.ServicePath); !errors.Is(serr, os.ErrNotExist) {
		t.Fatalf("service file remained after start rollback: %v", serr)
	}

	// 缺少 systemctl 属于真实失败，必须回滚新写入的 unit 文件
	userHome2 := t.TempDir()
	posseHome2 := filepath.Join(userHome2, ".posse")
	unitsDir2 := filepath.Join(userHome2, ".config", "systemd", "user")
	curDir2 := filepath.Join(posseHome2, "server", "current")
	if err := os.MkdirAll(curDir2, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(curDir2, "posse"), []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	st2, err := InstallSystemdUser(context.Background(), SystemdUserOptions{
		Home:     posseHome2,
		UserHome: userHome2,
		UnitsDir: unitsDir2,
		RunSystemctl: func(context.Context, ...string) ([]byte, error) {
			return nil, exec.ErrNotFound
		},
	})
	if err == nil || st2.Installed {
		t.Fatalf("expected error + rollback when systemctl missing: st=%+v err=%v", st2, err)
	}
	if _, serr := os.Stat(st2.ServicePath); !errors.Is(serr, os.ErrNotExist) {
		t.Fatalf("service file was not rolled back: %v", serr)
	}
}

func TestSystemctlEnvCustomXDGAndSocket(t *testing.T) {
	beforeEnv := slices.Clone(os.Environ())
	dir := t.TempDir()
	busPath := filepath.Join(dir, "bus")
	ln, err := net.Listen("unix", busPath)
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()

	// 已有自定义 XDG_RUNTIME_DIR 且缺少 DBUS_SESSION_BUS_ADDRESS 时，从该 XDG 目录推导并校验 Unix socket。
	env := systemctlEnvFrom([]string{"PATH=/usr/bin", "XDG_RUNTIME_DIR=" + dir, "DBUS_SESSION_BUS_ADDRESS="}, os.Getuid())
	if !slices.Contains(env, "XDG_RUNTIME_DIR="+dir) {
		t.Fatalf("custom XDG_RUNTIME_DIR missing: %v", env)
	}
	if !slices.Contains(env, "DBUS_SESSION_BUS_ADDRESS=unix:path="+busPath) {
		t.Fatalf("bus address not derived from custom XDG dir: %v", env)
	}
	// 普通文件不是 Unix socket，不应写入 DBUS_SESSION_BUS_ADDRESS。
	plainDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(plainDir, "bus"), []byte("not-a-socket"), 0o600); err != nil {
		t.Fatal(err)
	}
	envPlain := systemctlEnvFrom([]string{"XDG_RUNTIME_DIR=" + plainDir}, os.Getuid())
	for _, kv := range envPlain {
		if strings.HasPrefix(kv, "DBUS_SESSION_BUS_ADDRESS=") {
			t.Fatalf("regular file was accepted as DBus socket: %v", envPlain)
		}
	}
	if !slices.Equal(beforeEnv, os.Environ()) {
		t.Fatal("systemctlEnvFrom mutated process os.Environ")
	}
}

func TestManagedCurrentStrictResolverAndCancelableLock(t *testing.T) {
	home := t.TempDir()
	// 1. 仅放 bin/posse 而没有受管 server/current/posse 时，RequireManagedCurrent 必须拒绝且不得改写 bin/posse。
	binDir := filepath.Join(home, "bin")
	if err := os.MkdirAll(binDir, 0o755); err != nil {
		t.Fatal(err)
	}
	binPosse := filepath.Join(binDir, "posse")
	if err := os.WriteFile(binPosse, []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	if _, err := resolveManagedServerExecutable(home); err == nil {
		t.Fatal("resolveManagedServerExecutable unexpectedly fell back to bin/posse")
	}

	// 2. server/current/posse 指向无 .posse-server-managed 标记的版本目录也必须拒绝。
	verDir := filepath.Join(home, "server", "versions", "0.1.54-abc123456789")
	if err := os.MkdirAll(verDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(verDir, "posse"), []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(verDir, filepath.Join(home, "server", "current")); err != nil {
		t.Fatal(err)
	}
	if _, err := resolveManagedServerExecutable(home); err == nil {
		t.Fatal("resolveManagedServerExecutable accepted unmanaged version dir without marker")
	}

	// 3. 补齐受管标记后校验通过，返回稳定的 server/current/posse 路径。
	if err := os.WriteFile(filepath.Join(verDir, ServerManagedMarker), []byte("managed\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	got, err := resolveManagedServerExecutable(home)
	if err != nil || got != filepath.Join(home, "server", "current", "posse") {
		t.Fatalf("resolveManagedServerExecutable = %q, %v", got, err)
	}

	// 4. 锁被持有时，lockServiceUnitCtx 必须按 ctx 取消而不是无限阻塞。
	lockDir := t.TempDir()
	unlock, err := lockServiceUnitCtx(context.Background(), lockDir, "posse-test")
	if err != nil {
		t.Fatal(err)
	}
	defer unlock()
	cctx, cancel := context.WithTimeout(context.Background(), 60*time.Millisecond)
	defer cancel()
	if _, err := lockServiceUnitCtx(cctx, lockDir, "posse-test"); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("contested lock did not honor ctx cancel: err=%v", err)
	}

	// 5. 提前取消的上下文在无竞争目录上不得创建锁文件或拿锁成功。
	canceledDir := filepath.Join(t.TempDir(), "should-not-exist")
	preCanceled, preCancel := context.WithCancel(context.Background())
	preCancel()
	if _, err := lockServiceUnitCtx(preCanceled, canceledDir, "posse-test"); !errors.Is(err, context.Canceled) {
		t.Fatalf("pre-canceled lock succeeded: err=%v", err)
	}
	if _, err := os.Stat(canceledDir); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("pre-canceled lock created dir: err=%v", err)
	}

	// 6. systemd start 部分失败时回滚必须执行 stop timer。
	home2 := t.TempDir()
	verDir2 := filepath.Join(home2, "server", "versions", "0.1.54-def123456789")
	if err := os.MkdirAll(verDir2, 0o755); err != nil {
		t.Fatal(err)
	}
	_ = os.WriteFile(filepath.Join(verDir2, "posse"), []byte("#!/bin/sh\n"), 0o755)
	_ = os.WriteFile(filepath.Join(verDir2, ServerManagedMarker), []byte("managed\n"), 0o600)
	_ = os.Symlink(verDir2, filepath.Join(home2, "server", "current"))
	var calls [][]string
	_, err = InstallSystemdUser(context.Background(), SystemdUserOptions{
		Home:                  home2,
		UserHome:              t.TempDir(),
		UnitsDir:              t.TempDir(),
		RequireManagedCurrent: true,
		RunSystemctl: func(_ context.Context, args ...string) ([]byte, error) {
			calls = append(calls, slices.Clone(args))
			if len(args) >= 2 && args[1] == "is-enabled" {
				return []byte("not-found"), errors.New("exit 1")
			}
			if len(args) >= 2 && args[1] == "is-active" {
				return []byte("inactive"), errors.New("exit 3")
			}
			if len(args) >= 2 && args[1] == "start" {
				return []byte("service failed"), errors.New("exit 1")
			}
			return nil, nil
		},
	})
	if err == nil {
		t.Fatal("expected start failure")
	}
	stopped := false
	for _, c := range calls {
		if len(c) >= 2 && c[1] == "stop" {
			stopped = true
		}
	}
	if !stopped {
		t.Fatalf("systemd partial start failure rollback missed stop timer: calls=%v", calls)
	}

	// 7. macOS LaunchAgent 被系统禁用时应自动 enable 并重试 bootstrap；旧 plist 原先未加载时失败回滚不得误 bootstrap。
	agentsDir := t.TempDir()
	userHome3 := t.TempDir()
	label3 := LaunchAgentLabel(home2, userHome3)
	oldPlist := filepath.Join(agentsDir, label3+".plist")
	_ = os.WriteFile(oldPlist, []byte("<plist>old</plist>"), 0o644)
	var lcalls [][]string
	bootCount := 0
	st, err := InstallLaunchAgent(context.Background(), LaunchAgentOptions{
		Home:                  home2,
		UserHome:              userHome3,
		AgentsDir:             agentsDir,
		Domain:                "gui/501",
		RequireManagedCurrent: true,
		RunLaunchctl: func(_ context.Context, args ...string) ([]byte, error) {
			lcalls = append(lcalls, slices.Clone(args))
			switch args[0] {
			case "bootout":
				return []byte("Could not find service"), errors.New("exit status 113")
			case "bootstrap":
				bootCount++
				if bootCount == 1 {
					return []byte("Bootstrap failed: 125: Domain does not support specified action"), errors.New("exit status 125")
				}
				return nil, nil
			case "print-disabled":
				return []byte("\"" + label3 + "\" => true\n"), nil
			case "enable":
				return nil, nil
			}
			return nil, nil
		},
	})
	if err != nil || !st.Loaded {
		t.Fatalf("disabled LaunchAgent was not re-enabled: st=%+v err=%v calls=%v", st, err, lcalls)
	}

	// 回滚不误启未加载的旧 plist。
	var rbCalls [][]string
	_, err = InstallLaunchAgent(context.Background(), LaunchAgentOptions{
		Home:                  home2,
		UserHome:              userHome3,
		AgentsDir:             agentsDir,
		Domain:                "gui/501",
		RequireManagedCurrent: true,
		RunLaunchctl: func(_ context.Context, args ...string) ([]byte, error) {
			rbCalls = append(rbCalls, slices.Clone(args))
			switch args[0] {
			case "bootout":
				return []byte("Could not find service"), errors.New("exit status 113")
			case "bootstrap":
				return []byte("Bootstrap failed"), errors.New("exit status 5")
			case "print-disabled":
				return []byte("disabled services = {}\n"), nil
			}
			return nil, nil
		},
	})
	if err == nil {
		t.Fatal("expected bootstrap failure")
	}
	bootstraps := 0
	for _, c := range rbCalls {
		if len(c) > 0 && c[0] == "bootstrap" {
			bootstraps++
		}
	}
	if bootstraps != 1 {
		t.Fatalf("rollback unexpectedly re-bootstrapped previously unloaded plist: calls=%v", rbCalls)
	}
}
