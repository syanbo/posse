// Package namer 给 agent 会话起标题（posse session title）：规整标题格式，
// 写进 Codex、Grok 自己存标题的地方。Claude 只接受 hook 返回的 sessionTitle，由常驻进程转交。
package namer

import (
	"errors"
	"fmt"
	"regexp"
	"strings"
	"time"
	"unicode/utf8"
)

// Bar 是标题三段之间的全角竖线。
const Bar = "｜"

// kinds 是标题第二段的类型，中英文各一种写法：标题跟着 Posse 的界面语言写（Normalize 的 en）。
var kinds = []struct{ zh, en string }{
	{"修复", "fix"}, {"开发", "feat"}, {"调研", "research"}, {"调试", "debug"}, {"重构", "refactor"}, {"文档", "docs"}, {"杂项", "chore"},
}

// Types 是标题第二段可选的类型。
func Types(en bool) []string {
	out := make([]string, len(kinds))
	for i, k := range kinds {
		out[i] = k.zh
		if en {
			out[i] = k.en
		}
	}
	return out
}

// MaxSummary 是摘要最多几个字：英文一个意思要的字符多，放宽一倍。
func MaxSummary(en bool) int {
	if en {
		return 40
	}
	return 20
}

// AliasNote 是起名指南里说明哪些写法也认的一句。
func AliasNote(en bool) string {
	if en {
		return "Chinese type words and synonyms such as feature or config are converted"
	}
	return "feat counts as 开发, 配置 as 杂项"
}

// typeAliases 把同义词映射到 kinds 的下标，而不是直接拒绝；两种语言的标准写法也在 normalizeType 里认。
var typeAliases = map[string]int{
	"bugfix": 0, "bug": 0, "hotfix": 0, "修bug": 0, "修复bug": 0, "修正": 0,
	"feature": 1, "dev": 1, "develop": 1, "功能": 1, "新功能": 1, "新增": 1, "实现": 1,
	"investigate": 2, "explore": 2, "探索": 2, "分析": 2, "研究": 2,
	"排查": 3,
	"优化": 4, "perf": 4,
	"doc":  5,
	"misc": 6, "其他": 6, "其它": 6, "配置": 6, "config": 6, "setup": 6, "环境": 6, "构建": 6, "build": 6, "ci": 6,
}

var (
	barVariants = strings.NewReplacer("|", Bar, "丨", Bar, "︱", Bar, "│", Bar, "┃", Bar)
	dateRe      = regexp.MustCompile(`^\d{4}$`)
)

const trailingPunct = "。．.，,、；;：:！!？?~～"

// normalizeType 返回类型在 kinds 里的下标，认不出时是 -1。
func normalizeType(s string) int {
	key := strings.ToLower(strings.Trim(strings.TrimSpace(s), "[]【】()（）"))
	if key == "" {
		return -1
	}
	if i, ok := typeAliases[key]; ok {
		return i
	}
	for i, k := range kinds {
		if key == k.zh || key == k.en {
			return i
		}
	}
	return -1
}

// ErrFormat 说明标题该怎么写，给 agent 看了重拟；Normalize 返回的报错包着它，带上这种语言的类型和字数。
var ErrFormat = errors.New("bad title format: want MMDD" + Bar + "type" + Bar + "summary")

func formatError(en bool) error {
	return fmt.Errorf("%w; type is one of %s (the other language's words and common synonyms are converted), summary at most %d characters",
		ErrFormat, strings.Join(Types(en), " "), MaxSummary(en))
}

// Normalize 把输入规整成 `MMDD｜类型｜摘要`：日期一律换成 now 的本地日期，竖线换成标准写法，
// 类型换成 en 这种语言的写法；对不上格式或摘要超长时返回包着 ErrFormat 的报错（截断会留下半句话，让调用方重拟）。
func Normalize(raw string, now time.Time, en bool) (string, error) {
	line := strings.TrimSpace(raw)
	if line == "" || strings.Contains(line, "\n") {
		return "", formatError(en)
	}
	var parts []string
	for _, p := range strings.Split(barVariants.Replace(line), Bar) {
		if p = strings.TrimSpace(p); p != "" {
			parts = append(parts, p)
		}
	}
	if len(parts) >= 1 && dateRe.MatchString(parts[0]) {
		parts = parts[1:]
	}
	if len(parts) < 2 {
		return "", formatError(en)
	}
	i := normalizeType(parts[0])
	if i < 0 {
		return "", formatError(en)
	}
	typ := kinds[i].zh
	if en {
		typ = kinds[i].en
	}
	summary := strings.Join(strings.Fields(strings.Join(parts[1:], " ")), " ")
	summary = strings.TrimSpace(strings.TrimRight(summary, trailingPunct))
	summary = dropTypeWord(summary, parts[0], kinds[i].zh, kinds[i].en)
	if summary == "" || utf8.RuneCountInString(summary) > MaxSummary(en) {
		return "", formatError(en)
	}
	return now.Format("0102") + Bar + typ + Bar + summary, nil
}

// typeSeps 是类型词和摘要之间的分隔：「修复：登录按钮」「fix: login」。
const typeSeps = " ：:，,-—"

// dropTypeWord 去掉摘要开头重复的类型词。只在后面跟着分隔符时去：「修复率统计」「开发者工具」「fixture」里是词的一部分。
func dropTypeWord(summary string, words ...string) string {
	for _, w := range words {
		rest, ok := cutPrefixFold(summary, w)
		if !ok || rest == "" || !strings.ContainsRune(typeSeps, []rune(rest)[0]) {
			continue
		}
		if rest = strings.TrimLeft(rest, typeSeps); rest != "" {
			return rest
		}
	}
	return summary
}

func cutPrefixFold(s, prefix string) (string, bool) {
	if len(s) < len(prefix) || !strings.EqualFold(s[:len(prefix)], prefix) {
		return s, false
	}
	return s[len(prefix):], true
}
