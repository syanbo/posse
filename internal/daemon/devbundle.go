package daemon

import (
	"os"
	"path/filepath"
	"strings"
)

// HiveDev.app（scripts/dev-app.sh 装出的开发包）没设 HIVE_HOME、HIVE_MOBILE_PORT 时用自己的数据目录和端口。
// 双击打开、系统重新拉起、Computer Use 按包 ID 激活都不带环境变量：要是连上 ~/.hive 的日常常驻进程，
// 窗口发现常驻进程的包比自己旧会自动交接（internal/app/upgrade.go），日常的终端就跑到开发包上了，
// 下次重装开发包时被整个换掉。
const (
	devHomeName   = ".hive-dev"
	devMobilePort = 6790
)

// isDevBundle 判断可执行文件在不在 HiveDev.app 里。
func isDevBundle(exe string) bool {
	return strings.Contains(exe, "/HiveDev.app/Contents/MacOS/")
}

func runningDevBundle() bool {
	exe, err := os.Executable()
	return err == nil && isDevBundle(exe)
}

// ResolveHome 是数据目录：env 是 HIVE_HOME，空时日常用 ~/.hive、开发包用 ~/.hive-dev。
// 开发包连 env 指向 ~/.hive 也换掉：在 Hive 的终端里直接运行开发包的程序会继承终端的 HIVE_HOME。
func ResolveHome(env string) string {
	home, _ := os.UserHomeDir()
	return resolveHome(env, home, runningDevBundle())
}

func resolveHome(env, userHome string, dev bool) string {
	daily := filepath.Join(userHome, ".hive")
	if dev && (env == "" || filepath.Clean(env) == daily) {
		return filepath.Join(userHome, devHomeName)
	}
	if env == "" {
		return daily
	}
	return env
}
