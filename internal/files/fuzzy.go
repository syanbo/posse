package files

import (
	"path/filepath"
	"strconv"
	"strings"
)

// Match 是快速打开的一条结果：路径（相对清单的 Root）和命中的字节位置，供界面高亮。
type Match struct {
	Path  string
	Score int
	Pos   []int
}

// Fuzzy 在 files 里按子序列匹配 query（不分大小写，空格忽略），返回得分最高的 limit 条，从高到低。
// 只保留前 limit 条，不对全部结果排序。query 为空时按清单顺序返回前 limit 条。
func Fuzzy(files []string, query string, limit int) []Match {
	q := lowerASCII(strings.ReplaceAll(query, " ", ""))
	if q == "" {
		n := min(limit, len(files))
		out := make([]Match, n)
		for i := range n {
			out[i] = Match{Path: files[i]}
		}
		return out
	}
	var top []Match
	pos := make([]int, len(q))
	for _, f := range files {
		score, ok := scorePath(f, q, pos)
		if !ok {
			continue
		}
		if len(top) == limit && !better(score, f, top[len(top)-1]) {
			continue
		}
		m := Match{Path: f, Score: score, Pos: append([]int(nil), pos...)}
		// 插入排序：top 很短
		i := len(top)
		if i < limit {
			top = append(top, m)
		} else {
			i--
			top[i] = m
		}
		for ; i > 0 && better(top[i].Score, top[i].Path, top[i-1]); i-- {
			top[i], top[i-1] = top[i-1], top[i]
		}
	}
	return top
}

// better 报告 (score, path) 是否排在 m 前面：得分高的在前，同分时路径短的、字典序小的在前。
func better(score int, path string, m Match) bool {
	if score != m.Score {
		return score > m.Score
	}
	if len(path) != len(m.Path) {
		return len(path) < len(m.Path)
	}
	return path < m.Path
}

// scorePath 给一条路径打分，命中位置写进 pos。整个 query 能落在文件名里时只在文件名里找，加大分。
func scorePath(path, q string, pos []int) (int, bool) {
	base := strings.LastIndexByte(path, filepath.Separator) + 1
	if score, ok := scoreFrom(path, base, q, pos); ok {
		if pos[0] == base {
			score += 50 // 文件名以 query 开头
		}
		return score + 1000, true
	}
	return scoreFrom(path, 0, q, pos)
}

// scoreFrom 从 path[start:] 起贪心地按子序列匹配 q：连续命中、在词首命中加分，中间跳过的字符扣分。
func scoreFrom(path string, start int, q string, pos []int) (int, bool) {
	score, qi, last := 0, 0, -1
	for i := start; i < len(path) && qi < len(q); i++ {
		if lowerByte(path[i]) != q[qi] {
			continue
		}
		pos[qi] = i
		switch {
		case last >= 0 && i == last+1:
			score += 15
		case wordStart(path, i):
			score += 10
		default:
			score += 1
		}
		if last >= 0 {
			score -= min(i-last-1, 10)
		}
		last = i
		qi++
	}
	if qi < len(q) {
		return 0, false
	}
	// 路径越短越好
	return score - len(path)/8, true
}

// wordStart 报告 path[i] 是不是一个词的开头：路径开头、分隔符后面，或小写后的大写字母。
func wordStart(path string, i int) bool {
	if i == 0 {
		return true
	}
	p := path[i-1]
	switch p {
	case '/', '\\', '_', '-', '.', ' ':
		return true
	}
	return 'a' <= p && p <= 'z' && 'A' <= path[i] && path[i] <= 'Z'
}

func lowerByte(b byte) byte {
	if 'A' <= b && b <= 'Z' {
		return b + 'a' - 'A'
	}
	return b
}

// lowerASCII 只转 ASCII 字母：路径里的中文按原样逐字节比较。
func lowerASCII(s string) string {
	b := []byte(s)
	for i := range b {
		b[i] = lowerByte(b[i])
	}
	return string(b)
}

// SplitLine 拆开快速打开输入的 "路径:行号"，没有行号时 line 为 0。
func SplitLine(query string) (path string, line int) {
	i := strings.LastIndexByte(query, ':')
	if i < 0 {
		return query, 0
	}
	n, err := strconv.Atoi(strings.TrimSpace(query[i+1:]))
	if err != nil || n <= 0 {
		return query, 0
	}
	return query[:i], n
}
