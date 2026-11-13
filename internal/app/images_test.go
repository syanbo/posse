package app

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"image"
	"image/png"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/egoist/mygo/ui"
)

// writeClaudeImages 写一个 Claude 会话文件，里面 n 次截图工具各返回一张图片。
func writeClaudeImages(t *testing.T, n int) string {
	t.Helper()
	var buf bytes.Buffer
	if err := png.Encode(&buf, image.NewRGBA(image.Rect(0, 0, 8, 8))); err != nil {
		t.Fatal(err)
	}
	data := base64.StdEncoding.EncodeToString(buf.Bytes())
	var lines []string
	for i := range n {
		id := "tool" + string(rune('a'+i))
		at := time.Date(2026, 11, 13, 10, i, 0, 0, time.UTC)
		use, _ := json.Marshal(map[string]any{"type": "assistant", "timestamp": at, "message": map[string]any{"content": []any{
			map[string]any{"type": "tool_use", "id": id, "name": "screenshot"}}}})
		res, _ := json.Marshal(map[string]any{"type": "user", "timestamp": at, "message": map[string]any{"content": []any{
			map[string]any{"type": "tool_result", "tool_use_id": id, "content": []any{
				map[string]any{"type": "image", "source": map[string]any{"type": "base64", "media_type": "image/png", "data": data}}}}}}})
		lines = append(lines, string(use), string(res))
	}
	path := filepath.Join(t.TempDir(), "s1.jsonl")
	if err := os.WriteFile(path, []byte(strings.Join(lines, "\n")+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

// runImagesPoll 开始一轮读图片，在主线程的待办里跑到这一轮做完。
func runImagesPoll(t *testing.T, a *app, cur *terminalInfo) {
	t.Helper()
	newTester(func(c *ui.Context) { a.pollImages(c, cur) }, 100, 100).Frame()
	deadline := time.Now().Add(10 * time.Second)
	for a.images.poll.busy {
		if time.Now().After(deadline) {
			t.Fatal("images poll did not finish")
		}
		a.runPending()
		time.Sleep(time.Millisecond)
	}
}

func TestImagesListBeforeThumbnails(t *testing.T) {
	path := writeClaudeImages(t, 3)
	a := &app{}
	cur := &terminalInfo{ID: "t1", Agent: "claude", AgentState: &agentState{SessionID: "s1", TranscriptPath: path}}
	newTester(func(c *ui.Context) { a.pollImages(c, cur) }, 100, 100).Frame()
	// 第一批待办是列表本身：卡片先出来，缩略图还没有
	deadline := time.Now().Add(10 * time.Second)
	for a.images.poll.at.IsZero() {
		if time.Now().After(deadline) {
			t.Fatal("list was not published")
		}
		a.pendingMu.Lock()
		var first func()
		if len(a.pending) > 0 {
			first, a.pending = a.pending[0], a.pending[1:]
		}
		a.pendingMu.Unlock()
		if first != nil {
			first()
		} else {
			time.Sleep(time.Millisecond)
		}
	}
	v := &a.images
	if len(v.made) != 3 || !v.found || !v.poll.busy {
		t.Fatalf("list: made %d, found %v, busy %v", len(v.made), v.found, v.poll.busy)
	}
	for _, it := range v.made {
		if it.thumb != nil {
			t.Fatal("thumbnail arrived together with the list")
		}
	}
	for v.poll.busy {
		if time.Now().After(deadline) {
			t.Fatal("thumbnails did not finish")
		}
		a.runPending()
		time.Sleep(time.Millisecond)
	}
	for _, it := range v.made {
		if it.thumb == nil {
			t.Fatalf("missing thumbnail %s", it.Key)
		}
	}
}

func TestImagesWaitingForSession(t *testing.T) {
	a := &app{}
	cur := &terminalInfo{ID: "t1", Agent: "claude"}
	runImagesPoll(t, a, cur)
	if a.images.found || a.images.poll.at.IsZero() {
		t.Fatalf("no session: %+v", a.images)
	}
	path := writeClaudeImages(t, 1)
	cur.AgentState = &agentState{SessionID: "s1", TranscriptPath: path}
	a.images.poll.at = time.Time{}
	runImagesPoll(t, a, cur)
	if !a.images.found || len(a.images.made) != 1 {
		t.Fatalf("after the session started: found %v, made %d", a.images.found, len(a.images.made))
	}
}

// 面板关掉再打开后终端 ID 一样，上一轮还在后台做的结果不能落到新的这一轮上。
func TestImagesDropsStaleRound(t *testing.T) {
	path := writeClaudeImages(t, 2)
	a := &app{}
	cur := &terminalInfo{ID: "t1", Agent: "claude", AgentState: &agentState{SessionID: "s1", TranscriptPath: path}}
	newTester(func(c *ui.Context) { a.pollImages(c, cur) }, 100, 100).Frame()
	// 上一轮的结果还在路上时面板关掉了
	a.images = imagesView{term: "t1"}
	deadline := time.Now().Add(10 * time.Second)
	for {
		a.pendingMu.Lock()
		n := len(a.pending)
		a.pendingMu.Unlock()
		if n > 0 || time.Now().After(deadline) {
			break
		}
		time.Sleep(time.Millisecond)
	}
	time.Sleep(200 * time.Millisecond)
	a.runPending()
	if len(a.images.made) != 0 || !a.images.poll.at.IsZero() {
		t.Fatalf("stale round applied: made %d", len(a.images.made))
	}
}

func TestImagesReopenedPanelRejectsPreviousScan(t *testing.T) {
	oldPath := writeClaudeImages(t, 2)
	newPath := writeClaudeImages(t, 1)
	a := &app{}
	cur := &terminalInfo{ID: "t1", Agent: "claude", AgentState: &agentState{SessionID: "s1", TranscriptPath: oldPath}}
	newTester(func(c *ui.Context) { a.pollImages(c, cur) }, 100, 100).Frame()
	// 留住上一轮完整的列表、首张缩略图和最后一批结果，模拟 UI 尚未处理它们就重新打开面板。
	deadline := time.Now().Add(5 * time.Second)
	var old []func()
	for {
		a.pendingMu.Lock()
		if len(a.pending) >= 3 {
			old, a.pending = a.pending, nil
		}
		a.pendingMu.Unlock()
		if old != nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("old scan did not publish all batches")
		}
		time.Sleep(time.Millisecond)
	}
	a.images = imagesView{}
	cur.AgentState = &agentState{SessionID: "s2", TranscriptPath: newPath}
	newTester(func(c *ui.Context) { a.pollImages(c, cur) }, 100, 100).Frame()
	for _, apply := range old {
		apply()
	}
	if len(a.images.made) != 0 || !a.images.poll.busy || !a.images.poll.at.IsZero() {
		t.Fatalf("previous scan updated the reopened panel: made=%d busy=%v at=%v", len(a.images.made), a.images.poll.busy, a.images.poll.at)
	}
	for a.images.poll.busy {
		a.runPending()
		if time.Now().After(deadline) {
			t.Fatal("new scan did not finish")
		}
		time.Sleep(time.Millisecond)
	}
	if len(a.images.made) != 1 {
		t.Fatalf("new scan made %d images", len(a.images.made))
	}
}
