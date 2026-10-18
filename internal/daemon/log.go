package daemon

import (
	"os"
	"path/filepath"
)

// maxDaemonLog 是 daemon.log 的上限：拉起常驻进程前超过它就改名成 daemon.log.1，只留一份旧的。
// 日志由拉起方打开后交给常驻进程，常驻进程运行期间不轮转，按现在的写入量一次运行涨不了多少。
const maxDaemonLog = 5 << 20

// OpenLog 打开交给新常驻进程当 stdout/stderr 的 daemon.log，太大时先轮转。
// client.EnsureDaemon 和交接时拉起新进程都经过这里。
func OpenLog(home string) (*os.File, error) {
	path := filepath.Join(home, "daemon.log")
	rotateLog(path, maxDaemonLog)
	return os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
}

func rotateLog(path string, max int64) {
	if fi, err := os.Stat(path); err == nil && fi.Size() > max {
		os.Rename(path, path+".1")
	}
}
