// Package procmem 读取进程内存占用。
package procmem

import (
	"sync"
	"unsafe"

	"github.com/ebitengine/purego"
)

var (
	once   sync.Once
	rusage func(pid int32, flavor int32, buf unsafe.Pointer) int32
)

// Footprint 返回进程的 phys_footprint（字节），与活动监视器的"内存"一致；取不到时返回 0。
func Footprint(pid int) uint64 {
	once.Do(func() {
		h, err := purego.Dlopen("/usr/lib/libSystem.B.dylib", purego.RTLD_LAZY|purego.RTLD_GLOBAL)
		if err == nil {
			purego.RegisterLibFunc(&rusage, h, "proc_pid_rusage")
		}
	})
	if rusage == nil {
		return 0
	}
	// rusage_info_v4（flavor 4）：16 字节 uuid 之后第 8 个 uint64 字段是 ri_phys_footprint
	var buf [64]uint64
	if rusage(int32(pid), 4, unsafe.Pointer(&buf[0])) != 0 {
		return 0
	}
	return buf[2+7]
}
