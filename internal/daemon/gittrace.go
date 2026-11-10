package daemon

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log"
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"posse/internal/gitx"
	"posse/internal/rpc"
)

// Posse 终端里的 git 命令，给界面的 Git 操作记录用：终端环境带 GIT_TRACE2_EVENT=af_unix:<git-trace.sock>，
// git 的每个进程连一次 socket，把 trace2 事件逐行写过来（start 带参数、def_repo 带仓库、exit 带退出码），
// 再用 GIT_TRACE2_ENV_VARS 让它报出 POSSE_TERMINAL_ID。没人监听时 git 静默跳过，终端里看不到任何东西。
// 只记最外层、会改动仓库的命令（gitx.ChangesRepo）：git 自己拉起的子进程（pull 里的 fetch、maintenance）
// 和查询不记。记录追加到 $POSSE_HOME/git-trace.jsonl，重启、交接之后接着用。

const (
	// gitTraceMax 是每个项目留多少条，超过丢掉这个项目最早的：一个项目命令多不挤掉别的项目的
	gitTraceMax = 500
	// gitTraceLine 是一行事件最长多少：start 的参数可能很长（git add 一长串文件）
	gitTraceLine = 1 << 20
)

type gitTraceRecord struct {
	Seq      int64     `json:"seq"`
	Terminal string    `json:"terminal,omitempty"`
	Project  string    `json:"project,omitempty"` // 终端所在的项目，按它分别限条数
	Agent    string    `json:"agent,omitempty"`   // 命令执行时终端里的 agent
	Dir      string    `json:"dir,omitempty"`     // 仓库的工作目录；不在仓库里（clone、init）时是终端的目录
	Args     []string  `json:"args"`
	Start    time.Time `json:"start"`
	TookMs   int64     `json:"tookMs"`
	Code     int       `json:"code"` // 退出码；没等到 exit 事件（被杀）是 -1
	// 手机做的操作（切换分支）也记在这里：Source 是 mobile，Device 是手机名，Err 是 git 的报错
	Source string `json:"source,omitempty"`
	Device string `json:"device,omitempty"`
	Err    string `json:"err,omitempty"`
}

type gitTrace struct {
	path  string
	mu    sync.Mutex
	recs  []gitTraceRecord
	seq   int64
	lines int // 文件里的行数，攒多了按内存里的重写
}

func gitTraceSocket(home string) string { return filepath.Join(home, "git-trace.sock") }

// gitTraceEnv 是给终端加的环境。用户自己设了别的 GIT_TRACE2_EVENT 时不动它。
func gitTraceEnv(home string) []string {
	target := "af_unix:" + gitTraceSocket(home)
	if v := os.Getenv("GIT_TRACE2_EVENT"); v != "" && v != target {
		return nil
	}
	vars := "POSSE_TERMINAL_ID"
	if v := os.Getenv("GIT_TRACE2_ENV_VARS"); v != "" && v != vars {
		vars = v + "," + vars
	}
	return []string{"GIT_TRACE2_EVENT=" + target, "GIT_TRACE2_ENV_VARS=" + vars}
}

// openGitTrace 读回记录、开始监听。交接时新进程先删掉旧 socket 再监听，旧进程手里的监听自然收不到新连接；
// 退出时不删 socket 文件，免得删掉新进程的。
func (d *Daemon) openGitTrace(ctx context.Context) {
	g := &gitTrace{path: filepath.Join(d.cfg.Home, "git-trace.jsonl")}
	g.load()
	d.gitTrace = g
	sock := gitTraceSocket(d.cfg.Home)
	os.Remove(sock)
	ln, err := net.Listen("unix", sock)
	if err != nil {
		log.Printf("git trace: %v", err)
		return
	}
	os.Chmod(sock, 0o600)
	// Close 默认会删 socket 文件：交接后旧进程晚于新进程重建它才退出
	ln.(*net.UnixListener).SetUnlinkOnClose(false)
	go func() {
		<-ctx.Done()
		ln.Close()
	}()
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			go d.readGitTrace(c)
		}
	}()
}

type trace2Event struct {
	Event    string    `json:"event"`
	Sid      string    `json:"sid"`
	Time     time.Time `json:"time"`
	Argv     []string  `json:"argv"`
	Worktree string    `json:"worktree"`
	Param    string    `json:"param"`
	Value    string    `json:"value"`
	Code     int       `json:"code"`
	TAbs     float64   `json:"t_abs"`
}

// readGitTrace 读一个 git 进程的事件。不记的连接（子进程、查询）也要读到底，git 写不进去会卡住。
func (d *Daemon) readGitTrace(c net.Conn) {
	defer c.Close()
	sc := bufio.NewScanner(c)
	sc.Buffer(make([]byte, 64<<10), gitTraceLine)
	var r gitTraceRecord
	started, ended := false, false
	for sc.Scan() {
		line := sc.Bytes()
		// 大多数行是 region、data 之类，先粗筛再解码
		if !bytes.Contains(line, []byte(`"event":"start"`)) && !bytes.Contains(line, []byte(`"event":"def_repo"`)) &&
			!bytes.Contains(line, []byte(`"event":"def_param"`)) && !bytes.Contains(line, []byte(`"event":"exit"`)) {
			continue
		}
		var e trace2Event
		if json.Unmarshal(line, &e) != nil {
			continue
		}
		if strings.Contains(e.Sid, "/") {
			break
		}
		switch e.Event {
		case "start":
			if len(e.Argv) < 2 || !gitx.ChangesRepo(e.Argv[1:]) {
				io.Copy(io.Discard, c)
				return
			}
			started, r.Args, r.Start = true, e.Argv[1:], e.Time
		case "def_repo":
			if r.Dir == "" {
				r.Dir = e.Worktree
			}
		case "def_param":
			if e.Param == "POSSE_TERMINAL_ID" {
				r.Terminal = e.Value
			}
		case "exit":
			ended, r.Code, r.TookMs = true, e.Code, int64(e.TAbs*1000)
		}
	}
	io.Copy(io.Discard, c)
	if !started {
		return
	}
	if !ended {
		r.Code = -1
		r.TookMs = time.Since(r.Start).Milliseconds()
	}
	if s := d.liveSession(r.Terminal); s != nil {
		r.Agent = s.agentName()
		if r.Dir == "" {
			r.Dir = s.Dir
		}
		if w, err := d.store.Workspace(s.WorkspaceID); err == nil {
			r.Project = w.ProjectID
		}
	}
	d.gitTrace.add(r)
	d.traceGit(r)
}

func (g *gitTrace) load() {
	b, err := os.ReadFile(g.path)
	if err != nil {
		return
	}
	for _, line := range bytes.Split(b, []byte("\n")) {
		var r gitTraceRecord
		if json.Unmarshal(line, &r) != nil || r.Seq == 0 {
			continue
		}
		g.lines++
		g.recs = append(g.recs, r)
		g.seq = max(g.seq, r.Seq)
	}
	g.recs = trimPerProject(g.recs)
}

// trimPerProject 每个项目只留最近 gitTraceMax 条；不在 Posse 终端里的（没有项目）按目录算。
func trimPerProject(recs []gitTraceRecord) []gitTraceRecord {
	count := map[string]int{}
	keep := make([]bool, len(recs))
	n := 0
	for i := len(recs) - 1; i >= 0; i-- {
		key := recs[i].Project
		if key == "" {
			key = "dir:" + recs[i].Dir
		}
		if count[key]++; count[key] <= gitTraceMax {
			keep[i] = true
			n++
		}
	}
	if n == len(recs) {
		return recs
	}
	out := make([]gitTraceRecord, 0, n)
	for i, r := range recs {
		if keep[i] {
			out = append(out, r)
		}
	}
	return out
}

func (g *gitTrace) add(r gitTraceRecord) {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.seq++
	r.Seq = g.seq
	g.recs = trimPerProject(append(g.recs, r))
	// 文件攒到留着的两倍（至少 2 倍上限）时按内存里的重写
	if g.lines >= 2*max(len(g.recs), gitTraceMax) {
		var buf bytes.Buffer
		for _, x := range g.recs {
			line, _ := json.Marshal(x)
			buf.Write(append(line, '\n'))
		}
		if writeFileAtomic(g.path, buf.Bytes()) == nil {
			g.lines = len(g.recs)
		}
		return
	}
	line, _ := json.Marshal(r)
	f, err := os.OpenFile(g.path, os.O_WRONLY|os.O_CREATE|os.O_APPEND, 0o600)
	if err != nil {
		return
	}
	f.Write(append(line, '\n'))
	f.Close()
	g.lines++
}

// since 返回 after 之后的记录和最新的序号。
func (g *gitTrace) since(after int64) ([]gitTraceRecord, int64) {
	if g == nil {
		return nil, 0
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	out := []gitTraceRecord{}
	for _, r := range g.recs {
		if r.Seq > after {
			out = append(out, r)
		}
	}
	return out, g.seq
}

func (g *gitTrace) latest() int64 {
	if g == nil {
		return 0
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.seq
}

// gitTraceList 给界面：after 之后的终端 git 命令。
func (d *Daemon) gitTraceList(ctx context.Context, raw json.RawMessage) (any, error) {
	var p struct {
		After int64 `json:"after"`
	}
	if err := rpc.Decode(raw, &p); err != nil {
		return nil, err
	}
	recs, seq := d.gitTrace.since(p.After)
	return map[string]any{"records": recs, "seq": seq}, nil
}

func writeFileAtomic(path string, b []byte) error {
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, b, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}
