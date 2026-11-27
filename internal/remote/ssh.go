package remote

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"posse/internal/rpc"
)

const probeMarker = "POSSE_REMOTE_V1:"

// Options 的 ctx 只限定建连；成功后的隧道由 Session.Close 关闭。
type Options struct {
	Prompt                PromptFunc
	Start                 bool
	AutoInstall           bool
	LocalHome             string
	ServerArchive         string
	ServerSearch          []string
	ServerBaseURL         string
	ServerPubKey          string
	DisableRemoteDownload bool
	Required              []string
	config                string
}

type Session struct {
	Host         Host
	Probe        rpc.RemoteProbe
	Info         rpc.RemoteInfo
	Socket       string
	dir, control string
	cancel       context.CancelFunc
	done         chan struct{}
	stderr       boundedOutput
	closeOnce    sync.Once
}

func (s *Session) Done() <-chan struct{} { return s.done }

// Close 只停止 Posse 自己的 ssh master；不发送 daemon.stop 或 terminal.close。
func (s *Session) Close() {
	s.closeOnce.Do(func() { s.cancel(); <-s.done; os.RemoveAll(s.dir) })
}

func openMaster(ctx context.Context, h Host, opts Options) (_ *Session, err error) {
	if err := h.Validate(); err != nil {
		return nil, err
	}
	// macOS 的 Unix socket 路径上限很短，默认 TMPDIR 往往已占大半。
	dir, err := os.MkdirTemp("/tmp", "posse-ssh-")
	if err != nil {
		return nil, err
	}
	defer func() {
		if err != nil {
			os.RemoveAll(dir)
		}
	}()
	exe, err := os.Executable()
	if err != nil {
		return nil, err
	}
	auth, err := newAskpass(ctx, dir, exe, opts.Prompt)
	if err != nil {
		return nil, err
	}
	defer auth.close()
	masterCtx, cancel := context.WithCancel(context.Background())
	s := &Session{Host: h, dir: dir, control: filepath.Join(dir, "c"), Socket: filepath.Join(dir, "s"), cancel: cancel, done: make(chan struct{})}
	args := baseArgs(h)
	if opts.config != "" {
		args = append(args, "-F", opts.config)
	}
	args = append(args, "-M", "-N", "-S", s.control, "-oControlMaster=yes", "-oControlPersist=no", "-oClearAllForwardings=yes", "-oForkAfterAuthentication=no", "-oRemoteCommand=none", "-oStreamLocalBindMask=0177")
	if opts.Prompt == nil {
		args = append(args, "-oBatchMode=yes")
	} else {
		args = append(args, "-oBatchMode=no")
	}
	args = append(args, "--", h.Target)
	cmd := exec.CommandContext(masterCtx, "/usr/bin/ssh", args...)
	// ProxyJump 的子进程也归本次连接所有，取消时一起回收。
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Cancel = func() error { return syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL) }
	cmd.WaitDelay = 2 * time.Second
	cmd.Env = authEnv(auth)
	cmd.Stderr = &s.stderr
	if err = cmd.Start(); err != nil {
		cancel()
		return nil, err
	}
	go func() { cmd.Wait(); close(s.done) }()
	defer func() {
		if err != nil {
			s.Close()
		}
	}()
	if err = s.waitReady(ctx); err != nil {
		return nil, err
	}
	return s, nil
}

func Connect(ctx context.Context, h Host, opts Options) (_ *Session, err error) {
	s, err := openMaster(ctx, h, opts)
	if err != nil {
		return nil, err
	}
	defer func() {
		if err != nil {
			s.Close()
		}
	}()
	s.Probe, err = s.discover(ctx, false)
	if err != nil {
		var missing *RemoteNotInstalledError
		if errors.As(err, &missing) && opts.AutoInstall && h.Executable == "" {
			if err = validateRemoteHomeForInstall(missing.Home); err != nil {
				return nil, err
			}
			var pkg ServerPackage
			pkg, err = opts.resolveServer(ctx, missing.OS, missing.Arch)
			if err != nil {
				return nil, err
			}
			defer pkg.Close()
			var exePath string
			exePath, err = s.stageAndCommitServer(ctx, missing.Home, pkg)
			if err != nil {
				return nil, err
			}
			s.Probe, err = s.probeAt(ctx, exePath, missing.Home, opts.Start)
			if err != nil {
				return nil, err
			}
			if !opts.Start {
				// AutoInstall 但未允许 Start 时只提交并验证二进制，不拉起 daemon
				if err = s.activateServer(ctx, exePath, missing.Home, pkg.Tag); err != nil {
					return nil, err
				}
				return nil, errors.New("remote daemon is not running; start Posse on the remote Mac or connect with --start")
			}
			if !s.Probe.Running {
				return nil, errors.New("installed headless server did not start")
			}
			if err = s.mux(ctx, "forward", "-L", s.Socket+":"+s.Probe.Socket); err != nil {
				return nil, fmt.Errorf("SSH socket forwarding failed: %w", err)
			}
			if err = rpc.Call(ctx, s.Socket, "daemon.info", nil, &s.Info); err != nil {
				return nil, fmt.Errorf("remote daemon handshake failed; check socket forwarding and update remote Posse if needed: %w", err)
			}
			if err = validateInfo(s.Probe, s.Info, opts.Required); err != nil {
				return nil, err
			}
			// 确认新 daemon 握手通过后再原子提交 server/current
			if err = s.activateServer(ctx, exePath, missing.Home, pkg.Tag); err != nil {
				return nil, err
			}
			return s, nil
		}
		return nil, err
	}
	// 先验证平台及协议，再决定是否启动，避免在不支持的目标上产生副作用。
	if !s.Probe.Running && opts.Start {
		s.Probe, err = s.discover(ctx, true)
		if err != nil {
			return nil, err
		}
	}
	if !s.Probe.Running {
		return nil, errors.New("remote daemon is not running; start Posse on the remote Mac or connect with --start")
	}
	if err = s.mux(ctx, "forward", "-L", s.Socket+":"+s.Probe.Socket); err != nil {
		return nil, fmt.Errorf("SSH socket forwarding failed: %w", err)
	}
	if err = rpc.Call(ctx, s.Socket, "daemon.info", nil, &s.Info); err != nil {
		return nil, fmt.Errorf("remote daemon handshake failed; check socket forwarding and update remote Posse if needed: %w", err)
	}
	if err = validateInfo(s.Probe, s.Info, opts.Required); err != nil {
		// 仅对托管的 headless 节点在缺少必需能力时尝试热升级交接，并保留真实升级错误不静默吞掉
		var capErr *MissingCapabilityError
		if errors.As(err, &capErr) && opts.AutoInstall && h.Executable == "" && s.Info.Edition == "headless" {
			pkg, rerr := opts.resolveServer(ctx, s.Info.Platform, s.Info.Arch)
			if rerr != nil {
				return nil, fmt.Errorf("%w (auto-upgrade resolve failed: %v)", err, rerr)
			}
			defer pkg.Close()
			exePath, ierr := s.stageAndCommitServer(ctx, s.Info.Home, pkg)
			if ierr != nil {
				return nil, fmt.Errorf("%w (auto-upgrade install failed: %v)", err, ierr)
			}
			newInfo, herr := handoffAndVerify(ctx, s.Socket, exePath, s.Info.InstanceID, "headless", opts.Required, s.Info.Home)
			if herr != nil {
				return nil, fmt.Errorf("%w (auto-upgrade failed: %v)", err, herr)
			}
			if aerr := s.activateServer(ctx, exePath, s.Info.Home, pkg.Tag); aerr != nil {
				return nil, fmt.Errorf("remote server activate failed after handoff: %w", aerr)
			}
			s.Info = newInfo
			return s, nil
		}
		return nil, err
	}
	return s, nil
}

func (s *Session) discover(ctx context.Context, start bool) (rpc.RemoteProbe, error) {
	output, err := s.command(ctx, discoveryCommand(s.Host, start))
	if err != nil {
		return rpc.RemoteProbe{}, fmt.Errorf("remote discovery failed: %w", err)
	}
	return parseProbe(output)
}

func baseArgs(h Host) []string {
	args := []string{"-T", "-oForwardAgent=no", "-oForwardX11=no", "-oPermitLocalCommand=no", "-oStrictHostKeyChecking=ask", "-oUpdateHostKeys=no", "-oConnectTimeout=15", "-oConnectionAttempts=1", "-oServerAliveInterval=15", "-oServerAliveCountMax=3"}
	if h.User != "" {
		args = append(args, "-l", h.User)
	}
	if h.Port != 0 {
		args = append(args, "-p", strconv.Itoa(h.Port))
	}
	return args
}

func authEnv(a *askpass) []string {
	var env []string
	for _, e := range os.Environ() {
		key, _, _ := strings.Cut(e, "=")
		if strings.HasPrefix(key, "POSSE_ASKPASS_") || strings.HasPrefix(key, "SSH_ASKPASS") || key == "DISPLAY" {
			continue
		}
		env = append(env, e)
	}
	return append(env, "SSH_ASKPASS="+a.script, "SSH_ASKPASS_REQUIRE=force", "DISPLAY=posse:0", "POSSE_ASKPASS_SOCKET="+a.socket, "POSSE_ASKPASS_TOKEN="+a.token)
}

func (s *Session) waitReady(ctx context.Context) error {
	tick := time.NewTicker(50 * time.Millisecond)
	defer tick.Stop()
	for {
		if _, err := os.Stat(s.control); err == nil {
			checkCtx, cancel := context.WithTimeout(ctx, time.Second)
			err = s.mux(checkCtx, "check")
			cancel()
			if err == nil {
				return nil
			}
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-s.done:
			return fmt.Errorf("SSH connection failed: %s", s.stderr.String())
		case <-tick.C:
		}
	}
}

func (s *Session) mux(ctx context.Context, op string, extra ...string) error {
	// 只对自有 master 发控制命令；不加载 config 中额外的转发或本地命令。
	args := []string{"-F", "/dev/null", "-S", s.control, "-oControlMaster=no", "-oProxyCommand=false", "-O", op}
	args = append(args, extra...)
	args = append(args, "--", s.Host.Target)
	_, err := runSSH(ctx, args)
	return err
}

func (s *Session) command(ctx context.Context, command string) ([]byte, error) {
	args := []string{"-F", "/dev/null", "-T", "-S", s.control, "-oControlMaster=no", "-oBatchMode=yes", "-oProxyCommand=false", "-oClearAllForwardings=yes", "--", s.Host.Target, command}
	return runSSH(ctx, args)
}

func runSSH(ctx context.Context, args []string) ([]byte, error) {
	cmd := exec.CommandContext(ctx, "/usr/bin/ssh", args...)
	var stdout, stderr boundedOutput
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		return nil, fmt.Errorf("%w: %s", err, stderr.String())
	}
	if stdout.truncated {
		return nil, errors.New("remote discovery output exceeds limit")
	}
	return []byte(stdout.String()), nil
}

func remotePath(p string) string {
	if strings.HasPrefix(p, "~/") {
		return `"$HOME"/` + shellQuote(strings.TrimPrefix(p, "~/"))
	}
	return shellQuote(p)
}

func discoveryCommand(h Host, start bool) string {
	home := `"$HOME/.posse"`
	if h.Home != "" {
		home = remotePath(h.Home)
	}
	script := "export POSSE_HOME=" + home + "\nunset POSSE_SOCKET\n"
	if h.Executable != "" {
		script += "exe=" + remotePath(h.Executable) + "\n"
	} else {
		script += `exe=""
for candidate in "$POSSE_HOME/server/current/posse" "$POSSE_HOME/bin/posse" /Applications/Posse.app/Contents/MacOS/Posse "$HOME/Applications/Posse.app/Contents/MacOS/Posse" /opt/homebrew/bin/posse /usr/local/bin/posse "$HOME/.local/bin/posse"; do
  if test -x "$candidate"; then exe="$candidate"; break; fi
done
if test -z "$exe"; then
  h_esc=$(printf '%s' "$POSSE_HOME" | sed 's/\\/\\\\/g; s/"/\\"/g')
  printf 'POSSE_REMOTE_MISSING:{"os":"%s","arch":"%s","home":"%s"}\n' "$(uname -s)" "$(uname -m)" "$h_esc"
  exit 0
fi
`
	}
	script += `exec "$exe" remote probe`
	if start {
		script += " --start"
	}
	return "/bin/sh -c " + shellQuote(script)
}

func parseProbe(data []byte) (rpc.RemoteProbe, error) {
	var p rpc.RemoteProbe
	var missing *RemoteNotInstalledError
	foundProbe, foundMissing := false, false
	for _, line := range bytes.Split(data, []byte{'\n'}) {
		line = bytes.TrimSuffix(line, []byte{'\r'})
		if m, ok := parseMissing(line); ok {
			if foundMissing {
				return p, errors.New("ambiguous remote discovery response")
			}
			foundMissing = true
			missing = m
			continue
		}
		if !bytes.HasPrefix(line, []byte(probeMarker)) {
			continue
		}
		if foundProbe {
			return p, errors.New("ambiguous remote discovery response")
		}
		foundProbe = true
		if err := json.Unmarshal(line[len(probeMarker):], &p); err != nil {
			return p, fmt.Errorf("invalid remote discovery response: %w", err)
		}
	}
	if foundProbe && foundMissing {
		return p, errors.New("ambiguous remote discovery response")
	}
	if foundMissing {
		return p, missing
	}
	if !foundProbe {
		return p, errors.New("remote Posse discovery unavailable; update remote Posse")
	}
	if p.Protocol != rpc.RemoteProtocol || p.Platform != "darwin" {
		return p, errors.New("incompatible remote Posse; a supported Mac version is required")
	}
	if !filepath.IsAbs(p.Home) || !filepath.IsAbs(p.Socket) || strings.ContainsAny(p.Socket, "\x00\r\n:[]%\\$") || len(p.Socket) > 100 {
		return p, errors.New("unsupported remote home or socket path")
	}
	return p, nil
}

func validateInfo(p rpc.RemoteProbe, info rpc.RemoteInfo, required []string) error {
	if info.Protocol != rpc.RemoteProtocol || info.Platform != "darwin" || info.InstanceID == "" {
		return errors.New("incompatible remote daemon; update and restart remote Posse")
	}
	if filepath.Clean(info.Home) != filepath.Clean(p.Home) {
		return errors.New("remote daemon home does not match discovery")
	}
	for _, cap := range required {
		if !slices.Contains(info.Capabilities, cap) {
			return &MissingCapabilityError{Capability: cap}
		}
	}
	return nil
}

type boundedOutput struct {
	mu        sync.Mutex
	buf       []byte
	truncated bool
}

func (b *boundedOutput) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	n := len(p)
	const limit = 64 << 10
	if len(b.buf)+n > limit {
		b.truncated = true
	}
	if n >= limit {
		b.buf = append(b.buf[:0], p[n-limit:]...)
	} else {
		if extra := len(b.buf) + n - limit; extra > 0 {
			b.buf = slices.Delete(b.buf, 0, extra)
		}
		b.buf = append(b.buf, p...)
	}
	return n, nil
}
func (b *boundedOutput) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return strings.TrimSpace(string(b.buf))
}
