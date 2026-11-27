//go:build darwin

package term

import "golang.org/x/sys/unix"

// fionread 是 macOS 的 FIONREAD（x/sys 没有导出）：对主端取的是从端还没读的输入字节数。
const fionread = 0x4004667f

func ptyInputQueued(fd uintptr) int {
	n, _ := unix.IoctlGetInt(int(fd), fionread)
	return n
}

// flushPTYInput 在主端 TIOCFLUSH FREAD 清的是从端还没读的输入队列。
func flushPTYInput(fd uintptr) {
	unix.IoctlSetPointerInt(int(fd), unix.TIOCFLUSH, 1)
}
