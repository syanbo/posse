package mobile

import (
	"bytes"
	"encoding/json"
	"errors"
	"os"
	"syscall"
	"time"

	"hive/internal/history"
	"hive/internal/host"
)

const (
	chatPoll    = 500 * time.Millisecond
	chatPollMax = 5 * time.Second
	statusEvery = 250 * time.Millisecond
)

type chatSub struct {
	Agent          string `json:"agent"`
	SessionID      string `json:"sessionId"`
	TranscriptPath string `json:"transcriptPath"`
	Limit          int    `json:"limit"`
	BeforeOffset   int64  `json:"beforeOffset"`
	SubscriptionID string `json:"subscriptionId"`
	Capabilities   struct {
		TranscriptPending int `json:"transcriptPending"`
	} `json:"capabilities"`
}

func (c *conn) readNativeChat(id string, p json.RawMessage) error {
	var a chatSub
	if err := json.Unmarshal(p, &a); err != nil {
		return errorf("invalid_params", "%v", err)
	}
	page, err := history.ReadChatTail(a.Agent, a.SessionID, a.TranscriptPath, a.Limit, a.BeforeOffset)
	if errors.Is(err, history.ErrChatNotFound) {
		return errorf("not_found", "transcript not found")
	}
	if err != nil {
		return errorf("internal_error", "%v", err)
	}
	if page.Messages == nil {
		page.Messages = []history.ChatMessage{}
	}
	return c.reply(id, map[string]any{
		"beforeOffset": page.BeforeOffset, "hasMore": page.HasMore, "messages": page.Messages,
	})
}

func (c *conn) subscribeNativeChat(id string, p json.RawMessage) error {
	var a chatSub
	if err := json.Unmarshal(p, &a); err != nil {
		return errorf("invalid_params", "%v", err)
	}
	sub := a.SubscriptionID
	if sub == "" {
		sub = a.Agent + ":" + a.SessionID
	}
	key := "chat:" + sub
	ctx, gen := c.startKeyedStream(key)
	go c.chatStream(ctx, key, gen, id, a)
	return nil
}

func (c *conn) unsubscribeNativeChat(id string, p json.RawMessage) error {
	var a struct {
		SubscriptionID string `json:"subscriptionId"`
	}
	json.Unmarshal(p, &a)
	if a.SubscriptionID == "" {
		c.stopStreamsPrefix("chat:")
	} else {
		c.stopStream("chat:" + a.SubscriptionID)
	}
	return c.reply(id, map[string]any{"unsubscribed": true})
}

// chatStream 不订阅终端画面，所以不会按手机尺寸改桌面 PTY。
func (c *conn) chatStream(ctx contextDone, key string, gen uint64, reqID string, a chatSub) {
	defer func() {
		c.clearChatWatch(key, gen)
		c.stream(reqID, map[string]any{"type": "end"})
		c.endStreamGen(key, gen)
	}()
	c.noteChat(key, gen, reqID, a)
	path := a.TranscriptPath
	pos := int64(0)
	var inode uint64
	announced, ready := false, false
	delay := chatPoll
	timer := time.NewTimer(delay)
	defer timer.Stop()
	var wake <-chan struct{}
	var term host.Terminal
	// 状态行每次终端有输出都可能变，转圈符号一秒好几帧；最多这么久看一次屏幕，
	// 跳过的那次约好稍后再看，停下来的最后一帧不会漏
	status, statusAt := "", time.Time{}
	var recheck <-chan time.Time
	sendStatus := func() bool {
		if term == nil || !c.hasCapability("hive.status.v1") {
			return true
		}
		if wait := statusEvery - time.Since(statusAt); wait > 0 {
			if recheck == nil {
				recheck = time.After(wait)
			}
			return true
		}
		recheck = nil
		statusAt = time.Now()
		next := agentStatus(term.Screen())
		if next == status {
			return true
		}
		status = next
		return c.stream(reqID, map[string]any{"type": "status", "text": next}) == nil
	}
	arm := func(wait time.Duration) {
		c.noteChat(key, gen, reqID, a)
		wake = nil
		term = c.sessionTerminal(a.Agent, a.SessionID)
		if term != nil {
			wake = term.Changed()
		}
		timer.Reset(wait)
	}
	// 先发一帧，再进入轮询。文件还没出现时只有声明了 transcriptPending 才带 pending。
	ok, wait := c.chatTick(reqID, &a, &path, &pos, &inode, &announced, &ready, &delay)
	if !ok {
		return
	}
	arm(wait)
	if !sendStatus() {
		return
	}
	for {
		select {
		case <-ctx.Done():
			return
		case <-wake:
		case <-timer.C:
		case <-recheck:
			recheck = nil
			if !sendStatus() {
				return
			}
			continue
		}
		ok, wait = c.chatTick(reqID, &a, &path, &pos, &inode, &announced, &ready, &delay)
		if !ok {
			return
		}
		arm(wait)
		if !sendStatus() {
			return
		}
	}
}

func (c *conn) sessionTerminal(agentName, sessionID string) host.Terminal {
	if sessionID == "" {
		return nil
	}
	st := c.srv.host.State()
	for _, w := range st.Workspaces {
		for _, t := range w.Terminals {
			if t.SessionID == sessionID && (agentName == "" || t.Agent == agentName) {
				return c.srv.host.Terminal(t.ID)
			}
		}
	}
	return nil
}

// chatTick 看一眼文件。ok 为 false 表示流该停（发送失败）。wait 是这一轮之后再等多久：
// 文件还没有时从 500ms 翻倍到 5s，已经有文件就保持 500ms。
func (c *conn) chatTick(reqID string, a *chatSub, path *string, pos *int64, inode *uint64, announced, ready *bool, delay *time.Duration) (bool, time.Duration) {
	if *path == "" {
		*path = history.TranscriptPath(a.Agent, a.SessionID, "")
	}
	fi, err := os.Stat(*path)
	if err != nil || fi.IsDir() {
		if !*announced {
			frame := map[string]any{"type": "snapshot", "messages": []any{}, "hasMore": false, "beforeOffset": 0}
			if a.Capabilities.TranscriptPending == 1 {
				frame["pending"] = true
			}
			*announced = true
			if c.stream(reqID, frame) != nil {
				return false, 0
			}
		}
		wait := *delay
		*delay *= 2
		if *delay > chatPollMax {
			*delay = chatPollMax
		}
		if *delay < chatPoll {
			*delay = chatPoll
		}
		return true, wait
	}
	*delay = chatPoll
	ino := fileInode(fi)
	// announced 只表示发过“文件还没有”的空快照。第一次真正读到文件仍是 snapshot，
	// 之后变短或换了 inode（截短、压缩重写）才是 replacement。
	if !*ready || ino != *inode || fi.Size() < *pos {
		page, err := history.ReadChatTail(a.Agent, a.SessionID, *path, a.Limit, 0)
		if err != nil {
			ok := c.stream(reqID, map[string]any{"type": "error", "message": err.Error()}) == nil
			return ok, chatPoll
		}
		if page.Messages == nil {
			page.Messages = []history.ChatMessage{}
		}
		typ := "snapshot"
		if *ready {
			typ = "replacement"
		}
		if c.stream(reqID, map[string]any{
			"type": typ, "messages": page.Messages, "hasMore": page.HasMore, "beforeOffset": page.BeforeOffset,
		}) != nil {
			return false, 0
		}
		*ready = true
		*announced = true
		*inode = ino
		// 停在最后一个完整行之后。读的过程中新写的字节留给下一轮 appended，不把 pos 推过它们。
		*pos = completeOffset(*path, fi.Size())
		return true, chatPoll
	}
	if fi.Size() == *pos {
		return true, chatPoll
	}
	msgs, next, err := history.ReadChatNew(a.Agent, *path, *pos)
	if err != nil {
		ok := c.stream(reqID, map[string]any{"type": "error", "message": err.Error()}) == nil
		return ok, chatPoll
	}
	*pos = next
	if len(msgs) == 0 {
		return true, chatPoll
	}
	return c.stream(reqID, map[string]any{"type": "appended", "messages": msgs}) == nil, chatPoll
}

// completeOffset 把位置退到最后一个换行之后。文件末尾的半行下一轮整行再读，客户端按 id 替换。
func completeOffset(path string, size int64) int64 {
	if size <= 0 {
		return 0
	}
	f, err := os.Open(path)
	if err != nil {
		return size
	}
	defer f.Close()
	last := make([]byte, 1)
	if _, err := f.ReadAt(last, size-1); err != nil || last[0] == '\n' {
		return size
	}
	n := int64(64 << 10)
	if size < n {
		n = size
	}
	buf := make([]byte, n)
	if _, err := f.ReadAt(buf, size-n); err != nil {
		return size
	}
	if i := bytes.LastIndexByte(buf, '\n'); i >= 0 {
		return size - n + int64(i) + 1
	}
	return 0
}

func fileInode(fi os.FileInfo) uint64 {
	if st, ok := fi.Sys().(*syscall.Stat_t); ok {
		return uint64(st.Ino)
	}
	return 0
}

// sessionContext 是会话最后一次请求占用的上下文（Hive 扩展，声明了 hive.context.v1 才有）：
// 手机终端太窄，Claude 自己状态栏里的百分比常被截掉，手机把它写在标题下面。
// 只有 claude 和 codex 的会话文件记了用量，别家和还没有回复的会话回 known=false。
func (c *conn) sessionContext(id string, p json.RawMessage) error {
	if !c.hasCapability("hive.context.v1") {
		return errorf("method_not_found", "Unknown method: session.context")
	}
	var a chatSub
	if err := json.Unmarshal(p, &a); err != nil {
		return errorf("invalid_params", "%v", err)
	}
	path := history.TranscriptPath(a.Agent, a.SessionID, a.TranscriptPath)
	ctx, ok := history.ReadContext(a.Agent, path, "")
	if !ok {
		return c.reply(id, map[string]any{"known": false})
	}
	return c.reply(id, map[string]any{"known": true, "used": ctx.Used, "window": ctx.Window, "percent": ctx.Percent()})
}
