// Package namer 给 agent 会话起标题（hive session title）：规整标题格式，
// 写进 Codex、Grok 自己存标题的地方。Claude 只接受 hook 返回的 sessionTitle，由常驻进程转交。
package namer

import (
	"fmt"
	"regexp"
	"strings"
	"time"
	"unicode/utf8"
)

// Bar 是标题三段之间的全角竖线。
const Bar = "｜"

// MaxSummary 是摘要最多几个字。
const MaxSummary = 20

// Types 是标题第二段可选的类型。
var Types = []string{"修复", "feat", "调研", "调试", "重构", "文档", "杂项"}

// typeAliases 把同义词、英文类型映射回 Types，而不是直接拒绝。
var typeAliases = map[string]string{
	"fix": "修复", "bugfix": "修复", "bug": "修复", "修bug": "修复", "修复bug": "修复", "修正": "修复",
	"feat": "feat", "feature": "feat", "功能": "feat", "新功能": "feat", "新增": "feat", "实现": "feat",
	"调研": "调研", "research": "调研", "探索": "调研", "investigate": "调研", "分析": "调研", "研究": "调研",
	"调试": "调试", "debug": "调试", "排查": "调试",
	"重构": "重构", "refactor": "重构", "优化": "重构", "perf": "重构",
	"文档": "文档", "docs": "文档", "doc": "文档",
	"杂项": "杂项", "chore": "杂项", "misc": "杂项", "其他": "杂项", "其它": "杂项",
}

var (
	barVariants = strings.NewReplacer("|", Bar, "丨", Bar, "︱", Bar, "│", Bar, "┃", Bar)
	dateRe      = regexp.MustCompile(`^\d{4}$`)
)

const trailingPunct = "。．.，,、；;：:！!？?~～"

func normalizeType(s string) string {
	key := strings.ToLower(strings.Trim(strings.TrimSpace(s), "[]【】()（）"))
	if key == "" {
		return ""
	}
	if t, ok := typeAliases[key]; ok {
		return t
	}
	for _, t := range Types {
		if key == strings.ToLower(t) {
			return t
		}
	}
	return ""
}

// ErrFormat 说明标题该怎么写，给 agent 看了重拟。
var ErrFormat = fmt.Errorf("标题格式不对：需要 MMDD%s类型%s摘要，类型限 %s，摘要不超过 %d 字",
	Bar, Bar, strings.Join(Types, " "), MaxSummary)

// Normalize 把输入规整成 `MMDD｜类型｜摘要`：日期一律换成 now 的本地日期，
// 竖线和类型的变体换成标准写法；对不上格式或摘要超长时返回 ErrFormat（截断会留下半句话，让调用方重拟）。
func Normalize(raw string, now time.Time) (string, error) {
	line := strings.TrimSpace(raw)
	if line == "" || strings.Contains(line, "\n") {
		return "", ErrFormat
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
		return "", ErrFormat
	}
	typ := normalizeType(parts[0])
	if typ == "" {
		return "", ErrFormat
	}
	summary := strings.Join(strings.Fields(strings.Join(parts[1:], " ")), " ")
	summary = strings.TrimSpace(strings.TrimRight(summary, trailingPunct))
	// 「修复｜修复登录按钮」这类重复的类型词去掉一份
	if typ != "feat" && strings.HasPrefix(summary, typ) && utf8.RuneCountInString(summary) > utf8.RuneCountInString(typ)+1 {
		summary = strings.TrimLeft(strings.TrimPrefix(summary, typ), "：: ，,")
	}
	if summary == "" || utf8.RuneCountInString(summary) > MaxSummary {
		return "", ErrFormat
	}
	return now.Format("0102") + Bar + typ + Bar + summary, nil
}
