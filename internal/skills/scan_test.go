package skills

import (
	"os"
	"path/filepath"
	"slices"
	"testing"
)

func write(t *testing.T, path, body string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

func skill(t *testing.T, dir, name, extra string) {
	t.Helper()
	write(t, filepath.Join(dir, "SKILL.md"), "---\nname: "+name+"\ndescription: "+name+" 的说明\n"+extra+"---\n正文\n")
}

func link(t *testing.T, target, at string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(at), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, at); err != nil {
		t.Fatal(err)
	}
}

func find(t *testing.T, c Catalog, name string, scope Scope) Skill {
	t.Helper()
	for _, s := range c.Skills {
		if s.Name == name && s.Scope == scope {
			return s
		}
	}
	t.Fatalf("没找到 %s（%s）", name, scope)
	return Skill{}
}

func states(s Skill) [NumAgents]State {
	var out [NumAgents]State
	for _, a := range Agents {
		out[a] = s.Agents[a].State
	}
	return out
}

func issues(c Catalog, kind IssueKind) []Issue {
	var out []Issue
	for _, is := range c.Issues {
		if is.Kind == kind {
			out = append(out, is)
		}
	}
	return out
}

func TestParseMeta(t *testing.T) {
	cases := []struct{ src, desc string }{
		{"---\nname: a\ndescription: 一行\n---\n", "一行"},
		{"---\nname: a\ndescription: \"带: 冒号\"\n---\n", "带: 冒号"},
		{"---\nname: a\ndescription: >-\n  第一行\n  第二行\nlicense: MIT\n---\n", "第一行 第二行"},
		{"---\nname: a\ndescription: |\n  第一行\n  第二行\n---\n", "第一行\n第二行"},
		{"---\nname: a\ndescription: 开头\n  接着折行\n---\n", "开头 接着折行"},
		{"\ufeff---\r\nname: a\r\ndescription: 带 BOM\r\n---\r\n", "带 BOM"},
	}
	for _, c := range cases {
		if m := parseMeta(c.src); m.Description != c.desc || m.Name != "a" {
			t.Errorf("%q: 得到 %+v，要 %q", c.src, m, c.desc)
		}
	}
	m := parseMeta("---\nname: a\ndisable-model-invocation: true\nuser-invocable: false\nmetadata:\n  author: x\n---\n")
	if !m.ModelOff || !m.UserHidden {
		t.Errorf("开关没认出来：%+v", m)
	}
	if m := parseMeta("没有头部"); m != (Meta{}) {
		t.Errorf("没有头部时应为零值：%+v", m)
	}
}

func TestScanAgents(t *testing.T) {
	h := t.TempDir()
	hub := filepath.Join(h, ".agents", "skills")
	skill(t, filepath.Join(hub, "shared"), "shared", "")
	skill(t, filepath.Join(hub, "manual"), "manual", "disable-model-invocation: true\n")
	skill(t, filepath.Join(h, ".grok", "skills", "grok-only"), "grok-only", "")
	// npx skills 的做法：Claude 读不到 ~/.agents，给它建链接；Grok 本来就读，链接多余
	link(t, "../../.agents/skills/shared", filepath.Join(h, ".claude", "skills", "shared"))
	link(t, "../../.agents/skills/shared", filepath.Join(h, ".grok", "skills", "shared"))
	link(t, "../../.agents/skills/gone", filepath.Join(h, ".claude", "skills", "gone"))
	write(t, filepath.Join(h, ".agents", ".skill-lock.json"), `{"version":3,"skills":{"shared":{"source":"me/shared","sourceType":"github","skillPath":"skills/shared/SKILL.md"},"lost":{"source":"me/lost"},"grok-only":{"source":"me/grok"}}}`)

	c := Scan(Options{Home: h})
	s := find(t, c, "shared", ScopeUser)
	if got := states(s); got != [NumAgents]State{On, On, On, On} {
		t.Errorf("shared 各家状态 %v", got)
	}
	if s.Source != "me/shared" || len(s.Paths) != 3 {
		t.Errorf("shared 来源 %q，位置 %v", s.Source, s.Paths)
	}
	if s.SourceURL != "https://github.com/me/shared/tree/HEAD/skills/shared" {
		t.Errorf("shared 来源网页 %q", s.SourceURL)
	}
	if got := states(find(t, c, "manual", ScopeUser)); got != [NumAgents]State{Unseen, On, Manual, Manual} {
		t.Errorf("manual 各家状态 %v（Codex 不认 disable-model-invocation）", got)
	}
	if got := states(find(t, c, "grok-only", ScopeUser)); got != [NumAgents]State{Unseen, Unseen, On, Unseen} {
		t.Errorf("grok-only 各家状态 %v", got)
	}
	if !find(t, c, "shared", ScopeUser).Agents[Cursor].Inferred {
		t.Error("Cursor 的状态应标为推断")
	}

	if is := issues(c, BrokenLink); len(is) != 1 || is[0].Name != "gone" {
		t.Errorf("断链 %+v", is)
	}
	if is := issues(c, RedundantLink); len(is) != 1 || is[0].Path != filepath.Join(h, ".grok", "skills", "shared") {
		t.Errorf("多余的链接 %+v", is)
	}
	// grok-only 是装成副本放在 ~/.grok/skills 里的，不算对不上
	if is := issues(c, LockMissing); len(is) != 1 || is[0].Name != "lost" {
		t.Errorf("锁文件对不上 %+v", is)
	}
}

func TestScanDuplicate(t *testing.T) {
	h := t.TempDir()
	skill(t, filepath.Join(h, ".agents", "skills", "dup"), "dup", "")
	skill(t, filepath.Join(h, ".claude", "skills", "dup"), "dup", "")
	c := Scan(Options{Home: h})
	var claudeCopy, hubCopy Skill
	for _, s := range c.Skills {
		if s.Name != "dup" {
			continue
		}
		if filepath.Base(filepath.Dir(s.Dir)) == "skills" && filepath.Base(filepath.Dir(filepath.Dir(s.Dir))) == ".claude" {
			claudeCopy = s
		} else {
			hubCopy = s
		}
	}
	// Grok 先读 ~/.agents，Claude 的那份被盖住；Claude 只读自己那份
	if claudeCopy.Agents[Claude].State != On || claudeCopy.Agents[Grok].State != Shadowed || hubCopy.Agents[Grok].State != On {
		t.Errorf("同名两份：claude 那份 %v，正本 %v", states(claudeCopy), states(hubCopy))
	}
	if is := issues(c, Duplicate); len(is) != 1 || len(is[0].Agents) != 2 || !is[0].Same {
		t.Errorf("同名冲突 %+v（Grok、Cursor 都读两份，内容一样）", is)
	}

	// Grok 经链接读到正本、Cursor 读目录本身：还是同一条；内容改了就不再算一样
	os.MkdirAll(filepath.Join(h, ".grok", "skills"), 0o755)
	if err := os.Symlink(filepath.Join(h, ".agents", "skills", "dup"), filepath.Join(h, ".grok", "skills", "dup")); err != nil {
		t.Fatal(err)
	}
	write(t, filepath.Join(h, ".claude", "skills", "dup", "extra.md"), "x")
	if is := issues(Scan(Options{Home: h}), Duplicate); len(is) != 1 || len(is[0].Agents) != 2 || is[0].Same {
		t.Errorf("同名冲突 %+v（经链接读到的正本算同一条，内容不同）", is)
	}
}

func TestScanConfigs(t *testing.T) {
	h := t.TempDir()
	t.Setenv("CODEX_HOME", "")
	t.Setenv("GROK_CLAUDE_SKILLS_ENABLED", "")
	t.Setenv("GROK_CURSOR_SKILLS_ENABLED", "")
	for _, n := range []string{"a", "b", "c"} {
		skill(t, filepath.Join(h, ".agents", "skills", n), n, "")
		link(t, "../../.agents/skills/"+n, filepath.Join(h, ".claude", "skills", n))
	}
	skill(t, filepath.Join(h, ".claude", "skills", "claude-only"), "claude-only", "")
	write(t, filepath.Join(h, ".claude", "settings.json"), `{"skillOverrides":{"a":"off","b":"user-invocable-only","c":"name-only"}}`)
	write(t, filepath.Join(h, ".codex", "config.toml"), "[[skills.config]]\nname = \"a\"\nenabled = false\n\n[[skills.config]]\nname = \"b\"\nenabled = false\n\n[[skills.config]]\nname = \"b\"\nenabled = true\n")
	write(t, filepath.Join(h, ".grok", "config.toml"), "[skills]\ndisabled = [\"a\"]\nignore = [\"~/.agents/skills/b\"]\n\n[compat.claude]\nskills = false\n")

	c := Scan(Options{Home: h})
	if got := states(find(t, c, "a", ScopeUser)); got != [NumAgents]State{Off, Off, Off, On} {
		t.Errorf("a %v", got)
	}
	// Codex 后写的 enabled = true 盖过前面的；Grok 的 ignore 藏掉 ~/.agents 那条（算关了），又不读 Claude 的目录
	if got := states(find(t, c, "b", ScopeUser)); got != [NumAgents]State{Manual, On, Off, On} {
		t.Errorf("b %v", got)
	}
	if got := find(t, c, "c", ScopeUser).Agents[Claude]; got.State != On || got.Why == "" {
		t.Errorf("c 的 name-only 应是启用并写明原因：%+v", got)
	}
	if got := find(t, c, "claude-only", ScopeUser).Agents[Grok].State; got != Unseen {
		t.Errorf("[compat.claude] skills = false 时 Grok 不读 ~/.claude/skills：%v", got)
	}
}

func TestScanProject(t *testing.T) {
	h := t.TempDir()
	repo := filepath.Join(t.TempDir(), "repo")
	ws := filepath.Join(repo, "app")
	write(t, filepath.Join(repo, ".git", "HEAD"), "ref: refs/heads/main\n")
	skill(t, filepath.Join(ws, ".claude", "skills", "deploy"), "deploy", "")
	skill(t, filepath.Join(repo, ".agents", "skills", "lint"), "lint", "")
	skill(t, filepath.Join(h, ".claude", "skills", "deploy"), "deploy", "")
	write(t, filepath.Join(ws, ".claude", "settings.local.json"), `{"skillOverrides":{"lint":"off"}}`)

	c := Scan(Options{Home: h, Dir: ws})
	// Claude 个人的盖过项目的；Grok、Cursor 项目的优先
	deploy := find(t, c, "deploy", ScopeProject)
	if got := states(deploy); got != [NumAgents]State{Shadowed, Unseen, On, On} {
		t.Errorf("项目的 deploy %v", got)
	}
	// 仓库根的 .agents/skills：Codex、Grok 往上找得到，Claude 不读 .agents
	lint := find(t, c, "lint", ScopeProject)
	if got := states(lint); got != [NumAgents]State{Unseen, On, On, Unseen} {
		t.Errorf("仓库根的 lint %v", got)
	}

	if len(Scan(Options{Home: h}).Skills) != 1 {
		t.Error("不给工作区时不扫项目级")
	}
}

func TestApplyCodex(t *testing.T) {
	h := t.TempDir()
	skill(t, filepath.Join(h, ".agents", "skills", "a"), "a", "")
	plugin := filepath.Join(h, ".codex", "plugins", "cache", "m", "p", "1", "skills", "x", "SKILL.md")
	c := Scan(Options{Home: h, Codex: []CodexSkill{
		{Name: "a", Path: filepath.Join(h, ".agents", "skills", "a", "SKILL.md"), Enabled: false, Scope: "user"},
		{Name: "p:x", Path: plugin, Enabled: true, Scope: "user", PluginID: "p@m"},
	}})
	if !c.CodexLive {
		t.Error("给了 skills/list 的结果应标为 CodexLive")
	}
	if got := find(t, c, "a", ScopeUser).Agents[Codex].State; got != Off {
		t.Errorf("a 应按 skills/list 关掉：%v", got)
	}
	if got := find(t, c, "p:x", ScopePlugin).Agents[Codex].State; got != On {
		t.Errorf("插件技能应补进来：%v", got)
	}
}

// Grok 的 [skills] paths：目录往下各级找，也可以直接是一个 SKILL.md。
func TestGrokExtraPaths(t *testing.T) {
	h := t.TempDir()
	t.Setenv("GROK_CLAUDE_SKILLS_ENABLED", "")
	t.Setenv("GROK_CURSOR_SKILLS_ENABLED", "")
	skill(t, filepath.Join(h, "team", "flat"), "flat", "")
	skill(t, filepath.Join(h, "team", "group", "lint"), "lint", "")
	skill(t, filepath.Join(h, "one", "oneshot"), "oneshot", "")
	write(t, filepath.Join(h, ".grok", "config.toml"), "[skills]\npaths = [\"~/team\", \"~/one/oneshot/SKILL.md\"]\n")
	c := Scan(Options{Home: h})
	for _, n := range []string{"flat", "lint", "oneshot"} {
		if st := find(t, c, n, ScopeUser).Agents[Grok].State; st != On {
			t.Errorf("%s：%v", n, st)
		}
	}
}

func TestLockURL(t *testing.T) {
	cases := []struct {
		e    lockEntry
		want string
	}{
		{lockEntry{Source: "a/b", SourceType: "github", SkillPath: "SKILL.md"}, "https://github.com/a/b"},
		{lockEntry{Source: "a/b", SourceType: "github", SkillPath: "x/y/SKILL.md", Ref: "dev"}, "https://github.com/a/b/tree/dev/x/y"},
		{lockEntry{Source: "open.feishu.cn", SourceType: "well-known", SourceURL: "https://open.feishu.cn/.well-known/skills/lark-doc/SKILL.md"}, "https://open.feishu.cn/.well-known/skills/lark-doc/SKILL.md"},
		{lockEntry{Source: "gitlab.com/a/b", SourceType: "git", SourceURL: "https://gitlab.com/a/b.git"}, "https://gitlab.com/a/b"},
		{lockEntry{Source: "./local", SourceType: "local", SourceURL: "/Users/x/local"}, ""},
	}
	for _, c := range cases {
		if got := c.e.url(); got != c.want {
			t.Errorf("%+v：%q，要 %q", c.e, got, c.want)
		}
	}
}

func TestUnseenLink(t *testing.T) {
	h := t.TempDir()
	skill(t, filepath.Join(h, ".agents", "skills", "hub"), "hub", "")
	c := Scan(Options{Home: h})
	s := find(t, c, "hub", ScopeUser)
	st := s.Agents[Claude]
	if st.State != Unseen || st.NotRead != "~/.agents/skills" || !slices.Equal(st.Reads, []string{"~/.claude/skills"}) {
		t.Fatalf("Claude 读不到的原因 %+v", st)
	}
	link, ok := LinkFor(Options{Home: h}, s, Claude)
	if !ok || link != filepath.Join(h, ".claude", "skills", "hub") {
		t.Fatalf("链接位置 %q %v", link, ok)
	}
	if _, ok := LinkFor(Options{Home: h}, s, Codex); ok {
		t.Error("读得到的不该给链接")
	}
	if err := MakeLink(link, s.Dir); err != nil {
		t.Fatal(err)
	}
	if target, _ := os.Readlink(link); target != filepath.Join("..", "..", ".agents", "skills", "hub") {
		t.Errorf("链接指向 %q，要相对路径", target)
	}
	s = find(t, Scan(Options{Home: h}), "hub", ScopeUser)
	if s.Agents[Claude].State != On {
		t.Errorf("建了链接后 Claude %v", s.Agents[Claude])
	}
	if _, ok := LinkFor(Options{Home: h}, s, Claude); ok {
		t.Error("读得到了不该再给链接")
	}
}
