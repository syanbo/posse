// Package procmem 读取进程内存占用。
package procmem

import (
	"sync"
	"unsafe"

	"github.com/ebitengine/purego"
	"golang.org/x/sys/unix"
)

var (
	once   sync.Once
	rusage func(pid int32, flavor int32, buf unsafe.Pointer) int32
)

// rusage_info_v4（flavor 4）：16 字节 uuid 之后依次是 user_time、system_time、pkg_idle_wkups、
// interrupt_wkups、pageins、wired_size、resident_size、phys_footprint 等 uint64 字段
const (
	idxResident  = 2 + 6
	idxFootprint = 2 + 7
)

func read(pid, idx int) uint64 {
	once.Do(func() {
		h, err := purego.Dlopen("/usr/lib/libSystem.B.dylib", purego.RTLD_LAZY|purego.RTLD_GLOBAL)
		if err == nil {
			purego.RegisterLibFunc(&rusage, h, "proc_pid_rusage")
		}
	})
	if rusage == nil {
		return 0
	}
	var buf [64]uint64
	if rusage(int32(pid), 4, unsafe.Pointer(&buf[0])) != 0 {
		return 0
	}
	return buf[idx]
}

// Footprint 返回进程的 phys_footprint（字节），与活动监视器的"内存"一致；取不到时返回 0。
func Footprint(pid int) uint64 { return read(pid, idxFootprint) }

// Resident 返回进程的常驻内存（字节），与 ps 的 RSS 一致；取不到时返回 0。
func Resident(pid int) uint64 { return read(pid, idxResident) }

// Children 返回此刻所有进程的父子关系：父进程 pid → 子进程 pid。
func Children() map[int][]int {
	procs, err := unix.SysctlKinfoProcSlice("kern.proc.all")
	if err != nil {
		return nil
	}
	out := make(map[int][]int, len(procs))
	for _, p := range procs {
		pid, ppid := int(p.Proc.P_pid), int(p.Eproc.Ppid)
		if pid != ppid {
			out[ppid] = append(out[ppid], pid)
		}
	}
	return out
}
