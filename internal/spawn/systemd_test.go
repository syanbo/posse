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
