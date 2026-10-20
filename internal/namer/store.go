package namer

import (
	"bytes"
	"database/sql"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"

	_ "modernc.org/sqlite"
)

// 运行中的 Codex、Grok 不重读这些存储，也没有 hook 能返回标题：写进去之后 resume 列表、
// Hive 的历史页马上变，它们自己界面上的名字要重开会话才变。写法照它们各自的 /rename。

var stateDBRe = regexp.MustCompile(`^state_(\d+)\.sqlite$`)

// codexStateDB 找版本号最大的 state_N.sqlite。
func codexStateDB(home string) (string, error) {
	entries, err := os.ReadDir(home)
	if err != nil {
		return "", fmt.Errorf("找不到 Codex 目录 %s", home)
	}
	best, path := -1, ""
	for _, e := range entries {
		if m := stateDBRe.FindStringSubmatch(e.Name()); m != nil {
			if n, _ := strconv.Atoi(m[1]); n > best {
				best, path = n, filepath.Join(home, e.Name())
			}
		}
	}
	if path == "" {
		return "", fmt.Errorf("%s 下没有 state_*.sqlite", home)
	}
	return path, nil
}

// WriteCodex 写 Codex 线程名：resume 列表经 app-server 读 state 库，新格式（paginated）的线程显示 name，
// 旧格式显示 title，再回退到 session_index.jsonl；Codex 自己 /rename 时两处都写，这里照做。
func WriteCodex(home, threadID, title string) error {
	path, err := codexStateDB(home)
	if err != nil {
		return err
	}
	db, err := sql.Open("sqlite", "file:"+path+"?mode=rw&_pragma=busy_timeout(3000)")
	if err != nil {
		return err
	}
	defer db.Close()
	var mode sql.NullString
	if err := db.QueryRow(`SELECT history_mode FROM threads WHERE id = ?`, threadID).Scan(&mode); err != nil {
		if err == sql.ErrNoRows {
			return fmt.Errorf("Codex 的 state 库里没有线程 %s", threadID)
		}
		return err
	}
	column := "title"
	if mode.String == "paginated" {
		column = "name"
	}
	if _, err := db.Exec(`UPDATE threads SET `+column+` = ? WHERE id = ?`, title, threadID); err != nil {
		return err
	}
	line, _ := json.Marshal(map[string]string{
		"id": threadID, "thread_name": title,
		"updated_at": time.Now().UTC().Format("2006-01-02T15:04:05.000000Z"),
	})
	return appendLine(filepath.Join(home, "session_index.jsonl"), line)
}

// appendLine 追加一行；文件末尾缺换行时先补上，免得和上一行粘在一起。
func appendLine(path string, line []byte) error {
	f, err := os.OpenFile(path, os.O_RDWR|os.O_CREATE|os.O_APPEND, 0o644)
	if err != nil {
		return err
	}
	defer f.Close()
	var prefix []byte
	if fi, err := f.Stat(); err == nil && fi.Size() > 0 {
		last := make([]byte, 1)
		if _, err := f.ReadAt(last, fi.Size()-1); err == nil && last[0] != '\n' {
			prefix = []byte("\n")
		}
	}
	_, err = f.Write(append(append(prefix, line...), '\n'))
	return err
}

// grokSummary 找 grok 会话的 summary.json：分组目录是工作目录按 URL 编码（A-Za-z0-9-_.~ 之外都转义）；
// 目录太长时 grok 改用别的命名，只能按会话 ID 找。
func grokSummary(home, sessionID, cwd string) string {
	sessions := filepath.Join(home, "sessions")
	if cwd != "" {
		p := filepath.Join(sessions, grokEscape(cwd), sessionID, "summary.json")
		if _, err := os.Stat(p); err == nil {
			return p
		}
	}
	matches, _ := filepath.Glob(filepath.Join(sessions, "*", sessionID, "summary.json"))
	if len(matches) == 0 {
		return ""
	}
	return matches[0]
}

func grokEscape(s string) string {
	var b strings.Builder
	for i := 0; i < len(s); i++ {
		c := s[i]
		if 'A' <= c && c <= 'Z' || 'a' <= c && c <= 'z' || '0' <= c && c <= '9' || strings.IndexByte("-_.~", c) >= 0 {
			b.WriteByte(c)
		} else {
			fmt.Fprintf(&b, "%%%02X", c)
		}
	}
	return b.String()
}

// WriteGrok 照 grok 的 /rename 写 summary.json：generated_title、session_summary，
// 并置 title_is_manual，grok 的自动标题此后不再覆盖。其余字段原样保留。
func WriteGrok(home, sessionID, cwd, title string) error {
	path := grokSummary(home, sessionID, cwd)
	if path == "" {
		return fmt.Errorf("找不到 grok 会话 %s 的 summary.json", sessionID)
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	var data map[string]json.RawMessage
	if err := json.Unmarshal(raw, &data); err != nil || data == nil {
		return fmt.Errorf("%s 不是 JSON 对象，不覆盖", path)
	}
	t, _ := json.Marshal(title)
	data["generated_title"], data["session_summary"], data["title_is_manual"] = t, t, json.RawMessage("true")
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	enc.SetIndent("", "  ")
	if err := enc.Encode(data); err != nil {
		return err
	}
	return writeAtomic(path, buf.Bytes())
}

// writeAtomic 在同一目录写临时文件再改名，保留原来的权限位。
func writeAtomic(path string, b []byte) error {
	mode := os.FileMode(0o644)
	if fi, err := os.Stat(path); err == nil {
		mode = fi.Mode().Perm()
	}
	f, err := os.CreateTemp(filepath.Dir(path), "."+filepath.Base(path)+".*.tmp")
	if err != nil {
		return err
	}
	tmp := f.Name()
	_, err = f.Write(b)
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err == nil {
		err = os.Chmod(tmp, mode)
	}
	if err == nil {
		err = os.Rename(tmp, path)
	}
	if err != nil {
		os.Remove(tmp)
	}
	return err
}
