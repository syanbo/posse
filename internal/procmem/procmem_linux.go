//go:build linux

package procmem

import (
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

// Footprint 返回进程在 Linux 上的 PSS（按共享页分摊的实际物理占用），读不到 smaps_rollup 时回退到 VmRSS。
func Footprint(pid int) uint64 {
	if pid <= 0 {
		return 0
	}
	base := "/proc/" + strconv.Itoa(pid)
	if data, err := os.ReadFile(base + "/smaps_rollup"); err == nil {
		if v := parseSmapsRollup(data); v > 0 {
			return v
		}
	}
	return Resident(pid)
}

// Resident 返回进程的常驻内存（VmRSS，字节）；取不到时返回 0。
func Resident(pid int) uint64 {
	if pid <= 0 {
		return 0
	}
	data, err := os.ReadFile("/proc/" + strconv.Itoa(pid) + "/status")
	if err != nil {
		return 0
	}
	return parseProcStatusRSS(data)
}

// Children 返回进程此刻的直接子进程 PID（读取 /proc/<pid>/task/*/children，不扫描全系统进程表）。
func Children(pid int) []int {
	if pid <= 0 {
		return nil
	}
	taskDir := "/proc/" + strconv.Itoa(pid) + "/task"
	entries, err := os.ReadDir(taskDir)
	if err != nil {
		return nil
	}
	seen := map[int]bool{}
	var out []int
	for _, e := range entries {
		data, err := os.ReadFile(filepath.Join(taskDir, e.Name(), "children"))
		if err != nil {
			continue
		}
		for _, child := range parseProcChildren(data) {
			if !seen[child] {
				seen[child] = true
				out = append(out, child)
			}
		}
	}
	return out
}

// Args 返回进程的启动参数（argv）；取不到时返回 nil。
func Args(pid int) []string {
	_, args := ExeArgs(pid)
	return args
}

// ExeArgs 返回进程的可执行文件路径和启动参数；取不到时返回空。
func ExeArgs(pid int) (string, []string) {
	if pid <= 0 {
		return "", nil
	}
	base := "/proc/" + strconv.Itoa(pid)
	exe, _ := os.Readlink(base + "/exe")
	exe = parseProcExeLink(exe)
	data, err := os.ReadFile(base + "/cmdline")
	if err != nil {
		return exe, nil
	}
	return exe, parseProcCmdline(data)
}

// Cwd 返回进程的当前工作目录；取不到时返回空。
func Cwd(pid int) string {
	if pid <= 0 {
		return ""
	}
	target, err := os.Readlink("/proc/" + strconv.Itoa(pid) + "/cwd")
	if err != nil {
		return ""
	}
	return target
}

// OpenFiles 返回进程此刻打开着的普通文件与目录路径（跳过 socket/pipe/anon_inode）。
func OpenFiles(pid int) []string {
	if pid <= 0 {
		return nil
	}
	fdDir := "/proc/" + strconv.Itoa(pid) + "/fd"
	entries, err := os.ReadDir(fdDir)
	if err != nil {
		return nil
	}
	var out []string
	for _, e := range entries {
		target, err := os.Readlink(filepath.Join(fdDir, e.Name()))
		if err != nil || !strings.HasPrefix(target, "/") {
			continue
		}
		out = append(out, target)
	}
	return out
}
