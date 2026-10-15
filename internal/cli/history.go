package cli

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"path/filepath"
	"runtime/debug"
	"strings"
	"syscall"
	"time"

	"hive/internal/daemon"
	"hive/internal/history"
)

type historyStatus struct {
	Sessions int
	Messages int
	Indexing bool
	Progress history.Progress
	Error    string
}

func addHistoryFilters(fs *flag.FlagSet) (agent, cwd *string, here *bool, limit *int) {
	agent = fs.String("agent", "", "")
	cwd = fs.String("cwd", "", "")
	here = fs.Bool("here", false, "")
	limit = fs.Int("limit", 0, "")
	return
}

// historyCwd 返回按目录筛选用的绝对路径。agent 记录的是解析过软链接的真实路径
// （/tmp 记成 /private/tmp），这里也解析一遍才对得上。
func historyCwd(cwd string, here bool) (string, error) {
	var dir string
	var err error
	switch {
	case here:
		dir, err = os.Getwd()
	case cwd != "":
		dir, err = filepath.Abs(cwd)
	default:
		return "", nil
	}
	if err != nil {
		return "", err
	}
	if real, err := filepath.EvalSymlinks(dir); err == nil {
		dir = real
	}
	return dir, nil
}

func (e *env) historyNote(st historyStatus) {
	if st.Indexing {
		fmt.Fprintf(e.errOut, "(indexing session history: %d/%d files checked; results may be incomplete)\n", st.Progress.Done, st.Progress.Total)
	}
	if st.Error != "" {
		fmt.Fprintf(e.errOut, "(last index update failed: %s)\n", st.Error)
	}
}

func cmdHistorySearch(e *env, args []string) error {
	fs := flag.NewFlagSet("history search", flag.ContinueOnError)
	agent, cwd, here, limit := addHistoryFilters(fs)
	role := fs.String("role", "", "")
	pos, err := parse(fs, args)
	if err != nil {
		return err
	}
	if err := need(pos, 1, "history search <query> [--agent A] [--cwd DIR | --here] [--role user|assistant|tool] [--limit N]"); err != nil {
		return err
	}
	dir, err := historyCwd(*cwd, *here)
	if err != nil {
		return err
	}
	var res struct {
		Hits   []history.Hit
		Any    bool
		Status historyStatus
	}
	params := map[string]any{"query": strings.Join(pos, " "), "agent": *agent, "cwd": dir, "role": *role, "limit": *limit}
	return e.do("history.search", params, &res, func() {
		e.historyNote(res.Status)
		if len(res.Hits) == 0 {
			fmt.Fprintln(e.out, "no matches")
			return
		}
		if res.Any {
			fmt.Fprintln(e.errOut, "(no message contains every word; showing messages with any of them)")
		}
		bold := isTerminal(os.Stdout)
		for i, h := range res.Hits {
			if i > 0 {
				fmt.Fprintln(e.out)
			}
			matches := "title match"
			switch {
			case h.Matches == 1:
				matches = "1 match"
			case h.Matches > 1:
				matches = fmt.Sprintf("%d matches", h.Matches)
			}
			fmt.Fprintf(e.out, "%s  %s  %s ago  %s\n", h.ID, h.Agent, age(h.UpdatedAt), matches)
			fmt.Fprintf(e.out, "  %s\n", oneLine(h.Title, 100))
			if h.Cwd != "" {
				fmt.Fprintf(e.out, "  %s\n", tildePath(h.Cwd))
			}
			fmt.Fprintf(e.out, "  %s: %s\n", h.Role, highlight(h.Snippet, h.Spans, bold))
		}
	})
}

func cmdHistoryList(e *env, args []string) error {
	fs := flag.NewFlagSet("history list", flag.ContinueOnError)
	agent, cwd, here, limit := addHistoryFilters(fs)
	if _, err := parse(fs, args); err != nil {
		return err
	}
	dir, err := historyCwd(*cwd, *here)
	if err != nil {
		return err
	}
	n := *limit
	if n == 0 {
		n = 30
	}
	var res struct {
		Sessions []history.Session
		Status   historyStatus
	}
	return e.do("history.list", map[string]any{"agent": *agent, "cwd": dir, "limit": n}, &res, func() {
		e.historyNote(res.Status)
		if len(res.Sessions) == 0 {
			fmt.Fprintln(e.out, "no sessions")
			return
		}
		w := e.table()
		fmt.Fprintln(w, "SESSION\tAGENT\tUPDATED\tMSGS\tDIR\tTITLE")
		for _, s := range res.Sessions {
			fmt.Fprintf(w, "%s\t%s\t%s\t%d\t%s\t%s\n", s.ID, s.Agent, age(s.UpdatedAt), s.Messages, dash(tildePath(s.Cwd)), oneLine(s.Title, 60))
		}
		w.Flush()
	})
}

func cmdHistoryShow(e *env, args []string) error {
	fs := flag.NewFlagSet("history show", flag.ContinueOnError)
	tail := fs.Int("tail", 0, "")
	tools := fs.Bool("tools", false, "")
	pos, err := parse(fs, args)
	if err != nil {
		return err
	}
	if err := need(pos, 1, "history show <session> [--tail N] [--tools]"); err != nil {
		return err
	}
	var res struct {
		Session  history.Session
		Messages []history.Message
		Terminal string
	}
	return e.do("history.show", map[string]any{"session": pos[0], "tail": *tail, "tools": *tools}, &res, func() {
		s := res.Session
		fmt.Fprintf(e.out, "%s  %s\n%s\n", s.Agent, s.ID, s.Title)
		if s.Cwd != "" {
			fmt.Fprintln(e.out, tildePath(s.Cwd))
		}
		fmt.Fprintf(e.out, "%s, %d messages, updated %s ago\n", s.Path, s.Messages, age(s.UpdatedAt))
		if res.Terminal != "" {
			fmt.Fprintf(e.out, "running in hive terminal %s\n", res.Terminal)
		}
		for _, m := range res.Messages {
			fmt.Fprintf(e.out, "\n── %s", m.Role)
			if !m.At.IsZero() {
				fmt.Fprintf(e.out, "  %s", m.At.Local().Format("2006-01-02 15:04"))
			}
			fmt.Fprintf(e.out, "\n%s\n", strings.TrimSpace(m.Text))
		}
	})
}

func cmdHistoryResume(e *env, args []string) error {
	fs := flag.NewFlagSet("history resume", flag.ContinueOnError)
	ws := fs.String("workspace", "", "")
	pos, err := parse(fs, args)
	if err != nil {
		return err
	}
	if err := need(pos, 1, "history resume <session> [--workspace W]"); err != nil {
		return err
	}
	var t struct {
		ID          string
		WorkspaceID string
		AlreadyOpen bool
	}
	return e.do("history.resume", map[string]any{"session": pos[0], "workspace": *ws}, &t, func() {
		if t.AlreadyOpen {
			fmt.Fprintf(e.out, "already running in terminal %s\n", t.ID)
			return
		}
		fmt.Fprintf(e.out, "resumed in terminal %s\n", t.ID)
	})
}

// cmdHistoryIndex 在本进程里更新索引。常驻进程用 --progress 调用它，按行读进度。
func cmdHistoryIndex(e *env, args []string) error {
	fs := flag.NewFlagSet("history index", flag.ContinueOnError)
	progress := fs.Bool("progress", false, "")
	rebuild := fs.Bool("rebuild", false, "")
	if _, err := parse(fs, args); err != nil {
		return err
	}
	// 后台批量读文件，让出 CPU 给 agent 和界面
	syscall.Setpriority(syscall.PRIO_PROCESS, 0, 10)
	debug.SetMemoryLimit(128 << 20)
	if err := os.MkdirAll(e.home, 0o700); err != nil {
		return err
	}
	unlock, err := lockFile(filepath.Join(e.home, "history.lock"))
	if err != nil {
		return err
	}
	defer unlock()
	idx, err := history.Open(daemon.HistoryDBPath(e.home))
	if err != nil {
		return err
	}
	defer idx.Close()
	if *rebuild {
		if err := idx.Reset(); err != nil {
			return err
		}
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return err
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	start := time.Now()
	report := func(p history.Progress) {
		if *progress {
			b, _ := json.Marshal(p)
			fmt.Fprintln(e.out, string(b))
		}
	}
	if err := idx.Update(ctx, history.Roots{Home: home}, report); err != nil {
		return err
	}
	if *progress {
		return nil
	}
	st, err := idx.Stats(ctx)
	if err != nil {
		return err
	}
	return e.emit(st, func() {
		fmt.Fprintf(e.out, "indexed %d sessions, %d messages in %s\n", st.Sessions, st.Messages, time.Since(start).Round(time.Millisecond))
		for _, a := range history.Agents {
			if n := st.ByAgent[a]; n > 0 {
				fmt.Fprintf(e.out, "  %-7s %d\n", a, n)
			}
		}
	})
}

// lockFile 阻塞地独占文件锁：同时只有一个进程更新索引，后来的等前一个做完再做增量。
func lockFile(path string) (func(), error) {
	f, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, err
	}
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX); err != nil {
		f.Close()
		return nil, err
	}
	return func() { f.Close() }, nil
}

func isTerminal(f *os.File) bool {
	fi, err := f.Stat()
	return err == nil && fi.Mode()&os.ModeCharDevice != 0
}

func highlight(s string, spans []history.Span, bold bool) string {
	if !bold || len(spans) == 0 {
		return s
	}
	var b strings.Builder
	last := 0
	for _, sp := range spans {
		if sp.Start < last || sp.End > len(s) {
			continue
		}
		b.WriteString(s[last:sp.Start])
		b.WriteString("\x1b[1m" + s[sp.Start:sp.End] + "\x1b[22m")
		last = sp.End
	}
	b.WriteString(s[last:])
	return b.String()
}

func oneLine(s string, n int) string {
	s = strings.Join(strings.Fields(s), " ")
	r := []rune(s)
	if len(r) > n {
		return string(r[:n-1]) + "…"
	}
	return s
}

func tildePath(p string) string {
	if home, err := os.UserHomeDir(); err == nil && p != "" && (p == home || strings.HasPrefix(p, home+"/")) {
		return "~" + p[len(home):]
	}
	return p
}
