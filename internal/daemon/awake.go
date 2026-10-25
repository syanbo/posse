package daemon

import (
	"context"
	"encoding/json"
	"errors"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"sync"
	"time"

	"posse/internal/agent"
	"posse/internal/rpc"
)

// 防休眠照 Orca 的 agent-awake（Magpie 也是这样）：macOS 上起 /usr/bin/caffeinate -i -s 托住系统不睡，
// 屏幕照常按系统设置熄灭。放在常驻进程里，窗口关掉也有效，常驻进程退出才放开。
// 三档：off 不管；auto 只要常驻进程在跑就托住；display 和 auto 一样，另加 -d 让屏幕也不熄
// （照 Magpie 的 Screen on too：屏幕熄了会跟着锁屏）。原来的 on（一直托住）和现在的 auto 一样，读到时按 auto。
// 合盖仍会睡，-s 只在接电源时生效。

const (
	awakeOff     = "off"
	awakeAuto    = "auto"
	awakeDisplay = "display"

	// 定时重算：意外退出的 caffeinate 在这时补上
	awakeTick = 30 * time.Second
)

type awakeStatus struct {
	Mode    string `json:"mode"`
	Active  bool   `json:"active"`  // 此刻正托着不让睡
	Working int    `json:"working"` // 正在工作的 agent 数，不决定托不托住
}

type awake struct {
	mu      sync.Mutex
	path    string // $POSSE_HOME/awake.json，记用户选的档
	mode    string
	working int
	viewing bool // 有手机在看桌面：屏幕不能熄，不写 awake.json
	cmd     *exec.Cmd
	display bool          // 正在运行的 cmd 带不带 -d
	done    chan struct{} // cmd 退出时关闭
	spawn   func(display bool) (*exec.Cmd, error)
}

var errAwakeUnsupported = errors.New("keep awake is only supported on macOS")

func newAwake(home string) *awake {
	a := &awake{path: filepath.Join(home, "awake.json"), mode: awakeAuto, spawn: spawnCaffeinate}
	var saved struct {
		Mode string `json:"mode"`
	}
	if b, err := os.ReadFile(a.path); err == nil && json.Unmarshal(b, &saved) == nil {
		if m := normalAwakeMode(saved.Mode); validAwakeMode(m) {
			a.mode = m
		}
	}
	return a
}

func validAwakeMode(m string) bool {
	return m == awakeOff || m == awakeAuto || m == awakeDisplay
}

// normalAwakeMode 把旧的 on 换成 auto：旧的 awake.json 和旧窗口进程还会给 on。
func normalAwakeMode(m string) string {
	if m == "on" {
		return awakeAuto
	}
	return m
}

// spawnCaffeinate 起 caffeinate。-w 带上常驻进程的 pid：常驻进程被强杀时它跟着退出，
// 不会留下一个一直不让睡的进程。
func spawnCaffeinate(display bool) (*exec.Cmd, error) {
	if runtime.GOOS != "darwin" {
		return nil, errAwakeUnsupported
	}
	args := []string{"-i", "-s", "-w", strconv.Itoa(os.Getpid())}
	if display {
		args = append([]string{"-d"}, args...)
	}
	cmd := exec.Command("/usr/bin/caffeinate", args...)
	return cmd, cmd.Start()
}

// apply 按档位决定托不托住。auto、display 在常驻进程活着时就托住，不看 agent 在不在工作。
func (a *awake) apply(working int) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.working = working
	want := a.mode != awakeOff || a.viewing
	display := a.mode == awakeDisplay || a.viewing
	// 换档时屏幕那一项变了：先放掉旧的再按新的起
	if want && a.holding() && a.display != display {
		a.release()
	}
	switch {
	case want && !a.holding():
		cmd, err := a.spawn(display)
		if err != nil {
			if !errors.Is(err, errAwakeUnsupported) {
				log.Printf("keep awake: %v", err)
			}
			return
		}
		done := make(chan struct{})
		go func() {
			cmd.Wait()
			close(done)
		}()
		a.cmd, a.done, a.display = cmd, done, display
	case !want && a.cmd != nil:
		a.release()
	}
}

// holding 说明 caffeinate 还在运行；调用方持有 a.mu。
func (a *awake) holding() bool {
	if a.cmd == nil {
		return false
	}
	select {
	case <-a.done:
		a.cmd, a.done = nil, nil
		return false
	default:
		return true
	}
}

// release 结束 caffeinate；调用方持有 a.mu。
func (a *awake) release() {
	if a.cmd == nil {
		return
	}
	a.cmd.Process.Kill()
	<-a.done
	a.cmd, a.done = nil, nil
}

// setViewing 记下有没有手机在看桌面，调用方随后 apply。
func (a *awake) setViewing(on bool) {
	a.mu.Lock()
	a.viewing = on
	a.mu.Unlock()
}

func (a *awake) setMode(m string) error {
	a.mu.Lock()
	a.mode = m
	a.mu.Unlock()
	b, _ := json.Marshal(map[string]string{"mode": m})
	return os.WriteFile(a.path, b, 0o600)
}

func (a *awake) status() awakeStatus {
	a.mu.Lock()
	defer a.mu.Unlock()
	return awakeStatus{Mode: a.mode, Active: a.holding(), Working: a.working}
}

func (a *awake) close() {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.release()
}

// workingAgents 数运行中、状态是工作中的 agent，只报给界面，不参与托不托住。
func (d *Daemon) workingAgents() int {
	d.mu.Lock()
	defer d.mu.Unlock()
	n := 0
	for _, s := range d.terms {
		if !s.Running() || s.agentName() == "" {
			continue
		}
		if s.Agent().Status == agent.StatusWorking {
			n++
		}
	}
	return n
}

func (d *Daemon) refreshAwake() {
	if d.awake == nil {
		return
	}
	d.awake.apply(d.workingAgents())
}

func (d *Daemon) awakeLoop(ctx context.Context) {
	tick := time.NewTicker(awakeTick)
	defer tick.Stop()
	for {
		d.refreshAwake()
		select {
		case <-ctx.Done():
			return
		case <-tick.C:
		}
	}
}

func (d *Daemon) awakeSet(ctx context.Context, raw json.RawMessage) (any, error) {
	var p struct {
		Mode string `json:"mode"`
	}
	if err := rpc.Decode(raw, &p); err != nil {
		return nil, err
	}
	p.Mode = normalAwakeMode(p.Mode)
	if !validAwakeMode(p.Mode) {
		return nil, rpc.Errorf(rpc.CodeBadRequest, "mode must be off, auto or display")
	}
	if d.awake == nil {
		return nil, rpc.Errorf(rpc.CodeBadRequest, "keep awake is not available")
	}
	if err := d.awake.setMode(p.Mode); err != nil {
		return nil, err
	}
	d.refreshAwake()
	return d.awake.status(), nil
}
