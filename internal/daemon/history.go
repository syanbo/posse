package daemon

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"posse/internal/agent"
	"posse/internal/history"
	"posse/internal/host"
	"posse/internal/rpc"
	"posse/internal/store"
)

// historyFresh 内的索引视为最新；超过后下一次查询先做一次增量更新。
const historyFresh = 15 * time.Second

// historyWait 是查询等增量更新的最长时间；首次建索引要更久，先用已有的结果返回。
const historyWait = 3 * time.Second

// historyState 管理会话历史索引。索引在子进程（posse history index）里更新：
// 首次要读几 GB 的会话文件，放在子进程里，常驻进程的内存不受影响，读完就还给系统。
type historyState struct {
	mu          sync.Mutex
	idx         *history.Index // 常驻进程只读查询用的连接
	running     chan struct{}  // 更新进行中时非空，结束时关闭
	progress    history.Progress
	lastRun     time.Time
	lastErr     string
	revision    uint64                       // 删除使进行中的旧索引失效，不能把旧轮完成当作已刷新。
	gen         uint64                       // 一轮索引做完就加一，历史页靠它刷新，不必每秒重查。
	again       bool                         // 这一轮还在跑时目录又有变化，做完再来一轮。
	pageUntil   time.Time                    // 历史页还开着的截止时间；过了就停掉目录监视。
	watchStop   *historyWatch                // 非空表示正在监视会话目录。
	watchRoots  func() []string              // 测试替换要监视的目录，免得盯上用户本机的会话。
	moveToTrash func(string) (string, error) // 测试替换为隔离回收目录。
	// cancel 在常驻进程退出时停掉进行中的更新
	ctx    context.Context
	cancel context.CancelFunc
}

func HistoryDBPath(home string) string { return filepath.Join(home, "history.db") }

func (d *Daemon) historyIndex() (*history.Index, error) {
	h := &d.history
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.idx != nil {
		return h.idx, nil
	}
	idx, err := history.OpenReader(HistoryDBPath(d.cfg.Home))
	if err != nil {
		return nil, err
	}
	h.idx = idx
	return idx, nil
}

func (d *Daemon) closeHistory() {
	h := &d.history
	h.mu.Lock()
	running := h.running
	if h.watchStop != nil {
		h.watchStop.cancel()
	}
	if h.cancel != nil {
		h.cancel()
	}
	h.mu.Unlock()
	if running != nil {
		<-running
	}
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		h.mu.Lock()
		on := h.watchStop != nil
		h.mu.Unlock()
		if !on {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.idx != nil {
		h.idx.Close()
		h.idx = nil
	}
}

// refreshHistory 在索引过期时启动一次更新，最多等 wait；返回更新是否仍在进行。
func (d *Daemon) refreshHistory(ctx context.Context, force bool, wait time.Duration) bool {
	h := &d.history
	h.mu.Lock()
	done := h.running
	if done == nil && (force || time.Since(h.lastRun) > historyFresh) {
		if h.ctx == nil {
			h.ctx, h.cancel = context.WithCancel(context.Background())
		}
		done = make(chan struct{})
		h.running = done
		h.progress = history.Progress{}
		h.again = false
		go d.runHistoryUpdate(h.ctx, done, h.revision)
	} else if done != nil && force {
		h.again = true
	}
	h.mu.Unlock()
	if done == nil {
		return false
	}
	t := time.NewTimer(wait)
	defer t.Stop()
	select {
	case <-done:
		return false
	case <-t.C:
		return true
	case <-ctx.Done():
		return true
	}
}

func (d *Daemon) runHistoryUpdate(ctx context.Context, done chan struct{}, revision uint64) {
	h := &d.history
	err := d.updateHistory(ctx, func(p history.Progress) {
		h.mu.Lock()
		h.progress = p
		h.mu.Unlock()
	})
	h.mu.Lock()
	h.running = nil
	h.lastRun = time.Now()
	if h.revision != revision {
		h.lastRun = time.Time{}
	} else {
		h.gen++
	}
	again := h.again
	h.again = false
	h.lastErr = ""
	if err != nil {
		h.lastErr = err.Error()
		log.Printf("history index: %v", err)
	}
	h.mu.Unlock()
	close(done)
	if again && ctx.Err() == nil {
		d.refreshHistory(context.Background(), true, 0)
	}
}

// updateHistory 默认起子进程更新索引；测试里 cfg.HistoryRoots 非空时在进程内更新。
func (d *Daemon) updateHistory(ctx context.Context, progress func(history.Progress)) error {
	if d.cfg.HistoryRoots != nil {
		idx, err := history.Open(HistoryDBPath(d.cfg.Home))
		if err != nil {
			return err
		}
		defer idx.Close()
		return idx.Update(ctx, *d.cfg.HistoryRoots, progress)
	}
	ctx, cancel := context.WithTimeout(ctx, 30*time.Minute)
	defer cancel()
	cmd := exec.CommandContext(ctx, d.launcher.Exe, "history", "index", "--progress")
	cmd.Env = append(os.Environ(), "POSSE_HOME="+d.cfg.Home)
	if path := d.zcodeDatabase(); path != "" {
		cmd.Env = append(cmd.Env, "ZCODE_SESSION_DB_PATH="+path)
	}
	d.agents.mu.Lock()
	path := d.agents.path
	d.agents.mu.Unlock()
	if path != "" {
		cmd.Env = append(cmd.Env, "PATH="+path)
	}
	out, err := cmd.StdoutPipe()
	if err != nil {
		return err
	}
	var stderr strings.Builder
	cmd.Stderr = &stderr
	if err := cmd.Start(); err != nil {
		return err
	}
	sc := bufio.NewScanner(out)
	for sc.Scan() {
		var p history.Progress
		if json.Unmarshal(sc.Bytes(), &p) == nil {
			progress(p)
		}
	}
	if err := cmd.Wait(); err != nil {
		return fmt.Errorf("%v: %s", err, strings.TrimSpace(stderr.String()))
	}
	return nil
}

type historyStatus struct {
	history.Stats
	Indexing bool             `json:"indexing"`
	Progress history.Progress `json:"progress"`
	Error    string           `json:"error,omitempty"`
}

func (d *Daemon) historyStatus(ctx context.Context, indexing bool) historyStatus {
	st := historyStatus{Indexing: indexing}
	var statsErr error
	if idx, err := d.historyIndex(); err == nil {
		st.Stats, statsErr = idx.Stats(ctx)
	}
	h := &d.history
	h.mu.Lock()
	st.Progress, st.Error = h.progress, h.lastErr
	h.mu.Unlock()
	if statsErr != nil && st.Error == "" {
		st.Error = statsErr.Error()
	}
	return st
}

type historyQueryParams struct {
	Query string `json:"query"`
	Agent string `json:"agent"`
	Cwd   string `json:"cwd"`
	Role  string `json:"role"`
	Limit int    `json:"limit"`
}

func (p historyQueryParams) validate() error {
	if p.Agent != "" && !contains(history.Agents, p.Agent) {
		return rpc.Errorf(rpc.CodeBadRequest, "unknown agent %q (known: %s)", p.Agent, strings.Join(history.Agents, ", "))
	}
	if p.Role != "" && !contains([]string{"user", "assistant", "tool"}, p.Role) {
		return rpc.Errorf(rpc.CodeBadRequest, "role must be user, assistant or tool")
	}
	return nil
}

func contains(list []string, s string) bool {
	for _, v := range list {
		if v == s {
			return true
		}
	}
	return false
}

type historySearchResult struct {
	Hits []history.Hit `json:"hits"`
	// Any 为真表示没有结果同时含所有词，列出的是含任一词的
	Any    bool          `json:"any,omitempty"`
	Status historyStatus `json:"status"`
}

type historyListResult struct {
	Sessions []history.Session `json:"sessions"`
	Status   historyStatus     `json:"status"`
}

func (d *Daemon) historySearch(ctx context.Context, raw json.RawMessage) (any, error) {
	var p historyQueryParams
	if err := rpc.Decode(raw, &p); err != nil {
		return nil, err
	}
	if err := p.validate(); err != nil {
		return nil, err
	}
	indexing := d.refreshHistory(ctx, false, historyWait)
	idx, err := d.historyIndex()
	if err != nil {
		return nil, err
	}
	opts := history.SearchOptions{ListOptions: history.ListOptions{Agent: p.Agent, Cwd: p.Cwd, Limit: p.Limit}, Role: p.Role}
	hits, any, err := idx.Search(ctx, p.Query, opts)
	if err != nil {
		return nil, err
	}
	if hits == nil {
		hits = []history.Hit{}
	}
	return historySearchResult{Hits: hits, Any: any, Status: d.historyStatus(ctx, indexing)}, nil
}

func (d *Daemon) historyList(ctx context.Context, raw json.RawMessage) (any, error) {
	var p historyQueryParams
	if err := rpc.Decode(raw, &p); err != nil {
		return nil, err
	}
	if err := p.validate(); err != nil {
		return nil, err
	}
	indexing := d.refreshHistory(ctx, false, historyWait)
	idx, err := d.historyIndex()
	if err != nil {
		return nil, err
	}
	ss, err := idx.Sessions(ctx, history.ListOptions{Agent: p.Agent, Cwd: p.Cwd, Limit: p.Limit})
	if err != nil {
		return nil, err
	}
	if ss == nil {
		ss = []history.Session{}
	}
	return historyListResult{Sessions: ss, Status: d.historyStatus(ctx, indexing)}, nil
}

func (d *Daemon) historyBrief(ctx context.Context, raw json.RawMessage) (any, error) {
	var p struct {
		Cwd  string `json:"cwd"`
		Skip string `json:"skip"`
	}
	if err := rpc.Decode(raw, &p); err != nil {
		return nil, err
	}
	p.Cwd = filepath.Clean(p.Cwd)
	if !filepath.IsAbs(p.Cwd) || filepath.Dir(p.Cwd) == p.Cwd {
		return nil, rpc.Errorf(rpc.CodeBadRequest, "history brief requires an absolute project directory")
	}
	indexing := d.refreshHistory(ctx, false, historyWait)
	idx, err := d.historyIndex()
	if err != nil {
		return nil, err
	}
	dirs, err := d.store.AntigravitySessionDirs(ctx)
	if err != nil {
		return nil, err
	}
	var ids []string
	dirRoot := resolvePath(p.Cwd)
	for id, dir := range dirs {
		if rel, err := filepath.Rel(dirRoot, resolvePath(dir)); err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
			ids = append(ids, id)
		}
	}
	brief, err := idx.Brief(ctx, p.Cwd, p.Skip, ids...)
	if err != nil {
		return nil, err
	}
	return struct {
		Brief  string        `json:"brief"`
		Status historyStatus `json:"status"`
	}{Brief: brief, Status: d.historyStatus(ctx, indexing)}, nil
}

type historyShowParams struct {
	Session string `json:"session"`
	// Path 是会话文件，有它时按文件找：同一个 ID 可能对应几个不同的会话（见 history.FindPath）
	Path string `json:"path"`
	Tail int    `json:"tail"` // 只要最后几条，0 为全部
	// Tools 为 false 时不要工具消息；界面只传 session，不传时包含
	Tools *bool `json:"tools"`
	// Context 只在详情需要时读目标会话文件，列表、删除前检查和手机查询不额外读文件。
	Context bool `json:"context"`
}

type historyShowResult struct {
	Session  history.Session   `json:"session"`
	Messages []history.Message `json:"messages"`
	// Terminal 是正在 posse 里运行这个会话的终端
	Terminal string           `json:"terminal,omitempty"`
	Context  *history.Context `json:"context,omitempty"`
}

func (d *Daemon) findHistorySession(ctx context.Context, id string) (history.Session, error) {
	if id == "" {
		return history.Session{}, rpc.Errorf(rpc.CodeBadRequest, "session required")
	}
	idx, err := d.historyIndex()
	if err != nil {
		return history.Session{}, err
	}
	s, err := idx.Find(ctx, id)
	if errors.Is(err, history.ErrNotFound) {
		// 刚开的会话可能还没进索引
		d.refreshHistory(ctx, true, historyWait)
		s, err = idx.Find(ctx, id)
	}
	if errors.Is(err, history.ErrNotFound) {
		return s, rpc.Errorf(rpc.CodeNotFound, "session %q not found", id)
	}
	if err != nil && strings.Contains(err.Error(), "ambiguous") {
		return s, rpc.Errorf(rpc.CodeConflict, "%v", err)
	}
	return s, err
}

func (d *Daemon) findHistoryPath(ctx context.Context, path string) (history.Session, error) {
	idx, err := d.historyIndex()
	if err != nil {
		return history.Session{}, err
	}
	s, err := idx.FindPath(ctx, path)
	if errors.Is(err, history.ErrNotFound) {
		return s, rpc.Errorf(rpc.CodeNotFound, "session file %q not indexed", path)
	}
	return s, err
}

func (d *Daemon) historyShow(ctx context.Context, raw json.RawMessage) (any, error) {
	var p historyShowParams
	if err := rpc.Decode(raw, &p); err != nil {
		return nil, err
	}
	var s history.Session
	var err error
	if p.Path != "" {
		s, err = d.findHistoryPath(ctx, p.Path)
	} else {
		s, err = d.findHistorySession(ctx, p.Session)
	}
	if err != nil {
		return nil, err
	}
	idx, _ := d.historyIndex()
	msgs, err := idx.Transcript(ctx, s, history.TranscriptOptions{Tail: p.Tail, NoTools: p.Tools != nil && !*p.Tools})
	if err != nil {
		return nil, err
	}
	if msgs == nil {
		msgs = []history.Message{}
	}
	res := historyShowResult{Session: s, Messages: msgs}
	if p.Context {
		var value history.Context
		var ok bool
		if s.Agent == "antigravity" {
			value, ok = history.ReadReportedContext(d.cfg.Home, s.Agent, s.ID, s.Path)
		}
		if !ok {
			value, _ = history.ReadContext(s.Agent, s.Path, s.Model)
		}
		// 空对象表示目标端已查过但没有用量；nil 留给不支持此字段的旧服务端。
		res.Context = &value
	}
	if t := d.sessionRunning(s); t != nil {
		res.Terminal = t.ID
	}
	return res, nil
}

// sessionRunning 找正在运行这个 agent 会话的终端，避免同一会话开两个。
func (d *Daemon) sessionRunning(s history.Session) *session {
	d.mu.Lock()
	defer d.mu.Unlock()
	for _, t := range d.terms {
		if t.agentName() == s.Agent && t.Running() && t.Agent().SessionID == s.ID {
			return t
		}
	}
	return nil
}

type historyResumeParams struct {
	Session   string `json:"session"`
	Path      string `json:"path"`
	Workspace string `json:"workspace"`
	Cols      int    `json:"cols"`
	Rows      int    `json:"rows"`
}

type historyResumeResult struct {
	terminalInfo
	// AlreadyOpen 为真表示会话已经在这个终端里运行，没有新开
	AlreadyOpen bool `json:"alreadyOpen,omitempty"`
}

func (d *Daemon) historyResume(ctx context.Context, raw json.RawMessage) (any, error) {
	// 和待恢复终端、历史删除共用边界，检查运行状态后不能插入另一条恢复。
	d.restoreMu.Lock()
	defer d.restoreMu.Unlock()
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	var p historyResumeParams
	if err := rpc.Decode(raw, &p); err != nil {
		return nil, err
	}
	var s history.Session
	var err error
	if p.Path != "" {
		s, err = d.findHistoryPath(ctx, p.Path)
	} else {
		s, err = d.findHistorySession(ctx, p.Session)
	}
	if err != nil {
		return nil, err
	}
	if p.Path != "" && p.Session != "" && s.ID != p.Session {
		return nil, rpc.Errorf(rpc.CodeConflict, "session file no longer matches the selected session")
	}
	if s.Agent == "cursor" && history.IsCursorIDEChat(s.Path) {
		return nil, rpc.Errorf(rpc.CodeBadRequest, "Cursor IDE chats can't be resumed in a terminal; open them in Cursor")
	}
	if s.Agent == "zcode" {
		if d.installedAgents() != nil && !d.agentInstalled(s.Agent) {
			return nil, rpc.Errorf(rpc.CodeBadRequest, "ZCode interactive CLI is not available on this Mac")
		}
	} else {
		if _, err := os.Stat(s.Path); err != nil {
			return nil, err
		}
	}
	if t := d.sessionRunning(s); t != nil {
		return historyResumeResult{terminalInfo: d.terminalInfo(t), AlreadyOpen: true}, nil
	}
	dir := s.Cwd
	fromPosse := dir == "" && s.Agent == "antigravity" && p.Workspace == ""
	if fromPosse {
		dir, err = d.store.AntigravitySessionDir(ctx, s.ID)
		if err != nil {
			return nil, err
		}
		if !filepath.IsAbs(dir) {
			dir = ""
		}
	}
	if dir != "" {
		// 没留下文件的聊天文件夹在聊天结束时删掉了，恢复时建回来
		if _, err := os.Stat(dir); os.IsNotExist(err) && d.isChatFolder(dir) {
			os.Mkdir(dir, 0o755)
		}
		if fi, err := os.Stat(dir); err != nil || !fi.IsDir() {
			if !fromPosse {
				return nil, rpc.Errorf(rpc.CodeConflict, "the session's directory %s no longer exists", dir)
			}
			dir = ""
		}
	}
	var ws store.Workspace
	switch {
	case p.Workspace != "":
		ws, err = d.workspace(p.Workspace)
	case dir == "":
		err = rpc.Errorf(rpc.CodeWorkspaceRequired, "the session has no recorded directory; pass a workspace")
	default:
		// 会话目录所在的工作区；没有时由调用方决定是否把目录加成项目
		ws, err = d.currentWorkspace("", dir)
		var re *rpc.Error
		if fromPosse && errors.As(err, &re) && re.Code == rpc.CodeNotFound {
			err = rpc.Errorf(rpc.CodeWorkspaceRequired, "the session has no recorded directory; pass a workspace")
		}
	}
	if err != nil {
		return nil, err
	}
	if err := d.checkEnabled(s.Agent); err != nil {
		return nil, err
	}
	l, err := d.launcher.Resume(s.Agent, s.ID, dir)
	if err != nil {
		return nil, rpc.Errorf(rpc.CodeBadRequest, "%v", err)
	}
	// 聊天会话回到它自己的子文件夹：关掉时按它清理，从它派出的 worker 也放进去
	var cwd string
	if d.isChatFolder(dir) {
		cwd = dir
	}
	t, err := d.launchTerminal(ws, l.Argv, s.Agent, p.Cols, p.Rows, store.Terminal{Cwd: cwd})
	if err != nil {
		return nil, err
	}
	// 只记会话 ID 的情况：codex、cursor 改状态的第一个 hook 要到第一轮对话才来，先显示"启动中"就会一直停在那里
	st := host.AgentState{Status: agent.StatusStarting, SessionID: s.ID, At: time.Now()}
	if s.Agent == "codex" || s.Agent == "cursor" || s.Agent == "antigravity" || s.Agent == "zcode" {
		st.Status = ""
	}
	if s.Agent == "zcode" {
		st.Title = s.Title
	}
	t.SetAgent(st)
	return historyResumeResult{terminalInfo: d.terminalInfo(t)}, nil
}

type historyIndexParams struct {
	// Wait 为真时等更新完成再返回
	Wait bool `json:"wait"`
}

func (d *Daemon) historyIndexNow(ctx context.Context, raw json.RawMessage) (any, error) {
	var p historyIndexParams
	if err := rpc.Decode(raw, &p); err != nil {
		return nil, err
	}
	wait := time.Duration(0)
	if p.Wait {
		wait = 30 * time.Minute
	}
	indexing := d.refreshHistory(ctx, true, wait)
	return d.historyStatus(ctx, indexing), nil
}
