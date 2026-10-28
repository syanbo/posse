//go:build !darwin

package procmem

// Footprint 在 macOS 之外不可用，返回 0。
func Footprint(pid int) uint64 { return 0 }

// Resident 在 macOS 之外不可用，返回 0。
func Resident(pid int) uint64 { return 0 }

// Children 在其他平台上不实现。
func Children() map[int][]int { return nil }

// Args 在其他平台上不实现。
func Args(pid int) []string { return nil }

// ExeArgs 在其他平台上不实现。
func ExeArgs(pid int) (string, []string) { return "", nil }

// Cwd 在其他平台上不实现。
func Cwd(pid int) string { return "" }
