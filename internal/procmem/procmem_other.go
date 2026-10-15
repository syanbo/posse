//go:build !darwin

package procmem

// Footprint 在 macOS 之外不可用，返回 0。
func Footprint(pid int) uint64 { return 0 }
