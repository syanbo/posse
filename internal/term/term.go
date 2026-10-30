// Package term 托管 PTY 进程，并用 libghostty-vt 在常驻进程里维护每个终端的屏幕状态，
// 供 CLI 读取文本、界面接入时取快照。
package term

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"time"

	"github.com/creack/pty"
	"github.com/egoist/mygo/plugins/terminal"
	"golang.org/x/sys/unix"

	"posse/internal/vt"
)

const DefaultScrollback = 4 << 20

// 终端空闲这么久之后压缩回滚区
const compressAfter = 5 * time.Second

// 每个接入者排队未取走的输出上限；条数上限之外再限字节数，免得界面卡住时积压大量内存
const maxQueuedBytes = 4 << 20

type Options struct {
	ID          string
	WorkspaceID string
	Dir         string
	Command     []string
	Env         []string
	Cols, Rows  int
	Scrollback  int
	CreatedAt   time.Time // 空是现在；重启后恢复的终端沿用原来的，标签顺序不变
	OnExit      func(t *Terminal, code int)
	OnTitle     func(t *Terminal, title string)
}

// AgentState 来自 agent 的 hooks 上报。
type AgentState struct {
	Status         string `json:"status"` // working | waiting | idle | exited
	Event          string `json:"event,omitempty"`
	SessionID      string `json:"sessionId,omitempty"`
	TranscriptPath string `json:"transcriptPath,omitempty"`
	Message        string `json:"message,omitempty"`
	ToolName       string `json:"toolName,omitempty"`
	ToolInput      string `json:"toolInput,omitempty"` // 权限请求的一行摘要
	Prompt         string `json:"prompt,omitempty"`    // 手机上的 interactivePrompt：答题卡原文或审批 JSON
	// Title 是 posse session title 给这个会话起的标题，界面和手机用它当会话名；
	// TitlePending 是 Claude 还没拿到它（下一次 UserPromptSubmit 时经 hook 交过去）
	Title        string `json:"title,omitempty"`
	TitlePending bool   `json:"titlePending,omitempty"`
	// Subagents 是还在跑的后台子 agent（Claude 主线程 Stop 的 background_tasks，SubagentStop 划掉）：
	// 它们跑着时主线程停下不算本轮完成
	Subagents []string  `json:"subagents,omitempty"`
	At        time.Time `json:"at"`
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
	pid int // 接过来的终端没有 cmd，pid 来自交接记录
	pty *os.File
	// replies 把内核对终端查询的应答交给写入协程：应答产生于持锁的 Write 期间，不能在那里阻塞
	replies chan []byte

	lastOutput atomic.Int64
	lastInput  atomic.Int64
	// mouseLast 为 true 表示最近一次写进 PTY 的是鼠标报告，见 dropStaleMouse
	mouseLast atomic.Bool
	// size 是列数<<32|行数，与内核尺寸同步更新，读取时不必抢 smu
	size atomic.Uint64
	// answer 为 true 时由常驻进程里的内核应答程序的终端查询；
	// 界面接入后改由界面应答，避免程序收到两份回复。
	answer atomic.Bool

	exited   chan struct{}
	exitCode atomic.Int64
	readDone chan struct{} // 读协程结束（PTY 读到结尾）时关闭

	// pauseMu 保护 pause：交接期间读协程停在 park 里
	pauseMu sync.Mutex
	pause   *pauseState
	start   func() // 接过来的终端还没开始读 PTY 时，Resume 调它

	// smu 保护 emu（libghostty-vt 非线程安全），并串行化"喂给内核 + 广播给接入者"，
	// 接入时的快照与之后的输出流因此恰好衔接
	smu        sync.Mutex
	emu        *vt.Terminal
	scrollback int         // 内核的回滚区大小，重建内核时沿用
	grew       int         // 行数变多时 libghostty 留在底部的空行数，尺寸稳定后 pullBack 收掉
	settle     *time.Timer // 尺寸稳定的计时
	subs       map[*Subscriber]struct{}
	ended      bool   // PTY 已读到结尾
	tail       []byte // 读到的输出末尾没写完的转义序列或 UTF-8 字符，交接时带过去
	titleDirty bool
	idle       *time.Timer

	// pmu 串行化调整 PTY 尺寸和关闭 PTY
	pmu       sync.Mutex
	ptyClosed bool

	// zmu 保护尺寸的归属：手机查看期间终端按手机屏幕排版，界面的尺寸先记在 desk，手机全部离开后再恢复
	zmu   sync.Mutex
	holds []sizeHold
	desk  [2]int

	mu      sync.Mutex
	changed chan struct{} // 有新输出或状态变化时关闭并替换
	watched bool          // changed 已交给调用方；没人拿走时不必替换
	title   string
	agent   AgentState
	closing bool
	onTitle func(t *Terminal, title string)
}

// Subscriber 是一个接入的界面，收到快照之后的全部输出。
// C 被关闭表示程序已结束，或接入者跟不上被断开。
type Subscriber struct {
	C chan []byte
	// queued 是已放进 C 还没取走的字节数
	queued atomic.Int64
	// quiet 的接入者（手机）不应答终端查询，仍由常驻进程里的内核应答
	quiet bool
	// kicked 是内核重建后被断开：程序还在跑，接入者要马上重新接入拿新快照
	kicked atomic.Bool
}

// Kicked 报告 C 是因为内核重建而关闭的，程序仍在运行。
func (s *Subscriber) Kicked() bool { return s.kicked.Load() }

type sizeHold struct {
	key        any
	cols, rows int
}

// Received 由接入者在从 C 取出 p 后调用，释放排队额度。
func (s *Subscriber) Received(p []byte) { s.queued.Add(-int64(len(p))) }

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
	t := newTerminal(o)
	t.storeSize(o.Cols, o.Rows)
	emu, err := t.newEmu(o.Cols, o.Rows, o.Scrollback)
	if err != nil {
		return nil, err
	}
	t.emu = emu

	cmd := exec.Command(o.Command[0], o.Command[1:]...)
	cmd.Dir = o.Dir
	cmd.Env = Environ(o.Env)
	f, err := pty.StartWithSize(cmd, &pty.Winsize{Cols: uint16(o.Cols), Rows: uint16(o.Rows)})
	if err == nil {
		f, err = pollable(f)
	}
	if err != nil {
		emu.Free()
		return nil, err
	}
	t.cmd, t.pty = cmd, f
	t.idle = time.AfterFunc(compressAfter, t.compress)
	t.run()
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
		t.finish(code, o.OnExit)
	}()
	return t, nil
}

func newTerminal(o Options) *Terminal {
	if o.CreatedAt.IsZero() {
		o.CreatedAt = time.Now()
	}
	t := &Terminal{
		ID:          o.ID,
		WorkspaceID: o.WorkspaceID,
		Command:     o.Command,
		Dir:         o.Dir,
		CreatedAt:   o.CreatedAt,
		replies:     make(chan []byte, 256),
		exited:      make(chan struct{}),
		readDone:    make(chan struct{}),
		changed:     make(chan struct{}),
		subs:        map[*Subscriber]struct{}{},
		onTitle:     o.OnTitle,
		scrollback:  o.Scrollback,
	}
	t.answer.Store(true)
	t.lastOutput.Store(time.Now().UnixNano())
	return t
}

func (t *Terminal) newEmu(cols, rows, scrollback int) (*vt.Terminal, error) {
	if scrollback == 0 {
		scrollback = DefaultScrollback
	}
	emu, err := vt.NewTerminal(cols, rows, vt.Effects{
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
	emu.SetScrollback(scrollback)
	return emu, nil
}

// run 起读协程和应答写入协程。
func (t *Terminal) run() {
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
}

// finish 记下程序退出。
func (t *Terminal) finish(code int, onExit func(*Terminal, int)) {
	t.exitCode.Store(int64(code))
	// 先记录退出再放行等待者，常驻进程关闭时才不会在写库前关掉存储
	if onExit != nil {
		onExit(t, code)
	}
	close(t.exited)
	t.notify()
}

func (t *Terminal) readLoop() {
	buf := make([]byte, 64<<10)
	for {
		n, err := t.pty.Read(buf)
		if n > 0 {
			t.smu.Lock()
			title := ""
			t.tail = carry(t.tail, buf[:n])
			if t.emu != nil {
				tracking := mouseTracking(t.emu)
				t.emu.Write(buf[:n])
				if tracking && !mouseTracking(t.emu) {
					t.dropStaleMouse()
				}
				if t.titleDirty {
					t.titleDirty = false
					title = t.emu.Title()
				}
			}
			t.broadcast(buf[:n])
			t.smu.Unlock()
			t.lastOutput.Store(time.Now().UnixNano())
			t.idle.Reset(compressAfter)
			if title != "" {
				t.setTitle(title)
			}
			t.notify()
		}
		if err != nil {
			if errors.Is(err, os.ErrDeadlineExceeded) {
				t.park()
				continue
			}
			break
		}
	}
	defer close(t.readDone)
	t.smu.Lock()
	t.ended = true
	for s := range t.subs {
		close(s.C)
	}
	t.subs = nil
	t.smu.Unlock()
}

// broadcast 在持有 smu 时把一段输出推给所有接入者。
func (t *Terminal) broadcast(data []byte) {
	if len(t.subs) == 0 {
		return
	}
	chunk := append([]byte(nil), data...)
	for s := range t.subs {
		if s.queued.Add(int64(len(chunk))) <= maxQueuedBytes {
			select {
			case s.C <- chunk:
				continue
			default:
			}
		}
		// 跟不上的接入者直接断开，重新接入会拿到完整快照
		delete(t.subs, s)
		close(s.C)
	}
	t.updateAnswer()
}

// clearSeq 清掉屏幕和回滚区，光标回到左上角。
var clearSeq = []byte("\x1b[H\x1b[2J\x1b[3J")

// Clear 清屏并清空回滚区（界面的 ⌘K）：终端内核和各接入者都当作程序输出了一段清屏，
// 之后接入拿到的快照也是空的。redraw 时再给程序发 Ctrl+L，让 shell 重画提示符、TUI 重画界面；
// 在清屏之后发，程序重画的内容不会被清掉。
func (t *Terminal) Clear(redraw bool) error {
	t.smu.Lock()
	if t.emu != nil {
		t.emu.Write(clearSeq)
	}
	t.broadcast(clearSeq)
	t.smu.Unlock()
	t.notify()
	if redraw {
		return t.Send([]byte{0x0c})
	}
	return nil
}

// CursorKeysApp 报告程序是否开着光标键应用模式（DECCKM）：开着时方向键、Home、End 要发 ESC O 开头的编码，
// htop 这类 ncurses 程序只认这种。
func (t *Terminal) CursorKeysApp() bool {
	t.smu.Lock()
	defer t.smu.Unlock()
	return t.emu != nil && t.emu.Mode(vt.ModeCursorKeys)
}

// BracketedPaste 报告程序是否开着括号粘贴模式（DECSET 2004）：开着时整段文字包在 ESC[200~ 和 ESC[201~ 之间送进去，
// 程序当成一次粘贴，里面的换行不会被当成回车提交。
func (t *Terminal) BracketedPaste() bool {
	t.smu.Lock()
	defer t.smu.Unlock()
	return t.emu != nil && t.emu.Mode(vt.ModeBracketedPaste)
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
		// 刚改成的就是接入者的尺寸，不用等它稳定：先把底部的空行收掉，快照里就是拉回来的样子
		t.smu.Lock()
		t.pullBack()
		t.smu.Unlock()
	}
	return t.subscribe(&Subscriber{C: make(chan []byte, 1024)})
}

// Watch 与 Attach 相同但不改尺寸，接入者也不接管终端查询的应答。
func (t *Terminal) Watch() ([]byte, *Subscriber) {
	return t.subscribe(&Subscriber{C: make(chan []byte, 1024), quiet: true})
}

func (t *Terminal) subscribe(s *Subscriber) ([]byte, *Subscriber) {
	t.smu.Lock()
	defer t.smu.Unlock()
	var snap []byte
	if t.emu != nil {
		// PTY 一次读约 1KB，常停在转义序列中间，内核已经吃进前半截。快照后面接上这半截，
		// 接入方的解析器才和内核在同一处；否则后半截被当成文字，少一次光标移动，按差异重画的 agent 就一直错位
		snap = t.emu.Snapshot()
		snap = append(snap[:len(snap):len(snap)], t.tail...)
	}
	if t.ended {
		close(s.C)
		return snap, s
	}
	t.subs[s] = struct{}{}
	t.updateAnswer()
	return snap, s
}

// updateAnswer 在持有 smu 时调用：没有会应答的接入者时由内核应答。
func (t *Terminal) updateAnswer() {
	for s := range t.subs {
		if !s.quiet {
			t.answer.Store(false)
			return
		}
	}
	t.answer.Store(true)
}

func (t *Terminal) Detach(s *Subscriber) {
	t.smu.Lock()
	defer t.smu.Unlock()
	if _, ok := t.subs[s]; ok {
		delete(t.subs, s)
		close(s.C)
	}
	t.updateAnswer()
}

func (t *Terminal) notify() {
	t.mu.Lock()
	if t.watched {
		close(t.changed)
		t.changed = make(chan struct{})
		t.watched = false
	}
	t.mu.Unlock()
}

// Changed 返回一个在下次输出或状态变化时关闭的 channel。
func (t *Terminal) Changed() <-chan struct{} {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.watched = true
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
	if t.cmd == nil {
		return t.pid
	}
	if t.cmd.Process == nil {
		return 0
	}
	return t.cmd.Process.Pid
}

// Foreground 返回终端前台进程组的 ID，取不到（已关闭等）时返回 0。
// 经 SyscallConn 取描述符：调 Fd() 会把 PTY 改成阻塞模式。
func (t *Terminal) Foreground() int {
	t.pmu.Lock()
	defer t.pmu.Unlock()
	if t.ptyClosed {
		return 0
	}
	rc, err := t.pty.SyscallConn()
	if err != nil {
		return 0
	}
	pg := 0
	rc.Control(func(fd uintptr) {
		if v, err := unix.IoctlGetInt(int(fd), unix.TIOCGPGRP); err == nil {
			pg = v
		}
	})
	return pg
}

// ResetAgent 整个换掉 agent 状态，不沿用之前的会话信息：终端里换了一个 agent 时用。
func (t *Terminal) ResetAgent(s AgentState) {
	t.mu.Lock()
	t.agent = s
	t.mu.Unlock()
	t.notify()
}

// SetTitle 记下会话标题；agent 还没报过会话 ID 时用调用方给的。
func (t *Terminal) SetTitle(title, sessionID string, pending bool) {
	t.mu.Lock()
	if t.agent.SessionID == "" {
		t.agent.SessionID = sessionID
	}
	t.agent.Title, t.agent.TitlePending = title, pending
	t.mu.Unlock()
	t.notify()
}

// TakeTitle 返回还没交给 agent 的标题并标成已交，没有时返回空。
func (t *Terminal) TakeTitle() string {
	t.mu.Lock()
	defer t.mu.Unlock()
	if !t.agent.TitlePending {
		return ""
	}
	t.agent.TitlePending = false
	return t.agent.Title
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
	// 有的事件不带会话信息，沿用之前拿到的。离开等待就清掉卡片，免得上一张审批卡
	// 停在已经继续干活的会话上。仍在等待、但这次没带新提示的事件要留着原卡片：
	// Claude 的 Notification permission_prompt 会在答题卡还在时再响一次，payload 是空的。
	if s.SessionID == "" {
		s.SessionID = t.agent.SessionID
	}
	if s.TranscriptPath == "" {
		s.TranscriptPath = t.agent.TranscriptPath
	}
	// 标题跟着会话走：同一个会话的后续事件沿用，换了会话（新开的 agent、/clear）就不再是它的标题
	if s.Title == "" && s.SessionID == t.agent.SessionID {
		s.Title, s.TitlePending = t.agent.Title, t.agent.TitlePending
	}
	if s.Subagents == nil && s.SessionID == t.agent.SessionID {
		s.Subagents = t.agent.Subagents
	}
	if s.Status != "waiting" {
		s.ToolName, s.ToolInput, s.Prompt = "", "", ""
	} else if s.Prompt == "" {
		s.ToolName, s.ToolInput, s.Prompt = t.agent.ToolName, t.agent.ToolInput, t.agent.Prompt
	}
	t.agent = s
	t.mu.Unlock()
	t.notify()
}

// DropSubagent 划掉一个结束的子 agent，返回改后的状态。
func (t *Terminal) DropSubagent(id string) AgentState {
	t.mu.Lock()
	// 新建切片：交出去的旧状态里的切片不能跟着变
	var list []string
	for _, v := range t.agent.Subagents {
		if v != id {
			list = append(list, v)
		}
	}
	t.agent.Subagents = list
	s := t.agent
	t.mu.Unlock()
	t.notify()
	return s
}

// Size 返回终端尺寸；终端关闭后为 0, 0。
func (t *Terminal) Size() (cols, rows int) {
	v := t.size.Load()
	return int(v >> 32), int(uint32(v))
}

// storeSize 按内核的取值范围记录尺寸。
func (t *Terminal) storeSize(cols, rows int) {
	t.size.Store(uint64(max(1, min(cols, 0xFFFF)))<<32 | uint64(max(1, min(rows, 0xFFFF))))
}

// Resize 是界面或 CLI 调整尺寸；手机查看期间只记下来，等手机离开再生效。
func (t *Terminal) Resize(cols, rows int) {
	t.zmu.Lock()
	defer t.zmu.Unlock()
	if len(t.holds) > 0 {
		t.desk = [2]int{cols, rows}
		return
	}
	t.resize(cols, rows)
}

// HoldSize 让终端按 key（一个手机订阅）的尺寸排版，直到 ReleaseSize；多个手机时最后来的说了算。
func (t *Terminal) HoldSize(key any, cols, rows int) {
	t.zmu.Lock()
	defer t.zmu.Unlock()
	if len(t.holds) == 0 {
		t.desk[0], t.desk[1] = t.Size()
	}
	t.holds = slices.DeleteFunc(t.holds, func(h sizeHold) bool { return h.key == key })
	t.holds = append(t.holds, sizeHold{key, cols, rows})
	if c, r := t.Size(); c != cols || r != rows {
		t.resize(cols, rows)
	}
}

func (t *Terminal) ReleaseSize(key any) {
	t.zmu.Lock()
	defer t.zmu.Unlock()
	n := len(t.holds)
	t.holds = slices.DeleteFunc(t.holds, func(h sizeHold) bool { return h.key == key })
	if len(t.holds) == n || !t.Running() {
		return
	}
	want := t.desk
	if len(t.holds) > 0 {
		h := t.holds[len(t.holds)-1]
		want = [2]int{h.cols, h.rows}
	}
	if c, r := t.Size(); want[0] > 0 && want[1] > 0 && (c != want[0] || r != want[1]) {
		t.resize(want[0], want[1])
	}
}

// SizeHeld 报告终端现在是不是按手机的尺寸排版。
func (t *Terminal) SizeHeld() bool {
	t.zmu.Lock()
	defer t.zmu.Unlock()
	return len(t.holds) > 0
}

// ReclaimSize 让电脑收回尺寸：放掉所有手机的占用，按界面最后要的尺寸重排。手机的订阅还在，
// 之后它离开时 ReleaseSize 找不到自己的占用，什么也不做；手机重新打开这个终端才会再占。
func (t *Terminal) ReclaimSize() {
	t.zmu.Lock()
	defer t.zmu.Unlock()
	if len(t.holds) == 0 {
		return
	}
	t.holds = nil
	if c, r := t.Size(); t.Running() && t.desk[0] > 0 && t.desk[1] > 0 && (c != t.desk[0] || r != t.desk[1]) {
		t.resize(t.desk[0], t.desk[1])
	}
}

func (t *Terminal) resize(cols, rows int) {
	t.smu.Lock()
	if t.emu != nil {
		_, oldRows := t.emu.Size()
		_, cy := t.emu.Cursor()
		bar := t.emu.Scrollbar()
		if t.emu.Resize(cols, rows, 8, 16) == nil {
			t.storeSize(cols, rows)
			switch {
			case rows < oldRows:
				// 变少时 libghostty 先去掉底部的空行
				t.grew = max(0, t.grew-(oldRows-rows))
			case rows > oldRows && !t.emu.AltScreen() && bar.Total > bar.Len && cy < oldRows-1:
				t.grew += rows - oldRows
				if t.settle == nil {
					t.settle = time.AfterFunc(settleAfter, t.settleRows)
				} else {
					t.settle.Reset(settleAfter)
				}
			}
		}
	}
	t.smu.Unlock()
	t.setPTYSize(cols, rows)
}

// settleAfter 是改尺寸后多久没再改算稳定：拖窗口时每帧都来一次，不能每次都重建内核。
const settleAfter = 150 * time.Millisecond

func (t *Terminal) settleRows() {
	t.smu.Lock()
	defer t.smu.Unlock()
	t.pullBack()
}

// 快照后一段里恢复光标的序列，和程序设过的滚动区域
var (
	cupRe    = regexp.MustCompile(`\x1b\[(\d+);(\d+)H`)
	regionRe = regexp.MustCompile(`\x1b\[\d+;\d+r`)
)

// pullBack 把行数变多时留在底部的空行收掉、回滚区的内容拉回屏幕，像 Terminal.app 那样内容贴着底部，
// 光标和内容的相对位置不变。libghostty 只在光标在最后一行时这么做；光标在上面时（Claude Code 的光标在
// 输入框里，下面还有状态行）新加的行空在底部，程序收到 SIGWINCH 又只在原地重画，底部就一直空着。
// 窗口变高、手机看完离开改回电脑尺寸都会这样。做法是按同样的尺寸重建内核、重放去掉底部空行的快照，
// 再断开所有接入者：它们按自己当下的尺寸重新接入拿新快照（窗口经 OnExit 重新接入，手机的流重发快照），
// 比在输出流里广播快照稳妥：窗口拖动时发出去的快照到达时尺寸可能已经变了。
// 持有 smu 时调用。程序在备用屏幕、开了原点模式或设了滚动区域（自己管着整屏的 TUI）时不动。
func (t *Terminal) pullBack() {
	grew := t.grew
	t.grew = 0
	if grew == 0 || t.emu == nil || t.ended || t.emu.AltScreen() {
		return
	}
	cols, rows := t.emu.Size()
	_, cy := t.emu.Cursor()
	// 只收掉底部连续的空行，最多 grew 行，也不动光标所在的行
	k := 0
	for k < min(grew, rows-1-cy) && t.emu.RowBlank(rows-1-k) {
		k++
	}
	if k == 0 {
		return
	}
	body, suffix := t.emu.SnapshotParts(k)
	if suffix == nil || bytes.Contains(body, []byte("\x1b[?6h")) || regionRe.Match(suffix) {
		return
	}
	cups := cupRe.FindAllSubmatchIndex(suffix, -1)
	if len(cups) == 0 {
		return
	}
	emu, err := t.newEmu(cols, rows, t.scrollback)
	if err != nil {
		return
	}
	// 重放时内核不应答：快照里没有查询，尾巴是没写完的序列、凑不成查询
	t.answer.Store(false)
	emu.Write(body)
	_, endY := emu.Cursor() // 保留下来的最后一行现在的位置
	shift := endY - (rows - 1 - k)
	// 光标跟着内容挪：改写恢复光标那一句的行号，后面重打的字（待换行状态）照旧
	cup := cups[len(cups)-1]
	row, _ := strconv.Atoi(string(suffix[cup[2]:cup[3]]))
	fixed := append([]byte(nil), suffix[:cup[2]]...)
	fixed = strconv.AppendInt(fixed, int64(row+shift), 10)
	fixed = append(fixed, suffix[cup[3]:]...)
	emu.Write(fixed)
	if title := t.emu.Title(); title != "" {
		emu.Write([]byte("\x1b]2;" + title + "\x07"))
	}
	emu.Write(t.tail)
	t.titleDirty = false
	t.emu.Free()
	t.emu = emu
	if t.idle != nil {
		t.idle.Reset(compressAfter) // 新内核的回滚区还没压缩
	}
	for s := range t.subs {
		s.kicked.Store(true)
		delete(t.subs, s)
		close(s.C)
	}
	t.updateAnswer()
}

// setPTYSize 只改 PTY 的尺寸。不用 pty.Setsize：它经 Fd() 把 PTY 改回阻塞模式。
func (t *Terminal) setPTYSize(cols, rows int) {
	t.pmu.Lock()
	defer t.pmu.Unlock()
	if t.ptyClosed {
		return
	}
	if rc, err := t.pty.SyscallConn(); err == nil {
		rc.Control(func(fd uintptr) {
			unix.IoctlSetWinsize(int(fd), unix.TIOCSWINSZ, &unix.Winsize{Col: uint16(cols), Row: uint16(rows)})
		})
	}
}

// Send 直接写入 PTY，等同于用户键入。
func (t *Terminal) Send(data []byte) error {
	t.lastInput.Store(time.Now().UnixNano())
	return t.Inject(data)
}

// Inject 是 posse 自己往终端里打字（如提醒 agent 有新消息），不算作用户输入。
func (t *Terminal) Inject(data []byte) error {
	if !t.Running() {
		return errors.New("terminal has exited")
	}
	mouse := mouseReport.Match(data)
	if mouse && lossyMouse(data) {
		// 程序忙着没读输入时（Codex 回合中、重画长会话），macOS 的 PTY 输入队列只放得下约 1 KB，
		// 再写就在报告中间截断、等程序读走再写后半截；程序读到结尾孤零零的 ESC 当成按了 Esc，
		// 后半截"[<64;53;8M"成了输入框里的字。滚轮、移动的报告丢几个无妨，不往堆着没读的队列里写；
		// 按下、松开不丢，丢了松开程序会以为键一直按着。丢掉的也按写入成功返回。
		if q := t.inputQueued(); q > mouseQueued || q+len(data) > ptyInputMax {
			return nil
		}
	}
	t.mouseLast.Store(mouse)
	_, err := t.pty.Write(data)
	return err
}

// mouseQueued 是 PTY 输入队列里没读的字节超过多少时不再写滚轮、移动的报告：程序已经跟不上了。
// ptyInputMax 是写完后队列最多到多少字节，低于 macOS 约 1 KB 的上限，一次带着多条报告的写入也截不断。
const (
	mouseQueued = 256
	ptyInputMax = 960
)

// lossyMouse 判断 data（整段都是鼠标报告）是否全是滚轮或移动的报告：按键码带 64（滚轮）或 32（移动）。
func lossyMouse(data []byte) bool {
	for len(data) > 0 {
		var code, n int
		if m := sgrReport.FindSubmatch(data); m != nil {
			code, _ = strconv.Atoi(string(m[1]))
			n = len(m[0])
		} else if len(data) >= 6 {
			// X10：\x1b[M 后三个字节，按键码加了 32
			code, n = int(data[3])-32, 6
		} else {
			return false
		}
		if code&(32|64) == 0 {
			return false
		}
		data = data[n:]
	}
	return true
}

var sgrReport = regexp.MustCompile(`^\x1b\[<(\d+);\d+;\d+[Mm]`)

// fionread 是 macOS 的 FIONREAD（x/sys 没有导出）：对主端取的是从端还没读的输入字节数。
const fionread = 0x4004667f

// inputQueued 返回程序还没读的输入字节数，取不到时为 0。
func (t *Terminal) inputQueued() int {
	t.pmu.Lock()
	defer t.pmu.Unlock()
	if t.ptyClosed {
		return 0
	}
	n := 0
	if rc, err := t.pty.SyscallConn(); err == nil {
		rc.Control(func(fd uintptr) {
			n, _ = unix.IoctlGetInt(int(fd), fionread)
		})
	}
	return n
}

// mouseReport 匹配整段都是鼠标报告的输入（SGR 和 X10 两种编码）。
var mouseReport = regexp.MustCompile(`^(?:\x1b\[<\d+;\d+;\d+[Mm]|\x1b\[M[\x20-\xff]{3})+$`)

func mouseTracking(emu *vt.Terminal) bool {
	return emu.Mode(9) || emu.Mode(1000) || emu.Mode(1002) || emu.Mode(1003)
}

// dropStaleMouse 在程序关掉鼠标跟踪时丢掉 PTY 里还没读的输入，前提是最后写进去的是鼠标报告。
// 程序退出前先不读输入、最后才关鼠标跟踪（Codex 的 Shutting down…），这期间鼠标在终端上移动的
// 报告堆在 PTY 里，接着运行的 shell 会把它们读成一串"35;12;8M"。最后写的是键盘输入时不丢，免得吞掉用户打的字。
func (t *Terminal) dropStaleMouse() {
	if !t.mouseLast.Load() {
		return
	}
	t.pmu.Lock()
	defer t.pmu.Unlock()
	if t.ptyClosed {
		return
	}
	if rc, err := t.pty.SyscallConn(); err == nil {
		rc.Control(func(fd uintptr) {
			// 在主端 TIOCFLUSH FREAD 清的是从端还没读的输入队列
			unix.IoctlSetPointerInt(int(fd), unix.TIOCFLUSH, 1)
		})
	}
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

// ContainsText 判断 Lines() 的某一行是否包含 sub。
func (t *Terminal) ContainsText(sub string) bool {
	// sub 不跨行、也不以空白结尾时，在整段文本里能找到就等于在某一行里能找到（行尾被去掉的只有空白），
	// 免去逐行切分和去空白
	if sub != "" && !strings.Contains(sub, "\n") && !strings.HasSuffix(sub, " ") && !strings.HasSuffix(sub, "\t") {
		t.smu.Lock()
		text := ""
		if t.emu != nil {
			text = t.emu.Text()
		}
		t.smu.Unlock()
		return strings.Contains(text, sub)
	}
	for _, l := range t.Lines() {
		if strings.Contains(l, sub) {
			return true
		}
	}
	return false
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
		t.closePTY()
		select {
		case <-t.exited:
		case <-time.After(grace):
			if pid := t.Pid(); pid > 0 {
				syscall.Kill(-pid, syscall.SIGKILL)
			}
			<-t.exited
		}
	}
	t.closePTY()
	t.idle.Stop()
	t.smu.Lock()
	if t.emu != nil {
		t.emu.Free()
		t.emu = nil
		t.size.Store(0)
	}
	t.smu.Unlock()
}

// closePTY 与调整尺寸互斥：手机离开时恢复尺寸可能和关闭终端同时发生。
func (t *Terminal) closePTY() {
	t.pmu.Lock()
	defer t.pmu.Unlock()
	if !t.ptyClosed {
		t.ptyClosed = true
		t.pty.Close()
	}
}

// strippedEnv 是不应从启动常驻进程的环境泄漏进新终端的变量。
var strippedPrefixes = []string{"ORCA_", "CLAUDE_CODE_", "POSSE_", "TERM_PROGRAM", "GROK_"}
var strippedNames = map[string]bool{"CLAUDECODE": true, "CLAUDE_PID": true, "CLAUDE_EFFORT": true, "TERM": true, "COLORTERM": true, "FORCE_HYPERLINK": true,
	// agent 给工具调用设的非交互环境（关颜色、关分页、git 不开编辑器不问密码）。agent 在终端里用 open 拉起 Posse.app 时，
	// 窗口和常驻进程会继承它们，再漏进之后的每个终端：Claude Code 看到 NO_COLOR 就整屏没颜色。
	// 交互 shell 会重新读用户的启动文件，用户自己设的照样生效。
	"NO_COLOR": true, "FORCE_COLOR": true, "CLICOLOR": true, "CLICOLOR_FORCE": true,
	"PAGER": true, "GIT_PAGER": true, "GH_PAGER": true, "AWS_PAGER": true, "MANPAGER": true, "SYSTEMD_PAGER": true,
	"GIT_EDITOR": true, "GIT_SEQUENCE_EDITOR": true, "GIT_TERMINAL_PROMPT": true,
	"NPM_CONFIG_COLOR": true, "PIP_NO_COLOR": true, "PIP_PROGRESS_BAR": true, "CARGO_TERM_COLOR": true}

// strippedValues 只在值和 agent 的非交互设置一样时去掉：这些变量平时也用来放 JVM 参数。
var strippedValues = map[string]string{"GRADLE_OPTS": "-Dorg.gradle.console=plain", "MAVEN_OPTS": "-Dstyle.color=never"}

// Environ 生成终端进程的环境变量。
func Environ(extra []string) []string {
	var env []string
	hasLang := false
	for _, kv := range os.Environ() {
		k, v, _ := strings.Cut(kv, "=")
		if strippedNames[k] || hasAnyPrefix(k, strippedPrefixes) || (strippedValues[k] != "" && strippedValues[k] == v) {
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
	// 终端支持超链接（OSC 8），但 TERM_PROGRAM=posse 不在常见的支持名单里：Claude Code 等按 supports-hyperlinks
	// 判断，要 FORCE_HYPERLINK 才会把 [Image #N]、文件路径输出成可点的链接
	env = append(env, "TERM=xterm-256color", "COLORTERM=truecolor", "TERM_PROGRAM=posse", "FORCE_HYPERLINK=1")
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

// buildPath 把 posse 自身所在目录放到最前，保证终端里的 agent 能调用 posse。
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
