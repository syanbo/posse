package files

import (
	"bufio"
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"unicode/utf8"
)

// 随处搜索的"符号"页：按名字找声明。不建索引（按 mtime 常驻全部声明要几十 MB），每次现查：
// 先粗筛出含查询（不分大小写）的行，再用跳到定义的声明模式认出名字里含查询的那些声明。

// Symbol 是找到的一处声明。
type Symbol struct {
	Name  string
	Kind  string // 声明的种类（type、func、method、var…）
	Path  string // 相对清单的 Root
	Line  int    // 从 0 起
	Text  string
	Span  [2]int // 名字在 Text 里的字节范围
	Score int
}

// SymbolQuery 是一次符号搜索。TypesOnly 只要类型（class、struct、interface…，WebStorm 的"类"）。
type SymbolQuery struct {
	Text      string
	TypesOnly bool
	Dir       string // 只搜这个目录（相对 Root）下的文件，为空时搜整个清单
}

const (
	// MaxSymbols 是一次符号搜索最多返回的条数。
	MaxSymbols = 200
	// symbolLineCap 是粗筛最多看的行数：查询很短时含它的行成千上万，看到这么多就不再往下看
	symbolLineCap = 50000
)

// FindSymbols 在清单的源码文件里找名字含有 q.Text（不分大小写）的声明。只要带关键字或签名的声明（Decl），
// 赋值、字段这类弱匹配不算。按名字和查询的贴近程度排：一样的最前，然后是开头一样、大小写一样，名字短的在前。
// 返回结果是否没看完（命中太多，只看了一部分）。
func FindSymbols(ctx context.Context, l *List, overlay map[string]string, q SymbolQuery) ([]Symbol, bool, error) {
	text := strings.TrimSpace(q.Text)
	if text == "" {
		return nil, false, nil
	}
	name := `([\pL\pN_$]*(?i:` + regexp.QuoteMeta(text) + `)[\pL\pN_$]*)`
	pats := map[family][]pattern{}
	lower := strings.ToLower(text)
	var out []Symbol
	lines := 0
	truncated := false
	inDir := func(rel string) bool {
		return q.Dir == "" || rel == q.Dir || strings.HasPrefix(rel, q.Dir+string(filepath.Separator))
	}
	// 每一行：认得出声明就记下；返回 false 表示够了，不用再往下看
	visit := func(rel string, h lineHit) bool {
		fam := familyOf(rel)
		if fam == "" || !inDir(rel) {
			return true
		}
		lines++
		if lines > symbolLineCap {
			truncated = true
			return false
		}
		ps, ok := pats[fam]
		if !ok {
			ps = patternsWith(fam, name)
			pats[fam] = ps
		}
		d, ok := defOn(h, "", ps)
		if !ok || !d.Decl || q.TypesOnly && d.Kind != "type" {
			return true
		}
		n := d.Text[d.Span[0]:d.Span[1]]
		out = append(out, Symbol{Name: n, Kind: d.Kind, Path: rel, Line: d.Line, Text: d.Text, Span: d.Span, Score: symbolScore(n, text, lower, rel, d.Kind)})
		return true
	}
	for rel, body := range overlay {
		for _, h := range linesWithFold(body, lower) {
			if !visit(rel, h) {
				break
			}
		}
	}
	err := error(nil)
	done := false
	if l.Git {
		err = gitGrepFold(ctx, l.Root, text, func(rel string, h lineHit) bool {
			if _, ok := overlay[rel]; ok {
				return true
			}
			return visit(rel, h)
		})
		done = err == nil
	}
	if !done {
		// git 没跑成（不在 PATH、仓库坏了）：丢掉跑了一半的结果，逐个读
		if l.Git {
			out, lines, truncated = out[:0], 0, false
			for rel, body := range overlay {
				for _, h := range linesWithFold(body, lower) {
					visit(rel, h)
				}
			}
		}
		err = nil
		for _, rel := range l.Files {
			if ctx.Err() != nil {
				return nil, false, ctx.Err()
			}
			if _, ok := overlay[rel]; ok || familyOf(rel) == "" || !inDir(rel) {
				continue
			}
			b, rerr := readSearchable(filepath.Join(l.Root, rel))
			if rerr != nil || b == nil {
				continue
			}
			stop := false
			for _, h := range linesWithFold(string(b), lower) {
				if !visit(rel, h) {
					stop = true
					break
				}
			}
			if stop {
				break
			}
		}
	}
	if ctx.Err() != nil {
		return nil, false, ctx.Err()
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
	if len(out) > MaxSymbols {
		out, truncated = out[:MaxSymbols], true
	}
	return out, truncated, err
}

// symbolScore 给一处声明打分：名字和查询一样最高，其次是开头一样，大小写也一样的再加；类型、函数比变量靠前；
// 名字越长越靠后，测试文件减分。
func symbolScore(name, text, lower, rel, kind string) int {
	s := 0
	ln := strings.ToLower(name)
	switch {
	case ln == lower:
		s += 1000
	case strings.HasPrefix(ln, lower):
		s += 500
	}
	if strings.Contains(name, text) {
		s += 100
	}
	switch kind {
	case "type":
		s += 30
	case "func", "method":
		s += 20
	}
	if isTestPath(filepath.ToSlash(rel)) {
		s -= 50
	}
	return s - utf8.RuneCountInString(name)
}

// linesWithFold 返回 text 里含有 lower（已转小写）的行，不分大小写。
func linesWithFold(text, lower string) []lineHit {
	if !strings.Contains(strings.ToLower(text), lower) {
		return nil
	}
	var out []lineHit
	line := 0
	for len(text) > 0 {
		end := len(text)
		next := end
		if nl := strings.IndexByte(text, '\n'); nl >= 0 {
			end, next = nl, nl+1
		}
		if strings.Contains(strings.ToLower(text[:end]), lower) {
			out = append(out, lineHit{line: line, text: text[:end]})
		}
		text = text[next:]
		line++
	}
	return out
}

// gitGrepFold 用 git grep 不分大小写地找含有 text 的行，边读边交给 emit；emit 返回 false 时停下（结束 git）。
// 和 gitGrep 不同，不按整词，也不把输出一次读进内存：查询很短时输出可能很大。
func gitGrepFold(ctx context.Context, root, text string, emit func(rel string, h lineHit) bool) error {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	cmd := exec.CommandContext(ctx, "git", "-C", root, "grep", "-n", "-i", "-F", "-I", "-z", "--untracked", "--no-color", "-e", text, "--", ".")
	cmd.Env = append(os.Environ(), "GIT_TRACE2_EVENT=0")
	out, err := cmd.StdoutPipe()
	if err != nil {
		return err
	}
	if err := cmd.Start(); err != nil {
		return err
	}
	r := bufio.NewReaderSize(out, 64<<10)
	stopped := false
	for {
		rel, err := r.ReadString(0)
		if err != nil {
			break
		}
		num, err := r.ReadString(0)
		if err != nil {
			break
		}
		body, err := r.ReadString('\n')
		if err != nil && body == "" {
			break
		}
		line, _ := strconv.Atoi(strings.TrimSuffix(num, "\x00"))
		if !emit(filepath.FromSlash(strings.TrimSuffix(rel, "\x00")), lineHit{line: line - 1, text: strings.TrimSuffix(body, "\n")}) {
			stopped = true
			cancel()
			break
		}
	}
	err = cmd.Wait()
	if stopped {
		return nil
	}
	if ee, ok := err.(*exec.ExitError); ok && ee.ExitCode() == 1 {
		return nil // 没有命中
	}
	return err
}
