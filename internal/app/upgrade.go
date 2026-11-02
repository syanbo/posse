package app

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"posse/internal/daemon"
	"posse/internal/errtext"
	"posse/internal/rpc"
	"posse/internal/spawn"
	"posse/locales"
)

// upgradeState 记着把常驻进程换成本窗口的包（daemon.handoff）的进度，按常驻进程的 pid 记：
// 每个旧进程只自动试一次，失败了不反复试。
type upgradeState struct {
	mu          sync.Mutex
	tried       map[int]bool
	unsupported map[int]bool // 旧常驻进程没有交接能力，只能重启
	running     bool
}

func (u *upgradeState) begin(pid int, auto bool) bool {
	u.mu.Lock()
	defer u.mu.Unlock()
	if u.running || u.unsupported[pid] || (auto && u.tried[pid]) {
		return false
	}
	if u.tried == nil {
		u.tried, u.unsupported = map[int]bool{}, map[int]bool{}
	}
	u.tried[pid], u.running = true, true
	return true
}

func (u *upgradeState) end(pid int, unsupported bool) {
	u.mu.Lock()
	defer u.mu.Unlock()
	u.running = false
	if unsupported {
		u.unsupported[pid] = true
	}
}

// status 给设置页：正在更新、旧常驻进程不支持交接。
func (u *upgradeState) status(pid int) (running, unsupported bool) {
	u.mu.Lock()
	defer u.mu.Unlock()
	return u.running, u.unsupported[pid]
}

// buildTime 取构建标识里可执行文件的修改时间。
func buildTime(build string) (time.Time, bool) {
	_, ts, ok := strings.Cut(build, " ")
	if !ok {
		return time.Time{}, false
	}
	t, err := time.Parse(time.RFC3339, ts)
	return t, err == nil
}

// autoUpgrade 在轮询线程里调用：常驻进程的包比窗口的旧时自动交接。只在窗口的包更新时做，
// 打开一个旧的 Posse.app 不会把常驻进程换回旧包。
func (a *app) autoUpgrade(d daemonStatus) {
	if d.Pid == 0 || d.Build == "" || d.Build == daemon.Build {
		return
	}
	mine, ok1 := buildTime(daemon.Build)
	theirs, ok2 := buildTime(d.Build)
	if !ok1 || !ok2 || !mine.After(theirs) {
		return
	}
	a.upgradeDaemon(appL(), d.Pid, true)
}

// upgradeDaemon 让常驻进程把终端交给用本窗口的包启动的新进程，终端不断。
func (a *app) upgradeDaemon(m locales.Messages, pid int, auto bool) {
	if !a.upgrade.begin(pid, auto) {
		return
	}
	a.win.Update(func() {})
	go func() {
		exe, err := os.Executable()
		if err == nil {
			if resolved, rerr := filepath.EvalSymlinks(exe); rerr == nil {
				exe = resolved
			}
			// 常驻进程从新包的副本运行，旧版本的常驻进程不会自己做副本；准备不了就用原包，交接照常
			if rt, rerr := spawn.Runtime(exe, a.c.Home); rerr == nil {
				exe = rt
			}
			// 交接最多等 20 秒（handoffTimeout），多留一点
			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			err = a.c.Call(ctx, "daemon.handoff", map[string]string{"exe": exe}, nil)
			cancel()
		}
		var re *rpc.Error
		unsupported := errors.As(err, &re) && re.Code == rpc.CodeUnknown
		// 别处（另一个窗口、posse daemon handoff）已经在交接：结果一样，不算失败
		busy := errors.As(err, &re) && re.Code == rpc.CodeConflict
		a.upgrade.end(pid, unsupported)
		a.win.Update(func() {
			if err != nil && !unsupported && !busy {
				a.err = m.UpgradeError(errtext.Text(err))
			}
		})
		a.pollForce.Store(true)
		a.refresh()
	}()
}

// staleDaemonError 是连上了常驻进程、却读不到数据时的提示：多半是窗口换了新版本，常驻进程还是旧的，
// 接口对不上。版本一样时照原样报错。
func (a *app) staleDaemonError(m locales.Messages, d daemonStatus, err error) string {
	if d.Build == daemon.Build {
		return errtext.Text(err)
	}
	running, unsupported := a.upgrade.status(d.Pid)
	switch {
	case running:
		return m.UpgradeRunning()
	case unsupported || d.Build == "":
		return m.UpgradeTooOld(errtext.Text(err))
	default:
		return m.UpgradeMismatch(errtext.Text(err))
	}
}
