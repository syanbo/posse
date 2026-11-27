//go:build darwin

package term

import (
	"sync"
	"syscall"

	"golang.org/x/sys/unix"
)

// procWatch 用一个 kqueue 盯所有接过来的程序：它们的父进程是旧常驻进程（之后是 launchd），等不了 wait。
var procWatch struct {
	once sync.Once
	kq   int
	err  error
	mu   sync.Mutex
	fns  map[int]func(code int)
}

// watchExit 在 pid 退出时调 fn，正常退出给退出码，被信号杀掉或已经不在了给 -1（和 exec.ExitError 一致）。
func watchExit(pid int, fn func(code int)) {
	w := &procWatch
	w.once.Do(func() {
		if w.kq, w.err = unix.Kqueue(); w.err == nil {
			w.fns = map[int]func(int){}
			go watchLoop()
		}
	})
	if w.err != nil || pid <= 0 {
		go fn(-1)
		return
	}
	w.mu.Lock()
	w.fns[pid] = fn
	w.mu.Unlock()
	var ev unix.Kevent_t
	unix.SetKevent(&ev, pid, unix.EVFILT_PROC, unix.EV_ADD|unix.EV_ONESHOT)
	ev.Fflags = unix.NOTE_EXIT | unix.NOTE_EXITSTATUS
	if _, err := unix.Kevent(w.kq, []unix.Kevent_t{ev}, nil, nil); err != nil {
		if f := takeWatch(pid); f != nil {
			go f(-1)
		}
	}
}

func takeWatch(pid int) func(int) {
	w := &procWatch
	w.mu.Lock()
	defer w.mu.Unlock()
	fn := w.fns[pid]
	delete(w.fns, pid)
	return fn
}

func watchLoop() {
	events := make([]unix.Kevent_t, 16)
	for {
		n, err := unix.Kevent(procWatch.kq, nil, events, nil)
		if err != nil {
			if err == unix.EINTR {
				continue
			}
			return
		}
		for _, ev := range events[:n] {
			if ev.Fflags&unix.NOTE_EXIT == 0 {
				continue
			}
			code := -1
			if ws := syscall.WaitStatus(ev.Data); ws.Exited() {
				code = ws.ExitStatus()
			}
			if fn := takeWatch(int(ev.Ident)); fn != nil {
				go fn(code)
			}
		}
	}
}
