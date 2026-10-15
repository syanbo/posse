// Package term 托管 PTY 进程，并用 libghostty-vt 在常驻进程里维护每个终端的屏幕状态，
// 供 CLI 读取文本、界面接入时取快照。
package term

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"time"

	"github.com/creack/pty"
	"github.com/egoist/mygo/plugins/terminal"

	"hive/internal/vt"
)

const DefaultScrollback = 4 << 20

// 终端空闲这么久之后压缩回滚区
const compressAfter = 5 * time.Second

type Options struct {
	ID          string
	WorkspaceID string
	Dir         string
	Command     []string
	Env         []string
	Cols, Rows  int
	Scrollback  int
	OnExit      func(t *Terminal, code int)
	OnTitle     func(t *Terminal, title string)
}

// AgentState 来自 agent 的 hooks 上报。
type AgentState struct {
	Status         string    `json:"status"` // working | waiting | idle | exited
	Event          string    `json:"event,omitempty"`
	SessionID      string    `json:"sessionId,omitempty"`
	TranscriptPath string    `json:"transcriptPath,omitempty"`
	Message        string    `json:"message,omitempty"`
	At             time.Time `json:"at"`
}

var libOnce sync.Once
var libErr error

// loadLib 加载 MyGo 终端插件所用的同一个 libghostty-vt（首次运行时由插件下载）。
func loadLib() error {
	libOnce.Do(func() {
		path, err := terminal.LibraryPath()
		if err != nil {
			libErr = err
			return
		}
		libErr = vt.Load(path)
	})
	return libErr
}

type Terminal struct {
	ID          string
	WorkspaceID string
	Command     []string
	Dir         string
	CreatedAt   time.Time

	cmd *exec.Cmd
	pty *os.File
	// replies 把内核对终端查询的应答交给写入协程：应答产生于持锁的 Write 期间，不能在那里阻塞
	replies chan []byte

	lastOutput atomic.Int64
	lastInput  atomic.Int64
	// answer 为 true 时由常驻进程里的内核应答程序的终端查询；
	// 界面接入后改由界面应答，避免程序收到两份回复。
	answer atomic.Bool

	exited   chan struct{}
	exitCode atomic.Int64

	// smu 保护 emu（libghostty-vt 非线程安全），并串行化"喂给内核 + 广播给接入者"，
	// 接入时的快照与之后的输出流因此恰好衔接
	smu        sync.Mutex
	emu        *vt.Terminal
	subs       map[*Subscriber]struct{}
	ended      bool // PTY 已读到结尾
	titleDirty bool
	idle       *time.Timer

	mu      sync.Mutex
	changed chan struct{} // 有新输出或状态变化时关闭并替换
	title   string
	agent   AgentState
	closing bool
	onTitle func(t *Terminal, title string)
}

// Subscriber 是一个接入的界面，收到快照之后的全部输出。
// C 被关闭表示程序已结束，或接入者跟不上被断开。
type Subscriber struct {
	C chan []byte
}

func Start(o Options) (*Terminal, error) {
	if len(o.Command) == 0 {
		return nil, errors.New("empty command")
	}
	if err := loadLib(); err != nil {
		return nil, fmt.Errorf("libghostty-vt: %w", err)
	}
	if o.Cols <= 0 || o.Rows <= 0 {
		o.Cols, o.Rows = 120, 40
	}
	if o.Scrollback == 0 {
		o.Scrollback = DefaultScrollback
	}
	t := &Terminal{
		ID:          o.ID,
		WorkspaceID: o.WorkspaceID,
		Command:     o.Command,
		Dir:         o.Dir,
		CreatedAt:   time.Now(),
		replies:     make(chan []byte, 256),
		exited:      make(chan struct{}),
		changed:     make(chan struct{}),
		subs:        map[*Subscriber]struct{}{},
		onTitle:     o.OnTitle,
	}
	t.answer.Store(true)
	t.lastOutput.Store(time.Now().UnixNano())

	emu, err := vt.NewTerminal(o.Cols, o.Rows, vt.Effects{
		WritePTY: func(p []byte) {
			if !t.answer.Load() {
				return
			}
			select {
			case t.replies <- append([]byte(nil), p...):
			default:
			}
		},
		TitleChanged: func() { t.titleDirty = true },
		Size:         func() (int, int, int, int) { c, r := t.emu.Size(); return c, r, 8, 16 },
	})
	if err != nil {
		return nil, fmt.Errorf("terminal emulator: %w", err)
	}
	emu.SetTerminfoName("xterm-256color")
	// 字素簇占其宽度的格数，与界面里的 MyGo 终端一致
	emu.SetModeDefault(vt.ModeGraphemes, true)
	emu.SetScrollback(o.Scrollback)
	t.emu = emu

	cmd := exec.Command(o.Command[0], o.Command[1:]...)
	cmd.Dir = o.Dir
	cmd.Env = Environ(o.Env)
	f, err := pty.StartWithSize(cmd, &pty.Winsize{Cols: uint16(o.Cols), Rows: uint16(o.Rows)})
	if err != nil {
		emu.Free()
		return nil, err
	}
	t.cmd, t.pty = cmd, f
	t.idle = time.AfterFunc(compressAfter, t.compress)

	go t.readLoop()
	go func() {
		for {
			select {
			case p := <-t.replies:
				t.pty.Write(p)
			case <-t.exited:
				return
			}
		}
	}()
	go func() {
		code := 0
		if err := cmd.Wait(); err != nil {
			var ee *exec.ExitError
			if errors.As(err, &ee) {
				code = ee.ExitCode()
			} else {
				code = -1
			}
		}
		t.exitCode.Store(int64(code))
		// 先记录退出再放行等待者，常驻进程关闭时才不会在写库前关掉存储
		if o.OnExit != nil {
			o.OnExit(t, code)
		}
		close(t.exited)
		t.notify()
	}()
	return t, nil
}

func (t *Terminal) readLoop() {
	buf := make([]byte, 64<<10)
	for {
		n, err := t.pty.Read(buf)
		if n > 0 {
			t.smu.Lock()
			title := ""
			if t.emu != nil {
				t.emu.Write(buf[:n])
				if t.titleDirty {
					t.titleDirty = false
					title = t.emu.Title()
				}
			}
			if len(t.subs) > 0 {
				chunk := append([]byte(nil), buf[:n]...)
				for s := range t.subs {
					select {
					case s.C <- chunk:
					default:
						// 跟不上的接入者直接断开，重新接入会拿到完整快照
						delete(t.subs, s)
						close(s.C)
					}
				}
				t.answer.Store(len(t.subs) == 0)
			}
			t.smu.Unlock()
			t.lastOutput.Store(time.Now().UnixNano())
			t.idle.Reset(compressAfter)
			if title != "" {
				t.setTitle(title)
			}
			t.notify()
		}
		if err != nil {
			break
		}
	}
	t.smu.Lock()
	t.ended = true
	for s := range t.subs {
		close(s.C)
	}
	t.subs = nil
	t.smu.Unlock()
}

// compress 在终端空闲时压缩回滚区，降低常驻内存。
func (t *Terminal) compress() {
	t.smu.Lock()
	defer t.smu.Unlock()
	for t.emu != nil && t.emu.Compress() {
	}
}

func (t *Terminal) setTitle(title string) {
	t.mu.Lock()
	t.title = title
	t.mu.Unlock()
	if t.onTitle != nil {
		t.onTitle(t, title)
	}
}

// Attach 按界面的尺寸调整终端，返回当前快照和之后的输出流。
func (t *Terminal) Attach(cols, rows int) ([]byte, *Subscriber) {
	if cols > 0 && rows > 0 {
		t.Resize(cols, rows)
	}
	s := &Subscriber{C: make(chan []byte, 1024)}
	t.smu.Lock()
	defer t.smu.Unlock()
	var snap []byte
	if t.emu != nil {
		snap = t.emu.Snapshot()
	}
	if t.ended {
		close(s.C)
		return snap, s
	}
	t.subs[s] = struct{}{}
	t.answer.Store(false)
	return snap, s
}

func (t *Terminal) Detach(s *Subscriber) {
	t.smu.Lock()
	defer t.smu.Unlock()
	if _, ok := t.subs[s]; ok {
		delete(t.subs, s)
		close(s.C)
	}
	t.answer.Store(len(t.subs) == 0)
}

func (t *Terminal) notify() {
	t.mu.Lock()
	close(t.changed)
	t.changed = make(chan struct{})
	t.mu.Unlock()
}

// Changed 返回一个在下次输出或状态变化时关闭的 channel。
func (t *Terminal) Changed() <-chan struct{} {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.changed
}

func (t *Terminal) Exited() <-chan struct{} { return t.exited }

func (t *Terminal) Running() bool {
	select {
	case <-t.exited:
		return false
	default:
		return true
	}
}

func (t *Terminal) ExitCode() int { return int(t.exitCode.Load()) }

func (t *Terminal) Pid() int {
	if t.cmd.Process == nil {
		return 0
	}
	return t.cmd.Process.Pid
}

func (t *Terminal) LastOutput() time.Time { return time.Unix(0, t.lastOutput.Load()) }

func (t *Terminal) Title() string {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.title
}

func (t *Terminal) Agent() AgentState {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.agent
}

func (t *Terminal) SetAgent(s AgentState) {
	t.mu.Lock()
	// 有的事件不带会话信息，沿用之前拿到的
	if s.SessionID == "" {
		s.SessionID = t.agent.SessionID
	}
	if s.TranscriptPath == "" {
		s.TranscriptPath = t.agent.TranscriptPath
	}
	t.agent = s
	t.mu.Unlock()
	t.notify()
}

func (t *Terminal) Size() (cols, rows int) {
	t.smu.Lock()
	defer t.smu.Unlock()
	if t.emu == nil {
		return 0, 0
	}
	return t.emu.Size()
}

func (t *Terminal) Resize(cols, rows int) {
	t.smu.Lock()
	if t.emu != nil {
		t.emu.Resize(cols, rows, 8, 16)
	}
	t.smu.Unlock()
	pty.Setsize(t.pty, &pty.Winsize{Cols: uint16(cols), Rows: uint16(rows)})
}

// Send 直接写入 PTY，等同于用户键入。
func (t *Terminal) Send(data []byte) error {
	t.lastInput.Store(time.Now().UnixNano())
	return t.Inject(data)
}

// Inject 是 hive 自己往终端里打字（如提醒 agent 有新消息），不算作用户输入。
func (t *Terminal) Inject(data []byte) error {
	if !t.Running() {
		return errors.New("terminal has exited")
	}
	_, err := t.pty.Write(data)
	return err
}

// LastInput 是最近一次用户或其他程序键入的时间，从未键入时为零值。
func (t *Terminal) LastInput() time.Time {
	if v := t.lastInput.Load(); v != 0 {
		return time.Unix(0, v)
	}
	return time.Time{}
}

// Lines 返回回滚区加屏幕的文本，去掉行尾空白和末尾空行。
func (t *Terminal) Lines() []string {
	t.smu.Lock()
	text := ""
	if t.emu != nil {
		text = t.emu.Text()
	}
	t.smu.Unlock()
	lines := strings.Split(text, "\n")
	for i, l := range lines {
		lines[i] = strings.TrimRight(l, " \t")
	}
	for len(lines) > 0 && lines[len(lines)-1] == "" {
		lines = lines[:len(lines)-1]
	}
	return lines
}

// Screen 返回当前屏幕的每一行。
func (t *Terminal) Screen() []string {
	t.smu.Lock()
	defer t.smu.Unlock()
	if t.emu == nil {
		return nil
	}
	_, rows := t.emu.Size()
	lines := make([]string, rows)
	for y := range rows {
		text, _ := t.emu.RowText(y)
		lines[y] = strings.TrimRight(string(text), " \t")
	}
	return lines
}

// Close 挂断程序（SIGHUP），宽限期后仍未退出则杀掉整个进程组，最后释放终端内核。
func (t *Terminal) Close(grace time.Duration) {
	t.mu.Lock()
	if t.closing {
		t.mu.Unlock()
		return
	}
	t.closing = true
	t.mu.Unlock()
	if t.Running() {
		if pid := t.Pid(); pid > 0 {
			syscall.Kill(-pid, syscall.SIGHUP)
		}
		// 关闭主端等同于挂断终端，前台作业（如 agent，自成进程组）也会收到 SIGHUP
		t.pty.Close()
		select {
		case <-t.exited:
		case <-time.After(grace):
			if pid := t.Pid(); pid > 0 {
				syscall.Kill(-pid, syscall.SIGKILL)
			}
			<-t.exited
		}
	}
	t.pty.Close()
	t.idle.Stop()
	t.smu.Lock()
	if t.emu != nil {
		t.emu.Free()
		t.emu = nil
	}
	t.smu.Unlock()
}

// strippedEnv 是不应从启动常驻进程的环境泄漏进新终端的变量。
var strippedPrefixes = []string{"ORCA_", "CLAUDE_CODE_", "HIVE_", "TERM_PROGRAM"}
var strippedNames = map[string]bool{"CLAUDECODE": true, "CLAUDE_PID": true, "CLAUDE_EFFORT": true, "TERM": true, "COLORTERM": true}

// Environ 生成终端进程的环境变量。
func Environ(extra []string) []string {
	var env []string
	hasLang := false
	for _, kv := range os.Environ() {
		k, v, _ := strings.Cut(kv, "=")
		if strippedNames[k] || hasAnyPrefix(k, strippedPrefixes) {
			continue
		}
		switch k {
		case "PATH":
			kv = "PATH=" + buildPath(v)
		case "LANG":
			hasLang = true
		}
		env = append(env, kv)
	}
	env = append(env, "TERM=xterm-256color", "COLORTERM=truecolor", "TERM_PROGRAM=hive")
	if !hasLang {
		env = append(env, "LANG=en_US.UTF-8")
	}
	return append(env, extra...)
}

func hasAnyPrefix(s string, prefixes []string) bool {
	for _, p := range prefixes {
		if strings.HasPrefix(s, p) {
			return true
		}
	}
	return false
}

// buildPath 把 hive 自身所在目录放到最前，保证终端里的 agent 能调用 hive。
func buildPath(path string) string {
	var parts []string
	if exe, err := os.Executable(); err == nil {
		parts = append(parts, filepath.Dir(exe))
	}
	for _, p := range filepath.SplitList(path) {
		if p == "" || strings.Contains(p, "/Orca.app/") || (len(parts) > 0 && p == parts[0]) {
			continue
		}
		parts = append(parts, p)
	}
	return strings.Join(parts, string(filepath.ListSeparator))
}
