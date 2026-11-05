package files

import (
	"context"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"
)

const (
	// MaxHits 是一次检索最多收多少处命中，够用来定位，再多界面也看不过来。
	MaxHits = 2000
	// hitTextMax 是一处命中所在行最多保留的字节数。
	hitTextMax = 240
	// batchEvery 是检索中途把结果交给界面的间隔。
	batchEvery = 60 * time.Millisecond
)

// Query 是一次全文检索。Include、Exclude 是逗号分隔的 glob，见 matchGlob。
type Query struct {
	Text      string
	MatchCase bool
	Regex     bool
	Include   string
	Exclude   string
	Dir       string // 只搜这个目录（相对 Root）下的文件，为空时搜整个清单
}

// InDir 报告 rel 是否在 q.Dir 下面；Dir 为空时都算。
func (q Query) InDir(rel string) bool {
	return q.Dir == "" || rel == q.Dir || strings.HasPrefix(rel, q.Dir+string(filepath.Separator))
}

// Hit 是一行里的命中：Line 从 0 起；Spans 是命中在 Text 里的字节范围。
type Hit struct {
	Path  string // 相对清单的 Root
	Line  int
	Text  string
	Spans [][2]int
}

// Compile 把检索词编译成正则：不是正则模式时按字面匹配；不区分大小写时加 (?i)。
func (q Query) Compile() (*regexp.Regexp, error) {
	expr := q.Text
	if !q.Regex {
		expr = regexp.QuoteMeta(expr)
	}
	if !q.MatchCase {
		expr = "(?i)" + expr
	}
	return regexp.Compile(expr)
}

// Search 在清单的文件里检索，按清单顺序（路径顺序）逐个读文件。overlay 是打开着、有未保存改动的文件
// （相对路径 → 编辑器里的文本），搜它们的内容而不是磁盘上的。二进制和超过 ReadOnlySize 的文件跳过。
// 结果每 batchEvery 交给 emit 一批（在调用 Search 的 goroutine 里），最后一批在返回前交出。
// ctx 取消时尽快返回。返回是否因为命中太多而没搜完。
func Search(ctx context.Context, l *List, q Query, overlay map[string]string, emit func([]Hit)) (truncated bool, err error) {
	if q.Text == "" {
		return false, nil
	}
	re, err := q.Compile()
	if err != nil {
		return false, err
	}
	include, exclude := splitGlobs(q.Include), splitGlobs(q.Exclude)
	var batch []Hit
	total := 0
	last := time.Now()
	flush := func() {
		if len(batch) > 0 {
			emit(batch)
			batch = nil
		}
		last = time.Now()
	}
	defer flush()
	for _, rel := range l.Files {
		if ctx.Err() != nil {
			return false, ctx.Err()
		}
		if !q.InDir(rel) || len(include) > 0 && !anyGlob(include, rel) || anyGlob(exclude, rel) {
			continue
		}
		text, ok := overlay[rel]
		if !ok {
			b, err := readSearchable(filepath.Join(l.Root, rel))
			if err != nil || b == nil {
				continue
			}
			text = string(b)
		}
		hits := searchText(rel, text, re, MaxHits-total)
		batch = append(batch, hits...)
		total += len(hits)
		if total >= MaxHits {
			return true, nil
		}
		if time.Since(last) >= batchEvery {
			flush()
		}
	}
	return false, nil
}

// readSearchable 读要检索的文件，太大或二进制时返回 nil。
func readSearchable(path string) ([]byte, error) {
	fi, err := os.Stat(path)
	if err != nil || !fi.Mode().IsRegular() || fi.Size() > ReadOnlySize {
		return nil, err
	}
	b, err := os.ReadFile(path)
	if err != nil || IsBinary(b) {
		return nil, err
	}
	return b, nil
}

// searchText 找出 text 里的命中，同一行的合成一条；最多 limit 条。
func searchText(rel, text string, re *regexp.Regexp, limit int) []Hit {
	var out []Hit
	line, lineStart := 0, 0 // 已数到的行和它的起点
	// 像 "." 这样的正则在大文件里会有几百万处命中，限个数，免得一次分配太多
	for _, m := range re.FindAllStringIndex(text, 16*limit+16) {
		if m[0] == m[1] {
			continue // 空匹配（如 ^）没有意义
		}
		for {
			nl := strings.IndexByte(text[lineStart:], '\n')
			if nl < 0 || lineStart+nl >= m[0] {
				break
			}
			lineStart += nl + 1
			line++
		}
		end := len(text)
		if nl := strings.IndexByte(text[lineStart:], '\n'); nl >= 0 {
			end = lineStart + nl
		}
		s, e := m[0]-lineStart, min(m[1], end)-lineStart
		if n := len(out); n > 0 && out[n-1].Line == line {
			if e <= len(out[n-1].Text) {
				out[n-1].Spans = append(out[n-1].Spans, [2]int{s, e})
			}
			continue
		}
		if len(out) == limit {
			break
		}
		body := strings.TrimRight(text[lineStart:end], "\r")
		if len(body) > hitTextMax {
			body = cutUTF8(body, max(hitTextMax, e))
		}
		h := Hit{Path: rel, Line: line, Text: body}
		if e <= len(body) {
			h.Spans = [][2]int{{s, e}}
		}
		out = append(out, h)
	}
	return out
}

// cutUTF8 把 s 截到不超过 n 字节，不切开多字节字符。
func cutUTF8(s string, n int) string {
	if len(s) <= n {
		return s
	}
	for n > 0 && s[n]&0xC0 == 0x80 {
		n--
	}
	return s[:n]
}

func splitGlobs(s string) []string {
	var out []string
	for g := range strings.SplitSeq(s, ",") {
		if g = strings.TrimSpace(g); g != "" {
			out = append(out, g)
		}
	}
	return out
}

func anyGlob(globs []string, rel string) bool {
	for _, g := range globs {
		if matchGlob(g, rel) {
			return true
		}
	}
	return false
}

// matchGlob 判断相对路径是否符合 glob：
//   - 以 / 结尾的是目录，路径在它下面就算（"internal/app/"）
//   - 不含 / 的和路径里任一段比（"*.go" 比文件名，"testdata" 比各级目录名），开头的 **/ 去掉后同理
//   - 其余和整个路径比，或者路径在这个目录下面
func matchGlob(g, rel string) bool {
	rel = filepath.ToSlash(rel)
	g = strings.TrimPrefix(filepath.ToSlash(g), "./")
	if strings.HasSuffix(g, "/") {
		return strings.HasPrefix(rel, g)
	}
	if rest, ok := strings.CutPrefix(g, "**/"); ok && !strings.Contains(rest, "/") {
		g = rest
	}
	if !strings.Contains(g, "/") {
		for seg := range strings.SplitSeq(rel, "/") {
			if ok, _ := filepath.Match(g, seg); ok {
				return true
			}
		}
		return false
	}
	if ok, _ := filepath.Match(g, rel); ok {
		return true
	}
	return strings.HasPrefix(rel, strings.TrimSuffix(g, "/")+"/")
}
