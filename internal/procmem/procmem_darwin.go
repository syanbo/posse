// Package procmem 读取进程内存占用。
package procmem

import (
	"sync"
	"unsafe"

	"github.com/ebitengine/purego"
	"golang.org/x/sys/unix"
)

var (
	once    sync.Once
	rusage  func(pid int32, flavor int32, buf unsafe.Pointer) int32
	pidinfo func(pid, flavor int32, arg uint64, buf unsafe.Pointer, size int32) int32
	// listChildren 是 proc_listchildpids，返回写进 buf 的 pid 个数
	listChildren func(pid int32, buf unsafe.Pointer, size int32) int32
)

// rusage_info_v4（flavor 4）：16 字节 uuid 之后依次是 user_time、system_time、pkg_idle_wkups、
// interrupt_wkups、pageins、wired_size、resident_size、phys_footprint 等 uint64 字段
const (
	idxResident  = 2 + 6
	idxFootprint = 2 + 7
)

func load() {
	once.Do(func() {
		h, err := purego.Dlopen("/usr/lib/libSystem.B.dylib", purego.RTLD_LAZY|purego.RTLD_GLOBAL)
		if err == nil {
			purego.RegisterLibFunc(&rusage, h, "proc_pid_rusage")
			purego.RegisterLibFunc(&pidinfo, h, "proc_pidinfo")
			purego.RegisterLibFunc(&listChildren, h, "proc_listchildpids")
		}
	})
}

func read(pid, idx int) uint64 {
	load()
	if rusage == nil {
		return 0
	}
	var buf [64]uint64
	if rusage(int32(pid), 4, unsafe.Pointer(&buf[0])) != 0 {
		return 0
	}
	return buf[idx]
}

// proc_vnodepathinfo（PROC_PIDVNODEPATHINFO）：当前目录在前，每项是 152 字节的 vnode_info 加 MAXPATHLEN 的路径
const (
	pidVnodePathInfo = 9
	vnodeInfoSize    = 152
	maxPathLen       = 1024
)

// Cwd 返回进程的当前目录；取不到时（进程不在、没有权限）返回空。
func Cwd(pid int) string {
	load()
	if pidinfo == nil || pid <= 0 {
		return ""
	}
	var buf [2 * (vnodeInfoSize + maxPathLen)]byte
	if pidinfo(int32(pid), pidVnodePathInfo, 0, unsafe.Pointer(&buf[0]), int32(len(buf))) <= 0 {
		return ""
	}
	path := buf[vnodeInfoSize : vnodeInfoSize+maxPathLen]
	for i, c := range path {
		if c == 0 {
			return string(path[:i])
		}
	}
	return ""
}

// Footprint 返回进程的 phys_footprint（字节），与活动监视器的"内存"一致；取不到时返回 0。
func Footprint(pid int) uint64 { return read(pid, idxFootprint) }

// Resident 返回进程的常驻内存（字节），与 ps 的 RSS 一致；取不到时返回 0。
func Resident(pid int) uint64 { return read(pid, idxResident) }

// Children 返回进程此刻的直接子进程。只问这一个进程，不读全系统的进程表：
// 窗口每几秒统计一次终端的进程树，读整张表每次要分配几百 KB。
func Children(pid int) []int {
	load()
	if listChildren == nil || pid <= 0 {
		return nil
	}
	// 返回的是写进去的个数，缓冲满了就可能没列全，加大再取
	buf := make([]int32, 16)
	for {
		n := int(listChildren(int32(pid), unsafe.Pointer(&buf[0]), int32(len(buf)*4)))
		if n <= 0 {
			return nil
		}
		if n < len(buf) {
			out := make([]int, n)
			for i, p := range buf[:n] {
				out[i] = int(p)
			}
			return out
		}
		buf = make([]int32, len(buf)*4)
	}
}

// Args 返回进程的启动参数（argv）；取不到时返回 nil。
func Args(pid int) []string {
	_, args := ExeArgs(pid)
	return args
}

// ExeArgs 返回进程执行的文件路径（exec 时给的路径，符号链接不解开）和启动参数；取不到时都是空的。
// 经符号链接启动的程序 argv[0] 只是链接名，要靠路径认出是谁（grok 另装了一个叫 agent 的入口）。
func ExeArgs(pid int) (string, []string) {
	b, err := unix.SysctlRaw("kern.procargs2", pid)
	if err != nil || len(b) < 4 {
		return "", nil
	}
	argc := int(int32(uint32(b[0]) | uint32(b[1])<<8 | uint32(b[2])<<16 | uint32(b[3])<<24))
	b = b[4:]
	// 先是可执行文件的路径，再是对齐用的 NUL，然后才是 argv
	i := 0
	for i < len(b) && b[i] != 0 {
		i++
	}
	exe := string(b[:i])
	for i < len(b) && b[i] == 0 {
		i++
	}
	var args []string
	for len(args) < argc && i < len(b) {
		j := i
		for j < len(b) && b[j] != 0 {
			j++
		}
		args = append(args, string(b[i:j]))
		i = j + 1
	}
	return exe, args
}
