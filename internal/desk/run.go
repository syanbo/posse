package desk

import (
	"errors"
	"io"
	"runtime"
)

// Options 是桌面子进程的运行参数。Control 读到 EOF 就退出。
// Frames 和 Control 为空时用标准输出和标准输入。
type Options struct {
	Control io.Reader
	Frames  io.Writer
	Log     io.Writer

	// Seconds 到点就自己停。给不经过父进程的试跑用；父进程拉起时靠关掉 Control。
	Seconds int
	// Codec 是 hevc 或 h264。Bench 时忽略，两种都跑。
	Codec    string
	Bench    bool
	Probe    bool
	SelfTest bool
	// InputTest 不采集，把指针移到屏幕中心、单击、打一个字母，用来手动验收。
	InputTest bool
	Mov       string
	// Exclude 是采集时排除窗口的 App bundle id，可重复。
	Exclude []string
	// LongEdge 是输出长边的像素上限，0 用 1920。
	LongEdge int
	// Quiet 不每秒记编码统计，结束时仍记一行汇总。常驻进程拉起时用：日志进 daemon.log
	Quiet bool
	// Display 是开始时看哪块屏（CGDirectDisplayID），0 或已经不在了用主屏
	Display uint32
}

// Run 跑桌面子进程。非 macOS 直接报错。
func Run(o Options) error {
	if runtime.GOOS != "darwin" {
		return errors.New("desk: the desktop helper only runs on macOS")
	}
	return runDarwin(o)
}
