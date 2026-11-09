package daemon

import (
	"context"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net"
	"os"
	"os/exec"
	"strconv"
	"sync"
	"time"

	"golang.org/x/sys/unix"

	"posse/internal/host"
	"posse/internal/rpc"
	"posse/internal/spawn"
)

// 升级时把整个常驻进程交给新包启动的进程（方案见 docs/plans/terminal-host.md）。
// 旧进程拉起新进程，经 socketpair 交过去：possed.sock 的监听描述符、每个终端的交接记录和 PTY 描述符；
// 新进程接好后回话，旧进程退出时不关终端、不删 socket。

// handoffVersion 是交接协议的版本，两边不一致时不交接，旧进程照常运行。
const handoffVersion = 1

// handoffEnv 告诉新进程交接 socket 是哪个描述符。
const handoffEnv = "POSSE_HANDOFF_FD"

const handoffTimeout = 20 * time.Second

type handoffHello struct {
	Version int    `json:"version"`
	Build   string `json:"build"`
}

type handoffTerm struct {
	Term           host.Record    `json:"term"`
	Agent          string         `json:"agent,omitempty"`
	DiscussionTurn discussionTurn `json:"discussionTurn,omitzero"`
	Hooked         bool           `json:"hooked,omitempty"`
	HookedPg       int            `json:"hookedPg,omitempty"` // session.agentPg；沿用旧名，和旧版本互相交接时格式不变
	Fg             int            `json:"fg,omitempty"`
	// Unread 可以省略：旧进程交过来时没有，当作看过；交给旧进程时它不认，丢掉也只是少提醒一次，不用升 handoffVersion
	Unread bool `json:"unread,omitempty"`
	// Runtime 是拉起终端的常驻进程所在的副本（session.runtime）。新加的可选字段，没加 handoffVersion：
	// 旧版本的旧进程只认版本号相等的新进程，加了就没法从旧版本不断终端地升级上来；旧版本交来的为空，
	// 表示原包或不知道，不参与回收，新版本交给旧版本时它被忽略，副本留到下次回收
	Runtime string `json:"runtime,omitempty"`
}

// handoffBatch 之后依次跟着监听描述符和每个没结束的终端的 PTY 描述符。
type handoffBatch struct {
	Version int           `json:"version"`
	Terms   []handoffTerm `json:"terms"`
}

type handoffAck struct {
	Error string `json:"error,omitempty"`
}

// handoffState 记着旧进程这边的交接进度。
type handoffState struct {
	// startMu 让新建终端（读锁，覆盖检查、启动、登记整段）和交接开始（写锁）互斥，
	// 交接取终端快照时不会漏掉正在建的终端
	startMu sync.RWMutex

	mu       sync.Mutex
	on       bool            // 交接进行中
	done     bool            // 已经交出去了，退出时不关终端、不删 socket
	exported map[string]bool // PTY 已经交出去的终端，退出由新进程处理
	exits    []pendingExit
}

type pendingExit struct {
	id   string
	code int
}

func (h *handoffState) active() bool {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.on || h.done
}

func (h *handoffState) handedOff() bool {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.done
}

func (h *handoffState) export(id string) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.exported == nil {
		h.exported = map[string]bool{}
	}
	h.exported[id] = true
}

// deferExit 在 PTY 已经交出去的终端退出时记下，返回 true 表示调用方不用处理：
// 交接成功由新进程盯着（它收不到退出码时记 -1），失败时恢复后补上。
// 还没交出去的终端照常处理，交过去的是已结束的记录，新进程不再处理它的退出。
func (h *handoffState) deferExit(id string, code int) bool {
	h.mu.Lock()
	defer h.mu.Unlock()
	if !h.exported[id] {
		return false
	}
	if h.on {
		h.exits = append(h.exits, pendingExit{id, code})
	}
	return true
}

// gateListener 在交接期间停接新连接（它们排在 backlog 里，由新进程接），交接失败再放开。
type gateListener struct {
	*net.UnixListener
	mu     sync.Mutex
	wait   chan struct{}
	closed bool // 监听描述符已经在新进程手里
	shut   chan struct{}
	once   sync.Once
}

func newGateListener(l *net.UnixListener) *gateListener {
	return &gateListener{UnixListener: l, shut: make(chan struct{})}
}

func (l *gateListener) Close() error {
	l.once.Do(func() { close(l.shut) })
	return l.UnixListener.Close()
}

func (l *gateListener) Accept() (net.Conn, error) {
	for {
		c, err := l.UnixListener.Accept()
		if err == nil || !errors.Is(err, os.ErrDeadlineExceeded) {
			return c, err
		}
		l.mu.Lock()
		w, closed := l.wait, l.closed
		l.mu.Unlock()
		if closed {
			// 等常驻进程退出时关掉监听再返回，免得 Serve 提前结束、交接的回复还没写出去
			<-l.shut
			return nil, net.ErrClosed
		}
		if w != nil {
			<-w
		}
	}
}

func (l *gateListener) hold() {
	l.mu.Lock()
	l.wait = make(chan struct{})
	l.mu.Unlock()
	l.SetDeadline(time.Now())
}

// release 放开（交接失败）；closed 时不再接连接，监听描述符已经在新进程手里。
func (l *gateListener) release(closed bool) {
	if !closed {
		l.SetDeadline(time.Time{})
	} else {
		l.SetUnlinkOnClose(false)
	}
	l.mu.Lock()
	l.closed = closed
	if l.wait != nil {
		close(l.wait)
		l.wait = nil
	}
	l.mu.Unlock()
}

type daemonHandoffParams struct {
	// Exe 是新包里的 posse，为空时用自己的（测试交接本身）
	Exe string `json:"exe"`
}

type daemonHandoffResult struct {
	Pid       int `json:"pid"`
	Terminals int `json:"terminals"`
}

// daemonHandoff 是旧进程这边：拉起新进程、交出所有东西，成功后退出。
func (d *Daemon) daemonHandoff(ctx context.Context, raw json.RawMessage, stop context.CancelFunc) (any, error) {
	var p daemonHandoffParams
	if err := rpc.Decode(raw, &p); err != nil {
		return nil, err
	}
	if d.ln == nil {
		return nil, rpc.Errorf(rpc.CodeInternal, "listener not available")
	}
	exe := p.Exe
	if exe == "" {
		exe = d.launcher.Exe
	}
	h := &d.handoff
	h.startMu.Lock()
	h.mu.Lock()
	busy := h.on || h.done
	h.on = !busy
	h.mu.Unlock()
	h.startMu.Unlock()
	if busy {
		return nil, rpc.Errorf(rpc.CodeConflict, "handoff already in progress")
	}
	// 协作端口和到房主的连接先停：新进程拿到锁之后再接上，免得两代进程同时接受写入
	d.collabPause(false)

	n, pid, err := d.handOver(exe)
	h.mu.Lock()
	h.on = false
	exits := h.exits
	h.exits = nil
	if err == nil {
		h.done = true
	} else {
		h.exported = nil
	}
	h.mu.Unlock()
	if err != nil {
		log.Printf("handoff failed: %v", err)
		for _, e := range exits {
			d.onExit(e.id, e.code)
		}
		// 交接期间暂停的讨论派发、协作恢复
		d.collabPause(true)
		d.discKick()
		return nil, rpc.Errorf(rpc.CodeInternal, "handoff: %v", err)
	}
	log.Printf("handed off %d terminals to pid %d", n, pid)
	time.AfterFunc(100*time.Millisecond, stop)
	return daemonHandoffResult{Pid: pid, Terminals: n}, nil
}

func (d *Daemon) handOver(exe string) (n, pid int, err error) {
	fds, err := unix.Socketpair(unix.AF_UNIX, unix.SOCK_STREAM, 0)
	if err != nil {
		return 0, 0, err
	}
	unix.CloseOnExec(fds[0])
	unix.CloseOnExec(fds[1])
	mine, theirs := os.NewFile(uintptr(fds[0]), "handoff"), os.NewFile(uintptr(fds[1]), "handoff")
	c, err := net.FileConn(mine)
	mine.Close()
	if err != nil {
		theirs.Close()
		return 0, 0, err
	}
	defer c.Close()
	conn := c.(*net.UnixConn)

	// 新进程从新包的副本运行；调用方（窗口、posse daemon handoff）一般已经准备好，这里复用。
	// 准备不了就从原包拉起，交接照常
	if rt, err := spawn.Runtime(exe, d.cfg.Home); err != nil {
		log.Printf("runtime copy of %s: %v", exe, err)
	} else {
		exe = rt
	}
	cmd := exec.Command(exe, "daemon", "run", "--scrollback-mb", strconv.Itoa(d.cfg.Scrollback>>20))
	cmd.Dir = d.cfg.Home
	cmd.Env = append(os.Environ(), "POSSE_HOME="+d.cfg.Home, handoffEnv+"=3")
	cmd.ExtraFiles = []*os.File{theirs}
	// 和 EnsureDaemon 一样重新打开日志、太大先轮转：一直交接下去常驻进程不重启，不能一直沿用旧的
	if logf, lerr := OpenLog(d.cfg.Home); lerr == nil {
		defer logf.Close()
		cmd.Stdout, cmd.Stderr = logf, logf
	} else {
		cmd.Stdout, cmd.Stderr = os.Stdout, os.Stderr
	}
	// 新进程自己对系统授权负责：一路交接下去，最初拉起常驻进程的旧 Posse.app 可能早已被换掉
	proc, err := spawn.Start(cmd)
	theirs.Close()
	if err != nil {
		return 0, 0, fmt.Errorf("start %s: %w", exe, err)
	}
	ok := false
	defer func() {
		if !ok {
			proc.Kill()
		}
		// 收走子进程：失败时是刚杀掉的，成功时它在旧进程退出前不会结束
		go proc.Wait()
	}()
	conn.SetDeadline(time.Now().Add(handoffTimeout))

	var hello handoffHello
	if err := readFrame(conn, &hello); err != nil {
		return 0, 0, fmt.Errorf("new daemon did not answer: %w", err)
	}
	if hello.Version != handoffVersion {
		return 0, 0, fmt.Errorf("new daemon speaks handoff v%d, want v%d", hello.Version, handoffVersion)
	}

	d.ln.hold()
	lnFile, err := d.ln.File()
	if err != nil {
		d.ln.release(false)
		return 0, 0, err
	}
	defer lnFile.Close()

	ss := d.sessionsIn("")
	var paused []*session
	var files []*os.File
	defer func() {
		for _, f := range files {
			f.Close()
		}
		if !ok {
			// 先杀掉新进程再恢复，免得两边同时读同一个 PTY
			proc.Kill()
			for _, s := range paused {
				s.Resume()
			}
			d.ln.release(false)
		}
	}()
	batch := handoffBatch{Version: handoffVersion}
	for _, s := range ss {
		rec, f, err := s.Pause()
		if err != nil {
			return 0, 0, fmt.Errorf("terminal %s: %w", s.ID, err)
		}
		paused = append(paused, s)
		if f != nil {
			files = append(files, f)
			d.handoff.export(s.ID)
		}
		s.mu.Lock()
		batch.Terms = append(batch.Terms, handoffTerm{Term: rec, Agent: s.agent, Hooked: s.hooked, HookedPg: s.agentPg, Fg: s.fg, Unread: s.unread, Runtime: s.runtime, DiscussionTurn: s.discussionTurn})
		s.mu.Unlock()
	}
	if err := writeFrame(conn, batch); err != nil {
		return 0, 0, err
	}
	if err := sendFile(conn, lnFile); err != nil {
		return 0, 0, err
	}
	for _, f := range files {
		if err := sendFile(conn, f); err != nil {
			return 0, 0, err
		}
	}
	var ack handoffAck
	if err := readFrame(conn, &ack); err != nil {
		return 0, 0, fmt.Errorf("no answer after handing over: %w", err)
	}
	if ack.Error != "" {
		return 0, 0, errors.New(ack.Error)
	}
	ok = true
	d.ln.release(true)
	return len(batch.Terms), proc.Pid, nil
}

// incoming 是新进程这边收到的交接：PTY 描述符和 batch.Terms 一一对应，已结束的为 nil。
type incoming struct {
	conn  *net.UnixConn
	batch handoffBatch
	ptys  []*os.File
	ln    *net.UnixListener
}

// receiveHandoff 在新进程启动时调用，环境里有交接 socket 时接下旧进程交来的东西（还没回话）。
func receiveHandoff() (*incoming, error) {
	v := os.Getenv(handoffEnv)
	if v == "" {
		return nil, nil
	}
	os.Unsetenv(handoffEnv)
	fd, err := strconv.Atoi(v)
	if err != nil {
		return nil, fmt.Errorf("%s=%q", handoffEnv, v)
	}
	unix.CloseOnExec(fd)
	f := os.NewFile(uintptr(fd), "handoff")
	c, err := net.FileConn(f)
	f.Close()
	if err != nil {
		return nil, err
	}
	in := &incoming{conn: c.(*net.UnixConn)}
	if err := in.receive(); err != nil {
		in.close()
		return nil, fmt.Errorf("receive handoff: %w", err)
	}
	return in, nil
}

func (in *incoming) receive() error {
	c := in.conn
	c.SetDeadline(time.Now().Add(handoffTimeout))
	if err := writeFrame(c, handoffHello{Version: handoffVersion, Build: Build}); err != nil {
		return err
	}
	if err := readFrame(c, &in.batch); err != nil {
		return err
	}
	if in.batch.Version != handoffVersion {
		return fmt.Errorf("handoff v%d, want v%d", in.batch.Version, handoffVersion)
	}
	lf, err := recvFile(c, "possed.sock")
	if err != nil {
		return err
	}
	ln, err := net.FileListener(lf)
	lf.Close()
	if err != nil {
		return err
	}
	in.ln = ln.(*net.UnixListener)
	in.ptys = make([]*os.File, len(in.batch.Terms))
	for i, t := range in.batch.Terms {
		if t.Term.Ended {
			continue
		}
		if in.ptys[i], err = recvFile(c, "pty"); err != nil {
			return err
		}
	}
	return nil
}

// answer 告诉旧进程接好了（err 为空）或接不了，旧进程据此退出或恢复。
func (in *incoming) answer(err error) error {
	var ack handoffAck
	if err != nil {
		ack.Error = err.Error()
	}
	werr := writeFrame(in.conn, ack)
	in.conn.Close()
	return werr
}

func (in *incoming) close() {
	in.conn.Close()
	if in.ln != nil {
		in.ln.SetUnlinkOnClose(false)
		in.ln.Close()
	}
	for _, f := range in.ptys {
		if f != nil {
			f.Close()
		}
	}
}

// adopt 按交接记录重建所有终端。
func (d *Daemon) adopt(in *incoming) error {
	for i, ht := range in.batch.Terms {
		t, err := d.terminalHost().Adopt(host.StartOptions{Scrollback: d.cfg.Scrollback, OnTitle: d.onTitle, OnExit: d.onExit, OnProgramStatus: d.onProgramStatus}, ht.Term, in.ptys[i])
		if err != nil {
			return fmt.Errorf("terminal %s: %w", ht.Term.ID, err)
		}
		in.ptys[i] = nil
		info := t.Info()
		s := &session{Terminal: t, ID: info.ID, WorkspaceID: info.WorkspaceID, Dir: info.Dir, Command: info.Command, CreatedAt: info.CreatedAt,
			agent: ht.Agent, hooked: ht.Hooked, agentPg: ht.HookedPg, fg: ht.Fg, unread: ht.Unread, runtime: ht.Runtime, discussionTurn: ht.DiscussionTurn}
		// 标签名和恢复信息不在交接记录里：两边共用一个数据库
		if r, err := d.store.TerminalResume(s.ID); err == nil {
			s.name, s.saved = r.Title, resumeRec{agent: r.Agent, sid: r.SessionID, cwd: r.Cwd, label: r.Label}
		}
		d.mu.Lock()
		d.terms[s.ID] = s
		d.mu.Unlock()
	}
	return nil
}

func (in *incoming) ids() []string {
	var ids []string
	for _, t := range in.batch.Terms {
		if !t.Term.Ended {
			ids = append(ids, t.Term.ID)
		}
	}
	return ids
}

// --- 帧：8 字节长度加 JSON；描述符单独一条只带 1 字节的消息，收的时候恰好读 1 字节，不会和后面的数据混在一起

func writeFrame(c *net.UnixConn, v any) error {
	b, err := json.Marshal(v)
	if err != nil {
		return err
	}
	var hdr [8]byte
	binary.BigEndian.PutUint64(hdr[:], uint64(len(b)))
	if _, err := c.Write(hdr[:]); err != nil {
		return err
	}
	_, err = c.Write(b)
	return err
}

func readFrame(c *net.UnixConn, v any) error {
	var hdr [8]byte
	if _, err := io.ReadFull(c, hdr[:]); err != nil {
		return err
	}
	n := binary.BigEndian.Uint64(hdr[:])
	if n > 1<<30 {
		return fmt.Errorf("frame too large: %d", n)
	}
	b := make([]byte, n)
	if _, err := io.ReadFull(c, b); err != nil {
		return err
	}
	return json.Unmarshal(b, v)
}

// sendFile 经 SCM_RIGHTS 发一个描述符。不调 Fd()：它会把和 PTY 共用的打开状态改成阻塞模式。
// 前面的交接记录对方还没读完时，macOS 的 sendmsg 带描述符不等缓冲区腾出来，直接报 EMSGSIZE：
// 终端多、回滚长时记录有几 MB，一定会碰上，隔一会儿重发，最多等 sendFileWait。
func sendFile(c *net.UnixConn, f *os.File) error {
	rc, err := f.SyscallConn()
	if err != nil {
		return err
	}
	deadline := time.Now().Add(sendFileWait)
	for {
		var werr error
		if err := rc.Control(func(fd uintptr) {
			_, _, werr = c.WriteMsgUnix([]byte{0}, unix.UnixRights(int(fd)), nil)
		}); err != nil {
			return err
		}
		if !errors.Is(werr, unix.EMSGSIZE) || time.Now().After(deadline) {
			return werr
		}
		time.Sleep(5 * time.Millisecond)
	}
}

const sendFileWait = 10 * time.Second

func recvFile(c *net.UnixConn, name string) (*os.File, error) {
	buf := make([]byte, 1)
	oob := make([]byte, unix.CmsgSpace(4))
	_, oobn, _, _, err := c.ReadMsgUnix(buf, oob)
	if err != nil {
		return nil, err
	}
	msgs, err := unix.ParseSocketControlMessage(oob[:oobn])
	if err != nil || len(msgs) != 1 {
		return nil, fmt.Errorf("no descriptor received (%v)", err)
	}
	fds, err := unix.ParseUnixRights(&msgs[0])
	if err != nil || len(fds) != 1 {
		return nil, fmt.Errorf("no descriptor received (%v)", err)
	}
	unix.CloseOnExec(fds[0])
	return os.NewFile(uintptr(fds[0]), name), nil
}
