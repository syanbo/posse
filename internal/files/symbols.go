package files

import (
	"context"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"regexp"
	"slices"
	"sort"
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"
)

// 跳到定义（文件标签里 ⌘点击、F12）的纯逻辑。没有语法树和类型信息：按各语言声明的写法逐行正则匹配，
// 同名的定义都找出来按位置打分，由界面决定直接跳还是让用户选。

// Definition 是找到的一处定义。
type Definition struct {
	Path string // 相对清单的 Root
	Line int    // 从 0 起
	Text string // 这一行，去掉结尾的 \r
	Span [2]int // 名字在 Text 里的字节范围
	Kind string // 声明的种类（type、func、var…），只用于显示
	// Decl 报告这是带声明关键字或签名的定义（class、func、方法签名），不是赋值、字段这类弱匹配
	Decl  bool
	Score int
}

// DefinitionQuery 是一次查找：Word 是光标处的标识符，Qual 是它前面的限定符（a.b 里的 a，没有为空），
// From 是光标所在文件相对 Root 的路径（不在工作区里时为空），Prefer 是 import 这个名字的文件或目录（Imports），
// Imported 是这个文件 import 的所有文件或目录。
type DefinitionQuery struct {
	Word, Qual, From, Prefer string
	Imported                 []string
}

// MaxDefinitions 是一次查找最多返回的定义数。
const MaxDefinitions = 200

// WordAt 返回 line 里 col（字节）处的标识符、它前面的限定符（a.b、a::b、a->b、a?.b 里的 a）和标识符在
// line 里的范围；col 在标识符后面紧挨着时也算在它上面。不在标识符上时 word 为空。
func WordAt(line string, col int) (word, qual string, start, end int) {
	col = max(0, min(col, len(line)))
	start, end = col, col
	for start > 0 {
		r, n := utf8.DecodeLastRuneInString(line[:start])
		if !identRune(r) {
			break
		}
		start -= n
	}
	for end < len(line) {
		r, n := utf8.DecodeRuneInString(line[end:])
		if !identRune(r) {
			break
		}
		end += n
	}
	if start == end {
		return "", "", col, col
	}
	word = line[start:end]
	j := start
	switch {
	case j >= 2 && (line[j-2:j] == "::" || line[j-2:j] == "->" || line[j-2:j] == "?."):
		j -= 2
	case j >= 1 && line[j-1] == '.':
		j--
	default:
		return word, "", start, end
	}
	qs := j
	for qs > 0 {
		r, n := utf8.DecodeLastRuneInString(line[:qs])
		if !identRune(r) {
			break
		}
		qs -= n
	}
	qual = line[qs:j]
	switch qual {
	case "this", "self", "super", "":
		qual = ""
	}
	return word, qual, start, end
}

func identRune(r rune) bool {
	return r == '_' || r == '$' || unicode.IsLetter(r) || unicode.IsDigit(r)
}

// family 是按声明写法归的类，同一类共用一套模式。
type family string

const (
	famGo   family = "go"
	famJava family = "java" // Java、Kotlin、Scala、C#、Swift、Dart、Groovy：类型在前或 fun/func 在前的声明
	famJS   family = "js"
	famPy   family = "py"
	famRuby family = "rb"
	famRust family = "rs"
	famC    family = "c"
	famPHP  family = "php"
	famLua  family = "lua"
	famSh   family = "sh"
)

var families = map[string]family{
	".go":   famGo,
	".java": famJava, ".kt": famJava, ".kts": famJava, ".scala": famJava, ".cs": famJava, ".swift": famJava, ".dart": famJava, ".groovy": famJava,
	".js": famJS, ".jsx": famJS, ".mjs": famJS, ".cjs": famJS, ".ts": famJS, ".tsx": famJS, ".mts": famJS, ".cts": famJS, ".vue": famJS, ".svelte": famJS,
	".py": famPy, ".pyi": famPy, ".pyw": famPy,
	".rb": famRuby, ".rake": famRuby,
	".rs": famRust,
	".c":  famC, ".h": famC, ".cpp": famC, ".cc": famC, ".cxx": famC, ".hpp": famC, ".hh": famC, ".hxx": famC, ".m": famC, ".mm": famC,
	".php": famPHP,
	".lua": famLua,
	".sh":  famSh, ".bash": famSh, ".zsh": famSh,
}

// familyOf 返回文件按扩展名属于哪一类，不认识的（数据文件、文档）返回空，不在里面找。
func familyOf(rel string) family {
	return families[strings.ToLower(path.Ext(rel))]
}

// pattern 是一种声明的写法：re 的第 1 个分组是名字。
type pattern struct {
	re     *regexp.Regexp
	kind   string
	weight int // 3 带关键字的声明，2 签名，1 赋值和字段
}

const (
	// 标识符的边界：RE2 没有环视，用一个分组消耗掉前后的那个字符
	before = `(?:^|[^\pL\pN_$])`
	after  = `(?:$|[^\pL\pN_$])`
	// 类型声明各语言写法接近，共用
	typeKeywords = `(?:class|interface|enum|struct|trait|record|object|protocol|extension|typealias|type|module|mixin|namespace|union)`
)

// patternsFor 返回一类语言里 word 的声明模式，强的在前；一行只记第一个匹配到的。
func patternsFor(f family, word string) []pattern {
	return patternsWith(f, "("+regexp.QuoteMeta(word)+")")
}

// patternsWith 是 patternsFor 的实现，w 是匹配名字的分组（符号搜索用"含有查询的任意标识符"）。
func patternsWith(f family, w string) []pattern {
	mk := func(kind string, weight int, expr string) pattern {
		return pattern{regexp.MustCompile(expr), kind, weight}
	}
	typ := mk("type", 3, before+typeKeywords+`\s+`+w+after)
	switch f {
	case famGo:
		return []pattern{
			mk("func", 3, `^func\s+(?:\([^)]*\)\s*)?`+w+`\s*[\[(]`),
			typ,
			mk("var", 2, `^\s*(?:var|const)\s+`+w+after),
			mk("var", 1, `^\s*`+w+`\s*(?:,\s*[\pL\pN_]+\s*)*:?=[^=]`),
			mk("var", 1, `^\s+`+w+`\s*(?:,\s*[\pL\pN_]+\s*)*(?:=[^=]|[\pL_\*\[\]][^=]*=)`), // var/const 块里的一行
			mk("field", 1, `^\s+`+w+`\s+[\pL_\*\[\]\.]`),                                   // 结构体字段
			mk("method", 1, `^\s+`+w+`\s*\(`),                                              // 接口方法
		}
	case famJava:
		return []pattern{
			mk("func", 3, before+`(?:fun|func|def)\s+(?:<[^>]*>\s*)?(?:[\pL\pN_.]+\.)?`+w+`\s*[(<]`),
			typ,
			mk("method", 2, `^\s*(?:[\pL\pN_<>\[\],?.@!]+\s+)+`+w+`\s*\(`),
			mk("var", 1, before+`(?:val|var|let|const|final|late)\s+(?:[\pL\pN_<>]+\s+)?`+w+after),
			mk("field", 1, `^\s*(?:[\pL\pN_<>\[\],?.@]+\s+)+`+w+`\s*[=;,]`),
		}
	case famJS:
		return []pattern{
			mk("func", 3, before+`function\s*\*?\s*`+w+`\s*[(<]`),
			typ,
			mk("var", 2, before+`(?:const|let|var)\s+`+w+after),
			mk("method", 2, `^\s*(?:(?:export|default|public|private|protected|static|async|readonly|get|set|override|abstract|declare)\s+)*`+w+`\s*(?:<[^>]*>)?\s*\([^)]*\)\s*(?::[^{=;]*)?\{`),
			mk("func", 2, `^\s*(?:(?:export|public|private|protected|static|readonly|declare)\s+)*`+w+`\s*[:=]\s*(?:async\s*)?(?:function`+after+`|\([^)]*\)\s*(?::[^=]*)?=>|[\pL\pN_$]+\s*=>)`),
			mk("field", 1, `^\s*(?:(?:public|private|protected|static|readonly|declare|override)\s+)*`+w+`\s*[?!]?\s*[:=][^=]`),
		}
	case famPy:
		return []pattern{
			mk("func", 3, `^\s*(?:async\s+)?def\s+`+w+`\s*\(`),
			typ,
			mk("var", 1, `^\s*`+w+`\s*(?::[^=]*)?=[^=]`),
		}
	case famRuby:
		return []pattern{
			mk("func", 3, `^\s*def\s+(?:self\.)?`+w+after),
			typ,
			mk("field", 2, `^\s*attr_(?:reader|writer|accessor)\b.*:`+w+after),
			mk("var", 1, `^\s*`+w+`\s*=[^=]`),
		}
	case famRust:
		return []pattern{
			mk("func", 3, before+`fn\s+`+w+`\s*[(<]`),
			mk("macro", 3, before+`macro_rules!\s+`+w+after),
			mk("mod", 3, before+`mod\s+`+w+after),
			typ,
			mk("var", 2, before+`(?:static|const)\s+(?:mut\s+)?`+w+after),
			mk("var", 1, before+`let\s+(?:mut\s+)?`+w+after),
		}
	case famC:
		return []pattern{
			mk("macro", 3, `^\s*#\s*define\s+`+w+after),
			typ,
			mk("type", 2, `^\s*typedef\b.*`+before+w+`\s*;`),
			mk("func", 2, `^(?:[\pL\pN_\*&:<>,~\[\]\s]*\s)?[\*&]*`+w+`\s*\(`),
			mk("var", 1, `^\s*(?:[\pL\pN_:<>\*&,\[\]]+\s+)+[\*&]*`+w+`\s*(?:=[^=]|;|\[)`),
		}
	case famPHP:
		return []pattern{
			mk("func", 3, before+`function\s+&?`+w+`\s*\(`),
			typ,
			mk("var", 2, before+`const\s+`+w+after),
			mk("field", 1, before+`(?:public|private|protected|static|var)\s+(?:static\s+)?`+w+after),
			mk("var", 1, `^\s*`+w+`\s*=[^=]`),
		}
	case famLua:
		return []pattern{
			mk("func", 3, before+`function\s+(?:[\pL\pN_.:]+[.:])?`+w+`\s*\(`),
			mk("var", 2, before+`local\s+(?:function\s+)?`+w+after),
			mk("var", 1, `^\s*(?:[\pL\pN_.]+\.)?`+w+`\s*=[^=]`),
		}
	case famSh:
		return []pattern{
			mk("func", 3, `^\s*(?:function\s+)?`+w+`\s*\(\)`),
			mk("var", 1, `^\s*(?:export\s+|local\s+|readonly\s+|declare\s+\S+\s+)?`+w+`=`),
		}
	}
	return nil
}

// notDecl 是出现在名字前面就说明这一行是调用而不是声明的词：`return foo(`、`new Foo(`。
var notDecl = map[string]bool{
	"return": true, "new": true, "throw": true, "await": true, "yield": true, "else": true, "case": true,
	"goto": true, "delete": true, "typeof": true, "instanceof": true, "in": true, "of": true, "is": true, "as": true,
	"import": true, "package": true, "export": true, "extends": true, "implements": true, "with": true, "if": true,
	"while": true, "for": true, "switch": true, "until": true, "unless": true, "elif": true, "and": true, "or": true, "not": true,
}

// matchLine 在一行里按顺序试各模式，返回第一个匹配到的和名字的范围。
func matchLine(ps []pattern, line string) (pattern, [2]int, bool) {
	for _, p := range ps {
		m := p.re.FindStringSubmatchIndex(line)
		if m == nil || m[2] < 0 {
			continue
		}
		if p.weight == 2 && (p.kind == "method" || p.kind == "func") && hasNotDecl(line[:m[2]]) {
			continue
		}
		return p, [2]int{m[2], m[3]}, true
	}
	return pattern{}, [2]int{}, false
}

func hasNotDecl(prefix string) bool {
	for _, f := range strings.FieldsFunc(prefix, func(r rune) bool { return !identRune(r) }) {
		if notDecl[f] {
			return true
		}
	}
	return false
}

// lineHit 是某个文件里含有要找的词的一行。
type lineHit struct {
	line int // 从 0 起
	text string
}

// FindDefinitions 在清单的文件里找 q.Word 的定义。先粗筛出含这个词（和限定符）的行：git 仓库用 git grep -w，
// 毫秒级、自动按 gitignore；不是 git 仓库或 git 失败时在 Go 里逐个读文件。只看认识的源码文件（familyOf），
// 只在这些行上试声明模式，名字落在注释或字符串里的不算。有未保存改动的文件（overlay）搜编辑器里的内容。
// 有带声明的结果时，别的文件里的弱匹配（赋值、字段、对象字面量的键）不列。结果按分数从高到低，分数相同按路径和行号。
func FindDefinitions(ctx context.Context, l *List, overlay map[string]string, q DefinitionQuery) ([]Definition, error) {
	if q.Word == "" {
		return nil, nil
	}
	words := []string{q.Word}
	if q.Qual != "" && q.Qual != q.Word {
		words = append(words, q.Qual)
	}
	hits, order, err := grepLines(ctx, l, overlay, words)
	if err != nil {
		return nil, err
	}
	fromFam := familyOf(q.From)
	fromDir := path.Dir(filepath.ToSlash(q.From))
	pats := map[family][]pattern{}
	var qualType *regexp.Regexp
	if q.Qual != "" {
		qualType = regexp.MustCompile(before + typeKeywords + `\s+` + regexp.QuoteMeta(q.Qual) + after)
	}
	var out []Definition
	for _, rel := range order {
		if ctx.Err() != nil {
			return out, ctx.Err()
		}
		fam := familyOf(rel)
		if fam == "" {
			continue
		}
		ps, ok := pats[fam]
		if !ok {
			ps = patternsFor(fam, q.Word)
			pats[fam] = ps
		}
		slash := filepath.ToSlash(rel)
		lines := hits[rel]
		qualFile := q.Qual != "" && namesQual(slash, q.Qual)
		if !qualFile && qualType != nil {
			for _, h := range lines {
				if strings.Contains(h.text, q.Qual) && qualType.MatchString(h.text) {
					qualFile = true
					break
				}
			}
		}
		for _, h := range lines {
			if !strings.Contains(h.text, q.Word) {
				continue
			}
			d, ok := defOn(h, q.Word, ps)
			if !ok {
				continue
			}
			d.Path = rel
			d.Score += placeScore(slash, q, fromFam, fromDir, fam, qualFile)
			out = append(out, d)
			if len(out) >= MaxDefinitions {
				break
			}
		}
		if len(out) >= MaxDefinitions {
			break
		}
	}
	if slices.ContainsFunc(out, func(d Definition) bool { return d.Decl }) {
		out = slices.DeleteFunc(out, func(d Definition) bool { return !d.Decl && filepath.ToSlash(d.Path) != filepath.ToSlash(q.From) })
	}
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].Score != out[j].Score {
			return out[i].Score > out[j].Score
		}
		if out[i].Path != out[j].Path {
			return out[i].Path < out[j].Path
		}
		return out[i].Line < out[j].Line
	})
	return out, nil
}

// defOn 看一行是不是 word 的定义：套声明模式，名字在注释或字符串里的不算。
func defOn(h lineHit, word string, ps []pattern) (Definition, bool) {
	body := strings.TrimRight(h.text, "\r")
	p, span, ok := matchLine(ps, body)
	if !ok || inComment(body, span[0]) {
		return Definition{}, false
	}
	if _, _, quoted := quotedAt(body, span[0]+1); quoted {
		return Definition{}, false
	}
	if len(body) > hitTextMax {
		body = cutUTF8(body, max(hitTextMax, span[1]))
	}
	return Definition{Line: h.line, Text: body, Span: span, Kind: p.kind, Decl: p.weight >= 2, Score: p.weight}, true
}

// grepLines 找出清单里含有 words 之一的行，按文件归好；order 是文件出现的顺序。
// overlay 里的文件用给的内容，其余 git 仓库用 git grep，不然逐个读。
func grepLines(ctx context.Context, l *List, overlay map[string]string, words []string) (map[string][]lineHit, []string, error) {
	hits := map[string][]lineHit{}
	var order []string
	add := func(rel string, h lineHit) {
		if _, ok := hits[rel]; !ok {
			order = append(order, rel)
		}
		hits[rel] = append(hits[rel], h)
	}
	for rel, text := range overlay {
		if familyOf(rel) == "" {
			continue
		}
		for _, h := range linesWith(text, words) {
			add(rel, h)
		}
	}
	// 多仓库的清单不走这条：根目录的 git grep 搜不到嵌套仓库
	if l.Git && l.repos == nil {
		if err := gitGrep(ctx, l.Root, words, func(rel string, h lineHit) {
			if _, ok := overlay[rel]; !ok {
				add(rel, h)
			}
		}); err == nil {
			return hits, order, nil
		}
		// git 没跑成（不在 PATH、仓库坏了）：照常逐个读
	}
	for _, rel := range l.Files {
		if ctx.Err() != nil {
			return hits, order, ctx.Err()
		}
		if _, ok := overlay[rel]; ok || familyOf(rel) == "" {
			continue
		}
		b, err := readSearchable(filepath.Join(l.Root, rel))
		if err != nil || b == nil {
			continue
		}
		text := string(b)
		if !containsAny(text, words) {
			continue
		}
		for _, h := range linesWith(text, words) {
			add(rel, h)
		}
	}
	return hits, order, nil
}

func containsAny(text string, words []string) bool {
	for _, w := range words {
		if strings.Contains(text, w) {
			return true
		}
	}
	return false
}

// linesWith 返回 text 里含有 words 之一的行。
func linesWith(text string, words []string) []lineHit {
	var out []lineHit
	line := 0
	for len(text) > 0 {
		end := len(text)
		next := end
		if nl := strings.IndexByte(text, '\n'); nl >= 0 {
			end, next = nl, nl+1
		}
		if containsAny(text[:end], words) {
			out = append(out, lineHit{line: line, text: text[:end]})
		}
		text = text[next:]
		line++
	}
	return out
}

// gitGrep 用 git grep 按整词找 words：-z 时每条记录是 路径\0行号\0内容\n。--untracked 连未忽略的未跟踪文件一起搜。
func gitGrep(ctx context.Context, root string, words []string, emit func(rel string, h lineHit)) error {
	args := []string{"-C", root, "grep", "-n", "-w", "-F", "-I", "-z", "--untracked", "--no-color"}
	for _, w := range words {
		args = append(args, "-e", w)
	}
	args = append(args, "--", ".")
	cmd := exec.CommandContext(ctx, "git", args...)
	cmd.Env = append(os.Environ(), "GIT_TRACE2_EVENT=0")
	out, err := cmd.Output()
	if err != nil {
		// 没有命中时退出码是 1，不算错
		if ee, ok := err.(*exec.ExitError); ok && ee.ExitCode() == 1 && len(out) == 0 {
			return nil
		}
		return err
	}
	rest := string(out)
	for len(rest) > 0 {
		p := strings.IndexByte(rest, 0)
		if p < 0 {
			break
		}
		rel := rest[:p]
		rest = rest[p+1:]
		n := strings.IndexByte(rest, 0)
		if n < 0 {
			break
		}
		line, _ := strconv.Atoi(rest[:n])
		rest = rest[n+1:]
		end := len(rest)
		next := end
		if nl := strings.IndexByte(rest, '\n'); nl >= 0 {
			end, next = nl, nl+1
		}
		emit(filepath.FromSlash(rel), lineHit{line: line - 1, text: rest[:end]})
		rest = rest[next:]
	}
	return nil
}

// namesQual 报告文件名（或 Go 包的目录名）就是限定符：MediaPickerHelper.java、media_picker_helper.dart、pkg/x.go。
func namesQual(slash, qual string) bool {
	base := path.Base(slash)
	name := strings.TrimSuffix(base, path.Ext(base))
	// Swift、Kotlin 常有 Foo+Ext.swift、FooKt 这样的文件名，只认整个名字相等
	if strings.EqualFold(name, qual) || strings.EqualFold(name, snakeCase(qual)) {
		return true
	}
	return path.Ext(base) == ".go" && path.Base(path.Dir(slash)) == qual
}

func snakeCase(s string) string {
	var b strings.Builder
	for i, r := range s {
		if unicode.IsUpper(r) {
			if i > 0 {
				b.WriteByte('_')
			}
			r = unicode.ToLower(r)
		}
		b.WriteRune(r)
	}
	return b.String()
}

// placeScore 按定义所在的位置加分：import 这个名字的文件最高，然后是限定符指向的文件、这个文件 import 的别的文件、
// 光标所在的文件、同目录、同一类语言；测试文件减分。
func placeScore(slash string, q DefinitionQuery, fromFam family, fromDir string, fam family, qualFile bool) int {
	s := 0
	switch {
	case q.Prefer != "" && under(slash, q.Prefer):
		s += 8
	case slices.ContainsFunc(q.Imported, func(p string) bool { return under(slash, p) }):
		s += 5
	}
	if qualFile {
		s += 6
	}
	if q.From != "" && slash == filepath.ToSlash(q.From) {
		if q.Qual == "" {
			s += 4
		} else {
			s += 2
		}
	} else if q.From != "" && path.Dir(slash) == fromDir {
		s += 2
	}
	if fromFam != "" && fam == fromFam {
		s++
	}
	if isTestPath(slash) {
		s--
	}
	return s
}

// under 报告 slash 就是 p，或在目录 p 里。
func under(slash, p string) bool {
	return slash == p || strings.HasPrefix(slash, p+"/")
}

func isTestPath(slash string) bool {
	base := path.Base(slash)
	return strings.HasSuffix(strings.TrimSuffix(base, path.Ext(base)), "_test") ||
		strings.Contains(base, ".test.") || strings.Contains(base, ".spec.") || strings.Contains(base, "_spec.") ||
		strings.Contains(slash, "/test/") || strings.Contains(slash, "/tests/") || strings.Contains(slash, "/__tests__/")
}

// decisiveLead 是第一名比第二名至少高出多少分就直接跳：import 的文件（+8）、限定符指向的文件（+6）和光标所在的文件（+4）够，
// 只是同目录（+2）不够。
const decisiveLead = 4

// Unique 报告定义列表（已按分数排好）里有没有一个明确该跳过去的：只有一处；带声明的只有一处；
// 或者第一名带声明、比第二名高出 decisiveLead 以上。
func Unique(defs []Definition) (Definition, bool) {
	if len(defs) == 1 {
		return defs[0], true
	}
	if len(defs) > 1 && defs[0].Decl && defs[0].Score >= defs[1].Score+decisiveLead {
		return defs[0], true
	}
	var decl *Definition
	for i := range defs {
		if !defs[i].Decl {
			continue
		}
		if decl != nil {
			return Definition{}, false
		}
		decl = &defs[i]
	}
	if decl != nil {
		return *decl, true
	}
	return Definition{}, false
}
