//go:build !darwin && !linux

package term

func ptyInputQueued(uintptr) int { return 0 }

func flushPTYInput(uintptr) {}
