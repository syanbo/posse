// Package daemon 是常驻进程：托管终端、管理项目和工作区，通过 unix socket 对外提供 RPC。
package daemon

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net"
	"os"
	"path/filepath"
	"runtime/debug"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"time"

	"posse/internal/agent"
	"posse/internal/history"
	"posse/internal/host"
	"posse/internal/mobile"
	"posse/internal/procmem"
	"posse/internal/rpc"
	"posse/internal/spawn"
	"posse/internal/store"
	"posse/internal/term"
)

const Version = "0.1.18"

type Config struct {
	Home       string
	Scrollback int
	// HistoryRoots 非空时在进程内更新会话历史索引并从这里读会话文件，测试用
	HistoryRoots *history.Roots
}

func SocketPath(home string) string { return filepath.Join(home, "possed.sock") }

// session 是一个托管中的终端及其在 posse 里的元数据。
type session struct {
	host.Terminal
	// 建好后不变的信息，从 Terminal.Info 抄一份，免得到处调方法
	ID          string
	WorkspaceID string
	Dir         string
	Command     []string
	CreatedAt   time.Time

	mu      sync.Mutex
	agent   string // 终端里的 agent：启动时指定的，或之后在前台认出来的
	hooked  bool   // agent 是 posse 启动的那个，带着注入的 hooks
	agentPg int    // agent 的进程组（组长就是 agent 进程），在前台认出它或收到 hook 时记下，0 是还不知道
	fg      int    // 上次检查时的前台进程组
	// grok 上次空闲时标题里的会话名：工作中的标题只是状态，手机的标签名用它
	grokName string
	name     string    // 用户给标签起的名字（terminal.rename），桌面和手机共用，存在 terminals.title
	saved    resumeRec // 库里记着的恢复信息（saveResume）
	settling bool      // agent 不在了，已经约好 resumeSettle 之后复查
	unread   bool      // 本轮完成了还没看（unread.go）
	viewers  int       // 正看着它的手机页面数
	runtime  string    // 拉起这个终端的常驻进程所在的副本目录，不在副本里运行时为空；它活着副本就不能删
	hookSeq  uint64    // 收到的 hook 事件计数：本轮结束时交了提醒，靠它看 agent 之后有没有接着做
}

func (s *session) userName() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.name
}

// markHooked 在收到 hook 事件时调用：用户在 shell 里经 posse 的同名脚本手动启动的 agent 也带着 hooks，
// 状态以 hooks 为准，不再只看标题。
func (s *session) markHooked(name string) {
	pg := s.Foreground()
	// hook 可能在 agent 让出前台（开着编辑器）时到达，这时的前台不是 agent，不能记成它的进程组
	isAgent := false
	if pg > 0 {
		found, _ := agent.OfProcess(pg)
		isAgent = found == name
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if name != s.agent {
		s.agentPg = 0
	}
	s.agent, s.hooked = name, true
	if isAgent {
		s.agentPg = pg
	}
	if pg > 0 {
		s.fg = pg
	}
}

// clearAgent 在 agent 退出后摘掉它，终端退回普通 shell；之后在前台认出新的 agent 再挂上。
// pg 不为 0 时只在记下的还是这个进程组时才摘，免得检查期间换上的新 agent 被摘掉。
func (s *session) clearAgent(pg int) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.agent == "" || pg != 0 && pg != s.agentPg {
		return false
	}
	s.agent, s.hooked, s.agentPg = "", false, 0
	return true
}

// goneAgentPg 返回已经不在了的 agent 进程组，agent 还在或不知道时返回 0。崩溃、被杀、
// 没有 hooks 的 agent 退出都不发 SessionEnd，只能这样发现；只看前台不行，agent 临时打开编辑器
// 也会让出前台。看组长而不是整个组：agent 退出后它拉起的 MCP 服务等可能还留在组里。
func (s *session) goneAgentPg() int {
	s.mu.Lock()
	name, pg := s.agent, s.agentPg
	s.mu.Unlock()
	if name == "" || pg <= 0 {
		return 0
	}
	if err := syscall.Kill(pg, 0); errors.Is(err, syscall.ESRCH) {
		return pg
	}
	// 进程还在但 exec 成了别的程序（如 agent 退出后 exec 回 shell）；读不到 argv 时当作还在
	if found, args := agent.OfProcess(pg); args != nil && found != name {
		return pg
	}
	return 0
}

// terminalHost 是托管终端的宿主；测试里直接构造的 Daemon 没设，用进程内的。
func (d *Daemon) terminalHost() host.Host {
	if d.host == nil {
		return host.Local{}
	}
	return d.host
}

func (s *session) isHooked() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.hooked
}

func (s *session) agentName() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.agent
}

// detectAgent 看终端前台是哪个 agent。用户在 shell 里自己启动了另一个 agent（或者退出后
// 再手动启动一次）时换成它，这时没有 hooks。前台回到 shell 或别的程序时不改：
// agent 临时打开编辑器、跑 ! 命令也会让出前台，不能当成退出。
// 换了 agent 时 sid 是从它的参数里读到的会话 ID（SessionFromArgs），读不到为空。
func (s *session) detectAgent() (name string, hooked, changed bool, sid string) {
	pg := s.Foreground()
	s.mu.Lock()
	defer s.mu.Unlock()
	if pg <= 0 || pg == s.fg {
		return s.agent, s.hooked, false, ""
	}
	s.fg = pg
	found, args := agent.OfProcess(pg)
	switch {
	case found == "":
	case s.hooked && found == s.agent && (s.agentPg == 0 || s.agentPg == pg):
		s.agentPg = pg
	case found != s.agent || s.hooked:
		s.agent, s.hooked, s.agentPg, changed = found, false, pg, true
		sid = agent.SessionFromArgs(found, args)
	default:
		s.agentPg = pg
	}
	return s.agent, s.hooked, changed, sid
}

type Daemon struct {
	cfg      Config
	store    *store.Store
	started  time.Time
	launcher agent.Launcher
	runtime  string // 自己所在的副本目录（spawn.RuntimeDir），不在副本里运行时为空

	mu    sync.Mutex
	terms map[string]*session

	tmu      sync.Mutex
	waiting  map[string]int // 正阻塞在 task wait 里的派发方终端（同一终端可能同时有几次）
	tchanged chan struct{}  // 任务或消息变化时关闭并替换
	nudgeMu  sync.Mutex
	stopping atomic.Bool
	// exiting 在常驻进程开始退出（收到信号、daemon.stop）时置上：这之后终端和 agent 的退出都不记，
	// 留着等下次启动恢复
	exiting   atomic.Bool
	restoreMu sync.Mutex  // 恢复终端一个一个来，同一个终端不会拉起两次
	noLost    atomic.Bool // 已经没有等着恢复的终端了（lostIn）

	history  historyState
	mobile   *mobile.Server
	host     host.Host // 托管终端，测试里为空时用进程内的
	awake    *awake    // 防休眠，测试里为空
	desk     deskState // 手机看桌面，同一时间一台
	usage    usageState
	gitTrace *gitTrace // 终端里的 git 命令，测试里为空
	ln       *gateListener
	handoff  handoffState
	agents   agentScan // 本机装了哪些 agent（agents.go）

	// zsh 集成和同名脚本（agent.WriteShellFiles）：写成功一次就不再写
	shellMu     sync.Mutex
	shellReady  bool
	userZdotdir string // 用户原来的 ZDOTDIR
}

func (d *Daemon) shellFiles() error {
	d.shellMu.Lock()
	defer d.shellMu.Unlock()
	if d.shellReady {
		return nil
	}
	if err := agent.WriteShellFiles(d.cfg.Home, d.launcher.Exe); err != nil {
		log.Printf("shell integration: %v", err)
		return err
	}
	d.shellReady = true
	return nil
}

// userZdotdir 取用户原来的 ZDOTDIR。常驻进程可能是从 posse 终端里拉起的，
// 那时 ZDOTDIR 已经指向 posse 的集成目录，原来的在 POSSE_USER_ZDOTDIR 里。
func userZdotdir(home string) string {
	z := os.Getenv("ZDOTDIR")
	if z == agent.ZshDir(home) {
		return os.Getenv("POSSE_USER_ZDOTDIR")
	}
	return z
}

func Run(ctx context.Context, cfg Config) error {
	// Go 堆的软上限；libghostty-vt 的内存不受此限制，靠回滚上限控制
	debug.SetMemoryLimit(64 << 20)
	if cfg.Scrollback == 0 {
		cfg.Scrollback = term.DefaultScrollback
	}
	if err := os.MkdirAll(cfg.Home, 0o700); err != nil {
		return err
	}
	// 升级交接：旧进程还活着、持着锁，先把终端接过来，回话之后旧进程才退出、放锁
	in, err := receiveHandoff()
	if err != nil {
		return err
	}
	sock := SocketPath(cfg.Home)
	lockPath := filepath.Join(cfg.Home, "possed.lock")
	var lockMu sync.Mutex
	var unlock func()
	if in == nil {
		if unlock, err = lock(lockPath, 10*time.Second); err != nil {
			return err
		}
		// 交接过来的新进程拿到监听后不等锁就开始服务：socket 有人应答就说明它在，不能删掉它的 socket
		if c, err := net.DialTimeout("unix", sock, 500*time.Millisecond); err == nil {
			c.Close()
			unlock()
			return errors.New("another daemon is serving " + sock)
		}
		// 持有锁之后才动 socket：旧进程可能仍在关闭终端，必须等它完全退出
		os.Remove(sock)
	}

	st, err := store.Open(filepath.Join(cfg.Home, "posse.db"))
	if err != nil {
		if in != nil {
			in.answer(err)
			in.close()
		}
		return err
	}
	defer st.Close()

	exe, err := os.Executable()
	if err != nil {
		return err
	}
	if resolved, err := filepath.EvalSymlinks(exe); err == nil {
		exe = resolved
	}
	if err := spawn.Retain(exe, cfg.Home); err != nil {
		log.Printf("runtime copy: %v", err)
	}
	hookExe, err := linkPosse(cfg.Home, exe)
	if err != nil {
		log.Printf("link %s: %v", PosseLink(cfg.Home), err)
		hookExe = exe
	}
	d := &Daemon{cfg: cfg, store: st, started: time.Now(), terms: map[string]*session{}, tchanged: make(chan struct{}),
		launcher: agent.Launcher{Exe: hookExe, Home: cfg.Home}, runtime: spawn.RuntimeDir(exe),
		awake: newAwake(cfg.Home), userZdotdir: userZdotdir(cfg.Home), host: host.Local{}}

	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	context.AfterFunc(ctx, func() { d.exiting.Store(true) })
	var keep []string
	var ln *net.UnixListener
	if in != nil {
		err := d.adopt(in)
		if aerr := in.answer(err); err == nil && aerr != nil {
			err = aerr
		}
		if err != nil {
			in.close()
			return fmt.Errorf("adopt: %w", err)
		}
		keep, ln = in.ids(), in.ln
		// 回话成功才开始读 PTY：失败时旧进程恢复读，这边不能先读走输出
		d.mu.Lock()
		for _, s := range d.terms {
			s.Resume()
		}
		d.mu.Unlock()
		log.Printf("took over %d terminals", len(in.batch.Terms))
	}
	defer func() {
		lockMu.Lock()
		if unlock != nil {
			unlock()
		}
		lockMu.Unlock()
	}()

	if n, err := st.MarkRunningLost(time.Now(), keep); err != nil {
		return err
	} else if n > 0 {
		log.Printf("%d terminals from the previous daemon are waiting to be restored", n)
	}
	if err := st.PruneTerminals(); err != nil {
		return err
	}
	if err := st.MarkRunningTasksLost(time.Now(), keep); err != nil {
		return err
	}
	// 交接过来的终端还用着旧进程的副本，上一个常驻进程崩溃时留下的登记在这里去掉
	go d.collectRuntimes("")

	if ln == nil {
		l, err := net.Listen("unix", sock)
		if err != nil {
			return err
		}
		os.Chmod(sock, 0o600)
		ln = l.(*net.UnixListener)
	}
	d.ln = newGateListener(ln)
	// 交接出去之后 socket 和 pid 文件归新进程
	defer func() {
		if !d.handoff.handedOff() {
			os.Remove(sock)
		}
	}()
	pidFile := filepath.Join(cfg.Home, "possed.pid")
	os.WriteFile(pidFile, []byte(strconv.Itoa(os.Getpid())), 0o600)
	defer func() {
		if !d.handoff.handedOff() {
			os.Remove(pidFile)
		}
	}()

	defer d.awake.close()
	go d.awakeLoop(ctx)
	d.openUsage()
	d.openGitTrace(ctx)
	go d.agentLoop(ctx)
	go d.detectLoop(ctx)
	d.newMobile()
	if in == nil {
		d.startMobile()
	} else {
		// 交接过来的不等锁就开始服务（hook 只等 2 秒），旧进程退出放锁之后才开手机服务：端口在它手里。
		// 一直等下去，等不到也不退出，终端都在这个进程里
		go func() {
			for ctx.Err() == nil {
				u, err := lock(lockPath, time.Minute)
				if err != nil {
					log.Printf("waiting for the old daemon to exit: %v", err)
					continue
				}
				lockMu.Lock()
				unlock = u
				lockMu.Unlock()
				d.startMobile()
				return
			}
		}()
	}
	// 交接过来的进程不自动恢复：留下的终端在旧进程启动时就该恢复了，没恢复的是用户还没点开的
	if in == nil && agent.RestoreAll(cfg.Home) {
		go d.restoreAll()
	}
	srv := rpc.NewServer()
	d.register(srv, cancel)
	log.Printf("posse daemon %s (%s) listening on %s (pid %d)", Version, Build, sock, os.Getpid())
	err = srv.Serve(ctx, d.ln)
	if d.mobile != nil {
		d.mobile.Close()
	}
	d.closeAll()
	d.closeHistory()
	return err
}

// lock 独占 path 上的文件锁，保证一个 home 只有一个常驻进程。
func lock(path string, wait time.Duration) (func(), error) {
	f, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, err
	}
	deadline := time.Now().Add(wait)
	for {
		err = syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB)
		if err == nil {
			return func() { f.Close() }, nil
		}
		if time.Now().After(deadline) {
			f.Close()
			return nil, fmt.Errorf("another daemon holds %s", path)
		}
		time.Sleep(100 * time.Millisecond)
	}
}

func (d *Daemon) closeAll() {
	// Serve 出错返回时 ctx 还没取消，这里也要置上，关掉的终端留着恢复
	d.exiting.Store(true)
	// 交接出去的终端由新进程接着跑
	if d.handoff.handedOff() {
		return
	}
	// 常驻进程退出时关掉的 worker 不算意外退出，也不再提醒任何人
	d.stopping.Store(true)
	d.closeSessions(d.sessionsIn(""), 2*time.Second)
}

func (d *Daemon) register(srv *rpc.Server, stop context.CancelFunc) {
	srv.Handle("daemon.status", d.status)
	srv.Handle("desk.stop", d.deskStop)
	srv.Handle("daemon.handoff", func(ctx context.Context, raw json.RawMessage) (any, error) {
		return d.daemonHandoff(ctx, raw, stop)
	})
	srv.Handle("awake.set", d.awakeSet)
	srv.Handle("usage.get", d.usageGet)
	srv.Handle("usage.set", d.usageSet)
	srv.Handle("usage.refresh", d.usageRefresh)
	srv.Handle("session.title", d.sessionTitle)
	srv.Handle("git.trace", d.gitTraceList)
	srv.Handle("usage.report", d.usageReport)
	srv.Handle("daemon.stop", func(ctx context.Context, _ json.RawMessage) (any, error) {
		// 先回复再退出
		time.AfterFunc(100*time.Millisecond, stop)
		return map[string]bool{"stopping": true}, nil
	})
	srv.Handle("project.add", d.projectAdd)
	srv.Handle("project.list", d.projectList)
	srv.Handle("project.set", d.projectSet)
	srv.Handle("project.rm", d.projectRm)
	srv.Handle("workspace.create", d.workspaceCreate)
	srv.Handle("workspace.list", d.workspaceList)
	srv.Handle("workspace.show", d.workspaceShow)
	srv.Handle("workspace.current", d.workspaceCurrent)
	srv.Handle("workspace.rm", d.workspaceRm)
	srv.Handle("workspace.relocate", d.workspaceRelocate)
	srv.Handle("terminal.create", d.terminalCreate)
	srv.Handle("terminal.list", d.terminalList)
	srv.Handle("terminal.show", d.terminalShow)
	srv.Handle("terminal.read", d.terminalRead)
	srv.Handle("terminal.send", d.terminalSend)
	srv.Handle("terminal.wait", d.terminalWait)
	srv.Handle("terminal.resize", d.terminalResize)
	srv.Handle("terminal.clear", d.terminalClear)
	srv.Handle("terminal.rename", d.terminalRename)
	srv.Handle("terminal.seen", d.terminalSeen)
	srv.Handle("terminal.reclaimSize", d.terminalReclaimSize)
	srv.Handle("terminal.close", d.terminalClose)
	srv.Handle("workspace.diff", d.workspaceDiff)
	srv.Handle("workspace.merge", d.workspaceMerge)
	srv.Handle("task.start", d.taskStart)
	srv.Handle("task.list", d.taskList)
	srv.Handle("task.show", d.taskShow)
	srv.Handle("task.done", d.taskDone)
	srv.Handle("task.ask", d.taskAsk)
	srv.Handle("task.check", d.taskCheck)
	srv.Handle("task.wait", d.taskWait)
	srv.Handle("task.reply", d.taskReply)
	srv.Handle("task.send", d.taskSend)
	srv.Handle("task.close", d.taskClose)
	srv.Handle("task.clear", d.taskClear)
	srv.Handle("history.search", d.historySearch)
	srv.Handle("history.list", d.historyList)
	srv.Handle("history.show", d.historyShow)
	srv.Handle("history.resume", d.historyResume)
	srv.Handle("history.index", d.historyIndexNow)
	srv.Handle("hook.event", d.hookEvent)
	srv.Handle("hook.permission.hold", d.hookPermission)
	srv.Handle("mobile.pair", d.mobilePair)
	srv.Handle("mobile.devices", d.mobileDevices)
	srv.Handle("mobile.rm", d.mobileRm)
	srv.Handle("relay.status", d.relayStatus)
	srv.Handle("relay.set", d.relaySet)
	srv.Handle("relay.clear", d.relayClear)
	srv.Handle("relay.invite", d.relayInvite)
	srv.HandleStream("terminal.attach", d.terminalAttach)
}

type statusResult struct {
	Version   string    `json:"version"`
	Build     string    `json:"build"`
	Pid       int       `json:"pid"`
	Home      string    `json:"home"`
	StartedAt time.Time `json:"startedAt"`
	Terminals int       `json:"terminals"`
	Running   int       `json:"running"`
	RSSKB     int64     `json:"rssKB"`
	// FootprintKB 是 phys_footprint，与活动监视器一致；内存预算以它为准
	FootprintKB int64        `json:"footprintKB"`
	Awake       *awakeStatus `json:"awake,omitempty"`
	Usage       *usageStatus `json:"usage,omitempty"`
	// GitTraceSeq 是终端 git 命令记录的最新序号，界面看到它变了再取（git.trace）
	GitTraceSeq int64 `json:"gitTraceSeq,omitempty"`
	// MobileOnline 是现在连着的手机的名字，界面在侧栏「手机」上标出来
	MobileOnline []string `json:"mobileOnline,omitempty"`
	// DeskViewer 是正在看桌面的手机，界面在手机页写出来并给「停止」（desk.stop）
	DeskViewer string `json:"deskViewer,omitempty"`
	// DeskMissing 是看桌面缺的系统授权：screen、accessibility。看过一次才知道
	DeskMissing []string `json:"deskMissing,omitempty"`
	// Agents 是本机装了的 agent，界面和手机只列这些；还没检测完时为 null
	Agents []string `json:"agents"`
}

func (d *Daemon) status(ctx context.Context, _ json.RawMessage) (any, error) {
	d.mu.Lock()
	total, running := len(d.terms), 0
	for _, s := range d.terms {
		if s.Running() {
			running++
		}
	}
	d.mu.Unlock()
	r := statusResult{
		Version: Version, Build: Build, Pid: os.Getpid(), Home: d.cfg.Home, StartedAt: d.started,
		Terminals: total, Running: running,
		RSSKB: int64(procmem.Resident(os.Getpid()) >> 10), FootprintKB: int64(procmem.Footprint(os.Getpid()) >> 10),
	}
	if d.awake != nil {
		st := d.awake.status()
		r.Awake = &st
	}
	r.Usage = d.usageStatus()
	r.GitTraceSeq = d.gitTrace.latest()
	if d.mobile != nil {
		for _, dv := range d.mobile.Devices() {
			if dv.Online {
				r.MobileOnline = append(r.MobileOnline, dv.Name)
			}
		}
	}
	r.DeskViewer, r.DeskMissing = d.deskStatus()
	r.Agents = d.installedAgents()
	return r, nil
}

// --- 查找

func (d *Daemon) session(id string) (*session, error) {
	if id == "" {
		return nil, rpc.Errorf(rpc.CodeBadRequest, "terminal id required")
	}
	if s := d.liveSession(id); s != nil {
		return s, nil
	}
	// 上一个常驻进程留下的终端在第一次用到时恢复
	return d.restoreTerminal(id, 0, 0)
}

// liveSession 只找正托管着的终端，不恢复留下的。
func (d *Daemon) liveSession(id string) *session {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.terms[id]
}

func (d *Daemon) project(sel string) (store.Project, error) {
	if sel == "" {
		return store.Project{}, rpc.Errorf(rpc.CodeBadRequest, "project required")
	}
	p, err := d.store.FindProject(sel)
	if errors.Is(err, store.ErrNotFound) {
		return p, rpc.Errorf(rpc.CodeNotFound, "project %q not found", sel)
	}
	return p, err
}

func (d *Daemon) workspace(sel string) (store.Workspace, error) {
	if sel == "" {
		return store.Workspace{}, rpc.Errorf(rpc.CodeBadRequest, "workspace required")
	}
	ws, err := d.store.FindWorkspaces(sel)
	if err != nil {
		return store.Workspace{}, err
	}
	switch len(ws) {
	case 0:
		return store.Workspace{}, rpc.Errorf(rpc.CodeNotFound, "workspace %q not found", sel)
	case 1:
		return ws[0], nil
	}
	var names []string
	for _, w := range ws {
		names = append(names, d.workspaceLabel(w)+" ("+w.ID+")")
	}
	return store.Workspace{}, rpc.Errorf(rpc.CodeConflict, "workspace %q is ambiguous: %s", sel, strings.Join(names, ", "))
}

func (d *Daemon) workspaceLabel(w store.Workspace) string {
	p, err := d.store.FindProject(w.ProjectID)
	if err != nil {
		return w.Name
	}
	return p.Name + "/" + w.Name
}

// workspaceIndex 是一次请求里批量查好的工作区和项目名，列表接口用它代替逐条查询。
type workspaceIndex struct {
	workspaces map[string]store.Workspace
	projects   map[string]string // 项目 ID → 名称
}

func (d *Daemon) loadWorkspaceIndex(withWorkspaces bool) (*workspaceIndex, error) {
	ps, err := d.store.Projects()
	if err != nil {
		return nil, err
	}
	x := &workspaceIndex{projects: make(map[string]string, len(ps))}
	for _, p := range ps {
		x.projects[p.ID] = p.Name
	}
	if withWorkspaces {
		ws, err := d.store.Workspaces("")
		if err != nil {
			return nil, err
		}
		x.workspaces = make(map[string]store.Workspace, len(ws))
		for _, w := range ws {
			x.workspaces[w.ID] = w
		}
	}
	return x, nil
}

// label 与 workspaceLabel 相同。
func (x *workspaceIndex) label(w store.Workspace) string {
	if name, ok := x.projects[w.ProjectID]; ok {
		return name + "/" + w.Name
	}
	return w.Name
}

// closeSession 结束终端并从托管列表里移除。
func (d *Daemon) closeSession(s *session, grace time.Duration) {
	s.Close(grace)
	d.mu.Lock()
	delete(d.terms, s.ID)
	d.mu.Unlock()
	// 用户关的马上记成退出（onExit 要等 resumeSettle 才记）；常驻进程退出时关的留着恢复
	if !d.exiting.Load() {
		code := s.ExitCode()
		if err := d.store.EndTerminal(s.ID, "exited", &code, time.Now()); err != nil {
			log.Printf("terminal %s: record exit: %v", s.ID, err)
		}
	}
}

// closeSessions 并行关闭多个终端，等全部结束后返回。
func (d *Daemon) closeSessions(ss []*session, grace time.Duration) {
	var wg sync.WaitGroup
	for _, s := range ss {
		wg.Go(func() { d.closeSession(s, grace) })
	}
	wg.Wait()
}

func (d *Daemon) sessionsIn(workspaceID string) []*session {
	d.mu.Lock()
	defer d.mu.Unlock()
	var out []*session
	for _, s := range d.terms {
		if workspaceID == "" || s.WorkspaceID == workspaceID {
			out = append(out, s)
		}
	}
	sortSessions(out)
	return out
}

// sessionsByWorkspace 按工作区分组全部终端，各组按创建时间排序。
func (d *Daemon) sessionsByWorkspace() map[string][]*session {
	d.mu.Lock()
	out := map[string][]*session{}
	for _, s := range d.terms {
		out[s.WorkspaceID] = append(out[s.WorkspaceID], s)
	}
	d.mu.Unlock()
	for _, ss := range out {
		sortSessions(ss)
	}
	return out
}

func sortSessions(ss []*session) {
	sort.Slice(ss, func(i, j int) bool { return ss[i].CreatedAt.Before(ss[j].CreatedAt) })
}
