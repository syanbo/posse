package remote

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"posse/internal/rpc"
)

const (
	missingMarker   = "POSSE_REMOTE_MISSING:"
	installedMarker = "POSSE_REMOTE_INSTALLED:"
	activatedMarker = "POSSE_REMOTE_ACTIVATED:"
)

// RemoteNotInstalledError 表示目标主机上未找到 Posse 可执行文件，同时带回远端系统、架构与解析后的 home。
type RemoteNotInstalledError struct {
	OS   string `json:"os"`
	Arch string `json:"arch"`
	Home string `json:"home"`
}

func (e *RemoteNotInstalledError) Error() string {
	return "Posse executable not found on the remote host; run posse remote install or connect with auto-install"
}

// MissingCapabilityError 表示远端 daemon 缺少客户端要求的某项能力。
type MissingCapabilityError struct {
	Capability string
}

func (e *MissingCapabilityError) Error() string {
	return fmt.Sprintf("remote daemon does not support %s; update remote Posse", e.Capability)
}

// InstallResult 描述一次远程无头服务端推送安装的结果。
type InstallResult struct {
	OS         string `json:"os"`
	Arch       string `json:"arch"`
	Version    string `json:"version"`
	Tag        string `json:"tag"`
	Executable string `json:"executable"`
	Running    bool   `json:"running"`
	HandedOff  bool   `json:"handedOff"`
	Started    bool   `json:"started"`
}

func parseMissing(line []byte) (*RemoteNotInstalledError, bool) {
	if !bytes.HasPrefix(line, []byte(missingMarker)) {
		return nil, false
	}
	rest := bytes.TrimSpace(line[len(missingMarker):])
	if len(rest) == 0 {
		return nil, false
	}
	var m RemoteNotInstalledError
	if err := json.Unmarshal(rest, &m); err == nil && m.OS != "" && m.Arch != "" {
		return &m, true
	}
	return nil, false
}

func validateRemoteHomeForInstall(home string) error {
	if !filepath.IsAbs(home) {
		return errors.New("unsupported remote home or socket path")
	}
	sock := filepath.Join(home, "possed.sock")
	if strings.ContainsAny(sock, "\x00\r\n:[]%\\$") || len(sock) > 100 {
		return errors.New("unsupported remote home or socket path")
	}
	return nil
}

// stageAndCommitCommand 将压缩包解包到 versions/ 之外的 staging 临时目录（带 trap 清理），
// 再调用新解出的 posse server commit 持 versions/.lock 原子提交并核对 SHA-256，绝不提前切 current。
func stageAndCommitCommand(home string, pkg ServerPackage) string {
	homeExpr := `"$HOME/.posse"`
	if home != "" {
		homeExpr = remotePath(home)
	}
	libName := pkg.LibName
	if libName == "" {
		libName = "libghostty-vt.dylib"
		if pkg.OS == "linux" {
			libName = "libghostty-vt.so"
		}
	}
	script := "set -eu\n" +
		"export POSSE_HOME=" + homeExpr + "\n" +
		"unset POSSE_SOCKET\n" +
		"staging=\"$POSSE_HOME/server/staging\"\n" +
		"mkdir -p \"$staging\"\n" +
		"tmp=\"$staging/tmp-$$-\"" + shellQuote(pkg.Tag) + "\n" +
		"trap 'rm -rf \"$tmp\"' EXIT INT TERM\n" +
		"rm -rf \"$tmp\"\n" +
		"mkdir -p \"$tmp\"\n" +
		"chmod 700 \"$tmp\"\n" +
		"tar -xzf - -C \"$tmp\"\n" +
		"if test ! -f \"$tmp/posse\" || test -L \"$tmp/posse\"; then\n" +
		"  printf '%s\\n' 'extracted server archive is incomplete' >&2\n" +
		"  exit 1\n" +
		"fi\n" +
		"chmod 755 \"$tmp/posse\"\n" +
		"\"$tmp/posse\" server commit" +
		" --home \"$POSSE_HOME\"" +
		" --stage \"$tmp\"" +
		" --tag " + shellQuote(pkg.Tag) +
		" --lib " + shellQuote(libName) +
		" --posse-sha " + shellQuote(pkg.PosseSHA256) +
		" --lib-sha " + shellQuote(pkg.LibSHA256) + "\n"
	return "/bin/sh -c " + shellQuote(script)
}

func activateCommand(exePath, home, tag string) string {
	homeExpr := `"$HOME/.posse"`
	if home != "" {
		homeExpr = remotePath(home)
	}
	script := "set -eu\n" +
		"export POSSE_HOME=" + homeExpr + "\n" +
		"unset POSSE_SOCKET\n" +
		"exec " + remotePath(exePath) + " server activate --home \"$POSSE_HOME\" --tag " + shellQuote(tag)
	return "/bin/sh -c " + shellQuote(script)
}

func parseSingleMarker(data []byte, marker string) (string, error) {
	found := ""
	for _, line := range bytes.Split(data, []byte{'\n'}) {
		line = bytes.TrimSuffix(line, []byte{'\r'})
		if !bytes.HasPrefix(line, []byte(marker)) {
			continue
		}
		if found != "" {
			return "", fmt.Errorf("ambiguous remote marker %s", marker)
		}
		found = strings.TrimSpace(string(line[len(marker):]))
	}
	if found == "" {
		return "", fmt.Errorf("remote command did not report %s", marker)
	}
	return found, nil
}

type progressReader struct {
	r       io.Reader
	total   int64
	read    int64
	lastPct float64
	lastAt  time.Time
	report  func(Stage, float64)
}

func (p *progressReader) Read(b []byte) (int, error) {
	n, err := p.r.Read(b)
	if n > 0 && p.report != nil && p.total > 0 {
		p.read += int64(n)
		pct := float64(p.read) / float64(p.total)
		if pct > 1 {
			pct = 1
		}
		now := time.Now()
		if pct >= 1 || pct-p.lastPct >= 0.02 || now.Sub(p.lastAt) >= 100*time.Millisecond {
			p.lastPct = pct
			p.lastAt = now
			p.report(StageUpload, pct)
		}
	}
	return n, err
}

func (s *Session) stageAndCommitServer(ctx context.Context, home string, pkg ServerPackage, progress func(Stage, float64)) (string, error) {
	if home != "" {
		if err := validateRemoteHomeForInstall(home); err != nil {
			return "", err
		}
	}
	f, err := os.Open(pkg.Path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	var total int64
	if st, serr := f.Stat(); serr == nil {
		total = st.Size()
	}

	args := []string{
		"-F", "/dev/null", "-T", "-S", s.control,
		"-oControlMaster=no", "-oBatchMode=yes", "-oProxyCommand=false", "-oClearAllForwardings=yes",
		"--", s.Host.Target, stageAndCommitCommand(home, pkg),
	}
	cmd := exec.CommandContext(ctx, "/usr/bin/ssh", args...)
	if progress != nil && total > 0 {
		cmd.Stdin = &progressReader{r: f, total: total, report: progress}
	} else {
		cmd.Stdin = f
	}
	var stdout, stderr boundedOutput
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		if ctx.Err() != nil {
			return "", ctx.Err()
		}
		return "", fmt.Errorf("remote server install failed: %w: %s", err, strings.TrimSpace(stderr.String()))
	}
	return parseSingleMarker([]byte(stdout.String()), installedMarker)
}

func (s *Session) activateServer(ctx context.Context, exePath, home, tag string) error {
	out, err := s.command(ctx, activateCommand(exePath, home, tag))
	if err != nil {
		return fmt.Errorf("remote server activate failed: %w", err)
	}
	_, err = parseSingleMarker(out, activatedMarker)
	return err
}

func (s *Session) probeAt(ctx context.Context, exePath, home string, start bool) (rpc.RemoteProbe, error) {
	h := s.Host
	h.Executable = exePath
	if home != "" {
		h.Home = home
	}
	out, err := s.command(ctx, discoveryCommand(h, start))
	if err != nil {
		return rpc.RemoteProbe{}, fmt.Errorf("remote discovery failed: %w", err)
	}
	return parseProbe(out)
}

func (s *Session) probePlatformAndHome(ctx context.Context, configuredHome string) (osName, archName, resolvedHome string, err error) {
	homeExpr := `"$HOME/.posse"`
	if configuredHome != "" {
		homeExpr = remotePath(configuredHome)
	}
	script := `export POSSE_HOME=` + homeExpr + "\n" +
		`printf 'POSSE_REMOTE_MISSING:{"os":"%s","arch":"%s","home":"%s"}\n' "$(uname -s)" "$(uname -m)" "$POSSE_HOME"`
	out, err := s.command(ctx, "/bin/sh -c "+shellQuote(script))
	if err != nil {
		return "", "", "", fmt.Errorf("probe remote platform failed: %w", err)
	}
	_, perr := parseProbe(out)
	var m *RemoteNotInstalledError
	if errors.As(perr, &m) {
		return m.OS, m.Arch, m.Home, nil
	}
	return "", "", "", errors.New("failed to determine remote platform and architecture")
}

func (opts Options) resolveServer(ctx context.Context, targetOS, targetArch string) (ServerPackage, error) {
	return ResolveServerPackage(ctx, ServerResolveOptions{
		TargetOS:      targetOS,
		TargetArch:    targetArch,
		LocalHome:     opts.LocalHome,
		ExplicitFile:  opts.ServerArchive,
		SearchDirs:    opts.ServerSearch,
		BaseURL:       opts.ServerBaseURL,
		PublicKey:     opts.ServerPubKey,
		DisableRemote: opts.DisableRemoteDownload,
	})
}

// handoffAndVerify 向已转发的 socket 发起带实例与形态校验的热交接，确认新实例就绪后返回新 RemoteInfo。
func handoffAndVerify(ctx context.Context, socket, exePath, expectedInstance, expectedEdition string, required []string, expectedHome string) (rpc.RemoteInfo, error) {
	handoffCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
	err := rpc.Call(handoffCtx, socket, "daemon.handoff", map[string]string{
		"exe":                exePath,
		"expectedInstanceId": expectedInstance,
		"expectedEdition":    expectedEdition,
	}, nil)
	cancel()
	if err != nil {
		return rpc.RemoteInfo{}, fmt.Errorf("remote daemon handoff failed: %w", err)
	}
	var info rpc.RemoteInfo
	if err := rpc.Call(ctx, socket, "daemon.info", nil, &info); err != nil {
		return rpc.RemoteInfo{}, fmt.Errorf("verify daemon after handoff failed: %w", err)
	}
	if expectedInstance != "" && info.InstanceID == expectedInstance {
		return rpc.RemoteInfo{}, errors.New("remote daemon handoff did not switch to a new instance")
	}
	if info.Edition != "headless" {
		return rpc.RemoteInfo{}, fmt.Errorf("remote daemon edition after handoff is %q, want headless", info.Edition)
	}
	if expectedHome != "" && filepath.Clean(info.Home) != filepath.Clean(expectedHome) {
		return rpc.RemoteInfo{}, errors.New("remote daemon home changed unexpectedly after handoff")
	}
	for _, cap := range required {
		found := false
		for _, have := range info.Capabilities {
			if have == cap {
				found = true
				break
			}
		}
		if !found {
			return rpc.RemoteInfo{}, &MissingCapabilityError{Capability: cap}
		}
	}
	return info, nil
}

// Install 通过 SSH 将无头服务端推送安装到目标主机：
// 1. 锁定目标规范 home、平台和当前运行实例；
// 2. 持 versions/.lock 原子提交版本目录（不提前切换 current）；
// 3. 若远端常驻进程已在运行，核对实例后执行热交接并复检；否则用刚安装的绝对路径按 opts.Start 拉起；
// 4. 确认新版本就绪后再原子切换 server/current。
func Install(ctx context.Context, h Host, opts Options) (InstallResult, error) {
	if err := h.Validate(); err != nil {
		return InstallResult{}, err
	}
	s, err := openMaster(ctx, h, opts)
	if err != nil {
		return InstallResult{}, err
	}
	defer s.Close()

	var targetOS, targetArch, canonicalHome string
	probe, probeErr := s.discover(ctx, false)
	var missing *RemoteNotInstalledError
	switch {
	case probeErr == nil:
		targetOS, targetArch, canonicalHome = probe.Platform, probe.Arch, probe.Home
	case errors.As(probeErr, &missing):
		targetOS, targetArch, canonicalHome = missing.OS, missing.Arch, missing.Home
	default:
		targetOS, targetArch, canonicalHome, err = s.probePlatformAndHome(ctx, h.Home)
		if err != nil {
			return InstallResult{}, err
		}
	}
	if err := validateRemoteHomeForInstall(canonicalHome); err != nil {
		return InstallResult{}, err
	}

	opts.report(StagePackage, 0)
	pkg, err := opts.resolveServer(ctx, targetOS, targetArch)
	if err != nil {
		return InstallResult{}, err
	}
	defer pkg.Close()

	opts.report(StageUpload, 0)
	exePath, err := s.stageAndCommitServer(ctx, canonicalHome, pkg, opts.Progress)
	if err != nil {
		return InstallResult{}, err
	}
	res := InstallResult{
		OS:         pkg.OS,
		Arch:       pkg.Arch,
		Version:    pkg.Version,
		Tag:        pkg.Tag,
		Executable: exePath,
	}

	if probeErr == nil && probe.Running && probe.Socket != "" {
		if err := s.mux(ctx, "forward", "-L", s.Socket+":"+probe.Socket); err != nil {
			return res, fmt.Errorf("SSH socket forwarding failed: %w", err)
		}
		var before rpc.RemoteInfo
		if err := rpc.Call(ctx, s.Socket, "daemon.info", nil, &before); err != nil {
			return res, fmt.Errorf("remote daemon handshake failed before handoff: %w", err)
		}
		if filepath.Clean(before.Home) != filepath.Clean(canonicalHome) {
			return res, errors.New("remote daemon home does not match discovery")
		}
		if _, err := handoffAndVerify(ctx, s.Socket, exePath, before.InstanceID, "", opts.Required, canonicalHome); err != nil {
			return res, err
		}
		if err := s.activateServer(ctx, exePath, canonicalHome, pkg.Tag); err != nil {
			return res, err
		}
		res.Running = true
		res.HandedOff = true
		return res, nil
	}

	// 未运行：显式用刚提交的绝对路径探测/拉起，避免误命中旧 h.Executable
	p, err := s.probeAt(ctx, exePath, canonicalHome, opts.Start)
	if err != nil {
		return res, err
	}
	if opts.Start && !p.Running {
		return res, errors.New("installed headless server did not start")
	}
	if err := s.activateServer(ctx, exePath, canonicalHome, pkg.Tag); err != nil {
		return res, err
	}
	res.Running = p.Running
	res.Started = opts.Start && p.Running
	return res, nil
}
