//go:build !darwin

package spawn

import (
	"os"
	"os/exec"
	"syscall"
)

// Start 新开会话启动 cmd。授权归属只有 macOS 有，这里不处理。
func Start(cmd *exec.Cmd) (*os.Process, error) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	if err := cmd.Start(); err != nil {
		return nil, err
	}
	return cmd.Process, nil
}
