package skills

import (
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

func TestPosseSkillFiles(t *testing.T) {
	for _, s := range PosseSkills() {
		m := parseMeta(s.Content)
		if m.Name != s.Name || m.Description == "" || !m.Posse {
			t.Errorf("%s: %+v", s.Name, m)
		}
		if !strings.HasPrefix(s.Name, "posse-") {
			t.Errorf("%s: no posse prefix", s.Name)
		}
	}
	// description 里有 ": "，要加引号才是合法 YAML，Cursor 才认这个技能
	line := strings.SplitN(strings.TrimPrefix(TasksFrontmatter(), "---\nname: posse-tasks\ndescription: "), "\n", 2)[0]
	var desc string
	if err := json.Unmarshal([]byte(line), &desc); err != nil || desc != TasksDescription {
		t.Fatalf("description not a quoted string: %q %v", line, err)
	}
	if m := parseMeta(PosseSkills()[1].Content); !m.ModelOff {
		t.Error("posse-rename should be invoked by the user only")
	}
}

func TestReadPosse(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	st := ReadPosse(home)
	if st.Installed() || len(st.Missing) != 2 || st.Current() {
		t.Fatalf("empty home: %+v", st)
	}
	if err := CopyPosse(home, PosseNames(), true); err != nil {
		t.Fatal(err)
	}
	st = ReadPosse(home)
	if !st.Current() || !st.Installed() || !st.Claude || st.NPX {
		t.Fatalf("copied: %+v", st)
	}
	// Claude 那边是相对链接，扫描认得出是 Posse 的、同一份
	cat := Scan(Options{Home: home})
	for _, n := range PosseNames() {
		sk := find(t, cat, n, ScopeUser)
		if !sk.Meta.Posse || sk.Agents[Claude].State == Unseen || sk.Agents[Codex].State == Unseen {
			t.Errorf("%s: %+v", n, sk)
		}
	}
	if len(cat.Issues) != 0 {
		t.Errorf("issues: %+v", cat.Issues)
	}
	// 旧版的内容算要更新；再复制一次就是这一版
	file := filepath.Join(home, ".agents", "skills", TasksSkill, "SKILL.md")
	os.WriteFile(file, []byte(withMarker(TasksFrontmatter())+"old body\n"), 0o644)
	if st := ReadPosse(home); !slices.Equal(st.Outdated, []string{TasksSkill}) || !slices.Equal(st.Pending(), []string{TasksSkill}) {
		t.Fatalf("outdated: %+v", st)
	}
	if err := CopyPosse(home, []string{TasksSkill}, true); err != nil {
		t.Fatal(err)
	}
	if st := ReadPosse(home); !st.Current() {
		t.Fatalf("after update: %+v", st)
	}
	// 锁文件里记着 Posse 的来源：是 npx 装的
	src, _ := PosseSourceDir()
	lock, _ := json.Marshal(map[string]any{"version": 3, "skills": map[string]any{TasksSkill: map[string]string{"source": src, "sourceType": "local"}}})
	write(t, filepath.Join(home, ".agents", ".skill-lock.json"), string(lock))
	if st := ReadPosse(home); !st.NPX {
		t.Fatalf("npx: %+v", st)
	}
}

// 用户自己有同名技能：不算装了，也不覆盖
func TestPosseTaken(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	skill(t, filepath.Join(home, ".agents", "skills", TasksSkill), TasksSkill, "")
	skill(t, filepath.Join(home, ".claude", "skills", RenameSkill), RenameSkill, "")
	st := ReadPosse(home)
	if !slices.Equal(st.Taken, []string{TasksSkill, RenameSkill}) || st.Installed() || len(st.Pending()) != 0 {
		t.Fatalf("taken: %+v", st)
	}
	if err := CopyPosse(home, []string{TasksSkill}, false); err == nil {
		t.Fatal("overwrote the user's skill")
	}
	if err := CopyPosse(home, []string{RenameSkill}, true); err == nil {
		t.Fatal("replaced the user's Claude skill with a link")
	}
}

func TestWritePosseSource(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	dir, err := PosseSourceDir()
	if err != nil {
		t.Fatal(err)
	}
	write(t, filepath.Join(dir, "session-namer", "SKILL.md"), "old")
	if got, err := WritePosseSource(); err != nil || got != dir {
		t.Fatal(got, err)
	}
	entries, _ := os.ReadDir(dir)
	var names []string
	for _, e := range entries {
		names = append(names, e.Name())
	}
	if want := slices.Sorted(slices.Values(PosseNames())); !slices.Equal(names, want) {
		t.Fatalf("source: %v", names)
	}
	args := PosseInstall(dir, PosseNames(), true).Args()
	want := []string{dir, "-g", "-a", "codex", "-a", "claude-code", "-s", TasksSkill, "-s", RenameSkill, "-y"}
	if !slices.Equal(args[len(args)-len(want):], want) {
		t.Fatalf("install args %q", args)
	}
}

// posse task guide --skill 给用户自己放的技能不带标记，Posse 不去更新、删除它
func TestTasksFrontmatterUnmarked(t *testing.T) {
	if parseMeta(TasksFrontmatter() + "body").Posse {
		t.Fatal("--skill output carries the Posse marker")
	}
	if !strings.HasPrefix(PosseSkills()[0].Content, strings.TrimSuffix(TasksFrontmatter(), "---\n\n")) {
		t.Fatal("installed posse-tasks no longer starts with the same frontmatter")
	}
}

// Claude 的目录里同名的是用户自己的：整批不建 Claude 链接
func TestLinkClaude(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	if !ReadPosse(home).LinkClaude() {
		t.Fatal("first install should link Claude")
	}
	if err := CopyPosse(home, []string{TasksSkill}, true); err != nil {
		t.Fatal(err)
	}
	if err := CopyPosse(home, []string{RenameSkill}, false); err != nil {
		t.Fatal(err)
	}
	skill(t, filepath.Join(home, ".claude", "skills", RenameSkill), RenameSkill, "")
	os.WriteFile(filepath.Join(home, ".agents", "skills", RenameSkill, "SKILL.md"), []byte(PosseSkills()[1].Content+"old\n"), 0o644)
	st := ReadPosse(home)
	if !st.Claude || !slices.Equal(st.ClaudeTaken, []string{RenameSkill}) || st.LinkClaude() {
		t.Fatalf("claude taken: %+v link=%v", st, st.LinkClaude())
	}
}

// 改了技能内容要给 posseSkillsVersion 加一，再把这里换成新的摘要：
// 不加版本，装着旧内容的旧 Posse 和新 Posse 会轮流把对方装的换掉。
func TestPosseSkillsVersion(t *testing.T) {
	const version, digest = 1, "c994fcb52db0"
	if posseSkillsVersion != version || posseDigest() != digest {
		t.Fatalf("skills content is %s at version %d: bump posseSkillsVersion and update this test", posseDigest(), posseSkillsVersion)
	}
	for _, s := range PosseSkills() {
		if fileVersion(s.Content) != posseSkillsVersion {
			t.Errorf("%s: version %d", s.Name, fileVersion(s.Content))
		}
	}
}

// 装上的比这一版新（更新的 Posse 装的）：不算要更新，不降级；比这一版旧的照常更新
func TestPosseNewerInstalled(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	if err := CopyPosse(home, PosseNames(), true); err != nil {
		t.Fatal(err)
	}
	file := filepath.Join(home, ".agents", "skills", TasksSkill, "SKILL.md")
	newer := strings.Replace(PosseSkills()[0].Content, versionKey+`: "1"`, versionKey+`: "2"`, 1) + "newer body\n"
	os.WriteFile(file, []byte(newer), 0o644)
	if st := ReadPosse(home); !st.Current() {
		t.Fatalf("newer install treated as outdated: %+v", st)
	}
	older := strings.Replace(PosseSkills()[0].Content, "  "+versionKey+`: "1"`+"\n", "", 1) + "older body\n"
	os.WriteFile(file, []byte(older), 0o644)
	if st := ReadPosse(home); !slices.Equal(st.Outdated, []string{TasksSkill}) {
		t.Fatalf("older install: %+v", st)
	}
}
