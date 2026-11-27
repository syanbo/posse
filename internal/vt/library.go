package vt

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
)

// NativeLibrary 描述单个平台对应的 libghostty-vt 动态库元数据。
type NativeLibrary struct {
	Name   string
	SHA256 string
	URL    string
}

// libraryManifest 与 third_party/mygo/plugins/terminal/mygo-plugin.json 保持同步（由测试校验）。
var libraryManifest = map[string]NativeLibrary{
	"darwin-amd64": {
		Name:   "libghostty-vt.dylib",
		SHA256: "421e98d2683b39555209c59395655b4a217da0382e7669c92f6dff3949832664",
		URL:    "https://github.com/egoist/mygo/releases/download/libghostty-vt-befcdfd2c3a1/libghostty-vt-darwin-amd64.dylib",
	},
	"darwin-arm64": {
		Name:   "libghostty-vt.dylib",
		SHA256: "6f385d3221f8c776ba40530db9baf8c71478907897959849896eeb0ddbf16b41",
		URL:    "https://github.com/egoist/mygo/releases/download/libghostty-vt-befcdfd2c3a1/libghostty-vt-darwin-arm64.dylib",
	},
	"linux-amd64": {
		Name:   "libghostty-vt.so",
		SHA256: "9dcd8af0de67778a561f436cb673a9fb1738be8fa00849dad49606baed1e57d7",
		URL:    "https://github.com/egoist/mygo/releases/download/libghostty-vt-befcdfd2c3a1/libghostty-vt-linux-amd64.so",
	},
	"linux-arm64": {
		Name:   "libghostty-vt.so",
		SHA256: "fb163f8f123206956751f1c4f0303b40899513b7dc171a8ab8d85ba7cd541bc0",
		URL:    "https://github.com/egoist/mygo/releases/download/libghostty-vt-befcdfd2c3a1/libghostty-vt-linux-arm64.so",
	},
	"windows-amd64": {
		Name:   "ghostty-vt.dll",
		SHA256: "53e8e2dd979702e8f2c48107af19be5a76efc2e88eb7cf1097218e899665da41",
		URL:    "https://github.com/egoist/mygo/releases/download/libghostty-vt-befcdfd2c3a1/libghostty-vt-windows-amd64.dll",
	},
	"windows-arm64": {
		Name:   "ghostty-vt.dll",
		SHA256: "de56a362f12cce8be38e883ffc8aec90a890e0574eb73aac6e833a60fda26064",
		URL:    "https://github.com/egoist/mygo/releases/download/libghostty-vt-befcdfd2c3a1/libghostty-vt-windows-arm64.dll",
	},
}

// LibraryInfo 返回指定平台的 libghostty-vt 描述。
func LibraryInfo(goos, goarch string) (NativeLibrary, error) {
	target := goos + "-" + goarch
	lib, ok := libraryManifest[target]
	if !ok {
		return NativeLibrary{}, fmt.Errorf("terminal: libghostty-vt has no build for %s", target)
	}
	return lib, nil
}

// LibraryPath 按顺序查找当前平台的 libghostty-vt：
// 1. 环境变量 POSSE_GHOSTTY_VT / MYGO_GHOSTTY_VT；
// 2. 可执行文件真实路径同目录（无头服务端版本目录）及 .app 的 Contents/Resources；
// 3. $POSSE_HOME/natives/<sha256>/ 与用户缓存目录；
// 4. 非 headless 构建回退到 mygo 终端插件按需下载，headless 构建禁止运行期下载。
func LibraryPath() (string, error) {
	if p := os.Getenv("POSSE_GHOSTTY_VT"); p != "" {
		return p, nil
	}
	if p := os.Getenv("MYGO_GHOSTTY_VT"); p != "" {
		return p, nil
	}
	lib, err := LibraryInfo(runtime.GOOS, runtime.GOARCH)
	if err != nil {
		return "", err
	}
	exe, _ := os.Executable()
	if p, ok := findLibraryLocal(lib, exe, os.Getenv("POSSE_HOME")); ok {
		return p, nil
	}
	return fallbackLibraryPath(lib)
}

func findLibraryLocal(lib NativeLibrary, exe, posseHome string) (string, bool) {
	var dirs []string
	addDir := func(d string) {
		if d != "" {
			dirs = append(dirs, d)
		}
	}
	if exe != "" {
		if resolved, err := filepath.EvalSymlinks(exe); err == nil {
			exe = resolved
		}
		exeDir := filepath.Dir(exe)
		// 若位于 .app/Contents/MacOS，优先查 Contents/Resources（与应用同签名）
		if filepath.Base(exeDir) == "MacOS" {
			contents := filepath.Dir(exeDir)
			if filepath.Base(contents) == "Contents" && strings.HasSuffix(filepath.Dir(contents), ".app") {
				addDir(filepath.Join(contents, "Resources"))
			}
		}
		addDir(exeDir)
	}
	if posseHome != "" {
		addDir(filepath.Join(posseHome, "natives", lib.SHA256))
	}
	if cacheDir, err := os.UserCacheDir(); err == nil {
		addDir(filepath.Join(cacheDir, "mygo", "natives", lib.SHA256))
	}
	for _, dir := range dirs {
		p := filepath.Join(dir, lib.Name)
		if info, err := os.Stat(p); err == nil && !info.IsDir() {
			return p, true
		}
	}
	return "", false
}
