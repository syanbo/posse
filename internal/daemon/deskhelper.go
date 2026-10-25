package daemon

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"strconv"
	"sync"
	"time"

	"posse/internal/desk"
)

// 采集子进程出错退出（ScreenCaptureKit 停了流、编码连着失败）或卡死时，常驻进程重新拉起它，手机这一路不断。
// 照 RustDesk：没出新帧是画面没动，不算故障；只在明确出错时重建。重建前等一会儿，连着失败越等越久；
// deskRestartWindow 内重建超过 deskRestartLimit 次就不再试，这次观看以 capture_failed 结束。
var (
	deskRestartLimit  = 3
	deskRestartWindow = time.Minute
	deskRestartDelay  = 500 * time.Millisecond
	// deskHelperQuiet 内既没心跳也没画面，当作子进程卡死。心跳每秒一次，和画面动不动无关
	deskHelperQuiet = 10 * time.Second
	// deskExitWait 是关掉控制管道后等子进程自己退出多久，再久就杀掉
	deskExitWait = 3 * time.Second
)

// deskHelper 管着一次观看里的采集子进程：把它的帧原样转给 Serve，子进程换了也是同一条帧流；
// 写控制管道的人经它写到当前的子进程。
type deskHelper struct {
	spawn func(display uint32) (deskProc, []byte, byte, error)
	// deskRestart*、deskHelperQuiet 在创建时取一份，观看过程中不变
	limit                int
	window, delay, quiet time.Duration

	out *io.PipeWriter

	done chan struct{} // run 返回时关闭

	mu       sync.Mutex
	proc     deskProc
	closing  bool
	display  uint32 // 正在看的屏，重建时接着看它
	restarts []time.Time
}

func newDeskHelper(spawn func(uint32) (deskProc, []byte, byte, error), proc deskProc, display uint32, out *io.PipeWriter) *deskHelper {
	return &deskHelper{
		spawn: spawn, proc: proc, display: display, out: out, done: make(chan struct{}),
		limit: deskRestartLimit, window: deskRestartWindow, delay: deskRestartDelay, quiet: deskHelperQuiet,
	}
}

// Write 写到当前子进程的控制管道，几处同时写时由外面套的 desk.LockWriter 排队。重建的空档里写到已经关掉的旧管道上，
// 丢掉就是：那时画面本来就停着，重建后 Serve 会把码率等补发过去。
func (h *deskHelper) Write(p []byte) (int, error) {
	h.mu.Lock()
	proc := h.proc
	h.mu.Unlock()
	return proc.Control().Write(p)
}

// run 一直转发帧到观看结束，子进程退出或卡死时重建。返回时帧流已经关掉。
func (h *deskHelper) run() {
	defer close(h.done)
	for {
		h.mu.Lock()
		proc := h.proc
		h.mu.Unlock()
		err := h.pump(proc)
		exitErr := stopDeskProc(proc)
		h.mu.Lock()
		closing := h.closing
		h.mu.Unlock()
		// 子进程正常退出（用户在菜单栏停了录屏这类）照旧结束这次观看，只在出错退出或卡死时重建
		if closing || (err == nil && exitErr == nil) {
			h.out.Close()
			return
		}
		if err == nil {
			err = exitErr
		}
		if err = h.restart(err); err != nil {
			h.out.CloseWithError(err)
			return
		}
	}
}

// pump 把子进程的帧转给 Serve，直到子进程的帧流结束、deskHelperQuiet 内什么都没来或 Serve 不再读。
func (h *deskHelper) pump(proc deskProc) error {
	frames := make(chan desk.Frame)
	readErr := make(chan error, 1)
	done := make(chan struct{})
	defer close(done)
	go func() {
		for {
			f, err := desk.ReadFrame(proc.Frames())
			if err != nil {
				readErr <- err
				return
			}
			select {
			case frames <- f:
			case <-done:
				return
			}
		}
	}()
	quiet := time.NewTimer(h.quiet)
	defer quiet.Stop()
	for {
		select {
		case f := <-frames:
			h.note(f)
			if f.Kind == desk.KindHeartbeat || f.Kind == desk.KindVideo {
				quiet.Reset(h.quiet)
			}
			if err := desk.WriteFrame(h.out, f); err != nil {
				h.mu.Lock()
				h.closing = true
				h.mu.Unlock()
				return nil
			}
		case err := <-readErr:
			if errors.Is(err, io.EOF) {
				return nil
			}
			return err
		case <-quiet.C:
			return fmt.Errorf("desk: helper sent nothing for %s", h.quiet)
		}
	}
}

// note 记下重建时要用的：正在看哪块屏。
func (h *deskHelper) note(f desk.Frame) {
	if f.Kind != desk.KindDisplays {
		return
	}
	var ds desk.Displays
	if json.Unmarshal(f.Payload, &ds) == nil && ds.Current != 0 {
		h.mu.Lock()
		h.display = ds.Current
		h.mu.Unlock()
	}
}

// restart 重新拉起子进程，接到帧流上。cause 是上一个为什么没了。
func (h *deskHelper) restart(cause error) error {
	for {
		h.mu.Lock()
		now := time.Now()
		kept := h.restarts[:0]
		for _, at := range h.restarts {
			if now.Sub(at) < h.window {
				kept = append(kept, at)
			}
		}
		h.restarts = kept
		if len(h.restarts) >= h.limit {
			h.mu.Unlock()
			log.Printf("%v; the helper was restarted %d times in %s, giving up", cause, h.limit, h.window)
			return cause
		}
		h.restarts = append(h.restarts, now)
		delay := h.delay << (len(h.restarts) - 1)
		display := h.display
		h.mu.Unlock()
		log.Printf("%v; restarting the helper in %s", cause, delay)
		time.Sleep(delay)
		if h.isClosing() {
			return io.EOF
		}
		proc, head, status, err := h.spawn(display)
		// 报了没授权的子进程随即退出，err 也不为空：先看状态
		switch {
		case status == desk.StatusNoScreen:
			return errors.New("desk: screen recording is no longer allowed")
		case err != nil:
			cause = err
			continue
		}
		h.mu.Lock()
		if h.closing {
			h.mu.Unlock()
			stopDeskProc(proc)
			return io.EOF
		}
		h.proc = proc
		h.mu.Unlock()
		restarted := desk.Frame{Kind: desk.KindStatus, CaptureUnixNano: time.Now().UnixNano(), Payload: []byte{desk.StatusRestarted}}
		if desk.WriteFrame(h.out, restarted) != nil {
			return io.EOF
		}
		if _, err := h.out.Write(head); err != nil {
			return io.EOF
		}
		return nil
	}
}

func (h *deskHelper) isClosing() bool {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.closing
}

// close 在观看结束时停掉子进程：关控制管道它就退出、松开按着的键。不再重建。
func (h *deskHelper) close() {
	h.mu.Lock()
	h.closing = true
	proc := h.proc
	h.mu.Unlock()
	proc.Control().Close()
}

// stopDeskProc 让子进程退出并等它，返回它的退出结果：关控制管道，再关帧管道（没人读了，子进程写帧时直接出错，
// 不会卡在写满的管道上），等不到就杀掉。
func stopDeskProc(proc deskProc) error {
	proc.Control().Close()
	proc.Frames().Close()
	exited := make(chan error, 1)
	go func() { exited <- proc.Wait() }()
	select {
	case err := <-exited:
		return err
	case <-time.After(deskExitWait):
		log.Printf("desk: helper did not exit, killing it")
		proc.Kill()
		return <-exited
	}
}

// deskSpawnArgs 是采集子进程的参数，重建时 display 换成正在看的屏。
func deskSpawnArgs(base []string, display uint32) []string {
	args := append([]string(nil), base...)
	if display != 0 {
		args = append(args, "--display", strconv.FormatUint(uint64(display), 10))
	}
	return args
}

// deskSpawnFirst 拉起子进程并读到第一帧配置。失败时子进程已经停掉。
func deskSpawnFirst(args []string) (deskProc, []byte, byte, error) {
	proc, err := deskSpawn(args)
	if err != nil {
		return nil, nil, 0, err
	}
	head, status, err := deskFirstFrames(proc.Frames())
	if err != nil || status == desk.StatusNoScreen {
		stopDeskProc(proc)
	}
	return proc, head, status, err
}
