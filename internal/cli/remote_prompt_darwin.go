package cli

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"
	"sync"
	"unicode"

	"golang.org/x/sys/unix"
	"posse/internal/remote"
)

var remotePromptMu sync.Mutex

func remoteTerminalPrompt(ctx context.Context, p remote.Prompt) (string, error) {
	remotePromptMu.Lock()
	defer remotePromptMu.Unlock()
	if err := ctx.Err(); err != nil {
		return "", err
	}
	tty, err := os.OpenFile("/dev/tty", os.O_RDWR|unix.O_NONBLOCK, 0)
	if err != nil {
		return "", errors.New("SSH authentication needs an interactive terminal")
	}
	defer tty.Close()
	text := strings.Map(func(r rune) rune {
		if unicode.IsControl(r) && r != '\n' && r != '\t' {
			return -1
		}
		return r
	}, p.Text)
	if p.Notify {
		_, err := fmt.Fprintln(tty, text)
		return "", err
	}
	fd := int(tty.Fd())
	original, err := unix.IoctlGetTermios(fd, unix.TIOCGETA)
	if err != nil {
		return "", err
	}
	if !p.Confirm {
		mode := *original
		mode.Lflag &^= unix.ECHO
		if err := unix.IoctlSetTermios(fd, unix.TIOCSETA, &mode); err != nil {
			return "", err
		}
		defer unix.IoctlSetTermios(fd, unix.TIOCSETA, original)
		defer fmt.Fprintln(tty)
	}
	if _, err := fmt.Fprint(tty, text+" "); err != nil {
		return "", err
	}
	var line []byte
	for len(line) < 16<<10 {
		if err := ctx.Err(); err != nil {
			return "", err
		}
		fds := []unix.PollFd{{Fd: int32(fd), Events: unix.POLLIN}}
		if _, err := unix.Poll(fds, 100); err != nil && err != unix.EINTR {
			return "", err
		}
		if fds[0].Revents&(unix.POLLERR|unix.POLLHUP|unix.POLLNVAL) != 0 {
			return "", errors.New("SSH authentication terminal closed")
		}
		if fds[0].Revents&unix.POLLIN == 0 {
			continue
		}
		var buf [1]byte
		n, err := unix.Read(fd, buf[:])
		if err == unix.EAGAIN || err == unix.EINTR {
			continue
		}
		if err != nil {
			return "", err
		}
		if n == 0 {
			return "", errors.New("SSH authentication cancelled")
		}
		if buf[0] == '\n' {
			return strings.TrimSuffix(string(line), "\r"), nil
		}
		line = append(line, buf[0])
	}
	return "", errors.New("SSH authentication response exceeds limit")
}
