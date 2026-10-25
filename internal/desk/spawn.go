package desk

import (
	"fmt"
	"os"
	"os/exec"

	"posse/internal/spawn"
)

// Proc 是桌面子进程。Frames 是它写来的帧，关掉 Control 它就退出。
type Proc struct {
	Frames  *os.File
	Control *os.File
	Pid     int

	proc *os.Process
	done chan struct{}
	err  error
}

// Spawn 用同一个可执行文件拉起 desk helper。授权归属走 internal/spawn，子进程按自己的签名认。
func Spawn(args []string) (*Proc, error) {
	exe, err := os.Executable()
	if err != nil {
		return nil, err
	}
	controlR, controlW, err := os.Pipe()
	if err != nil {
		return nil, err
	}
	frameR, frameW, err := os.Pipe()
	if err != nil {
		controlR.Close()
		controlW.Close()
		return nil, err
	}
	cmd := exec.Command(exe, append([]string{"desk", "helper"}, args...)...)
	cmd.Stdin = controlR
	cmd.Stdout = frameW
	cmd.Stderr = os.Stderr
	proc, err := spawn.Start(cmd)
	controlR.Close()
	frameW.Close()
	if err != nil {
		controlW.Close()
		frameR.Close()
		return nil, err
	}
	p := &Proc{Frames: frameR, Control: controlW, Pid: proc.Pid, proc: proc, done: make(chan struct{})}
	go func() {
		state, err := proc.Wait()
		if err == nil && state.ExitCode() != 0 {
			err = fmt.Errorf("desk: helper exited %d", state.ExitCode())
		}
		p.err = err
		close(p.done)
	}()
	return p, nil
}

// Wait 等子进程结束。
func (p *Proc) Wait() error {
	<-p.done
	return p.err
}
