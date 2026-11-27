//go:build linux

package term

import "golang.org/x/sys/unix"

// openPTYSlavePeer 通过 Linux TIOCGPTPEER 直接从主端获取对应从端描述符，
// 不经 /dev/pts 路径解析；旧内核不支持时返回错误，调用方安全降级、绝不误清主端输出队列。
func openPTYSlavePeer(masterFD uintptr) (int, error) {
	flags := uintptr(unix.O_RDWR | unix.O_NOCTTY | unix.O_CLOEXEC | unix.O_NONBLOCK)
	r1, _, errno := unix.Syscall(unix.SYS_IOCTL, masterFD, uintptr(unix.TIOCGPTPEER), flags)
	if errno != 0 {
		return -1, errno
	}
	return int(r1), nil
}

// ptyInputQueued 返回从端尚未被程序读取的输入字节数；取不到安全从端时返回 0。
func ptyInputQueued(fd uintptr) int {
	slaveFD, err := openPTYSlavePeer(fd)
	if err != nil {
		return 0
	}
	defer unix.Close(slaveFD)
	n, _ := unix.IoctlGetInt(slaveFD, unix.TIOCINQ)
	return n
}

// flushPTYInput 仅清掉从端尚未被程序读取的输入队列（TCIFLUSH），绝不触碰主端输出队列。
func flushPTYInput(fd uintptr) {
	slaveFD, err := openPTYSlavePeer(fd)
	if err != nil {
		return
	}
	defer unix.Close(slaveFD)
	_ = unix.IoctlSetInt(slaveFD, unix.TCFLSH, unix.TCIFLUSH)
}
