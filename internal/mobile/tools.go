package mobile

import (
	"encoding/json"
	"sync"
	"time"

	"posse/internal/history"
)

// 手机的"技能与工具"页（Posse 扩展，声明了 posse.tools.v1 的连接才有），和桌面的技能与工具面板列的一样：
// session.tools 返回会话到目前为止用到的技能、MCP 服务和命令行程序，以及最近的调用记录（history.Usage）。
// 桌面面板在窗口进程里读，手机这份由常驻进程读，窗口关着也能看。
const toolsCacheMax = 8 // 记住几个会话的扫描进度

// toolsSession 是一个会话的扫描进度；会话文件要等 agent 写下第一条记录才出现，找到之前 scanner 为空。
type toolsSession struct {
	mu      sync.Mutex
	scanner *history.UsageScanner
	used    time.Time
}

// toolsCache 让同一个会话再列时只读会话文件新增的部分，几台手机、几条连接共用。
type toolsCache struct {
	mu       sync.Mutex
	sessions map[string]*toolsSession
}

func (tc *toolsCache) get(a mediaParams) *toolsSession {
	key := a.Agent + "\n" + a.SessionID + "\n" + a.TranscriptPath
	tc.mu.Lock()
	defer tc.mu.Unlock()
	if tc.sessions == nil {
		tc.sessions = map[string]*toolsSession{}
	}
	ts := tc.sessions[key]
	if ts == nil {
		if len(tc.sessions) >= toolsCacheMax {
			var oldest string
			for k, v := range tc.sessions {
				if oldest == "" || v.used.Before(tc.sessions[oldest].used) {
					oldest = k
				}
			}
			delete(tc.sessions, oldest)
		}
		ts = &toolsSession{}
		tc.sessions[key] = ts
	}
	ts.used = time.Now()
	return ts
}

func (ts *toolsSession) scan(a mediaParams) (history.Usage, bool, error) {
	ts.mu.Lock()
	defer ts.mu.Unlock()
	if ts.scanner == nil {
		path := history.TranscriptPath(a.Agent, a.SessionID, a.TranscriptPath)
		if path == "" {
			return history.Usage{}, false, nil
		}
		ts.scanner = history.NewUsageScanner(a.Agent, path)
	}
	u, err := ts.scanner.Scan()
	return u, true, err
}

// toolsMethod 处理 session.tools；不认识的方法返回 false。读会话文件要一会儿，放到自己的 goroutine 里。
func (c *conn) toolsMethod(req request, p json.RawMessage) bool {
	if req.Method != "session.tools" {
		return false
	}
	if !c.hasCapability("posse.tools.v1") {
		c.fail(req.ID, "method_not_found", "Unknown method: "+req.Method)
		return true
	}
	var a mediaParams
	if err := json.Unmarshal(p, &a); err != nil {
		c.fail(req.ID, "invalid_params", err.Error())
		return true
	}
	switch a.Agent {
	case "claude", "codex", "grok", "cursor":
	default:
		c.fail(req.ID, "unsupported", "目前只支持 Claude、Codex、Grok 和 Cursor 会话")
		return true
	}
	go func() {
		u, found, err := c.srv.tools.get(a).scan(a)
		if err != nil {
			c.replyOrFail(req.ID, nil, err)
			return
		}
		c.reply(req.ID, map[string]any{
			"found": found, "skills": u.Skills, "mcp": u.MCP, "cli": u.CLI, "events": u.Events,
		})
	}()
	return true
}
