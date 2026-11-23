package history

import (
	"bufio"
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"modernc.org/sqlite"

	"posse/internal/agent"
)

const zcodeHistoryPrefix = "zcode://"

// snapshotZCodeDB 通过 SQLite 读主库及 WAL。app-server 启动时会迁移库，只把副本交给它。
func snapshotZCodeDB(ctx context.Context, path, dest string) error {
	u := url.URL{Scheme: "file", Path: path, RawQuery: "mode=ro&_pragma=busy_timeout(500)"}
	db, err := sql.Open("sqlite", u.String())
	if err != nil {
		return err
	}
	defer db.Close()
	conn, err := db.Conn(ctx)
	if err != nil {
		return err
	}
	defer conn.Close()
	return conn.Raw(func(driver any) error {
		b, err := driver.(interface {
			NewBackup(string) (*sqlite.Backup, error)
		}).NewBackup(dest)
		if err != nil {
			return err
		}
		for ctx.Err() == nil {
			more, err := b.Step(256)
			if err != nil {
				b.Finish()
				return err
			}
			if !more {
				return b.Finish()
			}
		}
		b.Finish()
		return ctx.Err()
	})
}

type zcodeHistoryConn struct {
	in   io.WriteCloser
	out  *bufio.Reader
	seq  int
	dead bool
}

const zcodeFrameLimit = 16 << 20

var errZCodeFrameLimit = errors.New("ZCode history response exceeds the read limit")

// readFrame 丢弃超限帧的剩余内容，保留换行之后的协议数据供后续请求使用。
func (c *zcodeHistoryConn) readFrame() ([]byte, error) {
	var frame []byte
	large := false
	for {
		part, err := c.out.ReadSlice('\n')
		if len(frame)+len(part) > zcodeFrameLimit {
			frame, large = nil, true
		}
		if !large {
			frame = append(frame, part...)
		}
		switch {
		case err == bufio.ErrBufferFull:
			continue
		case err != nil:
			c.dead = true
			return nil, err
		case large:
			return nil, errZCodeFrameLimit
		default:
			return frame, nil
		}
	}
}

func (c *zcodeHistoryConn) call(method string, params any, out any) error {
	c.seq++
	if err := json.NewEncoder(c.in).Encode(map[string]any{"id": c.seq, "method": method, "params": params}); err != nil {
		c.dead = true
		return err
	}
	for {
		frame, err := c.readFrame()
		if err != nil {
			return err
		}
		var reply struct {
			ID     int             `json:"id"`
			Method string          `json:"method"`
			Result json.RawMessage `json:"result"`
			Error  json.RawMessage `json:"error"`
		}
		if err := json.Unmarshal(frame, &reply); err != nil {
			return err
		}
		if reply.Method != "" || reply.ID != c.seq {
			continue
		}
		if len(reply.Error) != 0 {
			return fmt.Errorf("ZCode history request %s failed", method)
		}
		return json.Unmarshal(reply.Result, out)
	}
}

func withZCodeHistory(ctx context.Context, cli, dir string, fn func(*zcodeHistoryConn) error) error {
	cmd := exec.CommandContext(ctx, cli, "app-server", "--stdio")
	cmd.Dir = dir
	cmd.Env = []string{"PATH=" + os.Getenv("PATH"), "HOME=" + dir, "ZCODE_DATA_BASE_DIR=" + dir,
		"ZCODE_SESSION_DB_PATH=" + filepath.Join(dir, "db.sqlite")}
	if builtin := os.Getenv("ZCODE_BUILTIN_PROVIDER_CONFIG_FILE"); builtin != "" {
		cmd.Env = append(cmd.Env, "ZCODE_BUILTIN_PROVIDER_CONFIG_FILE="+builtin)
	}
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	cmd.Cancel = func() error { return syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL) }
	cmd.WaitDelay = time.Second
	in, err := cmd.StdinPipe()
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := cmd.StdoutPipe()
	if err != nil {
		return err
	}
	defer out.Close()
	if err := cmd.Start(); err != nil {
		return err
	}
	err = fn(&zcodeHistoryConn{in: in, out: bufio.NewReaderSize(out, 64<<10)})
	in.Close()
	if err != nil {
		cmd.Cancel()
	}
	if waitErr := cmd.Wait(); err == nil {
		err = waitErr
	}
	return err
}

type zcodeSummary struct {
	ID        string `json:"sessionId"`
	Title     string `json:"title"`
	CreatedAt int64  `json:"createdAt"`
	UpdatedAt int64  `json:"updatedAt"`
	Workspace struct {
		Path string `json:"workspacePath"`
	} `json:"workspace"`
}

func (c *zcodeHistoryConn) messages(s zcodeSummary) (fileState, []Message, error) {
	st := fileState{SessionID: s.ID, Title: s.Title, Cwd: s.Workspace.Path, Created: s.CreatedAt, Updated: s.UpdatedAt}
	var msgs []Message
	cursor := ""
	textBytes := 0
	pageSize := 64
	for {
		params := map[string]any{"sessionId": s.ID, "direction": "forward", "limit": pageSize}
		if cursor != "" {
			params["afterMessageId"] = cursor
		}
		var page struct {
			Messages []struct {
				Info struct {
					ID        string `json:"messageId"`
					SessionID string `json:"sessionId"`
					Role      string `json:"role"`
					Time      struct {
						Created int64 `json:"created"`
					} `json:"time"`
					Model struct {
						ID string `json:"modelId"`
					} `json:"model"`
				} `json:"info"`
				Parts []struct {
					Type string `json:"type"`
					Text string `json:"text"`
				} `json:"parts"`
			} `json:"messages"`
		}
		if err := c.call("session/messages", params, &page); err != nil {
			if errors.Is(err, errZCodeFrameLimit) && pageSize > 1 {
				pageSize /= 2
				continue
			}
			return st, nil, err
		}
		if page.Messages == nil {
			return st, nil, fmt.Errorf("missing ZCode history messages")
		}
		for _, msg := range page.Messages {
			if msg.Info.SessionID != s.ID || msg.Info.ID == "" || cursor == msg.Info.ID {
				return st, nil, fmt.Errorf("unexpected ZCode history message identity")
			}
			cursor = msg.Info.ID
			if msg.Info.Role != "user" && msg.Info.Role != "assistant" {
				continue
			}
			if msg.Info.Model.ID != "" {
				st.Model = msg.Info.Model.ID
			}
			var parts []string
			for _, p := range msg.Parts {
				if p.Type == "text" && p.Text != "" {
					parts = append(parts, p.Text)
				}
			}
			if len(parts) == 0 {
				continue
			}
			text := strings.Join(parts, "\n")
			textBytes += len(text)
			if textBytes > maxLine {
				return st, nil, fmt.Errorf("ZCode history session exceeds the index read limit")
			}
			if st.FirstPrompt == "" && msg.Info.Role == "user" {
				st.FirstPrompt = text
			}
			st.Seq++
			msgs = append(msgs, Message{Seq: st.Seq, Role: msg.Info.Role, Text: text, At: time.UnixMilli(msg.Info.Time.Created)})
		}
		if len(page.Messages) < pageSize {
			return st, msgs, nil
		}
	}
}

func (x *Index) updateZCode(ctx context.Context, roots Roots, known map[string]knownFile, seen map[string]bool, redo bool) (bool, error) {
	// 命令缺失、数据库忙或读到一半时，保留上次结果。完整读到列表才能清理已删除的会话。
	markKnown := func() {
		for path := range known {
			if strings.HasPrefix(path, zcodeHistoryPrefix) {
				seen[path] = true
			}
		}
	}
	if roots.ZCodeCLI == "" {
		markKnown()
		return false, nil
	}
	path := agent.ZCodeDatabase(roots.Home)
	if fi, err := os.Stat(path); err != nil || !fi.Mode().IsRegular() {
		markKnown()
		return false, nil
	}
	stamp := path + "\n" + cursorDBStamp(path)
	var previous string
	_ = x.db.QueryRowContext(ctx, `SELECT value FROM meta WHERE key='zcode-db'`).Scan(&previous)
	if !redo && previous == stamp {
		markKnown()
		return true, nil
	}
	ctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	dir, err := os.MkdirTemp("", "posse-zcode-history-")
	if err == nil {
		defer os.RemoveAll(dir)
		err = snapshotZCodeDB(ctx, path, filepath.Join(dir, "db.sqlite"))
	}
	if err != nil {
		markKnown()
		return false, err
	}
	const listLimit = 10000
	listed := false
	err = withZCodeHistory(ctx, roots.ZCodeCLI, dir, func(c *zcodeHistoryConn) error {
		var list struct {
			Sessions []zcodeSummary `json:"sessions"`
		}
		if err := c.call("session/list", map[string]int{"limit": listLimit}, &list); err != nil {
			return err
		}
		if list.Sessions == nil {
			return fmt.Errorf("missing ZCode history sessions")
		}
		if len(list.Sessions) >= listLimit {
			markKnown()
		}
		for _, s := range list.Sessions {
			if agent.IsZCodeSession(s.ID) {
				seen[zcodeHistoryPrefix+s.ID] = true
			}
		}
		listed = true
		var firstErr error
		var failures int
		for _, s := range list.Sessions {
			if err := ctx.Err(); err != nil {
				return err
			}
			if !agent.IsZCodeSession(s.ID) {
				continue
			}
			key := zcodeHistoryPrefix + s.ID
			k := known[key]
			fp, _ := json.Marshal(s)
			fp = append([]byte(parserVersions["zcode"]+"\n"), fp...)
			// 一次更新超时前已成功写入的会话，下次从尚未完成的会话继续。
			if k.id != 0 && bytes.Equal(k.tail, fp) {
				continue
			}
			st, msgs, err := c.messages(s)
			if err != nil {
				if c.dead {
					return err
				}
				failures++
				if firstErr == nil {
					firstErr = fmt.Errorf("%s: %w", s.ID, err)
				}
				continue
			}
			if err := x.save(ctx, source{agent: "zcode", transcript: key}, k, true, st, msgs, 0, 0, 0, fp, 0); err != nil {
				return err
			}
		}
		if firstErr != nil {
			return fmt.Errorf("ZCode history could not index %d sessions: %w", failures, firstErr)
		}
		return nil
	})
	if err != nil {
		if !listed {
			markKnown()
		}
		return false, err
	}
	_, err = x.db.ExecContext(ctx, `INSERT INTO meta(key, value) VALUES ('zcode-db', ?) ON CONFLICT(key) DO UPDATE SET value=excluded.value`, stamp)
	return err == nil, err
}
