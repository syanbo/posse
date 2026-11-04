// Package errtext 把常驻进程、手机服务和系统调用的报错换成界面语言的说明。
//
// 源文本是英文：常驻进程和 CLI 的报错保持英文（agent 读的是它们），只在桌面窗口和手机服务这两个出口翻译。
// 每种界面语言一份译文表（如 zh.go），以原文的格式串为键；加一种语言就是加一张表、登记进 catalogs。
package errtext

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"net"
	"regexp"
	"strconv"
	"strings"
	"sync/atomic"
	"syscall"
	"unicode"
	"unicode/utf8"

	"posse/internal/rpc"
)

// Lang 是界面语言，取 BCP 47 的语言代码。
type Lang string

const (
	EN Lang = "en"
	ZH Lang = "zh"
)

// current 是 Text、Message 用的界面语言：窗口在主线程上换语言，后台协程同时在显示报错。
var current atomic.Value // Lang

func init() { current.Store(ZH) }

// SetCurrent 换 Text、Message 用的界面语言。
func SetCurrent(lang Lang) { current.Store(lang) }

// Current 是 Text、Message 用的界面语言。
func Current() Lang { return current.Load().(Lang) }

// catalogs 登记各语言的译文表；英文是源文本，不需要表。
var catalogs = map[Lang]*catalog{
	ZH: newCatalog(zh),
}

// 本包自己给出的说明（不是从别处传来的报错），和译文表用同一套格式串。
const (
	srcOldDaemon  = "the daemon is an older version and does not know this request; update it in Settings › About, terminals stay open"
	srcTimeout    = "the daemon did not answer in time; try again later"
	srcCanceled   = "canceled"
	srcNoResponse = "the daemon disconnected before answering, probably restarting or updating; try again later"
	srcDial       = "cannot connect to the daemon; try again later"
	srcNotExist   = "cannot find %s"
	srcPermission = "no permission to access %s"
	srcExist      = "%s already exists"
	srcNotDir     = "%s is not a directory"
	srcIsDir      = "%s is a directory"
	srcNotEmpty   = "%s is not an empty directory"
	srcNoSpace    = "not enough disk space to write %s"
	srcPathOther  = "%s: %s"
)

// Text 是 err 在界面上显示的文字（界面语言为 Current）。
func Text(err error) string { return TextIn(Current(), err) }

// TextIn 是 err 用 lang 显示的文字：常驻进程的报错去掉错误码，认得的翻译，认不出的原样给出，方便排查。
func TextIn(lang Lang, err error) string {
	return display(lang, textIn(lang, err))
}

func textIn(lang Lang, err error) string {
	if err == nil {
		return ""
	}
	var re *rpc.Error
	if errors.As(err, &re) {
		if re.Code == rpc.CodeUnknown {
			return sprintf(lang, srcOldDaemon)
		}
		return MessageIn(lang, re.Message)
	}
	var pe *fs.PathError
	switch {
	case errors.Is(err, context.DeadlineExceeded):
		return sprintf(lang, srcTimeout)
	case errors.Is(err, context.Canceled):
		return sprintf(lang, srcCanceled)
	case errors.Is(err, rpc.ErrNoResponse):
		return sprintf(lang, srcNoResponse)
	case isDial(err):
		return sprintf(lang, srcDial)
	case errors.As(err, &pe):
		return pathText(lang, pe)
	}
	return MessageIn(lang, err.Error())
}

func isDial(err error) bool {
	var oe *net.OpError
	return errors.As(err, &oe) && oe.Op == "dial"
}

func pathText(lang Lang, pe *fs.PathError) string {
	p := pe.Path
	switch {
	case errors.Is(pe.Err, fs.ErrNotExist):
		return sprintf(lang, srcNotExist, p)
	case errors.Is(pe.Err, fs.ErrPermission):
		return sprintf(lang, srcPermission, p)
	case errors.Is(pe.Err, fs.ErrExist):
		return sprintf(lang, srcExist, p)
	case errors.Is(pe.Err, syscall.ENOTDIR):
		return sprintf(lang, srcNotDir, p)
	case errors.Is(pe.Err, syscall.EISDIR):
		return sprintf(lang, srcIsDir, p)
	case errors.Is(pe.Err, syscall.ENOTEMPTY):
		return sprintf(lang, srcNotEmpty, p)
	case errors.Is(pe.Err, syscall.ENOSPC):
		return sprintf(lang, srcNoSpace, p)
	}
	return sprintf(lang, srcPathOther, p, pe.Err.Error())
}

// Message 把一条报错的原文翻译成界面语言；认不出的原样返回。
func Message(msg string) string { return MessageIn(Current(), msg) }

// MessageIn 把一条报错的原文翻译成 lang。
func MessageIn(lang Lang, msg string) string {
	c := catalogs[lang]
	if c == nil {
		return display(lang, msg)
	}
	return c.match(msg, 0)
}

// display 是给人看之前的最后一步：英文源文本按 Go 的习惯小写开头，界面上要像一句话，首字母大写。
// 只在这个出口做，CLI 和 agent 读到的原文不变。
func display(lang Lang, s string) string {
	if lang != EN {
		return s
	}
	return capitalize(s)
}

// capitalize 把开头的单词首字母大写；单词里本来就有大写（macOS、iPhone）、是代号（binary_file）或不是字母开头（路径、数字）的不动。
func capitalize(s string) string {
	end := 0
	for end < len(s) && s[end] >= 'a' && s[end] <= 'z' {
		end++
	}
	if end == 0 {
		return s
	}
	if end < len(s) {
		r, _ := utf8.DecodeRuneInString(s[end:])
		if unicode.IsLetter(r) || unicode.IsDigit(r) || r == '_' {
			return s
		}
	}
	return strings.ToUpper(s[:1]) + s[1:]
}

// sprintf 按格式串取 lang 的译文填参数；没有译文时用英文原文。
func sprintf(lang Lang, format string, args ...string) string {
	if c := catalogs[lang]; c != nil {
		if e, ok := c.byFormat[format]; ok {
			return fill(e.text, args)
		}
	}
	a := make([]any, len(args))
	for i, s := range args {
		a[i] = s
	}
	return fmt.Sprintf(format, a...)
}

// exactOnly 的格式串太宽（什么报错都匹配得上），只经 sprintf 按格式串取译文，不拿来匹配整条报错。
var exactOnly = map[string]bool{srcPathOther: true}

type catalog struct {
	entries  []entry
	byFormat map[string]*entry
}

type entry struct {
	format string // 原文的格式串，和代码里的一字不差（errtext_test 核对）
	text   string // 译文，{n} 是第 n 个参数，语序可以和原文不同
	re     *regexp.Regexp
	verbs  []byte
}

func newCatalog(pairs [][2]string) *catalog {
	c := &catalog{entries: make([]entry, len(pairs)), byFormat: map[string]*entry{}}
	for i, p := range pairs {
		re, verbs := compile(p[0])
		c.entries[i] = entry{format: p[0], text: p[1], re: re, verbs: verbs}
		c.byFormat[p[0]] = &c.entries[i]
	}
	return c
}

// match 按顺序找第一条匹配整条报错的译文；%v、%w 多半包着下一层报错，接着翻译。
func (c *catalog) match(msg string, depth int) string {
	if depth > 3 {
		return msg
	}
	// 多行的报错（几个仓库各一行）：单行的格式串不拿来整条匹配（%s 会把几行吞成一个参数），找不到整条的就逐行翻译
	multi := strings.Contains(msg, "\n")
	for _, e := range c.entries {
		if exactOnly[e.format] || multi && !strings.Contains(e.format, "\n") {
			continue
		}
		m := e.re.FindStringSubmatch(msg)
		if m == nil {
			continue
		}
		args := make([]string, len(e.verbs))
		for i, v := range e.verbs {
			a := m[i+1]
			switch v {
			case 'q':
				if s, err := strconv.Unquote(a); err == nil {
					a = s
				}
			case 'v', 'w':
				a = c.match(a, depth+1)
			}
			args[i] = a
		}
		return fill(e.text, args)
	}
	if multi {
		lines := strings.Split(msg, "\n")
		for i, l := range lines {
			lines[i] = c.match(l, depth)
		}
		return strings.Join(lines, "\n")
	}
	return msg
}

// fill 把 text 里的 {1}、{2}… 换成第几个参数。
func fill(text string, args []string) string {
	var b strings.Builder
	for i := 0; i < len(text); i++ {
		if text[i] == '{' {
			if j := strings.IndexByte(text[i:], '}'); j > 1 {
				if n, err := strconv.Atoi(text[i+1 : i+j]); err == nil && n >= 1 && n <= len(args) {
					b.WriteString(args[n-1])
					i += j
					continue
				}
			}
		}
		b.WriteByte(text[i])
	}
	return b.String()
}

var verbRe = regexp.MustCompile(`%[sqvdw]`)

// compile 把格式串变成匹配整条报错的正则：%s、%v、%w 匹配任意文字，%q 匹配带引号的字符串，%d 匹配整数。
func compile(format string) (*regexp.Regexp, []byte) {
	var b strings.Builder
	var verbs []byte
	b.WriteString(`(?s)^`)
	last := 0
	for _, loc := range verbRe.FindAllStringIndex(format, -1) {
		b.WriteString(regexp.QuoteMeta(format[last:loc[0]]))
		v := format[loc[0]+1]
		verbs = append(verbs, v)
		switch v {
		case 'q':
			b.WriteString(`("(?:[^"\\]|\\.)*")`)
		case 'd':
			b.WriteString(`(-?\d+)`)
		default:
			b.WriteString(`(.*?)`)
		}
		last = loc[1]
	}
	b.WriteString(regexp.QuoteMeta(format[last:]))
	b.WriteString(`$`)
	return regexp.MustCompile(b.String()), verbs
}
