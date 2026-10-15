package history

import (
	"strings"
	"unicode"
	"unicode/utf8"
)

// isCJK 判断没有空格分词习惯的文字：汉字、假名、韩文。
func isCJK(r rune) bool {
	return unicode.Is(unicode.Han, r) || unicode.Is(unicode.Hiragana, r) ||
		unicode.Is(unicode.Katakana, r) || unicode.Is(unicode.Hangul, r)
}

// splitCJK 在每个中日韩字符两侧加空格，让 unicode61 分词器把它们逐字切开。
// 否则一整句中文会成为一个词，只能按整句或前缀命中。
func splitCJK(s string) string {
	var b strings.Builder
	b.Grow(len(s) + len(s)/2)
	prevCJK := false
	for _, r := range s {
		cjk := isCJK(r)
		if (cjk || prevCJK) && !unicode.IsSpace(r) {
			b.WriteByte(' ')
		}
		b.WriteRune(r)
		prevCJK = cjk
	}
	return b.String()
}

// term 是查询里的一个词：用户用空格分开的一段，或引号括起来的短语。
type term struct {
	text   string
	phrase bool // 引号短语不做前缀匹配
}

func parseTerms(q string) []term {
	var out []term
	for len(q) > 0 {
		q = strings.TrimLeftFunc(q, unicode.IsSpace)
		if q == "" {
			break
		}
		if q[0] == '"' {
			end := strings.IndexByte(q[1:], '"')
			if end < 0 {
				end = len(q) - 1
			}
			if s := strings.TrimSpace(q[1 : 1+end]); hasWord(s) {
				out = append(out, term{text: s, phrase: true})
			}
			q = q[min(len(q), end+2):]
			continue
		}
		end := strings.IndexFunc(q, unicode.IsSpace)
		if end < 0 {
			end = len(q)
		}
		if s := q[:end]; hasWord(s) {
			out = append(out, term{text: s})
		}
		q = q[end:]
	}
	return out
}

func hasWord(s string) bool {
	return strings.IndexFunc(s, func(r rune) bool { return unicode.IsLetter(r) || unicode.IsNumber(r) }) >= 0
}

// matchExpr 把查询转成 FTS5 表达式：各词之间是 AND，每个词是一个短语，
// 中文逐字成短语即子串匹配；末尾是字母数字时按前缀匹配（hook 命中 hooks）。
func matchExpr(terms []term) string { return joinTerms(terms, " AND ") }

// anyExpr 是只要命中任一词的表达式，所有词同时出现的结果为空时退而用它。
func anyExpr(terms []term) string { return joinTerms(terms, " OR ") }

func joinTerms(terms []term, op string) string {
	parts := make([]string, 0, len(terms))
	for _, t := range terms {
		p := `"` + strings.ReplaceAll(splitCJK(t.text), `"`, `""`) + `"`
		if !t.phrase {
			last, _ := utf8.DecodeLastRuneInString(strings.TrimRightFunc(t.text, func(r rune) bool {
				return !unicode.IsLetter(r) && !unicode.IsNumber(r)
			}))
			if !isCJK(last) {
				p += " *"
			}
		}
		parts = append(parts, p)
	}
	return strings.Join(parts, op)
}

// needles 是在原文里定位命中位置用的小写片段：每个词按非字母数字切开。
func needles(terms []term) []string {
	seen := map[string]bool{}
	var out []string
	for _, t := range terms {
		for _, f := range strings.FieldsFunc(strings.ToLower(t.text), func(r rune) bool {
			return !unicode.IsLetter(r) && !unicode.IsNumber(r)
		}) {
			if !seen[f] {
				seen[f] = true
				out = append(out, f)
			}
		}
	}
	return out
}

// Span 是摘要里需要高亮的一段，单位是摘要字符串的字节偏移。
type Span struct {
	Start int `json:"start"`
	End   int `json:"end"`
}

// snippet 截取第一个命中词附近约 width 个字符，返回摘要和其中的命中位置。
// 换行和连续空白压成一个空格，一行就能显示。
func snippet(text string, ns []string, width int) (string, []Span) {
	text = collapseSpace(text)
	lower := strings.ToLower(text)
	if len(lower) != len(text) {
		// 个别字符小写后字节数会变，偏移对不上，退回按原文大小写匹配
		lower = text
	}
	first := -1
	for _, n := range ns {
		if i := strings.Index(lower, n); i >= 0 && (first < 0 || i < first) {
			first = i
		}
	}
	if first < 0 {
		// 中文短语可能隔着标点或空格命中，原文里找不到整段，退而找第一个字
		for _, n := range ns {
			if r, _ := utf8.DecodeRuneInString(n); isCJK(r) {
				if i := strings.IndexRune(lower, r); i >= 0 && (first < 0 || i < first) {
					first = i
				}
			}
		}
	}
	start, end := 0, len(text)
	if first >= 0 {
		start = backRunes(text, first, width/3)
	}
	end = forwardRunes(text, start, width)
	out := text[start:end]
	var spans []Span
	low := lower[start:end]
	for _, n := range ns {
		for i := 0; ; {
			j := strings.Index(low[i:], n)
			if j < 0 {
				break
			}
			spans = append(spans, Span{i + j, i + j + len(n)})
			i += j + len(n)
		}
	}
	prefix := ""
	if start > 0 {
		prefix = "…"
	}
	if prefix != "" {
		for i := range spans {
			spans[i].Start += len(prefix)
			spans[i].End += len(prefix)
		}
	}
	suffix := ""
	if end < len(text) {
		suffix = "…"
	}
	return prefix + out + suffix, mergeSpans(spans)
}

func collapseSpace(s string) string {
	var b strings.Builder
	b.Grow(len(s))
	space := false
	for _, r := range s {
		if unicode.IsSpace(r) {
			space = true
			continue
		}
		if space && b.Len() > 0 {
			b.WriteByte(' ')
		}
		space = false
		b.WriteRune(r)
	}
	return b.String()
}

// backRunes 从字节位置 i 往前退 n 个字符。
func backRunes(s string, i, n int) int {
	for ; n > 0 && i > 0; n-- {
		_, size := utf8.DecodeLastRuneInString(s[:i])
		i -= size
	}
	return i
}

func forwardRunes(s string, i, n int) int {
	for ; n > 0 && i < len(s); n-- {
		_, size := utf8.DecodeRuneInString(s[i:])
		i += size
	}
	return i
}

func mergeSpans(spans []Span) []Span {
	if len(spans) < 2 {
		return spans
	}
	for i := 1; i < len(spans); i++ {
		for j := i; j > 0 && spans[j].Start < spans[j-1].Start; j-- {
			spans[j], spans[j-1] = spans[j-1], spans[j]
		}
	}
	out := spans[:1]
	for _, s := range spans[1:] {
		last := &out[len(out)-1]
		if s.Start <= last.End {
			last.End = max(last.End, s.End)
			continue
		}
		out = append(out, s)
	}
	return out
}
