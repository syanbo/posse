package app

import (
	"context"
	"encoding/json"
	"fmt"
	"path/filepath"
	"slices"
	"sync/atomic"
	"testing"
	"time"

	"github.com/egoist/mygo/ui"

	"posse/internal/client"
	"posse/internal/rpc"
)

// 历史列表里上下键换选中：只走看得见的会话，折起来的分组跳过，到头不动。
func TestHistoryArrowKeys(t *testing.T) {
	a := previewApp(t)
	previewHistory(a, "")
	h := &a.history
	h.sel, h.detail = "", nil
	a.c = &client.Client{Socket: filepath.Join(t.TempDir(), "none.sock")} // 选中时取详情，连不上只报错
	tt := newTester(a.build, 1200, 760)
	tt.Frame()
	step := func(key ui.Key, want string) {
		t.Helper()
		tt.Key(0, key)
		tt.Frame()
		if h.sel != want {
			t.Fatalf("after %v: sel = %q, want %q", key, h.sel, want)
		}
	}
	// 焦点在搜索框里也管用
	if err := tt.Click("搜索会话"); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	// 默认只展开"今天"：s1、s2
	step(ui.KeyDown, "claude:s1")
	step(ui.KeyDown, "grok:s2")
	step(ui.KeyDown, "grok:s2")
	step(ui.KeyUp, "claude:s1")
	step(ui.KeyUp, "claude:s1")
	// 展开"昨天"后能走进去
	h.groupOpen = map[string]bool{h.groups[2]: true}
	step(ui.KeyDown, "grok:s2")
	step(ui.KeyDown, "codex:s3")
}

// 搜索时顶上的总计换成搜索结果的数目。
func TestHistoryTotalsFollowSearch(t *testing.T) {
	a := previewApp(t)
	previewHistory(a, "")
	tt := newTester(a.build, 1200, 760)
	tt.Frame()
	if !tt.HasText("424 个会话") {
		t.Fatalf("totals: %v", tt.Texts())
	}
	previewHistory(a, "磁盘")
	tt.Frame()
	if !tt.HasText("3 个会话匹配") || !tt.HasText("5 条消息命中") || tt.HasText("424 个会话") {
		t.Fatalf("search totals: %v", tt.Texts())
	}
}

// 敲搜索词后结果回来之前照旧显示原来的列表，不闪"没有找到"；等久了提示正在搜索，
// 这个词的结果确实为空才显示"没有找到"。
func TestHistorySearchPending(t *testing.T) {
	a := previewApp(t)
	offlineClient(t, a)
	previewHistory(a, "")
	h := &a.history
	tt := newTester(a.build, 1200, 760)
	tt.Frame()
	h.query, h.askedAt = "xyz", time.Now()
	tt.Frame()
	if tt.HasText("没有找到“xyz”") || !tt.HasText("清理磁盘") || tt.HasText("正在搜索…") {
		t.Fatal("old list should stay while the search runs")
	}
	h.askedAt = time.Now().Add(-time.Second)
	tt.Frame()
	if !tt.HasText("正在搜索…") {
		t.Fatal("slow search shows no progress")
	}
	h.hits, h.resQuery = nil, "xyz"
	h.setSessions(nil)
	tt.Frame()
	if !tt.HasText("没有找到“xyz”") || tt.HasText("正在搜索…") {
		t.Fatal("empty result should say so")
	}
}

// 按文件夹分组：会话归到所在的项目（工作区目录算项目的），不在项目里的用目录名；
// 组按其中最近的会话排，切回按时间恢复原来的顺序。
func TestHistoryGroupByFolder(t *testing.T) {
	a := previewApp(t)
	previewHistory(a, "")
	h := &a.history
	a.state.HistoryByFolder = true
	a.setHistoryGrouping()
	var got []string
	for _, r := range h.groupRows() {
		if r.session < 0 {
			got = append(got, fmt.Sprintf("%s %d", r.group, r.count))
		}
	}
	want := []string{"Apps 1", "Grok 2", "Claude 1", "Halloween-3 1", "Works 1"}
	if !slices.Equal(got, want) {
		t.Fatalf("groups = %v, want %v", got, want)
	}
	if h.sessions[1].ID != "s2" || h.sessions[2].ID != "s6" {
		t.Fatalf("same folder should be adjacent: %v %v", h.sessions[1].ID, h.sessions[2].ID)
	}
	a.state.HistoryByFolder = false
	a.setHistoryGrouping()
	for i, id := range []string{"s1", "s2", "s3", "s4", "s5", "s6"} {
		if h.sessions[i].ID != id {
			t.Fatalf("time order broken at %d: %s", i, h.sessions[i].ID)
		}
	}
}

// 历史列表取不到时报错只写在历史页里，不弹错误条；请求还在途时定时刷新不再发第二个。
func TestFetchHistoryErrors(t *testing.T) {
	release := make(chan struct{})
	var calls atomic.Int32
	_, c := startFakeDaemon(t, map[string]rpc.Handler{"history.list": func(ctx context.Context, raw json.RawMessage) (any, error) {
		calls.Add(1)
		<-release
		return nil, rpc.Errorf(rpc.CodeConflict, "index busy")
	}})
	a := &app{c: c}
	h := &a.history
	h.visible = true
	h.mu.Lock()
	h.req = 1
	h.mu.Unlock()
	done := make(chan struct{})
	go func() {
		a.fetchHistory(1)
		close(done)
	}()
	for calls.Load() == 0 {
		time.Sleep(5 * time.Millisecond)
	}
	for range 3 {
		a.historyTick()
	}
	time.Sleep(50 * time.Millisecond)
	if n := calls.Load(); n != 1 {
		t.Fatalf("%d requests in flight", n)
	}
	close(release)
	<-done
	a.runPending()
	if a.err != "" {
		t.Fatalf("err bar %q", a.err)
	}
}
