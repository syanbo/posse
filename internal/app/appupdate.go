package app

import (
	"context"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/egoist/mygo"
	"github.com/egoist/mygo/ui"
)

// Posse 的自动更新（方案见 docs/plans/mac-update.md）：后台只把新版本下载、解压到 app 旁边，
// 用户点「重启更新」或退出时才换包，接着交接常驻进程、重开窗口。包被换掉后，
// 窗口和常驻进程还没用过的隐私授权（文稿、桌面等）直接失效、不弹授权，所以换包和交接要连着做。

const (
	updateEvery = 6 * time.Hour    // 距上次成功检查满这么久才查
	updateTick  = 30 * time.Minute // 多久看一次满没满
	updateDelay = 10 * time.Second // 窗口启动后等这么久再查，不和启动抢网络和磁盘
)

type updatePhase int

const (
	updateIdle updatePhase = iota
	updateChecking
	updateDownloading
	updateReady
	updateFailed
)

// selfUpdate 是检查、下载的进度，检查协程写、界面线程读，都经 mu。
type selfUpdate struct {
	mu        sync.Mutex
	phase     updatePhase
	busy      bool   // 正在检查或下载：同一时间只跑一次
	installed bool   // 已经换过包（重启更新或退出时），不再换第二次
	version   string // 正在下载的版本
	done      int64
	total     int64
	err       string
	checkedAt time.Time // 上次成功检查的时间，存在 ui.json
	staged    *mygo.Staged

	// writable 是能不能写 app 所在的目录：mygo 每问一次都在里面建、删一个临时目录，设置页每帧都要用，只探一次
	writable func() bool
}

// updateView 是界面要显示的一份快照。
type updateView struct {
	phase     updatePhase
	busy      bool
	version   string
	notes     string
	percent   int
	err       string
	checkedAt time.Time
}

func (u *selfUpdate) view() updateView {
	u.mu.Lock()
	defer u.mu.Unlock()
	v := updateView{phase: u.phase, busy: u.busy, version: u.version, err: u.err, checkedAt: u.checkedAt}
	if u.total > 0 {
		v.percent = int(u.done * 100 / u.total)
	}
	if u.staged != nil {
		v.version, v.notes = u.staged.Version, u.staged.Notes
	}
	return v
}

// due 判断这次要不要查：手动的总是查，自动的距上次成功检查满 updateEvery 才查。
func (u *selfUpdate) due(manual bool, now time.Time) bool {
	return manual || u.checkedAt.IsZero() || now.Sub(u.checkedAt) >= updateEvery
}

// updateUnavailable 说明这个包为什么不能自动更新；空串表示能。
// 不带更新地址的包（PosseDev、go build、mygo dev）不显示更新，hidden 为真。
func (a *app) updateUnavailable() (reason string, hidden bool) {
	if !mygo.Updater.Configured() || a.exe.bundle == "" {
		return "", true
	}
	if !inApplications(a.exe.bundle) {
		// build/ 里用本地代码打的包也在这里：别让线上版本把它覆盖掉
		return "把 Posse 移到「应用程序」文件夹后才能自动更新。", false
	}
	if a.upd.writable == nil {
		a.upd.writable = sync.OnceValue(mygo.Updater.Enabled)
	}
	if !a.upd.writable() {
		return "没有写入「应用程序」文件夹的权限，不能自动更新。", false
	}
	return "", false
}

func inApplications(bundle string) bool {
	dirs := []string{"/Applications"}
	if home, err := os.UserHomeDir(); err == nil {
		dirs = append(dirs, filepath.Join(home, "Applications"))
	}
	for _, d := range dirs {
		if strings.HasPrefix(bundle, d+string(filepath.Separator)) {
			return true
		}
	}
	return false
}

// startUpdates 在窗口建好后调用：能自动更新时，启动后、之后每 updateTick、从睡眠唤醒时检查，
// 退出时换上已经下好的版本。
func (a *app) startUpdates() {
	if reason, hidden := a.updateUnavailable(); hidden || reason != "" {
		return
	}
	a.upd.checkedAt = a.state.UpdateCheckedAt
	// 上次没换上的暂存和中断的换包留下的目录。在能触发检查之前清，免得删掉正在暂存的
	mygo.Updater.CleanStaged()
	mygo.Power.OnResume(func() { go a.checkUpdate(false) })
	mygo.App.OnQuit(a.installOnQuit)
	go func() {
		time.Sleep(updateDelay)
		for {
			a.checkUpdate(false)
			time.Sleep(updateTick)
		}
	}()
}

// checkUpdate 查新版本，有就下载暂存。manual 是用户点的「检查更新」，不看距上次多久。
func (a *app) checkUpdate(manual bool) {
	if reason, _ := a.updateUnavailable(); reason != "" {
		return
	}
	u := &a.upd
	u.mu.Lock()
	if u.busy || u.installed || !u.due(manual, time.Now()) {
		u.mu.Unlock()
		return
	}
	u.busy, u.err = true, ""
	if u.phase != updateReady {
		u.phase = updateChecking
	}
	u.mu.Unlock()
	a.update(func() {})

	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	up, err := mygo.Updater.Check(ctx)
	cancel()
	if err == nil && up != nil && !a.alreadyStaged(up.Version) {
		err = a.stageUpdate(up)
	}

	u.mu.Lock()
	u.busy = false
	switch {
	case err != nil:
		u.err = err.Error()
		if u.staged == nil {
			u.phase = updateFailed
		}
	case u.staged != nil:
		u.phase = updateReady
	default:
		u.phase = updateIdle
	}
	if err == nil {
		u.checkedAt = time.Now()
	}
	checked := u.checkedAt
	u.mu.Unlock()
	if err != nil {
		log.Printf("posse: checking for updates: %v", err)
	}
	a.update(func() {
		if !checked.Equal(a.state.UpdateCheckedAt) {
			a.state.UpdateCheckedAt = checked
			a.saveState()
		}
	})
}

func (a *app) alreadyStaged(version string) bool {
	a.upd.mu.Lock()
	defer a.upd.mu.Unlock()
	return a.upd.staged != nil && a.upd.staged.Version == version
}

// stageUpdate 下载 up 并暂存在 app 旁边；之前暂存的旧一点的版本换成它。
func (a *app) stageUpdate(up *mygo.Update) error {
	u := &a.upd
	u.mu.Lock()
	u.version, u.done, u.total = up.Version, 0, 0
	if u.staged == nil {
		u.phase = updateDownloading
	}
	u.mu.Unlock()
	a.update(func() {})
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Minute)
	defer cancel()
	shown := -1
	staged, err := up.Stage(ctx, func(done, total int64) {
		u.mu.Lock()
		u.done, u.total = done, total
		u.mu.Unlock()
		// 下载时每读一块报一次，界面只显示整数百分比
		if total > 0 && int(done*100/total) != shown {
			shown = int(done * 100 / total)
			a.update(func() {})
		}
	})
	if err != nil {
		return err
	}
	u.mu.Lock()
	if u.installed {
		// 下载期间已经换上了之前暂存的版本
		u.mu.Unlock()
		staged.Discard()
		return nil
	}
	old := u.staged
	u.staged = staged
	u.mu.Unlock()
	if old != nil {
		old.Discard()
	}
	return nil
}

// takeStaged 取出下好的版本准备换包，之后不再检查、不再换第二次；正在下载的更新的版本下完就丢掉。
// 窗口的包已经被别处换掉（重新打包、手动覆盖安装）时不换：暂存的版本可能比它旧。
func (a *app) takeStaged() *mygo.Staged {
	u := &a.upd
	u.mu.Lock()
	defer u.mu.Unlock()
	if u.staged == nil || u.installed {
		return nil
	}
	s := u.staged
	u.staged, u.installed = nil, true
	if a.exe.replaced() {
		s.Discard()
		return nil
	}
	return s
}

// installUpdate 换上下好的版本，再把常驻进程交接到新包上：换包后旧常驻进程还没用过的授权会失效。
// 换包失败时暂存已经没了，回到可以重新检查、下载的状态。
func (a *app) installUpdate(s *mygo.Staged, wait time.Duration) error {
	if err := s.Apply(); err != nil {
		u := &a.upd
		u.mu.Lock()
		u.installed, u.phase, u.err = false, updateFailed, err.Error()
		u.mu.Unlock()
		return err
	}
	// 可执行文件的路径没变，里面已经是新版本；和 upgradeDaemon 一样交解析过符号链接的路径
	exe := a.exe.exe
	if resolved, err := filepath.EvalSymlinks(exe); err == nil {
		exe = resolved
	}
	ctx, cancel := context.WithTimeout(context.Background(), wait)
	defer cancel()
	if err := a.c.Call(ctx, "daemon.handoff", map[string]string{"exe": exe}, nil); err != nil {
		// 新窗口的 autoUpgrade 还会再试
		log.Printf("posse: handing the daemon off to the update: %v", err)
	}
	return nil
}

// restartToUpdate 是「重启更新」：先处理未保存的文件，再换包、交接常驻进程、重开窗口。
func (a *app) restartToUpdate() {
	if docs := a.dirtyDocs(); len(docs) > 0 {
		a.askUnsaved(docs, a.restartToUpdate)
		return
	}
	s := a.takeStaged()
	if s == nil {
		return
	}
	a.updating = true
	go func() {
		// 交接最多等 20 秒（handoffTimeout），多留一点
		err := a.installUpdate(s, 30*time.Second)
		a.update(func() {
			a.updating = false
			if err != nil {
				a.err = "更新失败，Posse 没有改动：" + err.Error()
				return
			}
			// 等交接的这段时间里可能又改了文件，重开前再问一次
			a.relaunch()
		})
	}()
}

// installOnQuit 在退出、事件循环停下后换上已经下好的版本，下次打开就是新版本。
func (a *app) installOnQuit() {
	s := a.takeStaged()
	if s == nil {
		return
	}
	// 退出时不久等：交接被注销、关机打断的话，下次打开窗口的 autoUpgrade 会接着交接
	if err := a.installUpdate(s, 10*time.Second); err != nil {
		log.Printf("posse: installing the update on quit: %v", err)
	}
}

// updateBar 是侧栏底部的"新版本已就绪"卡片，关掉后这个版本不再出现，设置页里照样能更新。
func (a *app) updateBar(c *ui.Context) {
	if !a.loaded || a.staleApp {
		return
	}
	v := a.upd.view()
	if v.phase != updateReady || v.version == a.state.UpdateDismissed {
		return
	}
	t := c.Theme()
	ui.Column(c).Margin(0, 12, 8, 12).Padding(12, 6, 12, 12).Gap(8).Radius(rControl).Background(t.Surface).Children(func() {
		ui.Row(c).Gap(6).AlignItems(ui.Start).Children(func() {
			ui.Text(c, fmt.Sprintf("Posse %s 已就绪，更新时终端不会断开。", v.version)).FontSize(fsSmall).TextColor(t.TextMuted).LineHeight(1.45).Grow(1).Shrink(1)
			if iconButtonSized(c, iconClose, "不再提示这个版本", compactBox(), smallIcon()).Clicked() {
				a.state.UpdateDismissed = v.version
				a.saveState()
			}
		})
		ui.Row(c).Gap(8).AlignItems(ui.Center).Children(func() {
			b := pressable(accentButton(c, "重启更新"))
			if a.updating {
				b.Disabled(true)
			}
			if b.Clicked() {
				a.restartToUpdate()
			}
			if pressable(textButton(c, "更新内容")).Clicked() {
				a.openAbout()
			}
		})
	})
}

func (a *app) openAbout() {
	a.settingsTab = "about"
	a.openSettings()
}

// updateSettings 是设置「关于」里的更新卡片。
func (a *app) updateSettings(c *ui.Context) {
	reason, hidden := a.updateUnavailable()
	if hidden {
		return
	}
	t := c.Theme()
	v := a.upd.view()
	settingsCard(c, "更新", "", func() {
		var status string
		switch {
		case reason != "":
			status = reason
		case a.updating:
			status = "正在更新…"
		case v.phase == updateChecking:
			status = "正在检查更新…"
		case v.phase == updateDownloading:
			status = fmt.Sprintf("正在下载 %s…%d%%", v.version, v.percent)
		case v.phase == updateReady:
			status = fmt.Sprintf("%s 已就绪，重启后生效，终端不会断开。", v.version)
		case v.phase == updateFailed:
			status = "检查更新失败：" + v.err
		case v.checkedAt.IsZero():
			status = "还没有检查过更新。"
		default:
			c.After(time.Minute)
			status = fmt.Sprintf("已是最新版本（%s检查）。", ago(v.checkedAt))
		}
		ui.Text(c, status).FontSize(fsBody).TextColor(t.Text).LineHeight(1.5)
		if v.phase == updateReady && v.err != "" && !a.updating {
			ui.Text(c, "再次检查更新失败："+v.err).FontSize(fsSmall).TextColor(t.TextMuted).LineHeight(1.5)
		}
		if reason != "" {
			return
		}
		ui.Row(c).Gap(8).Padding(4, 0, 0, 0).Children(func() {
			if v.phase == updateReady {
				b := pressable(accentButton(c, "重启更新"))
				if a.updating {
					b.Disabled(true)
				}
				if b.Clicked() {
					a.restartToUpdate()
				}
			}
			b := pressable(textButton(c, "检查更新"))
			if v.busy || a.updating {
				b.Disabled(true)
			}
			if b.Clicked() {
				go a.checkUpdate(true)
			}
		})
		if v.phase == updateReady && strings.TrimSpace(v.notes) != "" {
			ui.Column(c).Gap(4).Padding(4, 0, 0, 0).Children(func() {
				ui.Text(c, "更新内容").FontSize(fsBody).TextColor(t.TextMuted)
				ui.Text(c, strings.TrimSpace(v.notes)).FontSize(fsBody).TextColor(t.Text).LineHeight(1.5)
			})
		}
	})
}
