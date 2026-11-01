package cheatsheet

import (
	"encoding/json"
	"strings"
	"testing"
	"unicode/utf8"

	"posse/internal/agent"
)

// TestSheets 守着手工整理的表：每家都有，字段齐全，说明够短（面板只有 280 宽）。
func TestSheets(t *testing.T) {
	var list []Sheet
	if err := json.Unmarshal(raw, &list); err != nil {
		t.Fatal(err)
	}
	seen := map[string]bool{}
	for _, s := range list {
		if seen[s.Agent] {
			t.Errorf("%s 重复", s.Agent)
		}
		seen[s.Agent] = true
		if VersionIn(" "+s.Version) != s.Version {
			t.Errorf("%s 的版本 %q 不是三段数字", s.Agent, s.Version)
		}
		if s.SkillPrefix == "" {
			t.Errorf("%s 没写技能前缀", s.Agent)
		}
		for _, u := range []string{s.Docs, s.Changelog} {
			if u != "" && !strings.HasPrefix(u, "https://") {
				t.Errorf("%s 的网址 %q", s.Agent, u)
			}
		}
		names := map[string]bool{}
		for _, sec := range s.Sections {
			if sec.Title == "" || len(sec.Items) == 0 {
				t.Errorf("%s 有空组 %q", s.Agent, sec.Title)
			}
			if sec.Kind != KindCommand && sec.Kind != KindKey && sec.Kind != KindPrefix {
				t.Errorf("%s/%s 的类别 %q", s.Agent, sec.Title, sec.Kind)
			}
			for _, it := range sec.Items {
				if it.Name == "" || it.Desc == "" {
					t.Errorf("%s/%s 有条目缺名字或说明：%+v", s.Agent, sec.Title, it)
				}
				if n := utf8.RuneCountInString(it.Desc); n > 40 {
					t.Errorf("%s %s 的说明太长（%d 字）", s.Agent, it.Name, n)
				}
				if strings.Contains(it.Name+it.Args+it.Desc, "&lt;") {
					t.Errorf("%s %s 带着转义的 &lt;", s.Agent, it.Name)
				}
				if sec.Kind == KindCommand && !strings.HasPrefix(it.Name, "/") {
					t.Errorf("%s 命令 %q 不以 / 开头", s.Agent, it.Name)
				}
				k := string(sec.Kind) + " " + it.Name
				if names[k] {
					t.Errorf("%s 重复列了 %s", s.Agent, it.Name)
				}
				names[k] = true
			}
		}
	}
	for _, name := range agent.Known {
		if !seen[name] {
			t.Errorf("%s 没有速查表", name)
		}
	}
}

func TestCompare(t *testing.T) {
	for _, c := range []struct {
		a, b string
		want int
	}{
		{"2.1.316", "2.1.316", 0},
		{"2.1.316", "2.1.40", 1},
		{"0.166.0", "0.170.1", -1},
		{"2026.10.22", "2026.9.30", 1},
		{"1.0", "1.0.0", 0},
	} {
		if got := Compare(c.a, c.b); got != c.want {
			t.Errorf("Compare(%s, %s) = %d，要 %d", c.a, c.b, got, c.want)
		}
	}
}

func TestVersionIn(t *testing.T) {
	for in, want := range map[string]string{
		"/Users/x/.local/share/claude/versions/2.1.316":                   "2.1.316",
		"/opt/homebrew/Caskroom/codex/0.166.0/bin/codex":                  "0.166.0",
		"/Users/x/.grok/downloads/grok-1.0.73-macos-aarch64":              "1.0.73",
		"/Users/x/.local/share/cursor-agent/versions/2026.10.22-27dcf5f/": "2026.10.22",
		"2.1.316 (Claude Code)":                                           "2.1.316",
		"codex-cli 0.166.0":                                               "0.166.0",
		"/Applications/ChatGPT.app/Contents/MacOS/codex":                  "",
		"Python3.framework/Versions/3.9":                                  "",
	} {
		if got := VersionIn(in); got != want {
			t.Errorf("VersionIn(%q) = %q，要 %q", in, got, want)
		}
	}
}

func TestPathVersion(t *testing.T) {
	for _, c := range []struct{ path, cmd, want string }{
		{"/Users/x/.local/share/claude/versions/2.1.316", "claude", "2.1.316"},
		{"/opt/homebrew/Caskroom/codex/0.166.0/bin/codex", "codex", "0.166.0"},
		{"/Users/x/.grok/downloads/grok-1.0.73-macos-aarch64", "grok", "1.0.73"},
		// nvm、asdf 装的：版本号在 agent 名字前面，是 node 的
		{"/Users/x/.nvm/versions/node/v22.3.0/bin/codex", "codex", ""},
		{"/Users/x/.asdf/installs/nodejs/22.3.0/lib/node_modules/@openai/codex/bin/codex.js", "codex", ""},
		// 不是路径：提示词、配置
		{"see /tmp/codex/1.2.3 for details", "codex", ""},
		{"developer_instructions=codex 1.2.3", "codex", ""},
	} {
		if got := pathVersion(c.path, c.cmd); got != c.want {
			t.Errorf("pathVersion(%q) = %q，要 %q", c.path, got, c.want)
		}
	}
}

func TestInsert(t *testing.T) {
	if got := Insert(KindCommand, Item{Name: "/compact", Args: "[指示]"}); got != "/compact " {
		t.Errorf("带参数的命令：%q", got)
	}
	if got := Insert(KindCommand, Item{Name: "/clear"}); got != "/clear" {
		t.Errorf("不带参数的命令：%q", got)
	}
	if got := Insert(KindPrefix, Item{Name: "@"}); got != "@" {
		t.Errorf("前缀：%q", got)
	}
	if got := Insert(KindKey, Item{Name: "Esc Esc"}); got != "" {
		t.Errorf("按键不贴：%q", got)
	}
}
