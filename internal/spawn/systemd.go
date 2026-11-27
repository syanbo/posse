package spawn

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"os/user"
	"path/filepath"
	"strings"
	"time"
)

const (
	defaultSystemdUnit     = "posse-server"
	defaultSystemdInterval = 60
	minSystemdInterval     = 10
	maxSystemdInterval     = 86400
	systemdCleanupTimeout  = 5 * time.Second
)

// SystemdUserOptions 配置 Linux systemd --user 服务安装、卸载或状态查询参数。
type SystemdUserOptions struct {
	Home                  string
	UserHome              string
	UnitsDir              string
	Executable            string
	Interval              int
	NoStart               bool
	RequireManagedCurrent bool // 仅允许通过 serverVersionDir 校验的 server/current/posse，禁止回退或改写 bin/posse
	RunSystemctl          func(ctx context.Context, args ...string) ([]byte, error)
	RunLoginctl           func(ctx context.Context, args ...string) ([]byte, error)
}

// SystemdUserStatus 描述 Linux systemd --user 服务与定时器的安装与启用状态。
type SystemdUserStatus struct {
	Unit        string `json:"unit"`
	ServicePath string `json:"servicePath"`
	TimerPath   string `json:"timerPath"`
	Home        string `json:"home"`
	Executable  string `json:"executable"`
	Interval    int    `json:"interval,omitempty"`
	Installed   bool   `json:"installed"`
	Enabled     bool   `json:"enabled"`
	Active      bool   `json:"active"`
	Linger      string `json:"linger,omitempty"`
	Note        string `json:"note,omitempty"`
	NoteCode    string `json:"noteCode,omitempty"`
}

// SystemdUnitBase 为默认 ~/.posse 返回固定单元名，自定义 home 追加路径摘要隔离。
func SystemdUnitBase(home, userHome string) string {
	canonHome := canonicalPath(home)
	defaultHome := canonicalPath(filepath.Join(userHome, ".posse"))
	if canonHome == defaultHome {
		return defaultSystemdUnit
	}
	sum := sha256.Sum256([]byte(canonHome))
	return defaultSystemdUnit + "-" + hex.EncodeToString(sum[:4])
}

// lockSystemdUnit 串行化同一单元的安装与卸载，支持 ctx 取消与超时，避免阻塞 daemon 写操作。
func lockSystemdUnit(ctx context.Context, unitsDir, base string) (func(), error) {
	return lockServiceUnitCtx(ctx, unitsDir, base)
}

// InstallSystemdUser 安装并启用 Linux systemd --user 单元（Type=oneshot + KillMode=process + 定时器）。
// 将 daemon-reload、enable 与 start 分步执行，并在失败或取消时按超时上限精确回滚本次改动的链接与状态；
// 即使当前存在活跃 user bus，也会检查 linger 并在未开启时给出明确提示。
func InstallSystemdUser(ctx context.Context, opts SystemdUserOptions) (SystemdUserStatus, error) {
	cfg, err := normalizeSystemdOptions(opts, true)
	if err != nil {
		return SystemdUserStatus{}, err
	}
	interval := opts.Interval
	if interval == 0 {
		interval = defaultSystemdInterval
	}
	if interval < minSystemdInterval || interval > maxSystemdInterval {
		return SystemdUserStatus{}, fmt.Errorf("systemd timer interval must be between %d and %d seconds", minSystemdInterval, maxSystemdInterval)
	}

	unlock, err := lockSystemdUnit(ctx, cfg.unitsDir, cfg.base)
	if err != nil {
		return SystemdUserStatus{}, err
	}
	defer unlock()

	var exePath string
	if opts.RequireManagedCurrent {
		exePath, err = resolveManagedServerExecutable(cfg.home)
	} else {
		exePath, err = resolveLaunchAgentExecutable(cfg.home, opts.Executable)
	}
	if err != nil {
		return SystemdUserStatus{}, err
	}
	serviceData, err := renderSystemdService(exePath, cfg.home)
	if err != nil {
		return SystemdUserStatus{}, err
	}
	timerData := renderSystemdTimer(cfg.serviceUnit, interval)

	prevSvc, prevSvcErr := os.ReadFile(cfg.servicePath)
	hadPrevSvc := prevSvcErr == nil
	if prevSvcErr != nil && !errors.Is(prevSvcErr, os.ErrNotExist) {
		return SystemdUserStatus{}, prevSvcErr
	}
	prevTimer, prevTimerErr := os.ReadFile(cfg.timerPath)
	hadPrevTimer := prevTimerErr == nil
	if prevTimerErr != nil && !errors.Is(prevTimerErr, os.ErrNotExist) {
		return SystemdUserStatus{}, prevTimerErr
	}

	// 记录安装前的启用与激活状态，失败回滚时仅撤回本次新增的启用/启动副作用。
	wasEnabled := false
	wasActive := false
	if enOut, enErr := cfg.run(ctx, "--user", "is-enabled", cfg.timerUnit); enErr == nil && strings.TrimSpace(string(enOut)) == "enabled" {
		wasEnabled = true
	}
	if actOut, actErr := cfg.run(ctx, "--user", "is-active", cfg.timerUnit); actErr == nil && strings.TrimSpace(string(actOut)) == "active" {
		wasActive = true
	}

	enabledAttempted := false
	startedAttempted := false
	rollback := func() {
		cleanCtx, cancel := context.WithTimeout(context.Background(), systemdCleanupTimeout)
		defer cancel()
		// 即使 start/enable 仅部分生效便失败或超时，也必须撤销本次尝试新增的 timer 激活和启用状态。
		if startedAttempted && !wasActive {
			_, _ = cfg.run(cleanCtx, "--user", "stop", cfg.timerUnit)
		}
		if enabledAttempted && !wasEnabled {
			_, _ = cfg.run(cleanCtx, "--user", "disable", cfg.timerUnit, cfg.serviceUnit)
		}
		if hadPrevSvc {
			_ = atomicWriteFile(cfg.servicePath, prevSvc, 0o644)
		} else {
			_ = os.Remove(cfg.servicePath)
		}
		if hadPrevTimer {
			_ = atomicWriteFile(cfg.timerPath, prevTimer, 0o644)
		} else {
			_ = os.Remove(cfg.timerPath)
		}
		_, _ = cfg.run(cleanCtx, "--user", "daemon-reload")
	}

	if err := atomicWriteFile(cfg.servicePath, serviceData, 0o644); err != nil {
		return SystemdUserStatus{}, err
	}
	if err := atomicWriteFile(cfg.timerPath, timerData, 0o644); err != nil {
		rollback()
		return SystemdUserStatus{}, err
	}

	st := SystemdUserStatus{
		Unit:        cfg.serviceUnit,
		ServicePath: cfg.servicePath,
		TimerPath:   cfg.timerPath,
		Home:        cfg.home,
		Executable:  exePath,
		Interval:    interval,
		Installed:   true,
	}

	if out, err := cfg.run(ctx, "--user", "daemon-reload"); err != nil {
		if ctx.Err() == nil && isSystemdBusUnavailable(out, err) {
			st.NoteCode = "systemd_bus_inactive"
			st.Note = fmt.Sprintf("systemd user session bus is not active; unit files are installed. Enable linger (loginctl enable-linger $USER) and run: systemctl --user enable --now %s %s", cfg.serviceUnit, cfg.timerUnit)
			return st, nil
		}
		rollback()
		st.Installed = hadPrevSvc || hadPrevTimer
		return st, wrapSystemctlError("daemon-reload", out, err)
	}

	enabledAttempted = true
	if out, err := cfg.run(ctx, "--user", "enable", cfg.serviceUnit, cfg.timerUnit); err != nil {
		if ctx.Err() == nil && isSystemdBusUnavailable(out, err) {
			st.NoteCode = "systemd_bus_inactive"
			st.Note = fmt.Sprintf("systemd user session bus is not active; unit files are installed. Enable linger (loginctl enable-linger $USER) and run: systemctl --user enable --now %s %s", cfg.serviceUnit, cfg.timerUnit)
			return st, nil
		}
		rollback()
		st.Installed = hadPrevSvc || hadPrevTimer
		return st, wrapSystemctlError("enable", out, err)
	}
	st.Enabled = true

	if !opts.NoStart {
		startedAttempted = true
		if out, err := cfg.run(ctx, "--user", "start", cfg.timerUnit, cfg.serviceUnit); err != nil {
			if ctx.Err() == nil && isSystemdBusUnavailable(out, err) {
				st.Enabled = false
				st.NoteCode = "systemd_bus_inactive"
				st.Note = fmt.Sprintf("systemd user session bus is not active; unit files are installed. Enable linger (loginctl enable-linger $USER) and run: systemctl --user enable --now %s %s", cfg.serviceUnit, cfg.timerUnit)
				return st, nil
			}
			rollback()
			st.Installed = hadPrevSvc || hadPrevTimer
			st.Enabled = wasEnabled
			st.Active = wasActive
			return st, wrapSystemctlError("start", out, err)
		}
		st.Active = true
	}

	// 检查 linger：即便当前 SSH 会话存在 user bus，未开 linger 时最后一条 SSH 退出后 user manager 仍会被回收。
	if linger := querySystemdLinger(ctx, cfg.runLoginctl); linger != "" {
		st.Linger = linger
		if linger == "no" {
			st.NoteCode = "systemd_linger_disabled"
			st.Note = "systemd user linger is disabled; run `loginctl enable-linger $USER` so the timer and service survive SSH logout and start on boot"
		}
	}
	return st, nil
}

// UninstallSystemdUser 禁用定时器与服务并删除 unit 文件（不停止正在运行的 daemon 或终端）。
func UninstallSystemdUser(ctx context.Context, opts SystemdUserOptions) (SystemdUserStatus, error) {
	cfg, err := normalizeSystemdOptions(opts, false)
	if err != nil {
		return SystemdUserStatus{}, err
	}
	unlock, err := lockSystemdUnit(ctx, cfg.unitsDir, cfg.base)
	if err != nil {
		return SystemdUserStatus{}, err
	}
	defer unlock()

	if out, err := cfg.run(ctx, "--user", "disable", "--now", cfg.timerUnit, cfg.serviceUnit); err != nil {
		if ctx.Err() != nil {
			return SystemdUserStatus{}, ctx.Err()
		}
		if !isSystemdUnitNotFound(out, err) && !isSystemdBusUnavailable(out, err) {
			return SystemdUserStatus{}, wrapSystemctlError("disable --now", out, err)
		}
	}
	if err := os.Remove(cfg.timerPath); err != nil && !errors.Is(err, os.ErrNotExist) {
		return SystemdUserStatus{}, err
	}
	if err := os.Remove(cfg.servicePath); err != nil && !errors.Is(err, os.ErrNotExist) {
		return SystemdUserStatus{}, err
	}
	reloadCtx, cancel := context.WithTimeout(context.Background(), systemdCleanupTimeout)
	defer cancel()
	_, _ = cfg.run(reloadCtx, "--user", "daemon-reload")
	return SystemdUserStatus{
		Unit:        cfg.serviceUnit,
		ServicePath: cfg.servicePath,
		TimerPath:   cfg.timerPath,
		Home:        cfg.home,
		Executable:  launchAgentExecutableHint(cfg.home),
		Installed:   false,
		Enabled:     false,
		Active:      false,
	}, nil
}

// QuerySystemdUser 查询 systemd --user 单元文件的安装、启用和激活状态。
func QuerySystemdUser(ctx context.Context, opts SystemdUserOptions) (SystemdUserStatus, error) {
	cfg, err := normalizeSystemdOptions(opts, false)
	if err != nil {
		return SystemdUserStatus{}, err
	}
	st := SystemdUserStatus{
		Unit:        cfg.serviceUnit,
		ServicePath: cfg.servicePath,
		TimerPath:   cfg.timerPath,
		Home:        cfg.home,
		Executable:  launchAgentExecutableHint(cfg.home),
	}
	svcInfo, svcErr := os.Stat(cfg.servicePath)
	timerInfo, timerErr := os.Stat(cfg.timerPath)
	if svcErr != nil && !errors.Is(svcErr, os.ErrNotExist) {
		return SystemdUserStatus{}, svcErr
	}
	if timerErr != nil && !errors.Is(timerErr, os.ErrNotExist) {
		return SystemdUserStatus{}, timerErr
	}
	st.Installed = svcErr == nil && svcInfo.Mode().IsRegular() && timerErr == nil && timerInfo.Mode().IsRegular()
	st.Linger = querySystemdLinger(ctx, cfg.runLoginctl)

	enOut, enErr := cfg.run(ctx, "--user", "is-enabled", cfg.timerUnit)
	switch {
	case enErr == nil:
		st.Enabled = strings.TrimSpace(string(enOut)) == "enabled"
	case ctx.Err() != nil:
		return st, ctx.Err()
	case isSystemdBusUnavailable(enOut, enErr):
		st.NoteCode = "systemd_bus_inactive"
		st.Note = "systemd user session bus is not active"
		return st, nil
	case isSystemdNotEnabledOrMissing(enOut, enErr):
		st.Enabled = false
	default:
		return st, wrapSystemctlError("is-enabled", enOut, enErr)
	}

	actOut, actErr := cfg.run(ctx, "--user", "is-active", cfg.timerUnit)
	switch {
	case actErr == nil:
		st.Active = strings.TrimSpace(string(actOut)) == "active"
	case ctx.Err() != nil:
		return st, ctx.Err()
	case isSystemdBusUnavailable(actOut, actErr):
		st.NoteCode = "systemd_bus_inactive"
		st.Note = "systemd user session bus is not active"
		return st, nil
	case isSystemdInactiveOrMissing(actOut, actErr):
		st.Active = false
	default:
		return st, wrapSystemctlError("is-active", actOut, actErr)
	}
	if st.Linger == "no" {
		st.NoteCode = "systemd_linger_disabled"
		st.Note = "systemd user linger is disabled; run `loginctl enable-linger $USER` so the timer and service survive SSH logout and start on boot"
	}
	return st, nil
}

func querySystemdLinger(ctx context.Context, run func(context.Context, ...string) ([]byte, error)) string {
	if run == nil {
		return ""
	}
	u, err := user.Current()
	if err != nil || u.Username == "" {
		return ""
	}
	qctx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	out, err := run(qctx, "show-user", u.Username, "-p", "Linger", "--value")
	if err != nil {
		return ""
	}
	v := strings.ToLower(strings.TrimSpace(string(out)))
	if v == "yes" || v == "no" {
		return v
	}
	return ""
}

type resolvedSystemd struct {
	home        string
	unitsDir    string
	base        string
	serviceUnit string
	timerUnit   string
	servicePath string
	timerPath   string
	run         func(ctx context.Context, args ...string) ([]byte, error)
	runLoginctl func(ctx context.Context, args ...string) ([]byte, error)
}

func normalizeSystemdOptions(opts SystemdUserOptions, createHome bool) (resolvedSystemd, error) {
	home := strings.TrimSpace(opts.Home)
	if home == "" {
		return resolvedSystemd{}, errors.New("server home required")
	}
	if !filepath.IsAbs(home) {
		return resolvedSystemd{}, fmt.Errorf("server home %q must be an absolute path", home)
	}
	userHome := opts.UserHome
	if userHome == "" {
		var err error
		userHome, err = os.UserHomeDir()
		if err != nil {
			return resolvedSystemd{}, err
		}
	}
	var realHome string
	if createHome {
		var err error
		realHome, err = resolveOrCreateHome(home)
		if err != nil {
			return resolvedSystemd{}, err
		}
	} else {
		realHome = canonicalPath(home)
	}
	unitsDir := opts.UnitsDir
	if unitsDir == "" {
		if xdg := os.Getenv("XDG_CONFIG_HOME"); xdg != "" && filepath.IsAbs(xdg) {
			unitsDir = filepath.Join(xdg, "systemd", "user")
		} else {
			unitsDir = filepath.Join(userHome, ".config", "systemd", "user")
		}
	}
	run := opts.RunSystemctl
	if run == nil {
		run = defaultRunSystemctl
	}
	runLogin := opts.RunLoginctl
	if runLogin == nil && opts.RunSystemctl == nil {
		runLogin = defaultRunLoginctl
	}
	base := SystemdUnitBase(realHome, userHome)
	svcUnit := base + ".service"
	timerUnit := base + ".timer"
	return resolvedSystemd{
		home:        realHome,
		unitsDir:    unitsDir,
		base:        base,
		serviceUnit: svcUnit,
		timerUnit:   timerUnit,
		servicePath: filepath.Join(unitsDir, svcUnit),
		timerPath:   filepath.Join(unitsDir, timerUnit),
		run:         run,
		runLoginctl: runLogin,
	}, nil
}

func cLocaleEnv() []string {
	var env []string
	for _, kv := range os.Environ() {
		if strings.HasPrefix(kv, "LC_ALL=") || strings.HasPrefix(kv, "LANG=") || strings.HasPrefix(kv, "LANGUAGE=") {
			continue
		}
		env = append(env, kv)
	}
	return append(env, "LC_ALL=C", "LANG=C")
}

func systemctlEnvFrom(base []string, uid int) []string {
	var env []string
	var xdgDir, dbusAddr string
	for _, kv := range base {
		switch {
		case strings.HasPrefix(kv, "LC_ALL="), strings.HasPrefix(kv, "LANG="), strings.HasPrefix(kv, "LANGUAGE="):
			continue
		case strings.HasPrefix(kv, "XDG_RUNTIME_DIR="):
			v := strings.TrimSpace(strings.TrimPrefix(kv, "XDG_RUNTIME_DIR="))
			if v == "" {
				continue // 空值视作缺失，避免挡住回填
			}
			xdgDir = v
		case strings.HasPrefix(kv, "DBUS_SESSION_BUS_ADDRESS="):
			v := strings.TrimSpace(strings.TrimPrefix(kv, "DBUS_SESSION_BUS_ADDRESS="))
			if v == "" {
				continue
			}
			dbusAddr = v
		}
		env = append(env, kv)
	}
	env = append(env, "LC_ALL=C", "LANG=C")

	// 仅补齐 cmd.Env（绝不调 os.Setenv 污染常驻进程与后续终端环境）；若已有有效自定义 XDG_RUNTIME_DIR 则以它为准。
	if xdgDir == "" {
		cand := fmt.Sprintf("/run/user/%d", uid)
		if info, err := os.Stat(cand); err == nil && info.IsDir() {
			xdgDir = cand
			env = append(env, "XDG_RUNTIME_DIR="+xdgDir)
		}
	}
	if dbusAddr == "" && xdgDir != "" {
		busPath := filepath.Join(xdgDir, "bus")
		if info, err := os.Stat(busPath); err == nil && info.Mode()&os.ModeSocket != 0 {
			env = append(env, "DBUS_SESSION_BUS_ADDRESS=unix:path="+busPath)
		}
	}
	return env
}

func defaultRunSystemctl(ctx context.Context, args ...string) ([]byte, error) {
	cmd := exec.CommandContext(ctx, "systemctl", args...)
	cmd.Env = systemctlEnvFrom(os.Environ(), os.Getuid())
	return cmd.CombinedOutput()
}

func defaultRunLoginctl(ctx context.Context, args ...string) ([]byte, error) {
	cmd := exec.CommandContext(ctx, "loginctl", args...)
	cmd.Env = cLocaleEnv()
	return cmd.CombinedOutput()
}

func systemdEscapeQuoted(s string) (string, error) {
	if strings.ContainsAny(s, "\x00\r\n") {
		return "", fmt.Errorf("invalid control character in systemd value %q", s)
	}
	s = strings.ReplaceAll(s, `\`, `\\`)
	s = strings.ReplaceAll(s, `"`, `\"`)
	s = strings.ReplaceAll(s, `%`, `%%`)
	return s, nil
}

func renderSystemdService(exePath, home string) ([]byte, error) {
	escExe, err := systemdEscapeQuoted(exePath)
	if err != nil {
		return nil, err
	}
	escHome, err := systemdEscapeQuoted(home)
	if err != nil {
		return nil, err
	}
	var b bytes.Buffer
	b.WriteString("[Unit]\n")
	b.WriteString("Description=Posse headless server\n")
	b.WriteString("After=network-online.target\n\n")
	b.WriteString("[Service]\n")
	b.WriteString("Type=oneshot\n")
	b.WriteString("KillMode=process\n")
	fmt.Fprintf(&b, "Environment=\"POSSE_HOME=%s\"\n", escHome)
	fmt.Fprintf(&b, "ExecStart=\"%s\" daemon start\n\n", escExe)
	b.WriteString("[Install]\n")
	b.WriteString("WantedBy=default.target\n")
	return b.Bytes(), nil
}

func renderSystemdTimer(serviceUnit string, interval int) []byte {
	var b bytes.Buffer
	b.WriteString("[Unit]\n")
	b.WriteString("Description=Posse headless server watchdog timer\n\n")
	b.WriteString("[Timer]\n")
	b.WriteString("OnBootSec=15s\n")
	fmt.Fprintf(&b, "OnUnitActiveSec=%ds\n", interval)
	fmt.Fprintf(&b, "Unit=%s\n\n", serviceUnit)
	b.WriteString("[Install]\n")
	b.WriteString("WantedBy=timers.target\n")
	return b.Bytes()
}

func wrapSystemctlError(op string, out []byte, err error) error {
	msg := strings.TrimSpace(string(out))
	if msg != "" {
		return fmt.Errorf("systemctl --user %s failed: %w: %s", op, err, msg)
	}
	return fmt.Errorf("systemctl --user %s failed: %w", op, err)
}

func isSystemdBusUnavailable(out []byte, err error) bool {
	if err == nil || errors.Is(err, exec.ErrNotFound) {
		return false
	}
	s := strings.ToLower(string(out))
	return strings.Contains(s, "failed to connect to bus") ||
		strings.Contains(s, "failed to connect to user scope bus") ||
		strings.Contains(s, "no medium found") ||
		strings.Contains(s, "system has not been booted with systemd")
}

func isSystemdUnitNotFound(out []byte, err error) bool {
	if err == nil || errors.Is(err, exec.ErrNotFound) {
		return false
	}
	s := strings.ToLower(string(out))
	return strings.Contains(s, "does not exist") || strings.Contains(s, "not found") || strings.Contains(s, "not loaded")
}

func isSystemdNotEnabledOrMissing(out []byte, err error) bool {
	if err == nil || errors.Is(err, exec.ErrNotFound) {
		return false
	}
	s := strings.ToLower(strings.TrimSpace(string(out)))
	if s == "disabled" || s == "static" || s == "masked" || s == "indirect" || s == "not-found" {
		return true
	}
	return isSystemdUnitNotFound(out, err)
}

func isSystemdInactiveOrMissing(out []byte, err error) bool {
	if err == nil || errors.Is(err, exec.ErrNotFound) {
		return false
	}
	s := strings.ToLower(strings.TrimSpace(string(out)))
	if s == "inactive" || s == "failed" || s == "deactivating" || s == "unknown" {
		return true
	}
	return isSystemdUnitNotFound(out, err)
}
