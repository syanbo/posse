package files

import (
	"bufio"
	"encoding/json"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"strings"
	"unicode/utf8"
)

// ⌘点击处是什么：照 Zed 的 hover_links，先认网址和引号里的文件路径，都不是才当标识符去找定义。

// LinkKind 是指针下的东西的种类。
type LinkKind uint8

const (
	LinkNone LinkKind = iota
	LinkWord          // 标识符，Text 是它，Qual 是前面的限定符
	LinkPath          // 引号里或 #include <> 里的文件路径，Text 是路径本身（不含引号）
	LinkURL           // 网址
)

// Link 是指针下的东西和它在行里的范围（下划线画这一段）。
type Link struct {
	Kind       LinkKind
	Text, Qual string
	Start, End int
}

var urlRe = regexp.MustCompile(`https?://[^\s'"<>()\[\]）]+`)

// LinkAt 返回 line 里 col（字节）处的东西：网址；引号里的路径（含有 / 或 . 的才算，'react' 这种包名和普通字符串不算，
// 字符串里也不认标识符）；注释里不带引号的路径（`见 docs/x.md`，照 Zed 的 surrounding_filename 按空白分词、去掉两头的标点）；
// 不在字符串和注释里的标识符。
func LinkAt(line string, col int) Link {
	col = max(0, min(col, len(line)))
	for _, m := range urlRe.FindAllStringIndex(line, -1) {
		if col >= m[0] && col <= m[1] {
			return Link{Kind: LinkURL, Text: strings.TrimRight(line[m[0]:m[1]], ".,;:!?。，；："), Start: m[0], End: m[1]}
		}
	}
	if s, e, ok := quotedAt(line, col); ok {
		if text := line[s:e]; looksLikePath(text) {
			return Link{Kind: LinkPath, Text: text, Start: s, End: e}
		}
		return Link{}
	}
	comment := inComment(line, col)
	if s, e, ok := tokenAt(line, col); ok && (comment || strings.Contains(line[s:e], "/")) {
		if text := line[s:e]; looksLikePath(text) {
			return Link{Kind: LinkPath, Text: text, Start: s, End: e}
		}
	}
	if comment {
		return Link{}
	}
	word, qual, s, e := WordAt(line, col)
	if word == "" {
		return Link{}
	}
	return Link{Kind: LinkWord, Text: word, Qual: qual, Start: s, End: e}
}

// looksLikePath 报告一段文字像不像文件路径：有 / 或 .，没有空白。
func looksLikePath(s string) bool {
	return s != "" && !strings.ContainsAny(s, " \t") && strings.ContainsAny(s, "/.")
}

// tokenAt 返回 col 所在的、按空白分开的一段，两头包着的标点（括号、引号、句末标点）去掉；
// Markdown 链接 `[标题](路径)` 取括号里的路径（照 Zed 的 link_pattern_file_candidates）。
func tokenAt(line string, col int) (int, int, bool) {
	s, e := col, col
	for s > 0 && line[s-1] != ' ' && line[s-1] != '\t' {
		s--
	}
	for e < len(line) && line[e] != ' ' && line[e] != '\t' {
		e++
	}
	if i := strings.Index(line[s:e], "]("); i >= 0 && col >= s+i+2 {
		s += i + 2
	}
	for s < e {
		r, n := utf8.DecodeRuneInString(line[s:e])
		if !strings.ContainsRune("`([{<\"'（", r) {
			break
		}
		s += n
	}
	for e > s {
		r, n := utf8.DecodeLastRuneInString(line[s:e])
		if !strings.ContainsRune("`)]}>\"'.,:;!?，。：；）", r) {
			break
		}
		e -= n
	}
	if col < s || col > e || s == e {
		return 0, 0, false
	}
	return s, e, true
}

// inComment 报告 col 在不在注释里：行首的 //、#（#include 这类指令除外）、*、/*、--，或引号外、col 前面的 //。
func inComment(line string, col int) bool {
	trim := strings.TrimLeft(line, " \t")
	switch {
	case strings.HasPrefix(trim, "//"), strings.HasPrefix(trim, "/*"), strings.HasPrefix(trim, "* "), trim == "*", strings.HasPrefix(trim, "*/"),
		strings.HasPrefix(trim, "--"), strings.HasPrefix(trim, "#") && !strings.HasPrefix(trim, "#include") && !strings.HasPrefix(trim, "#import") && !strings.HasPrefix(trim, "#define") && !strings.HasPrefix(trim, "#if") && !strings.HasPrefix(trim, "#else") && !strings.HasPrefix(trim, "#endif") && !strings.HasPrefix(trim, "#pragma") && !strings.HasPrefix(trim, "#!"):
		return true
	}
	open := byte(0)
	for i := 0; i+1 < len(line) && i < col; i++ {
		c := line[i]
		switch {
		case open != 0 && c == '\\':
			i++
		case open != 0 && c == open:
			open = 0
		case open == 0 && (c == '\'' || c == '"' || c == '`'):
			open = c
		case open == 0 && c == '/' && line[i+1] == '/':
			return true
		}
	}
	return false
}

// IsCode 报告按扩展名认不认识这个文件的语言：不认识的（Markdown、JSON、纯文本）里不把词当标识符去找定义。
func IsCode(p string) bool {
	return familyOf(p) != ""
}

// quotedAt 返回 col 所在的引号（' " `）或 #include 的 <> 里的内容范围。
func quotedAt(line string, col int) (int, int, bool) {
	if strings.HasPrefix(strings.TrimSpace(line), "#") {
		if a := strings.IndexByte(line, '<'); a >= 0 {
			if b := strings.IndexByte(line[a:], '>'); b > 0 && col > a && col <= a+b {
				return a + 1, a + b, true
			}
		}
	}
	open, start := byte(0), 0
	for i := 0; i < len(line); i++ {
		c := line[i]
		switch {
		case open != 0 && c == '\\':
			i++
		case open != 0 && c == open:
			if col > start && col <= i {
				return start + 1, i, true
			}
			open = 0
		case open == 0 && (c == '\'' || c == '"' || c == '`'):
			open, start = c, i
		}
	}
	return 0, 0, false
}

// importExts 是补扩展名时试的顺序；目录再试 index.*。
var importExts = []string{".ts", ".tsx", ".js", ".jsx", ".mjs", ".cjs", ".mts", ".cts", ".vue", ".svelte", ".d.ts", ".json", ".css", ".scss", ".less", ".sass", ".dart", ".py", ".rs", ".go", ".md"}

// ResolveImport 把代码里写的路径 spec 解析成磁盘上的文件：相对路径按 from 所在目录（from 为空按 root），
// 绝对路径照原样，`~/`、`@/` 这类别名按 tsconfig、jsconfig 的 paths，没有就试 root/src 和 root；
// 没有扩展名的补上常见的扩展名，是目录的再找 index.*；Go 的 import 路径按 go.mod 的 module 名换成目录、取里面第一个 .go 文件。
// 路径后面的 :行号 去掉后返回。
func ResolveImport(root, from, spec string) (string, int, bool) {
	spec, line := SplitLine(spec)
	if spec == "" {
		return "", 0, false
	}
	var bases []string
	dir := root
	if from != "" {
		dir = filepath.Dir(from)
	}
	switch {
	case strings.HasPrefix(spec, "./") || strings.HasPrefix(spec, "../"):
		bases = []string{filepath.Join(dir, spec)}
	case filepath.IsAbs(spec):
		bases = []string{spec, filepath.Join(root, spec)}
	case strings.HasPrefix(spec, "~/"):
		bases = append(aliasBases(root, spec), filepath.Join(os.Getenv("HOME"), spec[2:]))
	case strings.HasPrefix(spec, "@/") || strings.HasPrefix(spec, "#/"):
		bases = aliasBases(root, spec)
	default:
		bases = append(aliasBases(root, spec), filepath.Join(dir, spec), filepath.Join(root, spec))
		if mod := goModule(root); mod != "" && (spec == mod || strings.HasPrefix(spec, mod+"/")) {
			bases = append(bases, filepath.Join(root, strings.TrimPrefix(spec, mod)))
		}
	}
	for _, b := range bases {
		if p, ok := fileAt(b); ok {
			return p, line, true
		}
	}
	return "", 0, false
}

// fileAt 返回 base 对应的文件：本身是文件，补扩展名，或目录里的 index.*、第一个 .go 文件。
func fileAt(base string) (string, bool) {
	if fi, err := os.Stat(base); err == nil {
		if !fi.IsDir() {
			return base, true
		}
		for _, ext := range importExts {
			if p := filepath.Join(base, "index"+ext); isFile(p) {
				return p, true
			}
		}
		if es, err := os.ReadDir(base); err == nil {
			for _, e := range es {
				if n := e.Name(); strings.HasSuffix(n, ".go") && !strings.HasSuffix(n, "_test.go") && !e.IsDir() {
					return filepath.Join(base, n), true
				}
			}
		}
		return "", false
	}
	for _, ext := range importExts {
		if isFile(base + ext) {
			return base + ext, true
		}
	}
	return "", false
}

func isFile(p string) bool {
	fi, err := os.Stat(p)
	return err == nil && !fi.IsDir()
}

// aliasBases 按 tsconfig.json、jsconfig.json 的 compilerOptions.paths 和 baseUrl 展开别名；没有配置时 `~/`、`@/` 试 src 和根目录。
func aliasBases(root, spec string) []string {
	var out []string
	for _, name := range []string{"tsconfig.json", "jsconfig.json"} {
		co, ok := compilerOptions(filepath.Join(root, name))
		if !ok {
			continue
		}
		base := filepath.Join(root, co.BaseURL)
		for pat, targets := range co.Paths {
			prefix, ok := strings.CutSuffix(pat, "*")
			if !ok {
				if pat == spec {
					for _, t := range targets {
						out = append(out, filepath.Join(base, t))
					}
				}
				continue
			}
			if rest, ok := strings.CutPrefix(spec, prefix); ok {
				for _, t := range targets {
					out = append(out, filepath.Join(base, strings.Replace(t, "*", rest, 1)))
				}
			}
		}
		if co.BaseURL != "" && !strings.HasPrefix(spec, ".") {
			out = append(out, filepath.Join(base, spec))
		}
		break
	}
	if i := strings.IndexByte(spec, '/'); i == 1 && (spec[0] == '~' || spec[0] == '@' || spec[0] == '#') {
		out = append(out, filepath.Join(root, "src", spec[2:]), filepath.Join(root, spec[2:]))
	}
	return out
}

type tsCompilerOptions struct {
	BaseURL string              `json:"baseUrl"`
	Paths   map[string][]string `json:"paths"`
}

// compilerOptions 读 tsconfig 的 compilerOptions；文件里常有注释和多余的逗号，去掉再解析。
func compilerOptions(p string) (tsCompilerOptions, bool) {
	b, err := os.ReadFile(p)
	if err != nil {
		return tsCompilerOptions{}, false
	}
	var cfg struct {
		CompilerOptions tsCompilerOptions `json:"compilerOptions"`
	}
	if json.Unmarshal(b, &cfg) != nil {
		if json.Unmarshal(stripJSONC(b), &cfg) != nil {
			return tsCompilerOptions{}, false
		}
	}
	return cfg.CompilerOptions, true
}

var (
	jsonLineComment   = regexp.MustCompile(`(?m)^\s*//.*$|\s//[^\n"]*$`)
	jsonBlockComment  = regexp.MustCompile(`(?s)/\*.*?\*/`)
	jsonTrailingComma = regexp.MustCompile(`,\s*([\]}])`)
)

func stripJSONC(b []byte) []byte {
	b = jsonBlockComment.ReplaceAll(b, nil)
	b = jsonLineComment.ReplaceAll(b, nil)
	return jsonTrailingComma.ReplaceAll(b, []byte("$1"))
}

// goModule 返回 root 下 go.mod 的 module 名，没有为空。
func goModule(root string) string {
	f, err := os.Open(filepath.Join(root, "go.mod"))
	if err != nil {
		return ""
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		if rest, ok := strings.CutPrefix(strings.TrimSpace(sc.Text()), "module "); ok {
			return strings.TrimSpace(rest)
		}
	}
	return ""
}

var importPathRe = regexp.MustCompile(`(?:\bfrom\s*|\brequire\s*\(\s*|\bimport\s*\(?\s*|^\s*import\s+|@import\s+(?:url\()?\s*|^\s*#\s*include\s*)['"<]([^'"<>]+)['">]`)

// goImportLineRe 是 Go import 块里的一行：可带别名，整行只有一个带引号的路径。
var goImportLineRe = regexp.MustCompile(`^\s*(?:[\pL_][\pL\pN_]*\s+)?"([^"]+)"\s*(?://.*)?$`)

// ImportPathOn 返回这一行 import、require、#include 引用的路径，没有为空：
// 跳 import 进来的名字时优先去那个文件里找定义。
func ImportPathOn(line string) string {
	if m := importPathRe.FindStringSubmatch(line); m != nil {
		return m[1]
	}
	if m := goImportLineRe.FindStringSubmatch(line); m != nil {
		return m[1]
	}
	return ""
}

// relIn 把 abs 换成相对 root 的斜杠路径，不在 root 里返回空。
func relIn(root, abs string) string {
	rel, err := filepath.Rel(root, abs)
	if err != nil || rel == ".." || strings.HasPrefix(rel, "../") || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return ""
	}
	return filepath.ToSlash(rel)
}

// PreferFor 返回光标所在行 import 的文件相对 root 的路径（Go 是目录），找定义时优先它；没有为空。
func PreferFor(root, from, line string) string {
	spec := ImportPathOn(line)
	if spec == "" {
		return ""
	}
	p, _, ok := ResolveImport(root, from, spec)
	if !ok {
		return ""
	}
	rel := relIn(root, p)
	if path.Ext(rel) == ".go" {
		return path.Dir(rel)
	}
	return rel
}
