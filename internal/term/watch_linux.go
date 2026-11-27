//go:build linux

package term

import (
	"os"
	"strconv"
	"strings"
	"sync"
	"time"

	"golang.org/x/sys/unix"
)

// procWatchLinux 用单个 epoll 实例统一监视所有交接过来的 pidfd，不随终端数占用 OS 线程。
var procWatchLinux struct {
	once sync.Once
	epfd int
	err  error
	mu   sync.Mutex
	fns  map[int]func(code int) // pidfd -> callback
}

// watchExit 在 Linux 上优先使用 pidfd_open + 共享 epoll 监视交接进程退出；
// 仅在旧内核不支持 pidfd_open（ENOSYS）时回退到 /proc/<pid>/stat（核对状态与启动时刻，防止僵尸或 PID 复用误判）。
func watchExit(pid int, fn func(code int)) {
	if pid <= 0 {
		go fn(-1)
		return
	}
	pfd, err := unix.PidfdOpen(pid, unix.PIDFD_NONBLOCK)
	if err == nil {
		w := &procWatchLinux
		w.once.Do(func() {
			if w.epfd, w.err = unix.EpollCreate1(unix.EPOLL_CLOEXEC); w.err == nil {
				w.fns = map[int]func(int){}
				go watchLoopLinux()
			}
		})
		if w.err != nil {
			_ = unix.Close(pfd)
			go fn(-1)
			return
		}
		w.mu.Lock()
		w.fns[pfd] = fn
		w.mu.Unlock()
		ev := unix.EpollEvent{Events: unix.EPOLLIN | unix.EPOLLONESHOT, Fd: int32(pfd)}
		if eerr := unix.EpollCtl(w.epfd, unix.EPOLL_CTL_ADD, pfd, &ev); eerr != nil {
			if f := takeWatchLinux(pfd); f != nil {
				_ = unix.Close(pfd)
				go f(-1)
			}
		}
		return
	}
	if err == unix.ENOSYS {
		watchExitProcStat(pid, fn)
		return
	}
	go fn(-1)
}

func takeWatchLinux(pfd int) func(int) {
	w := &procWatchLinux
	w.mu.Lock()
	defer w.mu.Unlock()
	fn := w.fns[pfd]
	delete(w.fns, pfd)
	return fn
}

func watchLoopLinux() {
	events := make([]unix.EpollEvent, 16)
	for {
		n, err := unix.EpollWait(procWatchLinux.epfd, events, -1)
		if err != nil {
			if err == unix.EINTR {
				continue
			}
			return
		}
		for _, ev := range events[:n] {
			pfd := int(ev.Fd)
			_ = unix.EpollCtl(procWatchLinux.epfd, unix.EPOLL_CTL_DEL, pfd, nil)
			fn := takeWatchLinux(pfd)
			_ = unix.Close(pfd)
			if fn != nil {
				go fn(-1)
			}
		}
	}
}

// watchExitProcStat 供不支持 pidfd_open 的旧 Linux 内核回退：记录初始启动时刻，
// 一旦进程消失、变成僵尸/死亡（Z/X）或 PID 被复用（starttime 变化）立即回报退出。
func watchExitProcStat(pid int, fn func(code int)) {
	startTime, alive := readProcStatStart(pid)
	if !alive {
		go fn(-1)
		return
	}
	go func() {
		tick := time.NewTicker(200 * time.Millisecond)
		defer tick.Stop()
		for range tick.C {
			cur, ok := readProcStatStart(pid)
			if !ok || (startTime != "" && cur != startTime) {
				fn(-1)
				return
			}
		}
	}()
}

// readProcStatStart 从 /proc/<pid>/stat 解析运行状态与启动时刻（第 22 字段）。
func readProcStatStart(pid int) (string, bool) {
	data, err := os.ReadFile("/proc/" + strconv.Itoa(pid) + "/stat")
	if err != nil {
		return "", false
	}
	s := string(data)
	rp := strings.LastIndexByte(s, ')')
	if rp < 0 || rp+2 >= len(s) {
		return "", false
	}
	fields := strings.Fields(s[rp+2:])
	// fields[0] 是 state（第 3 字段），fields[19] 是 starttime（第 22 字段）。
	if len(fields) < 20 {
		return "", false
	}
	if fields[0] == "Z" || fields[0] == "X" || fields[0] == "x" {
		return "", false
	}
	return fields[19], true
}
