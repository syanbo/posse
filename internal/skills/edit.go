package skills

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"

	"github.com/BurntSushi/toml"
)

// Change 是开关一个技能要对某家配置文件做的改动：先算好给用户确认，确认后 Apply。
type Change struct {
	Agent  Agent
	Skill  string
	Enable bool
	File   string
	// Before 是算改动时文件的内容（不存在时为空），After 是改完的内容。
	Before, After string
}

// ErrNoToggle 表示这家没有能写的开关（Cursor），或这个技能不归开关管（读不到、被同名盖住、SKILL.md 里写死的）。
var ErrNoToggle = errors.New("这一家没有能写的开关")

// ErrStale 表示算好改动后文件又被别人改过。
var ErrStale = errors.New("配置文件刚被改过，重新扫描后再试")

// CanToggle 报告 a 对 s 能不能开关：读得到、生效的那一份，状态是启用或关了（Claude 的 skillOverrides 设成别的值时也能改回）。
func CanToggle(a Agent, s Skill) bool {
	// Cursor 没有开关；Claude 账号同步的技能在 Claude 里带前缀（如 anthropic-skills:docx），skillOverrides 的键对不上
	if a == Cursor || a == Claude && s.Scope == ScopeSynced {
		return false
	}
	switch s.Agents[a].State {
	case On, Off:
		return true
	case Manual:
		return a == Claude && !s.Meta.ModelOff
	}
	return false
}

// PlanToggle 算出让 a 启用（enable）或关掉 s 要改的文件和内容。o 要和扫描时的一样（项目级技能的 Claude 开关写在 o.Dir 里）。
// 写法：Claude 是 settings.json 的 skillOverrides（项目级写工作区的 .claude/settings.local.json）；
// Codex 是 config.toml 的 [[skills.config]]（用户级按名字，项目级按 SKILL.md 的真实路径，只关这一份，Codex 自己也这样写）；
// Grok 是 config.toml 的 [skills] disabled（按名字），项目级的按目录加进 ignore（disabled 只认名字，会连别处同名的一起关）。
func PlanToggle(o Options, s Skill, a Agent, enable bool) (Change, error) {
	if !CanToggle(a, s) {
		return Change{}, ErrNoToggle
	}
	ch := Change{Agent: a, Skill: s.Name, Enable: enable}
	var err error
	switch a {
	case Claude:
		ch.File = filepath.Join(o.Home, ".claude", "settings.json")
		if s.Scope == ScopeProject && o.Dir != "" {
			ch.File = filepath.Join(o.Dir, ".claude", "settings.local.json")
		}
		ch.Before = readText(ch.File)
		value := "off"
		if enable {
			value = ""
			// 写在 settings.local.json 里时，删掉还会被用户的或项目 settings.json 里的设置关着：显式写 on 盖过去
			if ch.File != filepath.Join(o.Home, ".claude", "settings.json") &&
				(claudeOverrides(filepath.Join(o.Home, ".claude", "settings.json"))[s.Name] != "" ||
					claudeOverrides(filepath.Join(o.Dir, ".claude", "settings.json"))[s.Name] != "") {
				value = "on"
			}
		}
		ch.After, err = setJSONOverride(ch.Before, s.Name, value)
	case Codex:
		ch.File = filepath.Join(codexHome(o.Home), "config.toml")
		ch.Before = readText(ch.File)
		sel := codexEntry{Name: s.Name}
		if s.Scope == ScopeProject {
			sel = codexEntry{Path: realDir(s.File)}
		}
		ch.After = setCodexEntry(ch.Before, sel, s.Name, realDir(s.File), enable)
	case Grok:
		ch.File = filepath.Join(o.Home, ".grok", "config.toml")
		ch.Before = readText(ch.File)
		// ignore 按放技能的那一项比（链接本身，不是它指向的地方）
		dir := s.Agents[Grok].Via
		if dir == "" {
			dir = s.Dir
		}
		norm := func(v string) string { return expandAll(o.Home, []string{v})[0] }
		switch {
		case enable:
			// 打开时两处都去掉：不管是 Posse 关的还是用户自己写的
			ch.After, err = setTOMLList(ch.Before, "skills", "disabled", s.Name, false, nil)
			if err == nil {
				ch.After, err = setTOMLList(ch.After, "skills", "ignore", dir, false, norm)
			}
		case s.Scope == ScopeProject:
			ch.After, err = setTOMLList(ch.Before, "skills", "ignore", dir, true, norm)
		default:
			ch.After, err = setTOMLList(ch.Before, "skills", "disabled", s.Name, true, nil)
		}
	}
	if err != nil {
		return Change{}, err
	}
	if ch.After == ch.Before {
		return Change{}, fmt.Errorf("%s 的配置不用改", a)
	}
	return ch, nil
}

// Apply 写入改动：文件和算改动时不一样了就不写（ErrStale）；先写临时文件再改名。
// 配置文件是符号链接（dotfiles 仓库常这样放）时写到它指向的文件，不把链接换成普通文件。
func (ch Change) Apply() error {
	if readText(ch.File) != ch.Before {
		return ErrStale
	}
	file := ch.File
	if real, err := filepath.EvalSymlinks(file); err == nil {
		file = real
	}
	if err := os.MkdirAll(filepath.Dir(file), 0o755); err != nil {
		return err
	}
	mode := os.FileMode(0o644)
	if fi, err := os.Stat(file); err == nil {
		mode = fi.Mode().Perm()
	}
	tmp := file + ".posse-tmp"
	if err := os.WriteFile(tmp, []byte(ch.After), mode); err != nil {
		return err
	}
	if err := os.Rename(tmp, file); err != nil {
		os.Remove(tmp)
		return err
	}
	return nil
}

func readText(path string) string {
	b, _ := os.ReadFile(path)
	return string(b)
}

// --- Claude 的 settings.json：只改 skillOverrides 这一项，别的原样留着

// setJSONOverride 把顶层 skillOverrides 里 name 设成 value（空是删掉这一项；删空了连 skillOverrides 一起删）。
func setJSONOverride(src, name, value string) (string, error) {
	if strings.TrimSpace(src) == "" {
		if value == "" {
			return src, nil
		}
		src = "{}\n"
	}
	if !json.Valid([]byte(src)) {
		return "", errors.New("settings.json 不是合法的 JSON，没有改")
	}
	keyStart, valStart, valEnd, found := topLevelKey(src, "skillOverrides")
	// 已有的各项，保持原来的顺序；值原样留着（不是字符串的也不动）
	var keys []string
	vals := map[string]json.RawMessage{}
	if found {
		dec := json.NewDecoder(strings.NewReader(src[valStart:valEnd]))
		if t, err := dec.Token(); err != nil || t != json.Delim('{') {
			return "", errors.New("settings.json 的 skillOverrides 不是对象，没有改")
		}
		for dec.More() {
			t, err := dec.Token()
			if err != nil {
				return "", err
			}
			var v json.RawMessage
			if err := dec.Decode(&v); err != nil {
				return "", err
			}
			k := t.(string)
			if !slices.Contains(keys, k) {
				keys = append(keys, k)
			}
			vals[k] = v
		}
	}
	if value == "" {
		keys = slices.DeleteFunc(keys, func(k string) bool { return k == name })
	} else {
		if !slices.Contains(keys, name) {
			keys = append(keys, name)
		}
		vals[name], _ = json.Marshal(value)
	}
	if len(keys) == 0 {
		if !found {
			return src, nil
		}
		return removeMember(src, keyStart, valEnd), nil
	}
	// 顶层的键各占一行时照它的缩进写成多行；写在一行里（键前面还有别的内容）时也写成一行
	at := keyStart
	if !found {
		at = skipSpace(src, strings.IndexByte(src, '{')+1)
	}
	indent, ownLine := lineIndent(src, at)
	if !found && src[at] == '}' {
		indent, ownLine = "  ", true // 空对象
	}
	unit := indent
	if unit == "" {
		unit = "  "
	}
	var b strings.Builder
	b.WriteString("{")
	for i, k := range keys {
		kb, _ := json.Marshal(k)
		if ownLine {
			b.WriteString("\n" + indent + unit)
		}
		b.WriteString(string(kb) + ": " + string(vals[k]))
		if i < len(keys)-1 {
			b.WriteString(",")
			if !ownLine {
				b.WriteString(" ")
			}
		}
	}
	if ownLine {
		b.WriteString("\n" + indent)
	}
	b.WriteString("}")
	obj := b.String()
	if found {
		return src[:valStart] + obj + src[valEnd:], nil
	}
	// 加在顶层对象最后一项后面
	end := strings.LastIndexByte(src, '}')
	body := strings.TrimRight(src[:end], " \t\r\n")
	member := `"skillOverrides": ` + obj
	switch {
	case ownLine && strings.HasSuffix(body, "{"):
		member = "\n" + indent + member + "\n"
	case ownLine:
		member = ",\n" + indent + member + "\n"
	default:
		member = ", " + member
		return body + member + src[len(body):], nil
	}
	return body + member + src[end:], nil
}

// topLevelKey 找顶层对象里 key 的位置：键的开头、值的开头和结尾。src 要是合法的 JSON。
func topLevelKey(src, key string) (keyStart, valStart, valEnd int, ok bool) {
	i := skipSpace(src, 0)
	if i >= len(src) || src[i] != '{' {
		return
	}
	i++
	for {
		i = skipSpace(src, i)
		if i >= len(src) || src[i] == '}' {
			return
		}
		if src[i] == ',' {
			i++
			continue
		}
		ks := i
		ke := skipValue(src, i)
		k, _ := strconv.Unquote(src[ks:ke])
		i = skipSpace(src, ke)
		i++ // 冒号
		vs := skipSpace(src, i)
		ve := skipValue(src, vs)
		if k == key {
			return ks, vs, ve, true
		}
		i = ve
	}
}

func skipSpace(s string, i int) int {
	for i < len(s) && strings.IndexByte(" \t\r\n", s[i]) >= 0 {
		i++
	}
	return i
}

// skipValue 跳过从 i 开始的一个 JSON 值，返回它后面的位置。
func skipValue(s string, i int) int {
	switch s[i] {
	case '"':
		for j := i + 1; j < len(s); j++ {
			if s[j] == '\\' {
				j++
			} else if s[j] == '"' {
				return j + 1
			}
		}
		return len(s)
	case '{', '[':
		depth := 0
		for j := i; j < len(s); j++ {
			switch s[j] {
			case '"':
				j = skipValue(s, j) - 1
			case '{', '[':
				depth++
			case '}', ']':
				depth--
				if depth == 0 {
					return j + 1
				}
			}
		}
		return len(s)
	}
	j := i
	for j < len(s) && strings.IndexByte(",}] \t\r\n", s[j]) < 0 {
		j++
	}
	return j
}

// removeMember 删掉顶层对象里从 start 到 end 的一项，连同它和相邻项之间的逗号、它所在的行。
func removeMember(src string, start, end int) string {
	after := skipSpace(src, end)
	if after < len(src) && src[after] == ',' {
		end = after + 1
	} else {
		// 最后一项：去掉前面的逗号
		before := strings.TrimRight(src[:start], " \t\r\n")
		if strings.HasSuffix(before, ",") {
			start = len(before) - 1
		}
	}
	// 整行删掉：从这一行行首（只有空白时）到下一个非空白之前的换行
	ls := start
	for ls > 0 && (src[ls-1] == ' ' || src[ls-1] == '\t') {
		ls--
	}
	if ls > 0 && src[ls-1] == '\n' && src[start] != ',' {
		start = ls - 1
	}
	return src[:start] + src[end:]
}

// lineIndent 是 at 所在行行首到 at 的空白；at 前面这一行还有别的内容时 ownLine 为假。
func lineIndent(s string, at int) (indent string, ownLine bool) {
	ls := strings.LastIndexByte(s[:at], '\n') + 1
	indent = s[ls:at]
	return indent, strings.TrimLeft(indent, " \t") == ""
}

// --- Codex 的 config.toml：[[skills.config]] 一项一段

var tomlKeyRe = regexp.MustCompile(`^\s*([A-Za-z0-9_-]+)\s*=\s*(.*?)\s*(#.*)?$`)

// tomlBlock 是 config.toml 里一段 [[skills.config]]：从表头行到下一个表头之前的行号（不含）。
type tomlBlock struct {
	start, end int
	entry      codexEntry
}

func codexBlocks(lines []string) []tomlBlock {
	var out []tomlBlock
	for i := 0; i < len(lines); i++ {
		if strings.TrimSpace(lines[i]) != "[[skills.config]]" {
			continue
		}
		b := tomlBlock{start: i, end: len(lines)}
		for j := i + 1; j < len(lines); j++ {
			if t := strings.TrimSpace(lines[j]); strings.HasPrefix(t, "[") {
				b.end = j
				break
			}
		}
		var e codexEntry
		toml.Decode(strings.Join(lines[i+1:b.end], "\n"), &e)
		b.entry = e
		out = append(out, b)
		i = b.end - 1
	}
	return out
}

// setCodexEntry 关掉时：已有同一个选择的段就把 enabled 改成 false，没有就在文件末尾加一段；
// 打开时删掉选中它的段（按路径 file 写的，用户级的还有按名字写的：Codex 自己的界面会按路径写），
// 删完还被按名字的段关着（项目级）就加一段 enabled = true。
func setCodexEntry(src string, sel codexEntry, name, file string, enable bool) string {
	lines := strings.Split(src, "\n")
	// 路径按解开链接后的比：Codex 写的可能是经过链接的路径（/tmp 是 /private/tmp 的链接）
	same := func(e codexEntry) bool {
		return e.Name == sel.Name && (e.Path == sel.Path || e.Path != "" && realDir(e.Path) == realDir(sel.Path))
	}
	blocks := codexBlocks(lines)
	if !enable {
		for _, b := range slices.Backward(blocks) {
			if same(b.entry) {
				return strings.Join(setTOMLKey(lines, b.start, b.end, "enabled", "false"), "\n")
			}
		}
		return appendBlock(src, codexBlockText(sel, false))
	}
	selects := func(e codexEntry) bool {
		return same(e) || e.Path != "" && realDir(e.Path) == file || sel.Name != "" && e.Name == sel.Name && e.Path == ""
	}
	for _, b := range slices.Backward(blocks) {
		if !selects(b.entry) {
			continue
		}
		// 段到下一个表头之前，含它后面隔开的空行
		lines = slices.Delete(lines, b.start, b.end)
	}
	out := strings.Join(lines, "\n")
	if strings.HasSuffix(src, "\n") {
		out = strings.TrimRight(out, "\n") + "\n"
	}
	if strings.TrimSpace(out) == "" {
		out = ""
	}
	// 按路径打开的，还可能被按名字的那段关着
	for _, b := range codexBlocks(strings.Split(out, "\n")) {
		if sel.Path != "" && b.entry.Name == name && b.entry.Enabled != nil && !*b.entry.Enabled {
			return appendBlock(out, codexBlockText(sel, true))
		}
	}
	return out
}

func codexBlockText(sel codexEntry, enabled bool) string {
	var b strings.Builder
	b.WriteString("[[skills.config]]\n")
	if sel.Path != "" {
		b.WriteString("path = " + strconv.Quote(sel.Path) + "\n")
	} else {
		b.WriteString("name = " + strconv.Quote(sel.Name) + "\n")
	}
	b.WriteString("enabled = " + strconv.FormatBool(enabled) + "\n")
	return b.String()
}

func appendBlock(src, block string) string {
	src = strings.TrimRight(src, "\n")
	if src == "" {
		return block
	}
	return src + "\n\n" + block
}

// setTOMLKey 在 lines[start:end] 这一段里把 key 设成 value，没有就加在段末（段后空行之前）。
func setTOMLKey(lines []string, start, end int, key, value string) []string {
	for i := start + 1; i < end; i++ {
		if m := tomlKeyRe.FindStringSubmatch(lines[i]); m != nil && m[1] == key {
			lines[i] = strings.Replace(lines[i], m[2], value, 1)
			return lines
		}
	}
	at := end
	for at > start+1 && strings.TrimSpace(lines[at-1]) == "" {
		at--
	}
	return slices.Insert(lines, at, key+" = "+value)
}

// --- Grok 的 config.toml：[table] 里的一个字符串列表

// setTOMLList 在 [table] 的 key 列表里加上（add）或去掉 item；norm 不为空时按它规整后比较（路径里的 ~）。
// 列表改写成一行；表和键不存在时加上。
func setTOMLList(src, table, key, item string, add bool, norm func(string) string) (string, error) {
	if norm == nil {
		norm = func(s string) string { return s }
	}
	lines := strings.Split(src, "\n")
	hs, he := -1, len(lines)
	for i, l := range lines {
		t := strings.TrimSpace(l)
		if hs < 0 && t == "["+table+"]" {
			hs = i
			continue
		}
		if hs >= 0 && strings.HasPrefix(t, "[") {
			he = i
			break
		}
	}
	render := func(items []string) string {
		q := make([]string, len(items))
		for i, s := range items {
			q[i] = strconv.Quote(s)
		}
		return key + " = [" + strings.Join(q, ", ") + "]"
	}
	if hs < 0 {
		if !add {
			return src, nil
		}
		return appendBlock(src, "["+table+"]\n"+render([]string{item})+"\n"), nil
	}
	// 找到键所在的几行（数组可能跨行）
	ks, ke := -1, -1
	for i := hs + 1; i < he; i++ {
		if m := tomlKeyRe.FindStringSubmatch(lines[i]); m != nil && m[1] == key {
			ks = i
			depth := 0
			for j := i; j < he; j++ {
				depth += strings.Count(lines[j], "[") - strings.Count(lines[j], "]")
				if depth <= 0 {
					ke = j + 1
					break
				}
			}
			break
		}
	}
	var items []string
	if ks >= 0 {
		if ke < 0 {
			return "", fmt.Errorf("没认出 [%s] 的 %s，没有改", table, key)
		}
		var v map[string][]string
		if _, err := toml.Decode(strings.Join(lines[ks:ke], "\n"), &v); err != nil {
			return "", fmt.Errorf("没认出 [%s] 的 %s，没有改", table, key)
		}
		items = v[key]
	}
	has := slices.ContainsFunc(items, func(s string) bool { return norm(s) == norm(item) })
	switch {
	case add && has, !add && !has:
		return src, nil
	case add:
		items = append(items, item)
	default:
		items = slices.DeleteFunc(items, func(s string) bool { return norm(s) == norm(item) })
	}
	switch {
	case ks >= 0 && len(items) == 0:
		// 删空了连键一起删，表里只剩空行时连表头一起删
		lines = slices.Delete(lines, ks, ke)
		he -= ke - ks
		empty := true
		for _, l := range lines[hs+1 : he] {
			if strings.TrimSpace(l) != "" {
				empty = false
			}
		}
		if empty {
			start := hs
			for start > 0 && strings.TrimSpace(lines[start-1]) == "" {
				start--
			}
			lines = slices.Delete(lines, start, he)
		}
	case ks >= 0:
		lines = slices.Replace(lines, ks, ke, render(items))
	default:
		lines = slices.Insert(lines, hs+1, render(items))
	}
	out := strings.Join(lines, "\n")
	if strings.HasSuffix(src, "\n") && !strings.HasSuffix(out, "\n") && strings.TrimSpace(out) != "" {
		out += "\n"
	}
	if strings.TrimSpace(out) == "" {
		out = ""
	}
	return out, nil
}

// --- 给确认框看的 diff

// DiffLine 是 diff 的一行：Kind 是 ' '（上下文）、'-'、'+'；Kind 为 0 是省略掉的几行。
type DiffLine struct {
	Kind byte
	Text string
}

// Diff 按行比较，只留改动和它前后 context 行。
func Diff(before, after string, context int) []DiffLine {
	a, b := splitLines(before), splitLines(after)
	// 最长公共子序列，配置文件只有几百行
	n, m := len(a), len(b)
	lcs := make([][]int, n+1)
	for i := range lcs {
		lcs[i] = make([]int, m+1)
	}
	for i := n - 1; i >= 0; i-- {
		for j := m - 1; j >= 0; j-- {
			if a[i] == b[j] {
				lcs[i][j] = lcs[i+1][j+1] + 1
			} else {
				lcs[i][j] = max(lcs[i+1][j], lcs[i][j+1])
			}
		}
	}
	var all []DiffLine
	i, j := 0, 0
	for i < n || j < m {
		switch {
		case i < n && j < m && a[i] == b[j]:
			all = append(all, DiffLine{' ', a[i]})
			i++
			j++
		case i < n && (j == m || lcs[i+1][j] >= lcs[i][j+1]):
			// 先删后加，和 git diff 一样
			all = append(all, DiffLine{'-', a[i]})
			i++
		default:
			all = append(all, DiffLine{'+', b[j]})
			j++
		}
	}
	keep := make([]bool, len(all))
	for k, l := range all {
		if l.Kind != ' ' {
			for x := max(0, k-context); x <= min(len(all)-1, k+context); x++ {
				keep[x] = true
			}
		}
	}
	var out []DiffLine
	for k, l := range all {
		if keep[k] {
			out = append(out, l)
		} else if len(out) == 0 || out[len(out)-1].Kind != 0 {
			out = append(out, DiffLine{})
		}
	}
	if len(out) > 0 && out[len(out)-1].Kind == 0 {
		out = out[:len(out)-1]
	}
	if len(out) > 0 && out[0].Kind == 0 {
		out = out[1:]
	}
	return out
}

func splitLines(s string) []string {
	s = strings.TrimSuffix(s, "\n")
	if s == "" {
		return nil
	}
	return strings.Split(s, "\n")
}
