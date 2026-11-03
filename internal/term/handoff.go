package term

import (
	"errors"
	"os"
	"sync"
	"syscall"
	"time"

	"golang.org/x/sys/unix"
)

// State 是交接给新常驻进程的一个终端（方案见 docs/plans/terminal-host.md）：
// 新进程按同尺寸喂 Snapshot、标题、Tail 重建内核，接着读同一个 PTY。
type State struct {
	ID          string    `json:"id"`
	WorkspaceID string    `json:"workspaceId"`
	Dir         string    `json:"dir"`
	Command     []string  `json:"command"`
	CreatedAt   time.Time `json:"createdAt"`
	Pid         int       `json:"pid"`
	Cols        int       `json:"cols"`
	Rows        int       `json:"rows"`
	// DeskCols、DeskRows 是手机看着时界面那边的尺寸：手机连接随旧进程断开，新进程换回它
	DeskCols   int        `json:"deskCols,omitempty"`
	DeskRows   int        `json:"deskRows,omitempty"`
	Snapshot   []byte     `json:"snapshot"`
	Title      string     `json:"title,omitempty"`
	Tail       []byte     `json:"tail,omitempty"`
	Agent      AgentState `json:"agent"`
	LastOutput time.Time  `json:"lastOutput"`
	LastInput  time.Time  `json:"lastInput"`
	// Ended 表示程序已经结束，没有 PTY 交过去，只留着屏幕给界面看
	Ended    bool `json:"ended,omitempty"`
	ExitCode int  `json:"exitCode,omitempty"`
	// Colors 是窗口报来的配色；旧进程不带时新进程按系统外观选
	Colors *Colors `json:"colors,omitempty"`
}

// 未写完的序列超过这么长就不再带着（大段的图片、剪贴板 OSC），交接时丢掉这一段
const maxTail = 1 << 20

type pauseState struct {
	parked chan struct{} // 读协程停下后关闭
	resume chan struct{}
}

// Pause 停下读 PTY（输出由内核缓冲替程序攒着），返回交接用的状态和一份 PTY 描述符。
// 交接失败时调 Resume 接着读；成功时旧进程直接退出，不关 PTY。
func (t *Terminal) Pause() (State, *os.File, error) {
	t.pauseMu.Lock()
	if t.pause != nil {
		t.pauseMu.Unlock()
		return State{}, nil, errors.New("terminal already paused")
	}
	p := &pauseState{parked: make(chan struct{}), resume: make(chan struct{})}
	t.pause = p
	t.pauseMu.Unlock()

	ended := false
	if err := t.pty.SetReadDeadline(time.Now()); err != nil {
		select {
		case <-t.readDone:
			ended = true
		default:
			t.Resume()
			return State{}, nil, err
		}
	} else {
		select {
		case <-p.parked:
		case <-t.readDone:
			ended = true
		case <-time.After(2 * time.Second):
			t.Resume()
			return State{}, nil, errors.New("terminal reader did not stop")
		}
	}
	// 读到结尾后程序可能还没被收走，等一下退出码
	if ended {
		select {
		case <-t.exited:
		case <-time.After(time.Second):
		}
	}

	cols, rows := t.Size()
	t.zmu.Lock()
	var desk [2]int
	if len(t.holds) > 0 {
		desk = t.desk
	}
	t.zmu.Unlock()
	s := State{
		DeskCols: desk[0], DeskRows: desk[1],
		ID: t.ID, WorkspaceID: t.WorkspaceID, Dir: t.Dir, Command: t.Command, CreatedAt: t.CreatedAt,
		Pid: t.Pid(), Cols: cols, Rows: rows, Title: t.Title(), Agent: t.Agent(),
		LastOutput: t.LastOutput(), LastInput: t.LastInput(),
		Ended: ended || !t.Running(), ExitCode: t.ExitCode(),
	}
	colors := t.Colors()
	s.Colors = &colors
	t.smu.Lock()
	if t.emu != nil {
		s.Snapshot = t.emu.Snapshot()
	}
	s.Tail = append([]byte(nil), t.tail...)
	t.smu.Unlock()
	if s.Ended {
		return s, nil, nil
	}
	f, err := dupFile(t.pty)
	if err != nil {
		t.Resume()
		return State{}, nil, err
	}
	return s, f, nil
}

// Resume 在交接失败后接着读 PTY；接过来的终端在新进程回话成功后由它开始读。
func (t *Terminal) Resume() {
	t.pauseMu.Lock()
	p, start := t.pause, t.start
	t.pause, t.start = nil, nil
	t.pauseMu.Unlock()
	if p != nil {
		close(p.resume)
	}
	if start != nil {
		start()
	}
}

// park 在读协程因 Pause 设的期限返回时调用，等到 Resume 再接着读。
func (t *Terminal) park() {
	t.pauseMu.Lock()
	p := t.pause
	t.pauseMu.Unlock()
	if p != nil {
		close(p.parked)
		<-p.resume
	}
	t.pty.SetReadDeadline(time.Time{})
}

// Adopt 接过旧常驻进程交来的终端：f 是 PTY 主端（Ended 时为 nil），程序不是自己的子进程，退出靠 kqueue 盯 pid。
// 先不读 PTY，等调用方确认交接成功后 Resume。
func Adopt(o Options, s State, f *os.File) (*Terminal, error) {
	if err := loadLib(); err != nil {
		return nil, err
	}
	o.ID, o.WorkspaceID, o.Dir, o.Command = s.ID, s.WorkspaceID, s.Dir, s.Command
	t := newTerminal(o)
	t.CreatedAt = s.CreatedAt
	t.pid = s.Pid
	t.storeSize(s.Cols, s.Rows)
	if s.Colors != nil {
		c := *s.Colors
		t.colors.Store(&c)
	}
	emu, err := t.newEmu(s.Cols, s.Rows, o.Scrollback)
	if err != nil {
		return nil, err
	}
	// 重建时内核不应答：快照里没有查询，尾巴里的查询旧进程还没喂给内核，也就没回过，这里也不回，免得重复
	t.answer.Store(false)
	emu.Write(s.Snapshot)
	if s.Title != "" {
		emu.Write([]byte("\x1b]2;" + s.Title + "\x07"))
	}
	emu.Write(s.Tail)
	t.answer.Store(true)
	t.emu = emu
	t.titleDirty = false
	t.title = s.Title
	t.agent = s.Agent
	t.tail = append([]byte(nil), s.Tail...)
	t.lastOutput.Store(s.LastOutput.UnixNano())
	if !s.LastInput.IsZero() {
		t.lastInput.Store(s.LastInput.UnixNano())
	}

	if s.Ended || f == nil {
		t.ended, t.ptyClosed = true, true
		close(t.readDone)
		t.exitCode.Store(int64(s.ExitCode))
		close(t.exited)
		t.idle = time.AfterFunc(compressAfter, t.compress)
		return t, nil
	}
	if t.pty, err = pollable(f); err != nil {
		emu.Free()
		return nil, err
	}
	t.idle = time.AfterFunc(compressAfter, t.compress)
	watchExit(s.Pid, func(code int) { t.finish(code, o.OnExit) })
	t.start = func() {
		t.run()
		// 光标保存位置、调色板这些快照带不过去：改一下尺寸再改回来，让程序整屏重画。
		// 手机看着时换回界面的尺寸，本身就会重画
		go func() {
			time.Sleep(100 * time.Millisecond)
			if s.DeskCols > 0 && s.DeskRows > 0 && (s.DeskCols != s.Cols || s.DeskRows != s.Rows) {
				t.Resize(s.DeskCols, s.DeskRows)
				return
			}
			if s.Rows > 1 {
				t.setPTYSize(s.Cols, s.Rows-1)
				time.Sleep(50 * time.Millisecond)
			}
			t.setPTYSize(s.Cols, s.Rows)
		}()
	}
	return t, nil
}

// safeCut 返回 b 里最后一个"解析器回到起点"的位置，之后的字节是没写完的转义序列或半个 UTF-8 字符。
func safeCut(b []byte) int {
	safe, i := 0, 0
	for i < len(b) {
		c := b[i]
		switch {
		case c == 0x1b:
			j := i + 1
			if j >= len(b) {
				return safe
			}
			switch b[j] {
			case '[':
				j++
				for j < len(b) && !(b[j] >= 0x40 && b[j] <= 0x7e) {
					j++
				}
				if j >= len(b) {
					return safe
				}
				j++
			case ']', 'P', 'X', '^', '_':
				j++
				for {
					if j >= len(b) {
						return safe
					}
					if b[j] == 0x07 && b[i+1] == ']' {
						j++
						break
					}
					if b[j] == 0x1b && j+1 < len(b) && b[j+1] == '\\' {
						j += 2
						break
					}
					if b[j] == 0x1b && j+1 >= len(b) {
						return safe
					}
					j++
				}
			default:
				for j < len(b) && b[j] >= 0x20 && b[j] <= 0x2f {
					j++
				}
				if j >= len(b) {
					return safe
				}
				j++
			}
			i = j
		case c >= 0xc0:
			n := 2
			if c >= 0xf0 {
				n = 4
			} else if c >= 0xe0 {
				n = 3
			}
			if i+n > len(b) {
				return safe
			}
			i += n
		default:
			i++
		}
		safe = i
	}
	return safe
}

// carry 记下读到的输出末尾没写完的那截（tail 是上次剩下的），在持有 smu 时调用。
func carry(tail, p []byte) []byte {
	if len(tail) == 0 {
		c := safeCut(p)
		if c == len(p) {
			// 不留着底层数组：写过一段长 OSC 后它可能有 1MB
			return nil
		}
		if len(p)-c > maxTail {
			return nil
		}
		return append(tail, p[c:]...)
	}
	all := append(tail, p...)
	c := safeCut(all)
	if c == len(all) || len(all)-c > maxTail {
		return nil
	}
	return all[:copy(all, all[c:])]
}

// dupFD 复制一份描述符，不经过 Fd()（它会把 PTY 改成阻塞模式）。
func dupFD(f *os.File) (int, error) {
	rc, err := f.SyscallConn()
	if err != nil {
		return -1, err
	}
	nfd, derr := -1, error(nil)
	if err := rc.Control(func(fd uintptr) { nfd, derr = unix.FcntlInt(fd, unix.F_DUPFD_CLOEXEC, 0) }); err != nil {
		return -1, err
	}
	return nfd, derr
}

// dupFile 复制一份 PTY 描述符交给别的进程。
func dupFile(f *os.File) (*os.File, error) {
	fd, err := dupFD(f)
	if err != nil {
		return nil, err
	}
	return os.NewFile(uintptr(fd), f.Name()), nil
}

// pollable 把 PTY 换成非阻塞、由 Go 的轮询器管理的描述符，读才能设期限（交接时停下读协程）。
// os.NewFile 看到非阻塞的描述符才交给轮询器。原来的 f 会被关掉。
func pollable(f *os.File) (*os.File, error) {
	fd, err := dupFD(f)
	name := f.Name()
	f.Close()
	if err != nil {
		return nil, err
	}
	if err := unix.SetNonblock(fd, true); err != nil {
		unix.Close(fd)
		return nil, err
	}
	return os.NewFile(uintptr(fd), name), nil
}

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
