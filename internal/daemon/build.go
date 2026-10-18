package daemon

import (
	"os"
	"path/filepath"
	"runtime/debug"
	"time"
)

// Build 标识这个进程跑的是哪一次构建：git 提交加上可执行文件的修改时间。
// 打包常在有未提交改动时进行，只看提交分不出两次构建；修改时间在进程启动时就取好，
// 之后包被换掉也不变，窗口拿它和常驻进程的比，就知道常驻进程是不是还在跑旧包。
var Build = buildID()

func buildID() string {
	rev, dirty := "", false
	if info, ok := debug.ReadBuildInfo(); ok {
		for _, s := range info.Settings {
			switch s.Key {
			case "vcs.revision":
				rev = s.Value
			case "vcs.modified":
				dirty = s.Value == "true"
			}
		}
	}
	if len(rev) > 7 {
		rev = rev[:7]
	}
	if dirty {
		rev += "+"
	}
	if exe, err := os.Executable(); err == nil {
		if resolved, err := filepath.EvalSymlinks(exe); err == nil {
			exe = resolved
		}
		if fi, err := os.Stat(exe); err == nil {
			rev += " " + fi.ModTime().UTC().Format(time.RFC3339)
		}
	}
	return rev
}
