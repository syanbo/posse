package app

import (
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"

	"github.com/egoist/mygo"

	"posse/internal/errtext"
	"posse/locales"
)

// exeWatch 盯着窗口自己所在的 Posse.app 有没有被换掉（重新打包、覆盖安装）。
// 换掉后系统核实不了这个进程的代码身份，TCC 直接拒绝它访问桌面、文稿等目录（EPERM），
// 要重开窗口才恢复。不在 .app 里运行（go build 出来的 bin/posse）时不管：那时授权跟着启动它的终端走。
type exeWatch struct {
	bundle string // Posse.app 的路径，空表示不在 .app 里
	exe    string
	dev    int32
	ino    uint64
}

// bundleOf 从可执行文件路径取它所在的 .app：…/Posse.app/Contents/MacOS/Posse。
func bundleOf(exe string) string {
	macos := filepath.Dir(exe)
	contents := filepath.Dir(macos)
	app := filepath.Dir(contents)
	if filepath.Base(macos) != "MacOS" || filepath.Base(contents) != "Contents" || !strings.HasSuffix(app, ".app") {
		return ""
	}
	return app
}

func newExeWatch() exeWatch {
	exe, err := os.Executable()
	if err != nil {
		return exeWatch{}
	}
	var st syscall.Stat_t
	if bundleOf(exe) == "" || syscall.Stat(exe, &st) != nil {
		return exeWatch{}
	}
	return exeWatch{bundle: bundleOf(exe), exe: exe, dev: st.Dev, ino: st.Ino}
}

// replaced 报告启动时的可执行文件是否已被换掉或删除。打包和覆盖安装都是换整个 .app，inode 一定会变。
func (w exeWatch) replaced() bool {
	if w.bundle == "" {
		return false
	}
	var st syscall.Stat_t
	if err := syscall.Stat(w.exe, &st); err != nil {
		return os.IsNotExist(err)
	}
	return st.Dev != w.dev || st.Ino != w.ino
}

// checkReplaced 在轮询线程里调用，发现包被换掉后只报一次。
func (a *app) checkReplaced() {
	if a.exeReplaced.Load() || !a.exe.replaced() {
		return
	}
	a.exeReplaced.Store(true)
	a.update(func() { a.staleApp = true })
}

// relaunch 重开窗口：先处理未保存的文件，再退出、由后台脚本等本进程退出后用新包重新打开。
// 终端都在常驻进程里，不受影响；新窗口的包比常驻进程新时会照常自动交接。
func (a *app) relaunch(m locales.Messages) {
	if docs := a.dirtyDocs(); len(docs) > 0 {
		a.askUnsaved(docs, func() { a.doRelaunch(m) })
		return
	}
	a.doRelaunch(m)
}

func (a *app) doRelaunch(m locales.Messages) {
	args := []string{"-n"}
	// 端到端测试靠 POSSE_HOME 等变量隔离，open 起的新实例要带上
	for _, kv := range os.Environ() {
		if strings.HasPrefix(kv, "POSSE_") {
			args = append(args, "--env", kv)
		}
	}
	args = append(args, a.exe.bundle)
	// 等本进程完全退出再打开：同时开着两个窗口会争着应答终端查询
	script := `while kill -0 "$0" 2>/dev/null; do sleep 0.1; done; exec /usr/bin/open "$@"`
	cmd := exec.Command("/bin/sh", append([]string{"-c", script, strconv.Itoa(os.Getpid())}, args...)...)
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	if err := cmd.Start(); err != nil {
		a.err = m.RelaunchError(errtext.Text(err))
		return
	}
	go cmd.Wait()
	a.closeOK.Store(true)
	mygo.App.Quit()
}
