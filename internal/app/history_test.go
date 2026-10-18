package app

import (
	"path/filepath"
	"testing"

	"github.com/egoist/mygo/ui"

	"hive/internal/client"
)

// 历史列表里上下键换选中：只走看得见的会话，折起来的分组跳过，到头不动。
func TestHistoryArrowKeys(t *testing.T) {
	a := previewApp(t)
	previewHistory(a, "")
	h := &a.history
	h.sel, h.detail = "", nil
	a.c = &client.Client{Socket: filepath.Join(t.TempDir(), "none.sock")} // 选中时取详情，连不上只报错
	tt := ui.NewTester(a.build, 1200, 760)
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
