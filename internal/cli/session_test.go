package cli

import (
	"strings"
	"testing"
)

// 起名指南按界面语言填好类型和字数，不留占位符。
func TestSessionGuideLang(t *testing.T) {
	for _, c := range []struct {
		en   bool
		want string
	}{
		{false, "修复 开发 调研 调试 重构 文档 杂项"},
		{true, "fix feat research debug refactor docs chore"},
	} {
		g := guideFor(c.en)
		if !strings.Contains(g, c.want) || strings.Contains(g, "{{") {
			t.Errorf("guide en=%v:\n%s", c.en, g)
		}
	}
}
