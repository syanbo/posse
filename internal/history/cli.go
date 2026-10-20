package history

import (
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
)

// CLI 工具的来源。
const (
	SourceHomebrew = "homebrew"
	SourceSystem   = "system" // macOS 自带（/usr/bin 等）
	SourceOther    = ""       // 其他：npm、~/.local/bin、Android SDK 等，或者找不到
)

// 查来源的目录；测试里换掉。
var (
	brewDirs   = []string{"/opt/homebrew/bin", "/opt/homebrew/sbin"}
	brewLinks  = []string{"/usr/local/bin"} // Intel 上的 Homebrew 前缀，别的安装器也往这里放，要看链接指向
	systemDirs = []string{"/usr/bin", "/bin", "/usr/sbin", "/sbin"}
)

// cliSource 判断一个程序装在哪：Homebrew 排在系统目录前面，同名时（python3、git）用的是 Homebrew 的。
// 绝对路径按路径本身判断。
func cliSource(name, path string) string {
	if path != "" {
		if under(path, "/opt/homebrew") || under(path, "/usr/local/Cellar") || under(path, "/usr/local/Homebrew") {
			return SourceHomebrew
		}
		if slices.Contains(systemDirs, filepath.Dir(path)) {
			return SourceSystem
		}
		return SourceOther
	}
	for _, d := range brewDirs {
		if exists(filepath.Join(d, name)) {
			return SourceHomebrew
		}
	}
	for _, d := range brewLinks {
		if p, err := filepath.EvalSymlinks(filepath.Join(d, name)); err == nil &&
			(strings.Contains(p, "/Cellar/") || strings.Contains(p, "/Caskroom/") || strings.Contains(p, "/Homebrew/")) {
			return SourceHomebrew
		}
	}
	for _, d := range systemDirs {
		if exists(filepath.Join(d, name)) {
			return SourceSystem
		}
	}
	return SourceOther
}

func under(path, dir string) bool { return path == dir || strings.HasPrefix(path, dir+"/") }

func exists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}

// cliSkip 是不值得列出的命令：shell 内建、关键字和读写文件、处理文本的基础工具，几乎每条命令都有。
var cliSkip = setOf(`cd pushd popd ls cat head tail sed awk gawk grep egrep fgrep wc sort uniq cut tr tee
echo printf pwd mkdir rmdir rm cp mv ln touch chmod chown find test [ [[ true false : sleep date
export source . set unset shift exit return break continue which type whence where hash alias unalias
eval read local declare typeset readonly let trap wait jobs fg bg kill basename dirname realpath readlink
stat file diff cmp comm paste join column fold expand rev seq yes du df ps pgrep pkill nl less more
sh bash zsh dash fish getopts umask ulimit history clear tput stty tty whoami id hostname uname printenv
mktemp od xxd hexdump strings tac lsof`)

// cliWrappers 是把后面的命令包一层再跑的程序，值是带参数的选项。
var cliWrappers = map[string]map[string]bool{
	"sudo":     setOf("-u -g -h -p -C"),
	"env":      setOf("-u -C -S -P"),
	"time":     {},
	"nohup":    {},
	"command":  {},
	"exec":     setOf("-a"),
	"nice":     setOf("-n"),
	"timeout":  setOf("-s -k --signal --kill-after"),
	"gtimeout": setOf("-s -k --signal --kill-after"),
	"xargs":    setOf("-n -I -J -L -P -E -R -S -s -d -a"),
}

func setOf(words string) map[string]bool {
	m := map[string]bool{}
	for _, w := range strings.Fields(words) {
		m[w] = true
	}
	return m
}

var (
	cliNameRe    = regexp.MustCompile(`^[A-Za-z0-9_][A-Za-z0-9._+@-]*$`)
	assignmentRe = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*(\[[^]]*\])?\+?=`)
	durationRe   = regexp.MustCompile(`^[0-9.]+[smhd]?$`)
)

// cliProgram 是命令里实际运行的一个程序；Path 是写成绝对路径时的路径。
type cliProgram struct {
	Name, Path string
}

// cliPrograms 找出一条 shell 命令运行的程序，按出现顺序、去重，跳过 cliSkip 里的。
// 只做足够认出程序名的解析：引号、转义、管道和 && 这类分隔、$( )、<( ) 和反引号、重定向、heredoc、
// 环境变量赋值、sudo/env 这类包装、bash -c 里的脚本、命令里定义的函数；不展开变量和别名。
// 相对路径（./x、bin/x）和 Homebrew、系统目录以外的绝对路径是脚本或临时编出来的程序，不算。
func cliPrograms(cmd string) []cliProgram {
	var out []cliProgram
	seen := map[string]bool{}
	for _, w := range commandWords(cmd, 0, map[string]bool{}) {
		p := cliProgram{Name: w}
		if strings.Contains(w, "/") {
			if !filepath.IsAbs(w) {
				continue
			}
			p = cliProgram{Name: filepath.Base(w), Path: filepath.Clean(w)}
			if cliSource(p.Name, p.Path) == SourceOther {
				continue
			}
		}
		if !cliNameRe.MatchString(p.Name) || cliSkip[p.Name] || seen[p.Name] {
			continue
		}
		seen[p.Name] = true
		out = append(out, p)
	}
	return out
}

// shToken 是词法分析的结果：词（word）、操作符（op），或双引号里 $( ) 的内容（sub）。
type shToken struct {
	word string
	op   string
	sub  string
}

// commandWords 返回每个简单命令的程序名（原样，可能是路径）。funcs 收集命令里定义的函数，调用它们不算程序。
func commandWords(cmd string, depth int, funcs map[string]bool) []string {
	var words []string
	expect := true    // 下一个词是程序名
	skipNext := false // 下一个词是重定向的目标
	mode := ""        // for：跳到分隔符；case：跳到 in；pattern：跳到 )
	wrapper := ""     // 正在跳过包装程序的选项
	shell := 0        // 1：刚见到 sh/bash/zsh；2：见到了 -c，下一个词是脚本
	toks := shLex(cmd)
	for i := 0; i < len(toks); i++ {
		t := toks[i]
		if t.sub != "" {
			if depth < 3 {
				words = append(words, commandWords(t.sub, depth+1, funcs)...)
			}
			continue
		}
		if t.op != "" {
			if t.op != "redir" {
				skipNext = false
			}
			switch t.op {
			case "redir":
				skipNext = true
			case ";;":
				mode, expect = "pattern", false
			case ")":
				if mode == "pattern" {
					mode, expect = "", true
				} else {
					expect = false
				}
			case "`)":
				expect = false
			default: // ; \n & && || | |& ( $( `
				if mode != "case" && mode != "pattern" {
					mode = ""
				}
				expect, wrapper, shell = true, "", 0
			}
			continue
		}
		w := t.word
		if skipNext {
			skipNext = false
			continue
		}
		switch mode {
		case "for":
			continue
		case "case":
			if w == "in" {
				mode = "pattern"
			}
			continue
		case "pattern":
			if w == "esac" {
				mode, expect = "", false
			}
			continue
		}
		if shell > 0 {
			switch {
			case shell == 2:
				if depth < 3 {
					words = append(words, commandWords(w, depth+1, funcs)...)
				}
				shell = 0
			case strings.HasPrefix(w, "-") && !strings.HasPrefix(w, "--") && strings.Contains(w, "c"):
				shell = 2
			case strings.HasPrefix(w, "-"):
			default:
				shell = 0
			}
			continue
		}
		if !expect {
			continue
		}
		if wrapper != "" {
			if strings.HasPrefix(w, "-") {
				if cliWrappers[wrapper][w] {
					skipNext = true
				}
				if wrapper == "command" && (w == "-v" || w == "-V") {
					expect, wrapper = false, ""
				}
				continue
			}
			if (wrapper == "timeout" || wrapper == "gtimeout") && durationRe.MatchString(w) {
				continue
			}
		}
		if assignmentRe.MatchString(w) {
			continue
		}
		switch w {
		case "if", "then", "else", "elif", "do", "while", "until", "!", "{":
			continue
		case "fi", "done", "esac", "}":
			expect = false
			continue
		case "for", "select":
			mode, expect = "for", false
			continue
		case "case":
			mode, expect = "case", false
			continue
		case "function":
			// function name [()] { … }：跳过名字，接着认函数体里的命令
			if i+1 < len(toks) && toks[i+1].word != "" {
				funcs[toks[i+1].word] = true
				i++
			}
			if i+2 < len(toks) && toks[i+1].op == "(" && toks[i+2].op == ")" {
				i += 2
			}
			continue
		}
		// name() { … } 定义函数
		if i+2 < len(toks) && toks[i+1].op == "(" && toks[i+2].op == ")" {
			funcs[w] = true
			i += 2
			continue
		}
		if funcs[w] {
			expect = false
			continue
		}
		if _, ok := cliWrappers[w]; ok {
			wrapper = w
			continue
		}
		words = append(words, w)
		expect, wrapper = false, ""
		if b := filepath.Base(w); b == "sh" || b == "bash" || b == "zsh" || b == "dash" {
			shell = 1
		}
	}
	return words
}

// shLex 把命令切成词和操作符。引号里的内容并进词里（去掉引号），$( 和反引号当成命令分隔。
func shLex(s string) []shToken {
	var toks []shToken
	var word strings.Builder
	inWord := false
	type heredoc struct {
		delim string
		strip bool
	}
	var pending []heredoc
	wantDelim, stripTabs := false, false
	backtick := false

	flush := func() {
		if !inWord {
			return
		}
		w := word.String()
		word.Reset()
		inWord = false
		if wantDelim {
			pending = append(pending, heredoc{w, stripTabs})
			wantDelim = false
			return
		}
		toks = append(toks, shToken{word: w})
	}
	op := func(o string) {
		flush()
		toks = append(toks, shToken{op: o})
	}

	for i := 0; i < len(s); i++ {
		c := s[i]
		switch {
		case c == ' ' || c == '\t' || c == '\r':
			flush()
		case c == '\\':
			if i+1 < len(s) {
				i++
				if s[i] != '\n' {
					word.WriteByte(s[i])
					inWord = true
				}
			}
		case c == '#' && !inWord:
			for i+1 < len(s) && s[i+1] != '\n' {
				i++
			}
		case c == '\'':
			j := strings.IndexByte(s[i+1:], '\'')
			if j < 0 {
				j = len(s) - i - 1
			}
			word.WriteString(s[i+1 : i+1+j])
			inWord = true
			i += j + 1
		case c == '$' && i+1 < len(s) && s[i+1] == '\'':
			i++
			for i+1 < len(s) && s[i+1] != '\'' {
				i++
				if s[i] == '\\' && i+1 < len(s) {
					i++
				}
				word.WriteByte(s[i])
			}
			i++
			inWord = true
		case c == '"':
			for i+1 < len(s) && s[i+1] != '"' {
				i++
				if s[i] == '$' && strings.HasPrefix(s[i:], "$(") && !strings.HasPrefix(s[i:], "$((") {
					end := matchParen(s, i+2)
					toks = append(toks, shToken{sub: s[i+2 : end]})
					i = end
					continue
				}
				if s[i] == '\\' && i+1 < len(s) && strings.IndexByte("\"\\$`\n", s[i+1]) >= 0 {
					i++
				}
				word.WriteByte(s[i])
			}
			i++
			inWord = true
		case c == '$' && i+1 < len(s) && s[i+1] == '(':
			if i+2 < len(s) && s[i+2] == '(' {
				// $(( 算术 )) 不是命令
				j := strings.Index(s[i:], "))")
				if j < 0 {
					j = len(s) - i - 2
				}
				word.WriteString(s[i : i+j+2])
				inWord = true
				i += j + 1
				continue
			}
			i++
			op("$(")
		case c == '`':
			backtick = !backtick
			if backtick {
				op("`")
			} else {
				op("`)")
			}
		case c == '(':
			op("(")
		case c == ')':
			op(")")
		case c == ';':
			if i+1 < len(s) && s[i+1] == ';' {
				i++
				op(";;")
			} else {
				op(";")
			}
		case c == '&':
			switch {
			case i+1 < len(s) && s[i+1] == '&':
				i++
				op("&&")
			case i+1 < len(s) && s[i+1] == '>':
				i++
				if i+1 < len(s) && s[i+1] == '>' {
					i++
				}
				op("redir")
			default:
				op("&")
			}
		case c == '|':
			if i+1 < len(s) && (s[i+1] == '|' || s[i+1] == '&') {
				i++
				op(s[i-1 : i+1])
			} else {
				op("|")
			}
		case c == '\n':
			op("\n")
			// heredoc 的正文从下一行开始，到只有结束符的一行为止
			for _, h := range pending {
				for i+1 < len(s) {
					end := strings.IndexByte(s[i+1:], '\n')
					if end < 0 {
						end = len(s) - i - 1
					}
					line := s[i+1 : i+1+end]
					i += end + 1
					if h.strip {
						line = strings.TrimLeft(line, "\t")
					}
					if line == h.delim {
						break
					}
				}
			}
			pending = nil
		case (c == '<' || c == '>') && i+1 < len(s) && s[i+1] == '(':
			// <( ) >( ) 进程替换
			i++
			op("(")
		case c == '<' || c == '>':
			// 前面紧挨着的数字是文件描述符
			if inWord && strings.Trim(word.String(), "0123456789") == "" {
				word.Reset()
				inWord = false
			}
			flush()
			if c == '<' && strings.HasPrefix(s[i:], "<<<") {
				i += 2
				toks = append(toks, shToken{op: "redir"})
				continue
			}
			if c == '<' && strings.HasPrefix(s[i:], "<<") {
				i++
				stripTabs = i+1 < len(s) && s[i+1] == '-'
				if stripTabs {
					i++
				}
				wantDelim = true
				continue
			}
			for i+1 < len(s) && strings.IndexByte("<>|&", s[i+1]) >= 0 {
				i++
			}
			// >&2、<&- 这类复制描述符，目标就在操作符里
			if s[i] == '&' {
				for i+1 < len(s) && (s[i+1] == '-' || s[i+1] >= '0' && s[i+1] <= '9') {
					i++
				}
				continue
			}
			toks = append(toks, shToken{op: "redir"})
		default:
			word.WriteByte(c)
			inWord = true
		}
	}
	flush()
	return toks
}

// matchParen 返回 s[start:] 里和前面的 ( 配对的 ) 的位置，跳过引号里的括号；没有配对时返回 len(s)。
func matchParen(s string, start int) int {
	depth := 1
	for j := start; j < len(s); j++ {
		switch s[j] {
		case '\\':
			j++
		case '\'':
			k := strings.IndexByte(s[j+1:], '\'')
			if k < 0 {
				return len(s)
			}
			j += k + 1
		case '"':
			for j++; j < len(s) && s[j] != '"'; j++ {
				if s[j] == '\\' {
					j++
				} else if strings.HasPrefix(s[j:], "$(") {
					j = matchParen(s, j+2)
				}
			}
		case '(':
			depth++
		case ')':
			if depth--; depth == 0 {
				return j
			}
		}
	}
	return len(s)
}
