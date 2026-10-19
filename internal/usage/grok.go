package usage

import (
	"bytes"
	"os"
	"path/filepath"
	"sync"
	"syscall"
)

// Grok 取到额度时在自己的日志 logs/unified.jsonl 里记一行 "billing: fetched credits config"，
// ctx.config 是 billing 接口的原样回应：creditUsagePercent 是本期（每周）用了多少，currentPeriod 是本期起止，
// ctx.subscriptionTier 是套餐名。grok 启动和部分轮次开始时会取一次。
// 这是 grok 的运行日志，不是对外的接口：它改了写法就读不到，不影响别的。

var grokMarker = []byte(`"billing: fetched credits config"`)

func grokLine(line []byte) (Usage, bool) {
	var rec struct {
		Ts  string `json:"ts"`
		Msg string `json:"msg"`
		Ctx struct {
			Config struct {
				Percent *float64 `json:"creditUsagePercent"`
				Period  struct {
					Start string `json:"start"`
					End   string `json:"end"`
				} `json:"currentPeriod"`
			} `json:"config"`
			Tier string `json:"subscriptionTier"`
		} `json:"ctx"`
	}
	if !decode(line, &rec) || rec.Msg != "billing: fetched credits config" || rec.Ctx.Config.Percent == nil {
		return Usage{}, false
	}
	start, end := isoMs(rec.Ctx.Config.Period.Start), isoMs(rec.Ctx.Config.Period.End)
	w := Window{Percent: clampPercent(*rec.Ctx.Config.Percent), ResetsAt: end}
	if start > 0 && end > start {
		w.Minutes = int((end - start) / 60000)
	}
	u := Usage{Agent: "grok", Plan: rec.Ctx.Tier, Windows: []Window{w}, UpdatedAt: isoMs(rec.Ts)}
	return u, u.UpdatedAt > 0
}

// GrokLogPath 是 grok 的日志，设了 GROK_HOME 时在那里面。
func GrokLogPath() string {
	dir := os.Getenv("GROK_HOME")
	if dir == "" {
		home, _ := os.UserHomeDir()
		dir = filepath.Join(home, ".grok")
	}
	return filepath.Join(dir, "logs", "unified.jsonl")
}

// GrokLog 增量读 grok 的日志：记住读到哪，下次只看新写的部分。
// 日志会被裁短或换成新文件，这时从末尾往前重找。
type GrokLog struct {
	Path string
	mu   sync.Mutex
	ino  uint64
	off  int64 // 已经看过的位置，-1 是还没读过
}

func NewGrokLog(path string) *GrokLog {
	return &GrokLog{Path: path, off: -1}
}

// Read 返回上次之后新记下的最后一份额度。
func (g *GrokLog) Read() (Usage, bool) {
	g.mu.Lock()
	defer g.mu.Unlock()
	f, err := os.Open(g.Path)
	if err != nil {
		return Usage{}, false
	}
	defer f.Close()
	fi, err := f.Stat()
	if err != nil {
		return Usage{}, false
	}
	var ino uint64
	if st, ok := fi.Sys().(*syscall.Stat_t); ok {
		ino = st.Ino
	}
	size := fi.Size()
	from := g.off
	if from < 0 || ino != g.ino || size < from {
		from = 0
	}
	if size == from {
		return Usage{}, false
	}
	u, ok := scanBack(f, size, from, grokMarker, grokLine)
	// 最后一行可能还没写完：只记到最后一个换行，剩下的下次再看
	end := from
	n := min(size-from, 64<<10)
	buf := make([]byte, n)
	if _, err := f.ReadAt(buf, size-n); err == nil {
		if i := bytes.LastIndexByte(buf, '\n'); i >= 0 {
			end = size - n + int64(i) + 1
		}
	}
	g.ino, g.off = ino, end
	return u, ok
}
