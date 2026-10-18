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

	"hive/internal/agent"
	"hive/internal/rpc"
)

// 防休眠照 Orca 的 agent-awake（Magpie 也是这样）：macOS 上起 /usr/bin/caffeinate -i -s 托住系统不睡，
// 屏幕照常按系统设置熄灭。放在常驻进程里，窗口关掉、agent 照样在跑时也有效。
// 四档：off 不管；auto 有 agent 在工作时托住；display 和 auto 一样，另加 -d 让屏幕也不熄
// （照 Magpie 的 Screen on too，给操作桌面界面的 agent 用：屏幕熄了会跟着锁屏）；on 一直托住系统。
// 合盖仍会睡，-s 只在接电源时生效。

const (
	awakeOff     = "off"
	awakeAuto    = "auto"
	awakeDisplay = "display"
	awakeOn      = "on"

	// 状态这么久没更新的"工作中"不算：agent 卡住或 hook 丢了，不能一直不让睡
	awakeStale = 2 * time.Hour
	// 定时重算：过期的工作中状态、意外退出的 caffeinate 都在这时补上
	awakeTick = 30 * time.Second
)

type awakeStatus struct {
	Mode    string `json:"mode"`
	Active  bool   `json:"active"`  // 此刻正托着不让睡
	Working int    `json:"working"` // 算进 auto 的工作中的 agent 数
}

type awake struct {
	mu      sync.Mutex
	path    string // $HIVE_HOME/awake.json，记用户选的档
	mode    string
	working int
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
	if b, err := os.ReadFile(a.path); err == nil && json.Unmarshal(b, &saved) == nil && validAwakeMode(saved.Mode) {
		a.mode = saved.Mode
	}
	return a
}

func validAwakeMode(m string) bool {
	return m == awakeOff || m == awakeAuto || m == awakeDisplay || m == awakeOn
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

// apply 按档位和工作中的 agent 数决定托不托住。
func (a *awake) apply(working int) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.working = working
	want := a.mode == awakeOn || (a.mode == awakeAuto || a.mode == awakeDisplay) && working > 0
	display := a.mode == awakeDisplay
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

// workingAgents 数运行中、状态是工作中且没有过期的 agent。
func (d *Daemon) workingAgents() int {
	d.mu.Lock()
	defer d.mu.Unlock()
	n := 0
	for _, s := range d.terms {
		if !s.Running() || s.agentName() == "" {
			continue
		}
		if st := s.Agent(); st.Status == agent.StatusWorking && time.Since(st.At) < awakeStale {
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
	if !validAwakeMode(p.Mode) {
		return nil, rpc.Errorf(rpc.CodeBadRequest, "mode must be off, auto, display or on")
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
