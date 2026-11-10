package daemon

import (
	"os"
	"path/filepath"
	"strings"
)

// PosseDev.app（scripts/dev-app.sh 装出的开发包）没设 POSSE_HOME、POSSE_MOBILE_PORT 时用自己的数据目录和端口。
// 双击打开、系统重新拉起、Computer Use 按包 ID 激活都不带环境变量：要是连上 ~/.posse 的日常常驻进程，
// 窗口发现常驻进程的包比自己旧会自动交接（internal/app/upgrade.go），日常的终端就跑到开发包上了，
// 下次重装开发包时被整个换掉。
const (
	devHomeName   = ".posse-dev"
	devMobilePort = 6790
)

// isDevBundle 判断可执行文件在不在 PosseDev.app 里。
func isDevBundle(exe string) bool {
	return strings.Contains(exe, "/PosseDev.app/Contents/MacOS/")
}

// RunningDevBundle 报告这个进程是不是从 PosseDev.app 运行的。
func RunningDevBundle() bool { return runningDevBundle() }

func runningDevBundle() bool {
	exe, err := os.Executable()
	return err == nil && isDevBundle(exe)
}

// ResolveHome 是数据目录：env 是 POSSE_HOME，空时日常用 ~/.posse、开发包用 ~/.posse-dev。
// 开发包连 env 指向 ~/.posse 也换掉：在 Posse 的终端里直接运行开发包的程序会继承终端的 POSSE_HOME。
func ResolveHome(env string) string {
	home, _ := os.UserHomeDir()
	return resolveHome(env, home, runningDevBundle())
}

func resolveHome(env, userHome string, dev bool) string {
	daily := filepath.Join(userHome, ".posse")
	if dev && (env == "" || filepath.Clean(env) == daily) {
		return filepath.Join(userHome, devHomeName)
	}
	if env == "" {
		return daily
	}
	return env
}
