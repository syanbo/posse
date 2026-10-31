package skills

import (
	"strings"
	"testing"
)

func TestNPXArgs(t *testing.T) {
	in := Install{Source: " vercel-labs/skills ", Skills: []string{"find-skills"}, Claude: true, Global: true}
	if got := strings.Join(in.Args(), " "); got != "npx -y skills@"+NPXVersion+" add vercel-labs/skills -g -a codex -a claude-code -s find-skills -y" {
		t.Errorf("安装：%s", got)
	}
	if got := strings.Join(Install{Source: "./my skill"}.Args(), " "); got != "npx -y skills@"+NPXVersion+" add ./my skill -a codex -y" {
		t.Errorf("项目里、只给 codex：%s", got)
	}
	if got := ShellLine(Install{Source: "./my skill", Global: true}.Args()); got != "DISABLE_TELEMETRY=1 npx -y skills@"+NPXVersion+" add './my skill' -g -a codex -y" {
		t.Errorf("带空格的路径要加引号：%s", got)
	}
	if got := ShellLine(RemoveArgs("it's", true)); !strings.HasSuffix(got, `remove 'it'\''s' -g -y`) {
		t.Errorf("删除：%s", got)
	}
	if got := strings.Join(UpdateArgs(nil, true), " "); !strings.HasSuffix(got, "update -g -y") {
		t.Errorf("全部更新：%s", got)
	}
}
