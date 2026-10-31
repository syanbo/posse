package skills

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestSetJSONOverride(t *testing.T) {
	cases := []struct{ name, src, key, value, want string }{
		{"空文件", "", "a", "off", "{\n  \"skillOverrides\": {\n    \"a\": \"off\"\n  }\n}\n"},
		{"加在最后", "{\n  \"model\": \"opus\"\n}\n", "a", "off", "{\n  \"model\": \"opus\",\n  \"skillOverrides\": {\n    \"a\": \"off\"\n  }\n}\n"},
		{"空对象", "{}\n", "a", "off", "{\n  \"skillOverrides\": {\n    \"a\": \"off\"\n  }\n}\n"},
		{"保留顺序和别的项", "{\n    \"skillOverrides\": {\"b\": \"name-only\"},\n    \"x\": 1\n}\n", "a", "off",
			"{\n    \"skillOverrides\": {\n        \"b\": \"name-only\",\n        \"a\": \"off\"\n    },\n    \"x\": 1\n}\n"},
		{"删一项", "{\n  \"skillOverrides\": {\n    \"a\": \"off\",\n    \"b\": \"off\"\n  }\n}\n", "a", "",
			"{\n  \"skillOverrides\": {\n    \"b\": \"off\"\n  }\n}\n"},
		{"删空了连键一起删（中间）", "{\n  \"skillOverrides\": {\n    \"a\": \"off\"\n  },\n  \"x\": 1\n}\n", "a", "", "{\n  \"x\": 1\n}\n"},
		{"删空了连键一起删（最后）", "{\n  \"x\": 1,\n  \"skillOverrides\": {\n    \"a\": \"off\"\n  }\n}\n", "a", "", "{\n  \"x\": 1\n}\n"},
		{"嵌套里同名的键不算", "{\n  \"env\": {\"skillOverrides\": 1}\n}\n", "a", "off",
			"{\n  \"env\": {\"skillOverrides\": 1},\n  \"skillOverrides\": {\n    \"a\": \"off\"\n  }\n}\n"},
	}
	for _, c := range cases {
		got, err := setJSONOverride(c.src, c.key, c.value)
		if err != nil || got != c.want {
			t.Errorf("%s：得到 %q（%v），要 %q", c.name, got, err, c.want)
		}
		if err == nil && !strings.Contains(got, "{") == (got != "") {
			t.Errorf("%s：结果不是 JSON", c.name)
		}
	}
	if _, err := setJSONOverride("{坏的", "a", "off"); err == nil {
		t.Error("不合法的 JSON 不该改")
	}
}

func TestSetCodexEntry(t *testing.T) {
	src := "model = \"gpt\"\n\n[projects.\"/x\"]\ntrust_level = \"trusted\"\n"
	off := setCodexEntry(src, codexEntry{Name: "a"}, "a", "/h/a/SKILL.md", false)
	want := src + "\n[[skills.config]]\nname = \"a\"\nenabled = false\n"
	if off != want {
		t.Fatalf("关：%q", off)
	}
	if back := setCodexEntry(off, codexEntry{Name: "a"}, "a", "/h/a/SKILL.md", true); back != src {
		t.Errorf("再打开应回到原样：%q", back)
	}
	// 已有 enabled = true 的段改成 false，不重复加
	has := "[[skills.config]]\nname = \"a\"\nenabled = true\n\n[other]\nx = 1\n"
	if got := setCodexEntry(has, codexEntry{Name: "a"}, "a", "/h/a/SKILL.md", false); got != "[[skills.config]]\nname = \"a\"\nenabled = false\n\n[other]\nx = 1\n" {
		t.Errorf("改已有的段：%q", got)
	}
	// Codex 自己的界面按路径关的用户级技能：按名字打开时那段也删掉
	byPath := src + "\n[[skills.config]]\npath = \"/h/a/SKILL.md\"\nenabled = false\n"
	if got := setCodexEntry(byPath, codexEntry{Name: "a"}, "a", "/h/a/SKILL.md", true); got != src {
		t.Errorf("按路径关的应能打开：%q", got)
	}
	// 按路径打开，但按名字的那段还关着：加一段 enabled = true
	byName := "[[skills.config]]\nname = \"a\"\nenabled = false\n"
	if got := setCodexEntry(byName, codexEntry{Path: "/p/a/SKILL.md"}, "a", "/p/a/SKILL.md", true); !strings.Contains(got, "path = \"/p/a/SKILL.md\"\nenabled = true") {
		t.Errorf("按路径打开：%q", got)
	}
}

func TestSetTOMLList(t *testing.T) {
	src := "[marketplace]\nx = true\n"
	got, _ := setTOMLList(src, "skills", "disabled", "a", true, nil)
	if got != src+"\n[skills]\ndisabled = [\"a\"]\n" {
		t.Fatalf("加表：%q", got)
	}
	got, _ = setTOMLList(got, "skills", "disabled", "b", true, nil)
	if !strings.Contains(got, "disabled = [\"a\", \"b\"]") {
		t.Errorf("加一项：%q", got)
	}
	got, _ = setTOMLList(got, "skills", "disabled", "a", false, nil)
	if !strings.Contains(got, "disabled = [\"b\"]") {
		t.Errorf("去一项：%q", got)
	}
	// 跨行的数组、表里已有别的键
	multi := "[skills]\npaths = [\"~/x\"]\nignore = [\n  \"~/a\",\n]\n\n[compat.claude]\nskills = true\n"
	got, err := setTOMLList(multi, "skills", "ignore", "/home/b", true, nil)
	if err != nil || got != "[skills]\npaths = [\"~/x\"]\nignore = [\"~/a\", \"/home/b\"]\n\n[compat.claude]\nskills = true\n" {
		t.Errorf("跨行数组：%q %v", got, err)
	}
	// 按规整后的路径去掉 ~ 写的那项
	got, _ = setTOMLList(multi, "skills", "ignore", "/home/a", false, func(s string) string { return strings.Replace(s, "~", "/home", 1) })
	if !strings.Contains(got, "ignore = []") {
		t.Errorf("去掉 ~ 写的：%q", got)
	}
}

func TestPlanToggle(t *testing.T) {
	h := t.TempDir()
	t.Setenv("CODEX_HOME", "")
	skill(t, filepath.Join(h, ".agents", "skills", "a"), "a", "")
	link(t, "../../.agents/skills/a", filepath.Join(h, ".claude", "skills", "a"))
	ws := filepath.Join(t.TempDir(), "ws")
	write(t, filepath.Join(ws, ".git", "HEAD"), "")
	skill(t, filepath.Join(ws, ".agents", "skills", "p"), "p", "")
	o := Options{Home: h, Dir: ws}

	// 关掉再看扫描结果：三家都关了，Cursor 没有开关
	for _, ag := range []Agent{Claude, Codex, Grok} {
		s := find(t, Scan(o), "a", ScopeUser)
		ch, err := PlanToggle(o, s, ag, false)
		if err != nil {
			t.Fatalf("%s：%v", ag, err)
		}
		if err := ch.Apply(); err != nil {
			t.Fatal(err)
		}
	}
	s := find(t, Scan(o), "a", ScopeUser)
	if got := states(s); got != [NumAgents]State{Off, Off, Off, On} {
		t.Errorf("关掉后 %v", got)
	}
	if _, err := PlanToggle(o, s, Cursor, false); err != ErrNoToggle {
		t.Errorf("Cursor 应没有开关：%v", err)
	}
	// 再打开：配置回到原样（文件留着，内容里没有这个技能）
	for _, ag := range []Agent{Claude, Codex, Grok} {
		ch, err := PlanToggle(o, find(t, Scan(o), "a", ScopeUser), ag, true)
		if err != nil {
			t.Fatalf("%s：%v", ag, err)
		}
		if err := ch.Apply(); err != nil {
			t.Fatal(err)
		}
	}
	if got := states(find(t, Scan(o), "a", ScopeUser)); got != [NumAgents]State{On, On, On, On} {
		t.Errorf("打开后 %v", got)
	}
	if b, _ := os.ReadFile(filepath.Join(h, ".claude", "settings.json")); strings.Contains(string(b), "skillOverrides") {
		t.Errorf("打开后 settings.json 不该留 skillOverrides：%s", b)
	}

	// 项目级：Codex 按路径、Grok 按目录 ignore，只关这一份
	p := find(t, Scan(o), "p", ScopeProject)
	for _, ag := range []Agent{Codex, Grok} {
		ch, err := PlanToggle(o, p, ag, false)
		if err != nil {
			t.Fatal(err)
		}
		if ag == Codex && !strings.Contains(ch.After, "path = ") || ag == Grok && !strings.Contains(ch.After, "ignore = ") {
			t.Errorf("%s 项目级的写法：%q", ag, ch.After)
		}
		ch.Apply()
	}
	if got := states(find(t, Scan(o), "p", ScopeProject)); got[Codex] != Off || got[Grok] != Off {
		t.Errorf("项目级关掉后 %v", got)
	}

	// 算好改动后文件被改过：不写
	s = find(t, Scan(o), "a", ScopeUser)
	ch, _ := PlanToggle(o, s, Claude, false)
	write(t, ch.File, "{\"model\": \"x\"}\n")
	if err := ch.Apply(); err != ErrStale {
		t.Errorf("文件变了应报 ErrStale：%v", err)
	}
}

func TestDiff(t *testing.T) {
	d := Diff("a\nb\nc\nd\ne\nf\n", "a\nb\nc\nX\ne\nf\n", 1)
	want := []DiffLine{{0, ""}, {' ', "c"}, {'-', "d"}, {'+', "X"}, {' ', "e"}, {0, ""}}
	want = want[1 : len(want)-1]
	if len(d) != len(want) {
		t.Fatalf("%+v", d)
	}
	for i := range d {
		if d[i] != want[i] {
			t.Errorf("%d：%+v，要 %+v", i, d[i], want[i])
		}
	}
	if d := Diff("", "x\n", 2); len(d) != 1 || d[0].Kind != '+' {
		t.Errorf("新文件：%+v", d)
	}
}

// 配置文件是符号链接时写到它指向的文件，链接留着。
func TestApplyThroughSymlink(t *testing.T) {
	dir := t.TempDir()
	real := filepath.Join(dir, "dotfiles", "settings.json")
	write(t, real, "{}\n")
	at := filepath.Join(dir, "settings.json")
	link(t, real, at)
	ch := Change{File: at, Before: "{}\n", After: "{\"x\": 1}\n"}
	if err := ch.Apply(); err != nil {
		t.Fatal(err)
	}
	if fi, _ := os.Lstat(at); fi.Mode()&os.ModeSymlink == 0 {
		t.Error("链接被换成了普通文件")
	}
	if b, _ := os.ReadFile(real); string(b) != "{\"x\": 1}\n" {
		t.Errorf("链接指向的文件：%q", b)
	}
}

// Grok 的项目技能关掉后（写进 ignore）仍算关了、能再打开，打开时 ignore 里去掉。
func TestGrokProjectReenable(t *testing.T) {
	h := t.TempDir()
	ws := filepath.Join(t.TempDir(), "ws")
	write(t, filepath.Join(ws, ".git", "HEAD"), "")
	skill(t, filepath.Join(ws, ".grok", "skills", "p"), "p", "")
	o := Options{Home: h, Dir: ws}
	ch, err := PlanToggle(o, find(t, Scan(o), "p", ScopeProject), Grok, false)
	if err != nil || ch.Apply() != nil {
		t.Fatal(err)
	}
	p := find(t, Scan(o), "p", ScopeProject)
	if st := p.Agents[Grok]; st.State != Off || !st.Ignored {
		t.Fatalf("关掉后 %+v", st)
	}
	ch, err = PlanToggle(o, p, Grok, true)
	if err != nil {
		t.Fatal(err)
	}
	if err := ch.Apply(); err != nil {
		t.Fatal(err)
	}
	if st := find(t, Scan(o), "p", ScopeProject).Agents[Grok]; st.State != On {
		t.Errorf("打开后 %+v", st)
	}
}
