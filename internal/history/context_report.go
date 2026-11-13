package history

import (
	"bufio"
	"bytes"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"time"
)

// 上下文读数按会话保存，交接常驻进程后仍可用；不和账号级套餐额度共用开关。
type contextReport struct {
	Context  Context `json:"context"`
	Path     string  `json:"path,omitempty"`
	Size     int64   `json:"size,omitempty"`
	ModTime  int64   `json:"modTime,omitempty"`
	Offset   int64   `json:"offset"`
	At       int64   `json:"at"`
	Snapshot bool    `json:"snapshot"`
}

func contextReportPath(home, session string) string {
	return filepath.Join(home, "session-context", fmt.Sprintf("%x.json", sha256.Sum256([]byte(session))))
}

// ReportClaudeContext 保存 Claude 交给状态栏的读数，不依赖模型名字或套餐类型。
func ReportClaudeContext(home string, payload []byte) error {
	var in struct {
		Session string `json:"session_id"`
		Path    string `json:"transcript_path"`
		Window  *struct {
			Size    int      `json:"context_window_size"`
			Percent *float64 `json:"used_percentage"`
			Usage   *struct {
				Input  int `json:"input_tokens"`
				Create int `json:"cache_creation_input_tokens"`
				Read   int `json:"cache_read_input_tokens"`
			} `json:"current_usage"`
		} `json:"context_window"`
	}
	if json.Unmarshal(payload, &in) != nil || in.Session == "" || in.Window == nil {
		return nil
	}
	w := in.Window
	r := contextReport{Context: Context{Window: w.Size, ReportedPercent: w.Percent}, At: time.Now().UnixNano()}
	if w.Usage != nil {
		r.Context.Used = w.Usage.Input + w.Usage.Create + w.Usage.Read
	}
	// null 出现在首轮请求前和压缩后，保留未知读数，不能再翻出压缩前的 usage。
	if w.Percent == nil && (w.Usage == nil || w.Size <= 0) {
		r.Context = Context{}
	}
	r.Path = TranscriptPath("claude", in.Session, in.Path)
	if f, err := os.Open(r.Path); err == nil {
		defer f.Close()
		if fi, err := f.Stat(); err == nil {
			r.Size, r.ModTime = fi.Size(), fi.ModTime().UnixNano()
			r.Offset, err = contextCompleteOffset(f, r.Size)
			r.Snapshot = err == nil
		}
	}
	b, err := json.Marshal(r)
	if err != nil {
		return err
	}
	path := contextReportPath(home, in.Session)
	if old, err := os.ReadFile(path); err == nil && string(old) == string(b) {
		return nil
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	f, err := os.CreateTemp(filepath.Dir(path), ".context-*")
	if err != nil {
		return err
	}
	defer os.Remove(f.Name())
	if _, err := f.Write(b); err != nil {
		f.Close()
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	return os.Rename(f.Name(), path)
}

// contextCompleteOffset 不越过末尾未写完的一行，避免漏掉随后补全的 usage。
func contextCompleteOffset(f *os.File, end int64) (int64, error) {
	for read := int64(0); end > 0 && read < contextLimit; {
		n := min(int64(64<<10), end)
		b := make([]byte, n)
		if _, err := f.ReadAt(b, end-n); err != nil {
			return 0, err
		}
		if i := bytes.LastIndexByte(b, '\n'); i >= 0 {
			return end - n + int64(i) + 1, nil
		}
		end -= n
		read += n
	}
	if end > 0 {
		return 0, fmt.Errorf("context record exceeds read limit")
	}
	return 0, nil
}

// ReadReportedContext 只有新 usage、压缩或文件重写才使官方读数失效，普通日志不影响它。
// 返回 true 也可能是明确的未知（例如刚压缩），调用方不应回退到旧的会话记录。
func ReadReportedContext(home, agent, session, path string) (Context, bool) {
	if agent != "claude" || session == "" {
		return Context{}, false
	}
	b, err := os.ReadFile(contextReportPath(home, session))
	if err != nil {
		return Context{}, false
	}
	var r contextReport
	if json.Unmarshal(b, &r) != nil {
		return Context{}, false
	}
	if !r.Snapshot {
		// 初次上报可能早于会话文件创建；没有可核对的记录时，只短暂保留读数。
		if r.At == 0 || time.Since(time.Unix(0, r.At)) > time.Minute {
			return Context{}, false
		}
		if fi, err := os.Stat(path); err == nil && fi.ModTime().UnixNano() > r.At {
			return Context{}, false
		}
		return r.Context, true
	}
	if filepath.Clean(r.Path) != filepath.Clean(path) {
		return Context{}, false
	}
	f, err := os.Open(path)
	if err != nil {
		return Context{}, false
	}
	defer f.Close()
	fi, err := f.Stat()
	if err != nil || fi.Size() < r.Size || (fi.Size() == r.Size && fi.ModTime().UnixNano() != r.ModTime) {
		return Context{}, false
	}
	if fi.Size() == r.Size {
		return r.Context, true
	}
	if r.Offset < 0 || r.Offset > r.Size || fi.Size()-r.Offset > contextLimit {
		return Context{}, false
	}
	scan := bufio.NewScanner(io.NewSectionReader(f, r.Offset, fi.Size()-r.Offset))
	scan.Buffer(make([]byte, 64<<10), contextLimit)
	compact, changed := false, false
	for scan.Scan() {
		line := scan.Bytes()
		if claudeCompactBoundary(line) {
			compact = true
		}
		if c, ok := claudeContext(line, ""); ok {
			// 同一回复按内容块分行写入时用量相同，不应撤掉仍然适用的官方读数。
			if compact || r.Context.Used == 0 || c.Used != r.Context.Used {
				changed = true
			}
		}
	}
	if scan.Err() != nil || changed {
		return Context{}, false
	}
	if compact {
		return Context{}, true
	}
	return r.Context, true
}
