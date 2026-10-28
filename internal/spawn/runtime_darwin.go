package spawn

import (
	"fmt"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"unsafe"

	"github.com/ebitengine/purego"
	"golang.org/x/sys/unix"
)

// cloneTree 用 clonefile 克隆整个 .app（目录也能克隆，不占空间，保留属性和签名）；
// 不在同一个 APFS 卷上时退回 ditto。
func cloneTree(src, dst string) error {
	if err := unix.Clonefile(src, dst, unix.CLONE_NOFOLLOW); err == nil {
		return nil
	}
	if out, err := exec.Command("/usr/bin/ditto", src, dst).CombinedOutput(); err != nil {
		return fmt.Errorf("ditto: %w: %s", err, strings.TrimSpace(string(out)))
	}
	return nil
}

var (
	procOnce    sync.Once
	listAllPids func(buf unsafe.Pointer, size int32) int32
	procPidPath func(pid int32, buf unsafe.Pointer, size uint32) int32
	procMissing bool
)

func loadProc() {
	procOnce.Do(func() {
		lib, err := purego.Dlopen("/usr/lib/libSystem.B.dylib", purego.RTLD_LAZY|purego.RTLD_GLOBAL)
		if err != nil {
			procMissing = true
			return
		}
		for name, fn := range map[string]any{"proc_listallpids": &listAllPids, "proc_pidpath": &procPidPath} {
			sym, err := purego.Dlsym(lib, name)
			if err != nil {
				procMissing = true
				return
			}
			purego.RegisterFunc(fn, sym)
		}
	})
}

// runningIn 找出 dirs 里有进程正从中运行的目录。查不了进程时当作都有，宁可不删。
func runningIn(dirs []string) map[string]bool {
	busy := map[string]bool{}
	loadProc()
	if procMissing {
		for _, d := range dirs {
			busy[d] = true
		}
		return busy
	}
	pids := make([]int32, 8192)
	n := listAllPids(unsafe.Pointer(&pids[0]), int32(len(pids)*4))
	if n <= 0 {
		for _, d := range dirs {
			busy[d] = true
		}
		return busy
	}
	buf := make([]byte, 4096)
	for _, pid := range pids[:min(int(n), len(pids))] {
		l := procPidPath(pid, unsafe.Pointer(&buf[0]), uint32(len(buf)))
		if l <= 0 {
			continue
		}
		path := string(buf[:l])
		for _, d := range dirs {
			if strings.HasPrefix(path, d+string(filepath.Separator)) {
				busy[d] = true
			}
		}
	}
	return busy
}
