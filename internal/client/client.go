// Package client 是 CLI 和界面共用的常驻进程客户端：发起请求、按需拉起常驻进程、接入终端。
package client

import (
	"context"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"time"

	"posse/internal/daemon"
	"posse/internal/rpc"
	"posse/internal/spawn"
)

type Client struct {
	Home   string
	Socket string
}

func (c *Client) Call(ctx context.Context, method string, params, out any) error {
	return rpc.Call(ctx, c.Socket, method, params, out)
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
	if c.Up() {
		return nil
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
	proc, err := spawn.Start(cmd)
	if err != nil {
		return fmt.Errorf("start daemon: %w", err)
	}
	// 窗口一直开着，常驻进程交接或重启退出后要由这里收走，否则留下僵尸进程
	go proc.Wait()
	// 旧进程若还在退出，新进程要等它释放锁，所以多给些时间
	for i := 0; i < 300; i++ {
		if c.Up() {
			return nil
		}
		time.Sleep(50 * time.Millisecond)
	}
	return fmt.Errorf("daemon did not start; see %s", filepath.Join(c.Home, "daemon.log"))
}
