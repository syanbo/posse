package spawn

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/xml"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
)

const (
	defaultLaunchAgentLabel    = "com.dengbo.posse.server"
	defaultLaunchAgentInterval = 60
	minLaunchAgentInterval     = 10
	maxLaunchAgentInterval     = 86400
)

// LaunchAgentOptions 配置 macOS LaunchAgent 安装、卸载或状态查询参数。
type LaunchAgentOptions struct {
	Home         string
	UserHome     string
	AgentsDir    string
	Executable   string
	Interval     int
	Domain       string
	RunLaunchctl func(ctx context.Context, args ...string) ([]byte, error)
}

// LaunchAgentStatus 描述 macOS LaunchAgent 的安装与加载状态。
type LaunchAgentStatus struct {
	Label      string `json:"label"`
	Plist      string `json:"plist"`
	Domain     string `json:"domain"`
	Home       string `json:"home"`
	Executable string `json:"executable"`
	Interval   int    `json:"interval,omitempty"`
	Installed  bool   `json:"installed"`
	Loaded     bool   `json:"loaded"`
	Note       string `json:"note,omitempty"`
}

// LaunchAgentLabel 为默认 ~/.posse 返回固定标签，自定义 home 追加路径摘要隔离。
func LaunchAgentLabel(home, userHome string) string {
	canonHome := canonicalPath(home)
	defaultHome := canonicalPath(filepath.Join(userHome, ".posse"))
	if canonHome == defaultHome {
		return defaultLaunchAgentLabel
	}
	sum := sha256.Sum256([]byte(canonHome))
	return defaultLaunchAgentLabel + "." + hex.EncodeToString(sum[:4])
}

// InstallLaunchAgent 安装并尝试加载 macOS Aqua LaunchAgent（RunAtLoad + StartInterval，不设 KeepAlive）。
// 仅当明确确认 gui/<uid> 域不存在（无活跃 Aqua GUI 登录会话）时保留 plist 并在状态中说明；
// 权限错误、超时或工具不可用会回滚旧 plist 并返回错误。
func InstallLaunchAgent(ctx context.Context, opts LaunchAgentOptions) (LaunchAgentStatus, error) {
	cfg, err := normalizeLaunchAgentOptions(opts, true)
	if err != nil {
		return LaunchAgentStatus{}, err
	}
	interval := opts.Interval
	if interval == 0 {
		interval = defaultLaunchAgentInterval
	}
	if interval < minLaunchAgentInterval || interval > maxLaunchAgentInterval {
		return LaunchAgentStatus{}, fmt.Errorf("launchd start interval must be between %d and %d seconds", minLaunchAgentInterval, maxLaunchAgentInterval)
	}

	exePath, err := resolveLaunchAgentExecutable(cfg.home, opts.Executable)
	if err != nil {
		return LaunchAgentStatus{}, err
	}
	plistData, err := renderLaunchAgentPlist(cfg.label, exePath, cfg.home, interval)
	if err != nil {
		return LaunchAgentStatus{}, err
	}
	if err := os.MkdirAll(cfg.agentsDir, 0o755); err != nil {
		return LaunchAgentStatus{}, err
	}

	prevData, prevErr := os.ReadFile(cfg.plistPath)
	hadPrev := prevErr == nil
	if prevErr != nil && !errors.Is(prevErr, os.ErrNotExist) {
		return LaunchAgentStatus{}, prevErr
	}
	if err := atomicWriteFile(cfg.plistPath, plistData, 0o644); err != nil {
		return LaunchAgentStatus{}, err
	}

	st := LaunchAgentStatus{
		Label:      cfg.label,
		Plist:      cfg.plistPath,
		Domain:     cfg.domain,
		Home:       cfg.home,
		Executable: exePath,
		Interval:   interval,
		Installed:  true,
	}

	// 先卸载旧登记（未加载时忽略失败），再加载最新 plist。
	_, _ = cfg.run(ctx, "bootout", cfg.domain+"/"+cfg.label)
	out, err := cfg.run(ctx, "bootstrap", cfg.domain, cfg.plistPath)
	if err == nil {
		st.Loaded = true
		return st, nil
	}
	// 仅在上下文未取消且明确探测到 GUI Aqua 域不存在时，保留 plist 待下次 GUI 登录生效。
	if ctx.Err() == nil && !errors.Is(err, exec.ErrNotFound) {
		if dout, derr := cfg.run(ctx, "print-disabled", cfg.domain); isLaunchctlDomainNotFound(dout, derr) {
			st.Loaded = false
			st.Note = "GUI login session (Aqua) is not active; LaunchAgent is installed and will load after macOS GUI login"
			return st, nil
		}
	}

	// 真实加载失败：恢复旧 plist（如存在）或清理本次写入，避免留下半更新状态。
	if hadPrev {
		if rerr := atomicWriteFile(cfg.plistPath, prevData, 0o644); rerr == nil {
			_, _ = cfg.run(context.Background(), "bootstrap", cfg.domain, cfg.plistPath)
		}
	} else {
		_ = os.Remove(cfg.plistPath)
		st.Installed = false
	}
	msg := strings.TrimSpace(string(out))
	if msg != "" {
		return st, fmt.Errorf("launchctl bootstrap failed: %w: %s", err, msg)
	}
	return st, fmt.Errorf("launchctl bootstrap failed: %w", err)
}

// UninstallLaunchAgent 确认作业已从 launchd 卸载或不存在后，再删除对应的 plist 文件（不停止正在运行的 daemon）。
func UninstallLaunchAgent(ctx context.Context, opts LaunchAgentOptions) (LaunchAgentStatus, error) {
	cfg, err := normalizeLaunchAgentOptions(opts, false)
	if err != nil {
		return LaunchAgentStatus{}, err
	}
	out, err := cfg.run(ctx, "bootout", cfg.domain+"/"+cfg.label)
	if err != nil {
		if ctx.Err() != nil {
			return LaunchAgentStatus{}, ctx.Err()
		}
		if !isLaunchctlServiceNotFound(out, err) && !isLaunchctlDomainNotFound(out, err) {
			// bootout 返回未知错误时，用 print 核实作业是否已不存在；仍在运行或查询也出错则拒绝删除 plist。
			pout, perr := cfg.run(ctx, "print", cfg.domain+"/"+cfg.label)
			if perr == nil {
				return LaunchAgentStatus{}, fmt.Errorf("launchctl bootout failed and service is still loaded: %w: %s", err, strings.TrimSpace(string(out)))
			}
			if !isLaunchctlServiceNotFound(pout, perr) && !isLaunchctlDomainNotFound(pout, perr) {
				return LaunchAgentStatus{}, fmt.Errorf("launchctl bootout failed: %w: %s", err, strings.TrimSpace(string(out)))
			}
		}
	}
	if err := os.Remove(cfg.plistPath); err != nil && !errors.Is(err, os.ErrNotExist) {
		return LaunchAgentStatus{}, err
	}
	return LaunchAgentStatus{
		Label:      cfg.label,
		Plist:      cfg.plistPath,
		Domain:     cfg.domain,
		Home:       cfg.home,
		Executable: launchAgentExecutableHint(cfg.home),
		Installed:  false,
		Loaded:     false,
	}, nil
}

// QueryLaunchAgent 查询对应 LaunchAgent 的 plist 存在状态与 launchd 加载状态。
func QueryLaunchAgent(ctx context.Context, opts LaunchAgentOptions) (LaunchAgentStatus, error) {
	cfg, err := normalizeLaunchAgentOptions(opts, false)
	if err != nil {
		return LaunchAgentStatus{}, err
	}
	st := LaunchAgentStatus{
		Label:      cfg.label,
		Plist:      cfg.plistPath,
		Domain:     cfg.domain,
		Home:       cfg.home,
		Executable: launchAgentExecutableHint(cfg.home),
	}
	info, err := os.Stat(cfg.plistPath)
	switch {
	case err == nil:
		st.Installed = info.Mode().IsRegular()
	case errors.Is(err, os.ErrNotExist):
		st.Installed = false
	default:
		return LaunchAgentStatus{}, err
	}

	out, err := cfg.run(ctx, "print", cfg.domain+"/"+cfg.label)
	switch {
	case err == nil:
		st.Loaded = true
	case ctx.Err() != nil:
		return st, ctx.Err()
	case isLaunchctlServiceNotFound(out, err) || isLaunchctlDomainNotFound(out, err):
		st.Loaded = false
	default:
		msg := strings.TrimSpace(string(out))
		if msg != "" {
			return st, fmt.Errorf("launchctl print failed: %w: %s", err, msg)
		}
		return st, fmt.Errorf("launchctl print failed: %w", err)
	}
	return st, nil
}

func isLaunchctlDomainNotFound(out []byte, err error) bool {
	if err == nil || errors.Is(err, exec.ErrNotFound) {
		return false
	}
	s := strings.ToLower(string(out))
	if strings.Contains(s, "could not find domain") || strings.Contains(s, "no such process") {
		return true
	}
	var ee *exec.ExitError
	if errors.As(err, &ee) && ee.ExitCode() == 113 && !strings.Contains(s, "permission") && !strings.Contains(s, "not permitted") {
		return true
	}
	return false
}

func isLaunchctlServiceNotFound(out []byte, err error) bool {
	if err == nil || errors.Is(err, exec.ErrNotFound) {
		return false
	}
	s := strings.ToLower(string(out))
	if strings.Contains(s, "could not find service") || strings.Contains(s, "service could not be found") || strings.Contains(s, "no such process") {
		return true
	}
	var ee *exec.ExitError
	if errors.As(err, &ee) && (ee.ExitCode() == 3 || ee.ExitCode() == 113) && !strings.Contains(s, "permission") && !strings.Contains(s, "not permitted") {
		return true
	}
	return false
}

type resolvedLaunchAgent struct {
	home      string
	agentsDir string
	label     string
	plistPath string
	domain    string
	run       func(ctx context.Context, args ...string) ([]byte, error)
}

func normalizeLaunchAgentOptions(opts LaunchAgentOptions, createHome bool) (resolvedLaunchAgent, error) {
	home := strings.TrimSpace(opts.Home)
	if home == "" {
		return resolvedLaunchAgent{}, errors.New("server home required")
	}
	if !filepath.IsAbs(home) {
		return resolvedLaunchAgent{}, fmt.Errorf("server home %q must be an absolute path", home)
	}
	userHome := opts.UserHome
	if userHome == "" {
		var err error
		userHome, err = os.UserHomeDir()
		if err != nil {
			return resolvedLaunchAgent{}, err
		}
	}
	var realHome string
	if createHome {
		var err error
		realHome, err = resolveOrCreateHome(home)
		if err != nil {
			return resolvedLaunchAgent{}, err
		}
	} else {
		realHome = canonicalPath(home)
	}
	agentsDir := opts.AgentsDir
	if agentsDir == "" {
		agentsDir = filepath.Join(userHome, "Library", "LaunchAgents")
	}
	domain := opts.Domain
	if domain == "" {
		domain = "gui/" + strconv.Itoa(os.Getuid())
	}
	run := opts.RunLaunchctl
	if run == nil {
		run = defaultRunLaunchctl
	}
	label := LaunchAgentLabel(realHome, userHome)
	return resolvedLaunchAgent{
		home:      realHome,
		agentsDir: agentsDir,
		label:     label,
		plistPath: filepath.Join(agentsDir, label+".plist"),
		domain:    domain,
		run:       run,
	}, nil
}

func defaultRunLaunchctl(ctx context.Context, args ...string) ([]byte, error) {
	cmd := exec.CommandContext(ctx, "/bin/launchctl", args...)
	return cmd.CombinedOutput()
}

func canonicalPath(p string) string {
	p = filepath.Clean(p)
	if resolved, err := filepath.EvalSymlinks(p); err == nil {
		return resolved
	}
	return p
}

func isExecutableRegularFile(path string) bool {
	info, err := os.Stat(path)
	return err == nil && info.Mode().IsRegular() && info.Mode()&0o111 != 0
}

func launchAgentExecutableHint(home string) string {
	currentServer := filepath.Join(home, "server", "current", "posse")
	if isExecutableRegularFile(currentServer) {
		return currentServer
	}
	return filepath.Join(home, "bin", "posse")
}

// resolveLaunchAgentExecutable 为 LaunchAgent 选择稳定且总指向最新激活版本的可执行路径：
// 托管无头模式固定使用 $POSSE_HOME/server/current/posse（随 server activate 原子切换，即使未启动 daemon 也不落回旧 bin 链接）；
// 非无头模式则使用 $POSSE_HOME/bin/posse 并校验其目标为具备执行权限的普通文件。
func resolveLaunchAgentExecutable(home, fallbackExe string) (string, error) {
	currentServer := filepath.Join(home, "server", "current", "posse")
	if isExecutableRegularFile(currentServer) {
		return currentServer, nil
	}

	link := filepath.Join(home, "bin", "posse")
	if isExecutableRegularFile(link) {
		return link, nil
	}
	if linfo, err := os.Lstat(link); err == nil && linfo.IsDir() && linfo.Mode()&os.ModeSymlink == 0 {
		return "", fmt.Errorf("posse link path %s is a directory", link)
	}

	exe := fallbackExe
	if exe == "" {
		var err error
		exe, err = os.Executable()
		if err != nil {
			return "", err
		}
	}
	if resolved, err := filepath.EvalSymlinks(exe); err == nil {
		exe = resolved
	}
	if !filepath.IsAbs(exe) {
		return "", fmt.Errorf("invalid posse executable path %q", exe)
	}
	if !isExecutableRegularFile(exe) {
		return "", fmt.Errorf("posse executable not found or not executable at %s", exe)
	}

	if err := os.MkdirAll(filepath.Dir(link), 0o700); err != nil {
		return "", err
	}
	tmp := fmt.Sprintf("%s.%d.tmp", link, os.Getpid())
	_ = os.Remove(tmp)
	if err := os.Symlink(exe, tmp); err != nil {
		return "", err
	}
	if err := os.Rename(tmp, link); err != nil {
		_ = os.Remove(tmp)
		return "", err
	}
	return link, nil
}

func renderLaunchAgentPlist(label, exePath, home string, interval int) ([]byte, error) {
	logPath := filepath.Join(home, "launchd.log")
	var b bytes.Buffer
	writeEsc := func(s string) error {
		return xml.EscapeText(&b, []byte(s))
	}
	b.WriteString(`<?xml version="1.0" encoding="UTF-8"?>` + "\n")
	b.WriteString(`<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">` + "\n")
	b.WriteString(`<plist version="1.0">` + "\n<dict>\n")
	b.WriteString("\t<key>Label</key>\n\t<string>")
	if err := writeEsc(label); err != nil {
		return nil, err
	}
	b.WriteString("</string>\n")

	b.WriteString("\t<key>ProgramArguments</key>\n\t<array>\n\t\t<string>")
	if err := writeEsc(exePath); err != nil {
		return nil, err
	}
	b.WriteString("</string>\n\t\t<string>daemon</string>\n\t\t<string>start</string>\n\t</array>\n")

	b.WriteString("\t<key>EnvironmentVariables</key>\n\t<dict>\n\t\t<key>POSSE_HOME</key>\n\t\t<string>")
	if err := writeEsc(home); err != nil {
		return nil, err
	}
	b.WriteString("</string>\n\t</dict>\n")

	b.WriteString("\t<key>RunAtLoad</key>\n\t<true/>\n")
	fmt.Fprintf(&b, "\t<key>StartInterval</key>\n\t<integer>%d</integer>\n", interval)
	b.WriteString("\t<key>LimitLoadToSessionType</key>\n\t<array>\n\t\t<string>Aqua</string>\n\t</array>\n")
	b.WriteString("\t<key>ProcessType</key>\n\t<string>Interactive</string>\n")

	b.WriteString("\t<key>StandardOutPath</key>\n\t<string>")
	if err := writeEsc(logPath); err != nil {
		return nil, err
	}
	b.WriteString("</string>\n\t<key>StandardErrorPath</key>\n\t<string>")
	if err := writeEsc(logPath); err != nil {
		return nil, err
	}
	b.WriteString("</string>\n")
	b.WriteString("</dict>\n</plist>\n")
	return b.Bytes(), nil
}

func atomicWriteFile(dest string, data []byte, mode os.FileMode) error {
	tmp, err := os.CreateTemp(filepath.Dir(dest), "."+filepath.Base(dest)+".*.tmp")
	if err != nil {
		return err
	}
	tmpPath := tmp.Name()
	defer os.Remove(tmpPath)
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Chmod(mode); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmpPath, dest)
}
