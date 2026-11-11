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
	rm, _ := RemoveArgs("it's", true)
	if got := ShellLine(rm); !strings.HasSuffix(got, `remove 'it'\''s' -g -y`) {
		t.Errorf("删除：%s", got)
	}
	up, _ := UpdateArgs(nil, true)
	if got := strings.Join(up, " "); !strings.HasSuffix(got, "update -g -y") {
		t.Errorf("全部更新：%s", got)
	}
}

func TestInstallCheck(t *testing.T) {
	if (Install{Source: "a/b", Skills: []string{"--force"}}).Check() == nil {
		t.Error("技能名以 - 开头应挡住")
	}
	if (Install{Source: "-x"}).Check() == nil {
		t.Error("来源以 - 开头应挡住")
	}
	if err := (Install{Source: "a/b", Skills: []string{"x-y"}}).Check(); err != nil {
		t.Error(err)
	}
}

func TestParseList(t *testing.T) {
	out := "\x1b[?25l│\n\x1b[1G\x1b[J◇  Found 3 skills\n\x1b[?25h\n│\n◇  Available Skills\n" +
		"Academy Guide\n│\n│    academy-guide\n│\n│      Recommends courses.\n\n" +
		"General\n│\n│    template-skill\n│\n│      Replace with description.\n│      Second line.\n\n" +
		"│\n│    pdf\n│\n\n│\n└  Use --skill <name> to install specific skills\n\n"
	list, err := ParseList(out)
	if err != nil {
		t.Fatal(err)
	}
	want := []Listed{
		{Name: "academy-guide", Description: "Recommends courses.", Group: "Academy Guide"},
		{Name: "template-skill", Description: "Replace with description. Second line.", Group: "General"},
		{Name: "pdf", Group: "General"},
	}
	if len(list) != len(want) {
		t.Fatalf("%+v", list)
	}
	for i := range want {
		if list[i] != want[i] {
			t.Errorf("第 %d 个：%+v", i, list[i])
		}
	}

	_, err = ParseList("◒  Cloning repository…..│\n■  Failed to clone repository\n│\n│  Authentication failed for https://github.com/a/b.git.\n│\n└  Installation failed\n\n\x1b[1G\x1b[J■  Canceled\n")
	if err == nil || err.Error() != "Failed to clone repository: Authentication failed for https://github.com/a/b.git." {
		t.Errorf("克隆失败：%v", err)
	}
	_, err = ParseList("◇  No skills found\n│\n└  No valid skills found. Skills require a SKILL.md with name and description.\n")
	if err == nil || !strings.HasPrefix(err.Error(), "No valid skills found") {
		t.Errorf("没有技能：%v", err)
	}
}

// SKILL.md 里写的名字以 - 开头时不交给 npx，免得 name: --all 被当成选项。
func TestRemoveUpdateRejectDashNames(t *testing.T) {
	if _, err := RemoveArgs("--all", true); err == nil {
		t.Fatal("remove accepted --all")
	}
	if _, err := UpdateArgs([]string{"ok", "-g"}, false); err == nil {
		t.Fatal("update accepted -g")
	}
	if _, err := UpdateArgs([]string{"ok"}, false); err != nil {
		t.Fatal(err)
	}
}
