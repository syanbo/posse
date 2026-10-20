package app

import (
	"testing"

	"hive/internal/usage"
)

func TestCursorPools(t *testing.T) {
	u := usage.Usage{Agent: "cursor", Windows: []usage.Window{
		{Percent: 2.7, Minutes: 44640},
		{Percent: 0.2, Minutes: 44640, Name: "Auto"},
		{Percent: 77, Minutes: 44640, Name: "API"},
	}}
	ws := planWindows(u)
	if len(ws) != 2 || windowName(ws[0]) != "Cursor 模型" || windowName(ws[1]) != "其他模型" {
		t.Fatalf("windows = %+v", ws)
	}
	if h := headWindow(u); h.Name != "API" {
		t.Fatalf("head = %+v", h)
	}
	// 只有总额度的旧读数照常显示
	u.Windows = u.Windows[:1]
	if ws := planWindows(u); len(ws) != 1 || headWindow(u).Percent != 2.7 {
		t.Fatalf("windows = %+v", ws)
	}
	// Claude 的分池不影响状态栏取 5 小时
	c := usage.Usage{Agent: "claude", Windows: []usage.Window{
		{Percent: 40, Minutes: 300}, {Percent: 10, Minutes: 10080}, {Percent: 90, Minutes: 10080, Name: "Fable"},
	}}
	if len(planWindows(c)) != 3 || headWindow(c).Minutes != 300 {
		t.Fatalf("claude head = %+v", headWindow(c))
	}
}
