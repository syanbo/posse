//go:build linux

package spawn

import (
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
)

func cloneTree(src, dst string) error { return errors.New("clone not supported") }

// runningIn 在 Linux 上扫描同 UID 进程的 /proc/<pid>/exe，找出仍有进程从中运行的受管无头版本目录；
// 若同 UID 进程因权限限制（EACCES/EPERM）无法确认 exe，保守延后回收避免误删正在运行的版本。
func runningIn(dirs []string) map[string]bool {
	busy := map[string]bool{}
	if len(dirs) == 0 {
		return busy
	}
	markAllBusy := func() map[string]bool {
		for _, d := range dirs {
			busy[d] = true
		}
		return busy
	}
	prefixes := make([]string, len(dirs))
	for i, d := range dirs {
		d = canonicalPath(d)
		prefixes[i] = d + string(filepath.Separator)
	}
	entries, err := os.ReadDir("/proc")
	if err != nil {
		return markAllBusy()
	}
	myUID := uint32(os.Getuid())
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		if _, err := strconv.Atoi(e.Name()); err != nil {
			continue
		}
		procDir := filepath.Join("/proc", e.Name())
		info, serr := os.Stat(procDir)
		if serr != nil {
			if errors.Is(serr, os.ErrNotExist) || errors.Is(serr, syscall.ESRCH) {
				continue
			}
			return markAllBusy()
		}
		if st, ok := info.Sys().(*syscall.Stat_t); ok && st.Uid != myUID {
			continue
		}
		target, rerr := os.Readlink(filepath.Join(procDir, "exe"))
		if rerr != nil {
			if errors.Is(rerr, os.ErrNotExist) || errors.Is(rerr, syscall.ESRCH) {
				continue
			}
			// 同 UID 进程无法读取 exe（如 EACCES/EPERM）：保守标记全部候选忙碌，避免误删。
			return markAllBusy()
		}
		target = strings.TrimSuffix(target, " (deleted)")
		for i, pref := range prefixes {
			if strings.HasPrefix(target, pref) {
				busy[dirs[i]] = true
			}
		}
	}
	return busy
}
