package app

import (
	"context"
	"encoding/json"
	"fmt"
	"path/filepath"
	"slices"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/egoist/mygo/ui"

	"posse/internal/client"
	"posse/internal/history"
	"posse/internal/rpc"
)

func TestHandoffText(t *testing.T) {
	got := handoffText(history.Session{ID: "abc", Cwd: "/work/shop"})
	if !strings.Contains(got, "session: abc") || !strings.Contains(got, "directory: /work/shop") || !strings.Contains(got, "posse history show abc") {
		t.Fatal(got)
	}
	if strings.Contains(handoffText(history.Session{ID: "abc"}), "directory:") {
		t.Fatal("empty directory was written")
	}
}

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
	// 这里只推进界面的等待状态，离线客户端的真实失败会提前结束等待。
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

// 父会话很久没动、子会话今天更新：整棵树排在前面、归到"今天"，子会话缩进在父会话下面。
func TestHistoryGroupByTreeLatest(t *testing.T) {
	now := time.Now()
	h := &historyView{}
	h.setSessions([]history.Session{
		{Agent: "claude", ID: "other", Path: "/o", UpdatedAt: now.AddDate(0, 0, -1)},
		{Agent: "claude", ID: "parent", Path: "/p", UpdatedAt: now.AddDate(0, 0, -40),
			Children: []history.Session{{Agent: "claude", ID: "kid", Path: "/k", UpdatedAt: now}}},
	})
	if h.sessions[0].ID != "parent" || h.groups[0] != groupToday || h.groups[1] != groupYesterday {
		t.Fatalf("order = %v %v", h.sessions[0].ID, h.groups)
	}
	rows := h.groupRows()
	if len(rows) < 3 || rows[0].session >= 0 || rows[0].count != 1 {
		t.Fatalf("rows = %+v", rows)
	}
	if s := h.rowSession(rows[1]); s.ID != "parent" || len(rows[1].path) != 0 {
		t.Fatalf("row 1 = %+v", rows[1])
	}
	if s := h.rowSession(rows[2]); s.ID != "kid" || len(rows[2].path) != 1 {
		t.Fatalf("row 2 = %+v", rows[2])
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
		a.request().fetchHistory(1)
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
	if h.failed == "" {
		t.Fatal("history failure was not shown in the page")
	}
}

// 索引做完（次数变了）马上刷新，不等 15 秒；同一次数不重复请求。
func TestHistoryTickFollowsIndexGen(t *testing.T) {
	var calls atomic.Int32
	_, c := startFakeDaemon(t, map[string]rpc.Handler{"history.list": func(context.Context, json.RawMessage) (any, error) {
		calls.Add(1)
		return map[string]any{}, nil
	}})
	a := &app{c: c}
	h := &a.history
	h.mu.Lock()
	h.visible = true
	h.fetched = time.Now()
	h.indexGen, h.appliedGen = 2, 1
	h.mu.Unlock()
	a.historyTick()
	for calls.Load() == 0 {
		time.Sleep(5 * time.Millisecond)
	}
	a.runPending()
	a.historyTick()
	time.Sleep(30 * time.Millisecond)
	if n := calls.Load(); n != 1 {
		t.Fatalf("%d list requests", n)
	}
}

func TestStatusParamsReportsHistoryPage(t *testing.T) {
	a := &app{}
	if a.statusParams() != nil {
		t.Fatal("history page is closed")
	}
	a.history.mu.Lock()
	a.history.visible = true
	a.history.mu.Unlock()
	p, _ := a.statusParams().(map[string]any)
	if p["history"] != true {
		t.Fatalf("params %v", p)
	}
}

// 列表不截断，搜索仍只取最相关的一批。分组条数才和顶上的总会话数一致。
func TestHistoryListUnlimited(t *testing.T) {
	var got struct {
		Limit int    `json:"limit"`
		Query string `json:"query"`
	}
	_, c := startFakeDaemon(t, map[string]rpc.Handler{
		"history.list": func(ctx context.Context, raw json.RawMessage) (any, error) {
			if err := json.Unmarshal(raw, &got); err != nil {
				t.Error(err)
			}
			return map[string]any{"sessions": []map[string]any{{"id": "a"}}}, nil
		},
		"history.search": func(ctx context.Context, raw json.RawMessage) (any, error) {
			if err := json.Unmarshal(raw, &got); err != nil {
				t.Error(err)
			}
			return map[string]any{"hits": []map[string]any{}}, nil
		},
	})
	a := &app{c: c}
	h := &a.history
	h.mu.Lock()
	h.req = 1
	h.mu.Unlock()
	a.request().fetchHistory(1)
	a.runPending()
	if got.Query != "" || got.Limit != 0 {
		t.Fatalf("list query=%q limit=%d, want all sessions", got.Query, got.Limit)
	}
	h.mu.Lock()
	h.wQuery = "磁盘"
	h.req = 2
	h.mu.Unlock()
	a.request().fetchHistory(2)
	a.runPending()
	if got.Query != "磁盘" || got.Limit != historySearchLimit {
		t.Fatalf("search query=%q limit=%d", got.Query, got.Limit)
	}
}
