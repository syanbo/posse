// Package host 是控制进程和终端宿主之间的边界（方案见 docs/plans/terminal-host.md）：
// 常驻进程和手机只经这里的接口操作终端。现在只有进程内实现，直接包着 internal/term；
// 之后终端宿主独立成进程时，换成走 RPC 的实现，接口不变。
package host

import (
	"os"
	"time"

	"hive/internal/term"
)

// AgentState 是宿主替控制进程存着的 agent 状态，控制进程重启后从这里读回。
type AgentState = term.AgentState

// Record 是升级时交给新常驻进程的一个终端，和 PTY 描述符一起经交接 socket 传过去。
type Record = term.State

// Info 是终端建好后就不变的信息。
type Info struct {
	ID          string
	WorkspaceID string
	Dir         string
	Command     []string
	CreatedAt   time.Time
}

// StartOptions 开一个终端。回调只带终端 ID：宿主独立成进程后它们来自事件订阅。
type StartOptions struct {
	ID          string
	WorkspaceID string
	Dir         string
	Command     []string
	Env         []string
	Cols, Rows  int
	Scrollback  int
	OnTitle     func(id, title string)
	OnExit      func(id string, code int)
}

// Host 托管所有终端。
type Host interface {
	Start(o StartOptions) (Terminal, error)
	// Adopt 按交接记录重建终端，f 是 PTY 主端（程序已结束时为 nil）；o 只用回调和回滚上限
	Adopt(o StartOptions, r Record, f *os.File) (Terminal, error)
}

// Terminal 是控制进程能对一个终端做的事。
type Terminal interface {
	Info() Info

	Running() bool
	Exited() <-chan struct{}
	ExitCode() int
	Pid() int
	// Foreground 是前台进程组 ID，取不到时为 0
	Foreground() int
	Title() string
	Size() (cols, rows int)
	LastOutput() time.Time
	LastInput() time.Time
	// Changed 在输出、标题、agent 状态变化时关闭
	Changed() <-chan struct{}

	Agent() AgentState
	SetAgent(s AgentState)
	ResetAgent(s AgentState)
	// DropSubagent 划掉一个结束的后台子 agent（它们跑着时主线程停下不算完成）
	DropSubagent(id string) AgentState
	// SetTitle 给当前会话记下标题，TakeTitle 取走还没交给 agent 的那个（Claude 经 hook 拿）
	SetTitle(title, sessionID string, pending bool)
	TakeTitle() string

	// Send 是有人键入（会让"桌面有人在打字"的判断生效），Inject 是 hive 自己输入的提醒
	Send(data []byte) error
	Inject(data []byte) error
	Resize(cols, rows int)
	// HoldSize 让终端暂时按 key 的尺寸排版（手机看着时），ReleaseSize 放掉
	HoldSize(key string, cols, rows int)
	ReleaseSize(key string)
	// SizeHeld 报告是不是按手机的尺寸排版，ReclaimSize 让电脑收回尺寸（放掉所有手机的占用）
	SizeHeld() bool
	// CursorKeysApp 报告程序是否开着光标键应用模式，见 term.Terminal.CursorKeysApp
	CursorKeysApp() bool
	ReclaimSize()
	Clear(redraw bool) error

	Lines() []string
	ContainsText(sub string) bool
	Screen() []string

	// Attach 接入界面：返回快照和之后的输出，接入期间由接入方应答终端查询。
	// Watch 是只看不应答的接入（手机）。
	Attach(cols, rows int) ([]byte, Subscription)
	Watch() ([]byte, Subscription)

	Close(grace time.Duration)

	// Pause 停下读 PTY，导出交接记录和一份 PTY 描述符；交接失败时 Resume 接着读
	Pause() (Record, *os.File, error)
	Resume()
}

// Subscription 是一路输出。Output 关闭表示程序结束或接入方跟不上被断开。
type Subscription interface {
	Output() <-chan []byte
	// Received 在从 Output 取出 p 之后调用，释放排队额度
	Received(p []byte)
	Close()
}

// Local 是进程内的宿主：终端就在调用方的进程里。
type Local struct{}

func (Local) Start(o StartOptions) (Terminal, error) {
	t, err := term.Start(termOptions(o))
	if err != nil {
		return nil, err
	}
	return Wrap(t), nil
}

func (Local) Adopt(o StartOptions, r Record, f *os.File) (Terminal, error) {
	t, err := term.Adopt(termOptions(o), r, f)
	if err != nil {
		return nil, err
	}
	return Wrap(t), nil
}

func termOptions(o StartOptions) term.Options {
	opts := term.Options{
		ID: o.ID, WorkspaceID: o.WorkspaceID, Dir: o.Dir, Command: o.Command, Env: o.Env,
		Cols: o.Cols, Rows: o.Rows, Scrollback: o.Scrollback,
	}
	if o.OnTitle != nil {
		opts.OnTitle = func(t *term.Terminal, title string) { o.OnTitle(t.ID, title) }
	}
	if o.OnExit != nil {
		opts.OnExit = func(t *term.Terminal, code int) { o.OnExit(t.ID, code) }
	}
	return opts
}

// Wrap 把进程内的终端包成 Terminal（测试里自己 term.Start 的终端也用它）。
func Wrap(t *term.Terminal) Terminal { return &local{t} }

type local struct{ t *term.Terminal }

func (l *local) Info() Info {
	return Info{ID: l.t.ID, WorkspaceID: l.t.WorkspaceID, Dir: l.t.Dir, Command: l.t.Command, CreatedAt: l.t.CreatedAt}
}

func (l *local) Running() bool                     { return l.t.Running() }
func (l *local) Exited() <-chan struct{}           { return l.t.Exited() }
func (l *local) ExitCode() int                     { return l.t.ExitCode() }
func (l *local) Pid() int                          { return l.t.Pid() }
func (l *local) Foreground() int                   { return l.t.Foreground() }
func (l *local) Title() string                     { return l.t.Title() }
func (l *local) Size() (int, int)                  { return l.t.Size() }
func (l *local) LastOutput() time.Time             { return l.t.LastOutput() }
func (l *local) LastInput() time.Time              { return l.t.LastInput() }
func (l *local) Changed() <-chan struct{}          { return l.t.Changed() }
func (l *local) Agent() AgentState                 { return l.t.Agent() }
func (l *local) SetAgent(s AgentState)             { l.t.SetAgent(s) }
func (l *local) ResetAgent(s AgentState)           { l.t.ResetAgent(s) }
func (l *local) DropSubagent(id string) AgentState { return l.t.DropSubagent(id) }
func (l *local) SetTitle(title, sessionID string, pending bool) {
	l.t.SetTitle(title, sessionID, pending)
}
func (l *local) TakeTitle() string             { return l.t.TakeTitle() }
func (l *local) Send(data []byte) error        { return l.t.Send(data) }
func (l *local) Inject(data []byte) error      { return l.t.Inject(data) }
func (l *local) Resize(cols, rows int)         { l.t.Resize(cols, rows) }
func (l *local) HoldSize(key string, c, r int) { l.t.HoldSize(key, c, r) }
func (l *local) ReleaseSize(key string)        { l.t.ReleaseSize(key) }
func (l *local) SizeHeld() bool                { return l.t.SizeHeld() }
func (l *local) CursorKeysApp() bool           { return l.t.CursorKeysApp() }
func (l *local) ReclaimSize()                  { l.t.ReclaimSize() }
func (l *local) Clear(redraw bool) error       { return l.t.Clear(redraw) }
func (l *local) Lines() []string               { return l.t.Lines() }
func (l *local) ContainsText(sub string) bool  { return l.t.ContainsText(sub) }
func (l *local) Screen() []string              { return l.t.Screen() }
func (l *local) Close(grace time.Duration)     { l.t.Close(grace) }
func (l *local) Resume()                       { l.t.Resume() }

func (l *local) Pause() (Record, *os.File, error) { return l.t.Pause() }

func (l *local) Attach(cols, rows int) ([]byte, Subscription) {
	snap, sub := l.t.Attach(cols, rows)
	return snap, &localSub{l.t, sub}
}

func (l *local) Watch() ([]byte, Subscription) {
	snap, sub := l.t.Watch()
	return snap, &localSub{l.t, sub}
}

type localSub struct {
	t   *term.Terminal
	sub *term.Subscriber
}

func (s *localSub) Output() <-chan []byte { return s.sub.C }
func (s *localSub) Received(p []byte)     { s.sub.Received(p) }
func (s *localSub) Close()                { s.t.Detach(s.sub) }
