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
	"os/exec"
	"path/filepath"
	"runtime/debug"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"time"

	"hive/internal/agent"
	"hive/internal/history"
	"hive/internal/procmem"
	"hive/internal/rpc"
	"hive/internal/store"
	"hive/internal/term"
)

const Version = "0.1.0"

type Config struct {
	Home       string
	Scrollback int
	// HistoryRoots 非空时在进程内更新会话历史索引并从这里读会话文件，测试用
	HistoryRoots *history.Roots
}

func SocketPath(home string) string { return filepath.Join(home, "hived.sock") }

// session 是一个托管中的终端及其在 hive 里的元数据。
type session struct {
	*term.Terminal
	agent string
}

type Daemon struct {
	cfg      Config
	store    *store.Store
	started  time.Time
	launcher agent.Launcher

	mu    sync.Mutex
	terms map[string]*session

	tmu      sync.Mutex
	tchanged chan struct{} // 任务或消息变化时关闭并替换
	nudgeMu  sync.Mutex
	stopping atomic.Bool

	history historyState
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
	unlock, err := lock(filepath.Join(cfg.Home, "hived.lock"), 10*time.Second)
	if err != nil {
		return err
	}
	defer unlock()
	// 持有锁之后才动 socket：旧进程可能仍在关闭终端，必须等它完全退出
	sock := SocketPath(cfg.Home)
	os.Remove(sock)

	st, err := store.Open(filepath.Join(cfg.Home, "hive.db"))
	if err != nil {
		return err
	}
	defer st.Close()
	if n, err := st.MarkRunningLost(time.Now()); err != nil {
		return err
	} else if n > 0 {
		log.Printf("marked %d terminals from the previous daemon as lost", n)
	}
	if err := st.MarkRunningTasksLost(time.Now()); err != nil {
		return err
	}

	ln, err := net.Listen("unix", sock)
	if err != nil {
		return err
	}
	os.Chmod(sock, 0o600)
	defer os.Remove(sock)
	pidFile := filepath.Join(cfg.Home, "hived.pid")
	os.WriteFile(pidFile, []byte(strconv.Itoa(os.Getpid())), 0o600)
	defer os.Remove(pidFile)

	exe, err := os.Executable()
	if err != nil {
		return err
	}
	if resolved, err := filepath.EvalSymlinks(exe); err == nil {
		exe = resolved
	}
	d := &Daemon{cfg: cfg, store: st, started: time.Now(), terms: map[string]*session{}, tchanged: make(chan struct{}),
		launcher: agent.Launcher{Exe: exe, Home: cfg.Home}}
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	srv := rpc.NewServer()
	d.register(srv, cancel)
	log.Printf("hive daemon %s listening on %s (pid %d)", Version, sock, os.Getpid())
	err = srv.Serve(ctx, ln)
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
	// 常驻进程退出时关掉的 worker 不算意外退出，也不再提醒任何人
	d.stopping.Store(true)
	d.mu.Lock()
	terms := make([]*session, 0, len(d.terms))
	for _, s := range d.terms {
		terms = append(terms, s)
	}
	d.mu.Unlock()
	var wg sync.WaitGroup
	for _, s := range terms {
		wg.Add(1)
		go func() {
			defer wg.Done()
			s.Close(2 * time.Second)
		}()
	}
	wg.Wait()
}

func (d *Daemon) register(srv *rpc.Server, stop context.CancelFunc) {
	srv.Handle("daemon.status", d.status)
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
	srv.Handle("terminal.create", d.terminalCreate)
	srv.Handle("terminal.list", d.terminalList)
	srv.Handle("terminal.show", d.terminalShow)
	srv.Handle("terminal.read", d.terminalRead)
	srv.Handle("terminal.send", d.terminalSend)
	srv.Handle("terminal.wait", d.terminalWait)
	srv.Handle("terminal.resize", d.terminalResize)
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
	srv.HandleStream("terminal.attach", d.terminalAttach)
}

type statusResult struct {
	Version   string    `json:"version"`
	Pid       int       `json:"pid"`
	Home      string    `json:"home"`
	StartedAt time.Time `json:"startedAt"`
	Terminals int       `json:"terminals"`
	Running   int       `json:"running"`
	RSSKB     int64     `json:"rssKB"`
	// FootprintKB 是 phys_footprint，与活动监视器一致；内存预算以它为准
	FootprintKB int64 `json:"footprintKB"`
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
	return statusResult{
		Version: Version, Pid: os.Getpid(), Home: d.cfg.Home, StartedAt: d.started,
		Terminals: total, Running: running, RSSKB: selfRSS(), FootprintKB: int64(procmem.Footprint(os.Getpid()) >> 10),
	}, nil
}

// selfRSS 读取本进程常驻内存，包含 libghostty-vt 分配的部分。
func selfRSS() int64 {
	out, err := exec.Command("ps", "-o", "rss=", "-p", strconv.Itoa(os.Getpid())).Output()
	if err != nil {
		return 0
	}
	v, _ := strconv.ParseInt(strings.TrimSpace(string(out)), 10, 64)
	return v
}

// --- 查找

func (d *Daemon) session(id string) (*session, error) {
	if id == "" {
		return nil, rpc.Errorf(rpc.CodeBadRequest, "terminal id required")
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	if s, ok := d.terms[id]; ok {
		return s, nil
	}
	return nil, rpc.Errorf(rpc.CodeNotFound, "terminal %s not found", id)
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

// closeSession 结束终端并从托管列表里移除。
func (d *Daemon) closeSession(s *session) {
	s.Close(2 * time.Second)
	d.mu.Lock()
	delete(d.terms, s.ID)
	d.mu.Unlock()
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
	sort.Slice(out, func(i, j int) bool { return out[i].CreatedAt.Before(out[j].CreatedAt) })
	return out
}
