package skills

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

// Posse 自带的技能装进用户的技能目录：~/.agents/skills（Codex、Grok、Cursor 都读），Claude 读的 ~/.claude/skills 链过去。
// 正文只指向 posse 程序输出的说明（`posse task guide`、`posse session guide`），规则跟着程序版本走，技能本身很少要改。
// 安装经 npx skills，来源是 PosseSourceDir 里由窗口写出的目录：日常版和开发版共用用户的技能目录和锁文件，
// 来源不放在各自的 POSSE_HOME 里，免得谁最后装锁文件就记谁的路径。npx skills 不查本地来源的更新，
// 装上的版本（posseSkillsVersion）旧了、或同一版内容不同时再装一次；装上的比这一版新（另一个更新的 Posse 装的）不动，
// 免得日常版和开发版轮流把对方的换掉。没有 npx 时直接复制（CopyPosse），照 npx 的布局放。

const (
	TasksSkill  = "posse-tasks"
	RenameSkill = "posse-rename"

	TasksDescription = "Dispatch work to other agents (claude, codex, grok, cursor) in Posse terminals and coordinate them with `posse task`: start workers with a chosen model and reasoning effort, wait for results, answer their questions, retry failures, merge worktrees. Use when running inside Posse (POSSE_TERMINAL_ID is set) and the user asks to delegate, parallelize, hand off, supervise or fan out work to other agents."
	tasksBody        = "Before running any `posse task` command, run `posse task guide` and follow it: it is the guide for this exact posse version.\n"

	// posseMarker 写在 frontmatter 的 metadata 里，认出 Posse 装的这一份（复制装的没有锁文件可查），
	// 同名但没有它的是用户自己的，不覆盖。
	posseMarker = "managed-by: posse"
	// posseSkillsVersion 是自带技能内容的版本，跟着 posseMarker 写进 metadata。改了技能内容就加一
	// （TestPosseSkillsVersion 守着），比装上的旧的 Posse 不去降级它。
	posseSkillsVersion = 1
	versionKey         = "posse-skills-version"
)

// posseMetadata 是 Posse 装的技能 frontmatter 里的 metadata：标记和内容的版本。
var posseMetadata = "metadata:\n  " + posseMarker + "\n  " + versionKey + ": \"" + strconv.Itoa(posseSkillsVersion) + "\"\n"

var renameSkill = `---
name: posse-rename
description: Name the current session (MMDD｜类型｜摘要) with posse session title. Use only when the user invokes posse-rename inside Posse.
disable-model-invocation: true
argument-hint: "[摘要或完整标题，可省略]"
allowed-tools: Bash(posse session guide), Bash(posse session title:*)
` + posseMetadata + `---

User arguments: $ARGUMENTS
(If the line above still shows the literal ` + "`$ARGUMENTS`" + `, the arguments are whatever the user typed after invoking this skill, possibly nothing.)

Run ` + "`posse session guide`" + ` and follow it: draft the title from the arguments and this session, then set it with ` + "`posse session title`" + `.
`

// TasksFrontmatter 是编排技能的头部，不带 Posse 的标记：`posse task guide --skill` 给用户自己放的技能用，
// 带了标记会被当成 Posse 装的去更新、删除。description 里有 ": "，不加引号不是合法 YAML：
// Claude 照样认，Cursor 解析失败会整个丢掉这个技能。JSON 字符串同时是 YAML 的双引号字符串。
func TasksFrontmatter() string {
	desc, _ := json.Marshal(TasksDescription)
	return "---\nname: " + TasksSkill + "\ndescription: " + string(desc) + "\n---\n\n"
}

// withMarker 在头部的结束线前加上 Posse 的标记和版本。
func withMarker(frontmatter string) string {
	return strings.TrimSuffix(frontmatter, "---\n\n") + posseMetadata + "---\n\n"
}

// PosseSkill 是一个 Posse 自带的技能：名字和 SKILL.md 的全文。
type PosseSkill struct {
	Name    string
	Content string
}

// PosseSkills 按界面上的顺序列出 Posse 自带的技能。
func PosseSkills() []PosseSkill {
	return []PosseSkill{
		{TasksSkill, withMarker(TasksFrontmatter()) + tasksBody},
		{RenameSkill, renameSkill},
	}
}

// PosseVersion 是这一版自带技能内容的版本：变了，界面上的安装提示再出现一次。
func PosseVersion() string { return strconv.Itoa(posseSkillsVersion) }

// posseDigest 是自带技能内容的摘要，测试拿它核对改了内容有没有加版本。
func posseDigest() string {
	h := sha256.New()
	for _, s := range PosseSkills() {
		h.Write([]byte(s.Name + "\x00" + s.Content + "\x00"))
	}
	return hex.EncodeToString(h.Sum(nil))[:12]
}

// PosseNames 是 Posse 自带技能的名字。
func PosseNames() []string {
	var out []string
	for _, s := range PosseSkills() {
		out = append(out, s.Name)
	}
	return out
}

// frontmatterLines 是 SKILL.md 头部的各行（去掉首尾空白），没有头部时为空。
func frontmatterLines(src string) []string {
	rest, ok := strings.CutPrefix(src, "---\n")
	if !ok {
		return nil
	}
	head, _, _ := strings.Cut(rest, "\n---")
	lines := strings.Split(head, "\n")
	for i, l := range lines {
		lines[i] = strings.TrimSpace(l)
	}
	return lines
}

// isPosseFile 报告 SKILL.md 的内容是不是 Posse 装的（frontmatter 里有标记）。
func isPosseFile(src string) bool {
	for _, l := range frontmatterLines(src) {
		if l == posseMarker {
			return true
		}
	}
	return false
}

// fileVersion 是 Posse 装的 SKILL.md 记的内容版本，没记时是 0。
func fileVersion(src string) int {
	for _, l := range frontmatterLines(src) {
		if v, ok := strings.CutPrefix(l, versionKey+":"); ok {
			n, _ := strconv.Atoi(unquote(strings.TrimSpace(v)))
			return n
		}
	}
	return 0
}

// PosseSourceDir 是安装的来源目录：~/Library/Application Support/Posse/skills。
func PosseSourceDir() (string, error) {
	dir, err := os.UserConfigDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "Posse", "skills"), nil
}

// WritePosseSource 写出来源目录，内容没变的不重写；不再自带的技能目录删掉（来源里有什么 npx 就装什么）。
func WritePosseSource() (string, error) {
	dir, err := PosseSourceDir()
	if err != nil {
		return "", err
	}
	if err := writePosse(dir); err != nil {
		return "", err
	}
	keep := map[string]bool{}
	for _, s := range PosseSkills() {
		keep[s.Name] = true
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		return "", err
	}
	for _, e := range entries {
		if !keep[e.Name()] {
			if err := os.RemoveAll(filepath.Join(dir, e.Name())); err != nil {
				return "", err
			}
		}
	}
	return dir, nil
}

// writePosse 把各技能写到 dir/<名字>/SKILL.md，内容没变的不重写。
func writePosse(dir string) error {
	for _, s := range PosseSkills() {
		path := filepath.Join(dir, s.Name, "SKILL.md")
		if old, err := os.ReadFile(path); err == nil && string(old) == s.Content {
			continue
		}
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			return err
		}
		if err := os.WriteFile(path, []byte(s.Content), 0o644); err != nil {
			return err
		}
	}
	return nil
}

// PosseState 是 Posse 技能在用户目录里的安装情况。
type PosseState struct {
	// Missing 是没装的；Outdated 是装了但和这一版不同的；Taken 是用户自己有同名技能（没有标记），不装、不覆盖。
	Missing, Outdated, Taken []string
	// Claude 是装了的那些在 ~/.claude/skills 里有链接（npx 装时带了 claude-code）。
	Claude bool
	// ClaudeTaken 是 ~/.claude/skills 里同名的一项是用户自己的（不是指向 Posse 那份的链接）：
	// npx 的 -a claude-code 对一次装的所有技能都生效，有它时整批不给 Claude 建链接，免得换掉用户的。
	ClaudeTaken []string
	// NPX 是装了的那些经 npx skills 从 Posse 的来源装：锁文件里有记录。为假时是直接复制的。
	NPX bool
}

// Installed 报告至少装了一个。
func (s PosseState) Installed() bool {
	return len(s.Missing)+len(s.Taken) < len(PosseSkills())
}

// Current 报告能装的都装了，且都是这一版。
func (s PosseState) Current() bool { return len(s.Missing) == 0 && len(s.Outdated) == 0 }

// LinkClaude 报告装、更新 Pending 时要不要给 Claude 建链接：第一次装、或之前装时建过，
// 且要装的这几个在 Claude 的目录里都没被用户占着。
func (s PosseState) LinkClaude() bool {
	for _, n := range s.Pending() {
		if contains(s.ClaudeTaken, n) {
			return false
		}
	}
	return !s.Installed() || s.Claude
}

// Pending 是要装或要更新的技能名，按 PosseSkills 的顺序。
func (s PosseState) Pending() []string {
	var out []string
	for _, n := range PosseNames() {
		if contains(s.Missing, n) || contains(s.Outdated, n) {
			out = append(out, n)
		}
	}
	return out
}

func contains(list []string, s string) bool {
	for _, x := range list {
		if x == s {
			return true
		}
	}
	return false
}

// ReadPosse 看 home 下 Posse 技能装了没有、是不是这一版。
func ReadPosse(home string) PosseState {
	var st PosseState
	lock := readLock(home)
	src, _ := PosseSourceDir()
	for _, s := range PosseSkills() {
		b, err := os.ReadFile(filepath.Join(home, ".agents", "skills", s.Name, "SKILL.md"))
		switch {
		case err != nil:
			// 用户只在 Claude 的目录里放了同名的，也不去盖它
			if _, err := os.Lstat(filepath.Join(home, ".claude", "skills", s.Name)); err == nil && !posseLinked(home, s.Name) {
				st.Taken = append(st.Taken, s.Name)
			} else {
				st.Missing = append(st.Missing, s.Name)
			}
			continue
		case !isPosseFile(string(b)):
			st.Taken = append(st.Taken, s.Name)
			continue
		case string(b) != s.Content && fileVersion(string(b)) <= posseSkillsVersion:
			st.Outdated = append(st.Outdated, s.Name)
		}
		if posseLinked(home, s.Name) {
			st.Claude = true
		} else if _, err := os.Lstat(filepath.Join(home, ".claude", "skills", s.Name)); err == nil {
			st.ClaudeTaken = append(st.ClaudeTaken, s.Name)
		}
		if e, ok := lock[s.Name]; ok && e.SourceType == "local" && filepath.Clean(e.Source) == src {
			st.NPX = true
		}
	}
	return st
}

// posseLinked 报告 ~/.claude/skills/<名字> 是指向 ~/.agents/skills/<名字> 的链接。
func posseLinked(home, name string) bool {
	at := filepath.Join(home, ".claude", "skills", name)
	t := linkTarget(at)
	return t != "" && realDir(t) == realDir(filepath.Join(home, ".agents", "skills", name))
}

// PosseInstall 是用 npx skills 装（或再装一次来更新）names 这几个 Posse 技能的命令。
// claude 为假时不给 Claude 建链接：用户之前装的时候删掉了 Claude 那条，更新时不替他加回来。
func PosseInstall(source string, names []string, claude bool) Install {
	return Install{Source: source, Skills: names, Claude: claude, Global: true}
}

// CopyPosse 不经 npx，把 names 这几个 Posse 技能照 npx 的布局放进 home：~/.agents/skills/<名字>/SKILL.md，
// claude 为真时 ~/.claude/skills/<名字> 是指向它的相对链接。同名但不是 Posse 装的不动，报错。
func CopyPosse(home string, names []string, claude bool) error {
	for _, s := range PosseSkills() {
		if !contains(names, s.Name) {
			continue
		}
		dir := filepath.Join(home, ".agents", "skills", s.Name)
		file := filepath.Join(dir, "SKILL.md")
		if b, err := os.ReadFile(file); err == nil && !isPosseFile(string(b)) {
			return &fs.PathError{Op: "install", Path: file, Err: fs.ErrExist}
		}
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return err
		}
		if err := os.WriteFile(file, []byte(s.Content), 0o644); err != nil {
			return err
		}
		if !claude {
			continue
		}
		at := filepath.Join(home, ".claude", "skills", s.Name)
		if posseLinked(home, s.Name) {
			continue
		}
		if _, err := os.Lstat(at); err == nil {
			return &fs.PathError{Op: "link", Path: at, Err: fs.ErrExist}
		} else if !errors.Is(err, fs.ErrNotExist) {
			return err
		}
		if err := os.MkdirAll(filepath.Dir(at), 0o755); err != nil {
			return err
		}
		rel, err := filepath.Rel(filepath.Dir(at), dir)
		if err != nil {
			return err
		}
		if err := os.Symlink(rel, at); err != nil {
			return err
		}
	}
	return nil
}
