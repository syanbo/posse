// Package client 是 CLI 和界面共用的常驻进程客户端：发起请求、按需拉起常驻进程、接入终端。
package client

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync/atomic"
	"time"

	"posse/internal/daemon"
	"posse/internal/rpc"
	"posse/internal/spawn"
)

type Client struct {
	Home     string
	UserHome string
	Platform string
	Socket   string
	Remote   bool
	// Desktop 随 Client 固定；控制代次变化时创建新 Client，旧异步请求保留旧身份。
	Desktop *rpc.DesktopIdentity
}

func (c *Client) WithDesktop(id rpc.DesktopIdentity) *Client {
	next := *c
	next.Desktop = &id
	return &next
}

func (c *Client) Open(ctx context.Context, method string, params, out any) (net.Conn, *bufio.Reader, error) {
	if c.Remote && c.Desktop == nil {
		return nil, nil, rpc.Errorf(rpc.CodeReadOnly, "desktop identity required for the remote connection")
	}
	if c.Desktop != nil {
		if method != "terminal.attach" {
			return nil, nil, rpc.Errorf(rpc.CodeUnknown, "method %q is not available through the desktop connection", method)
		}
		data, err := json.Marshal(params)
		if err != nil {
			return nil, nil, err
		}
		var p map[string]json.RawMessage
		if err := json.Unmarshal(data, &p); err != nil {
			return nil, nil, err
		}
		if p == nil {
			p = make(map[string]json.RawMessage)
		}
		p["desktop"], _ = json.Marshal(c.Desktop)
		params = p
	}
	return rpc.Open(ctx, c.Socket, method, params, out)
}

// 窗口进程内共用序号，复制固定 Client 或重复绑定同一代身份也不会复用请求。
var desktopSequence atomic.Uint64

func (c *Client) Call(ctx context.Context, method string, params, out any) error {
	if c.Remote && c.Desktop == nil && method != "daemon.info" && !strings.HasPrefix(method, "desktop.") {
		return rpc.Errorf(rpc.CodeReadOnly, "desktop identity required for the remote connection")
	}
	if c.Desktop == nil || strings.HasPrefix(method, "desktop.") {
		return rpc.Call(ctx, c.Socket, method, params, out)
	}
	payload, err := json.Marshal(params)
	if err != nil {
		return err
	}
	p := rpc.DesktopCall{Desktop: *c.Desktop, Method: method, Params: payload}
	write, _ := rpc.DesktopMethod(method)
	if write {
		p.Sequence = desktopSequence.Add(1)
	}
	return callDesktop(ctx, c.Socket, p, write, out)
}

func (c *Client) Up() bool {
	conn, err := net.DialTimeout("unix", c.Socket, time.Second)
	if err != nil {
		return false
	}
	conn.Close()
	return true
}

// EnsureDaemon 在常驻进程没运行时拉起它（posse daemon run），并等它开始监听。
func (c *Client) EnsureDaemon() error {
	return c.EnsureDaemonContext(context.Background())
}

// EnsureDaemonContext 允许窗口取消等待；已启动的常驻进程独立运行，不随等待取消退出。
func (c *Client) EnsureDaemonContext(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if c.Up() {
		return nil
	}
	if c.Remote {
		return fmt.Errorf("remote SSH connection is unavailable; reconnect to the host")
	}
	exe, err := os.Executable()
	if err != nil {
		return err
	}
	if err := os.MkdirAll(c.Home, 0o700); err != nil {
		return err
	}
	logf, err := daemon.OpenLog(c.Home)
	if err != nil {
		return err
	}
	defer logf.Close()
	// 从包的副本运行，包被换掉时它和它拉起的终端都不受影响（见 internal/spawn 的 Runtime）；
	// 准备不了就从原包运行
	if rt, err := spawn.Runtime(exe, c.Home); err != nil {
		fmt.Fprintf(logf, "runtime copy of %s: %v\n", exe, err)
	} else {
		exe = rt
	}
	cmd := exec.Command(exe, "daemon", "run")
	cmd.Dir = c.Home
	cmd.Stdout, cmd.Stderr = logf, logf
	cmd.Env = append(os.Environ(), "POSSE_HOME="+c.Home)
	// 脱离调用方的会话，调用方的终端或界面关闭时常驻进程不受影响；
	// 授权按常驻进程自己的签名认，不记在拉起它的终端 App 上
	if err := ctx.Err(); err != nil {
		return err
	}
	proc, err := spawn.Start(cmd)
	if err != nil {
		return fmt.Errorf("start daemon: %w", err)
	}
	// 窗口一直开着，常驻进程交接或重启退出后要由这里收走，否则留下僵尸进程
	go proc.Wait()
	// 旧进程若还在退出，新进程要等它释放锁，所以多给些时间
	tick := time.NewTicker(50 * time.Millisecond)
	defer tick.Stop()
	for i := 0; i < 300; i++ {
		if err := ctx.Err(); err != nil {
			return err
		}
		if c.Up() {
			return nil
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-tick.C:
		}
	}
	return fmt.Errorf("daemon did not start; see %s", filepath.Join(c.Home, "daemon.log"))
}
