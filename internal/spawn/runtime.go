package spawn

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"time"
)

// 常驻进程不直接从 Posse.app 运行，而是从它的一份副本（APFS 克隆）运行。
// 终端里的进程对系统授权的负责进程是拉起它们的常驻进程，tccd 按那个常驻进程的程序路径核实身份：
// 常驻进程还活着时包被换掉（重新打包、自动更新），运行中的代码和路径上的文件对不上，访问桌面等目录直接 EPERM，
// 而且这个拒绝会一直留着，交接到新进程也恢复不了；路径上的文件被删掉同样 EPERM。
// 副本按构建区分目录、永不原地改写，只在它拉起的终端全部退出后才删，终端就一直认得出自己的负责进程。
//
// 目录结构（根目录是 ~/Library/Application Support/Posse/runtime）：
//
//	<包ID>/.lock                 创建、登记、回收副本时持有的文件锁
//	<包ID>/<版本>-<构建>/Posse.app 副本
//	<包ID>/<版本>-<构建>/refs/<home 的哈希>  这个 POSSE_HOME 还在用它（内容是 home 路径），不能放进 .app 里：会破坏签名
//
// 一个 home 不再用某份副本（不是自己的常驻进程、也没有它拉起的终端还活着）时去掉自己的登记，
// 所有 home 都去掉了、也没有进程从里面运行时删掉。

// refGrace 内刚登记的副本不去登记：调用方先准备副本、再拉起或交接常驻进程，中间那一下常驻进程还不知道它。
const refGrace = time.Minute

// RuntimeRoot 是副本的根目录。
func RuntimeRoot() string {
	dir, err := os.UserConfigDir()
	if err != nil {
		return ""
	}
	return filepath.Join(dir, "Posse", "runtime")
}

// Runtime 把 exe 所在的 .app 克隆一份（已有就复用），登记给 home，返回副本里的可执行文件。
// 不在 .app 里运行（go build 的 bin/posse）时原样返回 exe；已经是副本时只登记。
// 出错时返回原来的 exe 和错误，调用方照常从原包拉起，只是不受这层保护。
func Runtime(exe, home string) (string, error) {
	if resolved, err := filepath.EvalSymlinks(exe); err == nil {
		exe = resolved
	}
	root := RuntimeRoot()
	if root == "" || BundleOf(exe) == "" {
		return exe, nil
	}
	path, err := prepare(root, exe, home)
	if err != nil {
		return exe, err
	}
	return path, nil
}

// BundleOf 从可执行文件路径取它所在的 .app：…/Posse.app/Contents/MacOS/Posse，不在 .app 里时为空。
func BundleOf(exe string) string {
	macos := filepath.Dir(exe)
	contents := filepath.Dir(macos)
	app := filepath.Dir(contents)
	if filepath.Base(macos) != "MacOS" || filepath.Base(contents) != "Contents" || !strings.HasSuffix(app, ".app") {
		return ""
	}
	return app
}

// BundleID 是 exe 所在 .app 的包 ID（副本和原包相同），不在 .app 里或读不出时为空。
func BundleID(exe string) string {
	if resolved, err := filepath.EvalSymlinks(exe); err == nil {
		exe = resolved
	}
	app := BundleOf(exe)
	if app == "" {
		return ""
	}
	id, _, err := bundleInfo(app)
	if err != nil {
		return ""
	}
	return id
}

// RuntimeDir 是 exe 所在副本的目录（<根>/<包ID>/<版本>-<构建>），不是副本时为空。
func RuntimeDir(exe string) string {
	return runtimeDir(RuntimeRoot(), exe)
}

func runtimeDir(root, exe string) string {
	if root == "" {
		return ""
	}
	if resolved, err := filepath.EvalSymlinks(exe); err == nil {
		exe = resolved
	}
	if resolvedRoot, err := filepath.EvalSymlinks(root); err == nil {
		root = resolvedRoot
	}
	app := BundleOf(exe)
	rel, err := filepath.Rel(root, app)
	if app == "" || err != nil {
		return ""
	}
	// <包ID>/<版本>-<构建>/X.app
	parts := strings.Split(rel, string(filepath.Separator))
	if len(parts) != 3 || parts[0] == ".." || strings.HasPrefix(parts[1], ".") {
		return ""
	}
	return filepath.Join(root, parts[0], parts[1])
}

func prepare(root, exe, home string) (string, error) {
	if dir := runtimeDir(root, exe); dir != "" {
		unlock, err := lockDir(filepath.Dir(dir))
		if err != nil {
			return "", err
		}
		defer unlock()
		if _, err := os.Stat(exe); err != nil {
			return "", err
		}
		return exe, addRef(dir, home)
	}
	app := BundleOf(exe)
	id, version, err := bundleInfo(app)
	if err != nil {
		return "", err
	}
	st, err := os.Stat(exe)
	if err != nil {
		return "", err
	}
	base := filepath.Join(root, id)
	if err := os.MkdirAll(base, 0o700); err != nil {
		return "", err
	}
	unlock, err := lockDir(base)
	if err != nil {
		return "", err
	}
	defer unlock()
	dir := filepath.Join(base, version+"-"+buildTag(st))
	rel, _ := filepath.Rel(app, exe)
	copied := filepath.Join(dir, filepath.Base(app), rel)
	if _, err := os.Stat(copied); errors.Is(err, os.ErrNotExist) {
		// 先克隆到临时目录再改名：中途失败或被杀，留下的不会被当成完整的副本
		tmp := filepath.Join(base, fmt.Sprintf(".tmp-%d-%s", os.Getpid(), filepath.Base(dir)))
		os.RemoveAll(tmp)
		if err := os.MkdirAll(tmp, 0o700); err != nil {
			return "", err
		}
		if err := cloneTree(app, filepath.Join(tmp, filepath.Base(app))); err != nil {
			os.RemoveAll(tmp)
			return "", fmt.Errorf("copy %s: %w", app, err)
		}
		os.RemoveAll(dir)
		if err := os.Rename(tmp, dir); err != nil {
			os.RemoveAll(tmp)
			return "", err
		}
	} else if err != nil {
		return "", err
	}
	return copied, addRef(dir, home)
}

// buildTag 区分同一版本的不同构建：每次打包、安装都换掉整个 .app，可执行文件的 inode 和修改时间一定会变。
func buildTag(st os.FileInfo) string {
	var dev, ino uint64
	if sys, ok := st.Sys().(*syscall.Stat_t); ok {
		dev, ino = uint64(sys.Dev), sys.Ino
	}
	sum := sha256.Sum256(fmt.Appendf(nil, "%d:%d:%d:%d", dev, ino, st.ModTime().UnixNano(), st.Size()))
	return st.ModTime().UTC().Format("20060102T150405Z") + "-" + hex.EncodeToString(sum[:4])
}

// bundleInfo 从 Info.plist（XML）读包 ID 和版本。
func bundleInfo(app string) (id, version string, err error) {
	b, err := os.ReadFile(filepath.Join(app, "Contents", "Info.plist"))
	if err != nil {
		return "", "", err
	}
	vals := map[string]string{}
	dec := xml.NewDecoder(bytes.NewReader(b))
	var key string
	for {
		tok, err := dec.Token()
		if err == io.EOF {
			break
		}
		if err != nil {
			return "", "", fmt.Errorf("Info.plist: %w", err)
		}
		se, ok := tok.(xml.StartElement)
		if !ok {
			continue
		}
		var text string
		switch se.Name.Local {
		case "key", "string":
			if err := dec.DecodeElement(&text, &se); err != nil {
				return "", "", fmt.Errorf("Info.plist: %w", err)
			}
		}
		if se.Name.Local == "key" {
			key = text
		} else if se.Name.Local == "string" && key != "" {
			vals[key], key = text, ""
		} else {
			key = ""
		}
	}
	id, version = vals["CFBundleIdentifier"], vals["CFBundleShortVersionString"]
	if id == "" || id == "." || id == ".." || strings.Contains(id, "/") {
		return "", "", fmt.Errorf("Info.plist: bad CFBundleIdentifier %q", id)
	}
	if version == "" || strings.Contains(version, "/") {
		version = "0"
	}
	return id, version, nil
}

func refName(home string) string {
	sum := sha256.Sum256([]byte(filepath.Clean(home)))
	return hex.EncodeToString(sum[:8])
}

// addRef 登记 home 在用 dir，重写一遍好更新时间（refGrace 从这时算）。
func addRef(dir, home string) error {
	refs := filepath.Join(dir, "refs")
	if err := os.MkdirAll(refs, 0o700); err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(refs, refName(home)), []byte(filepath.Clean(home)+"\n"), 0o600)
}

// Retain 登记 home 在用 exe 所在的副本（常驻进程启动时调用）；exe 不是副本时什么也不做。
func Retain(exe, home string) error {
	dir := RuntimeDir(exe)
	if dir == "" {
		return nil
	}
	unlock, err := lockDir(filepath.Dir(dir))
	if err != nil {
		return err
	}
	defer unlock()
	return addRef(dir, home)
}

// Collect 回收 own（常驻进程自己的副本目录，RuntimeDir 的结果）所在包 ID 下的副本：
// home 不在 keep 里的副本去掉 home 的登记，没有任何登记、也没有进程从里面运行的删掉。
// own 为空（不在副本里运行）时不做事。返回删掉的目录。
func Collect(own, home string, keep []string) ([]string, error) {
	if own == "" {
		return nil, nil
	}
	return collect(filepath.Dir(own), home, append(keep, own), runningIn, time.Now())
}

func collect(base, home string, keep []string, running func(dirs []string) map[string]bool, now time.Time) ([]string, error) {
	unlock, err := lockDir(base)
	if err != nil {
		return nil, err
	}
	defer unlock()
	entries, err := os.ReadDir(base)
	if err != nil {
		return nil, err
	}
	kept := map[string]bool{}
	for _, k := range keep {
		if k != "" {
			kept[filepath.Clean(k)] = true
		}
	}
	var idle []string
	for _, e := range entries {
		name := e.Name()
		path := filepath.Join(base, name)
		// 持锁时不会有人正在克隆或删除，留下的是中途被杀的
		if strings.HasPrefix(name, ".tmp-") || strings.HasPrefix(name, ".trash-") {
			os.RemoveAll(path)
			continue
		}
		if !e.IsDir() || strings.HasPrefix(name, ".") {
			continue
		}
		if kept[path] {
			// 还要用的补上登记：登记丢了（被手动删、或在别处被去掉）时，别的 home 回收会把它当成没人用
			addRef(path, home)
			continue
		}
		ref := filepath.Join(path, "refs", refName(home))
		if fi, err := os.Stat(ref); err == nil && now.Sub(fi.ModTime()) >= refGrace {
			os.Remove(ref)
		}
		refs, err := os.ReadDir(filepath.Join(path, "refs"))
		if err != nil && !errors.Is(err, os.ErrNotExist) || len(refs) > 0 {
			continue
		}
		idle = append(idle, path)
	}
	if len(idle) == 0 {
		return nil, nil
	}
	busy := running(idle)
	var removed []string
	for _, path := range idle {
		if busy[path] {
			continue
		}
		// 先改名再删：删到一半失败，剩下的也不会被当成完整的副本复用
		trash := filepath.Join(base, fmt.Sprintf(".trash-%d-%s", os.Getpid(), filepath.Base(path)))
		if err := os.Rename(path, trash); err != nil {
			continue
		}
		os.RemoveAll(trash)
		removed = append(removed, path)
	}
	return removed, nil
}

func lockDir(dir string) (func(), error) {
	f, err := os.OpenFile(filepath.Join(dir, ".lock"), os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, err
	}
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX); err != nil {
		f.Close()
		return nil, err
	}
	return func() { f.Close() }, nil
}
