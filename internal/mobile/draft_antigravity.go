package mobile

import (
	"strings"
	"unicode"
	"unicode/utf8"
)

// agy 的正文没有块头，同样缩进两格的还有用户提示的续行。
// 从输入框上方找正文，靠会话日志里的用户原文确认提示从哪里结束。
func antigravityReplyBlock(screen []string, prompt string, continuing bool) ([]string, bool, bool) {
	top := antigravityInputTop(screen)
	if top < 0 {
		return nil, false, false
	}
	end := top
	for end > 0 && antigravityFooterLine(screen[end-1]) {
		end--
	}
	head := end - 1
	for head >= 0 && (screen[head] == "" || strings.HasPrefix(screen[head], "  ")) {
		head--
	}
	start := head + 1
	anchored := head >= 0
	const collapsedTool = " (ctrl+o to expand)"
	const expandedTool = " (ctrl+o to collapse)"
	switch {
	case head >= 0 && strings.HasPrefix(screen[head], "> "):
		next, ok := antigravityAfterPrompt(screen[head:end], prompt)
		if !ok {
			return nil, false, false
		}
		start = head + next
	case head >= 0 && (antigravityThoughtHeader(screen[head]) ||
		strings.HasPrefix(screen[head], "● ") && strings.HasSuffix(screen[head], collapsedTool)):
		// 思考摘要和收起的工具可能有续行，正文从它们之后第一个空行开始。
		for start < end && screen[start] != "" {
			start++
		}
	case head < 0 && continuing,
		head >= 0 && strings.HasPrefix(screen[head], "● ") && strings.HasSuffix(screen[head], expandedTool):
		// 展开工具的最后一行也带收起提示，块头滚走后仍能找到正文的起点。
		tool := anchored
		for i := start; i < end; i++ {
			line := screen[i]
			if strings.HasPrefix(strings.TrimSpace(line), "⎿ ") {
				tool = true
			}
			if strings.HasSuffix(line, expandedTool) {
				if i+1 == end || screen[i+1] != "" {
					return nil, false, false
				}
				start, anchored, tool = i+1, true, false
			}
		}
		if tool {
			return nil, false, false
		}
	default:
		// 第一次看到的只有缩进行时，无法确认是回复、提示词还是工具输出的续行。
		return nil, false, false
	}
	for start < end && screen[start] == "" {
		start++
	}
	if start >= end {
		return nil, false, false
	}
	return dedent(screen[start:end]), anchored, true
}

func antigravityThoughtHeader(line string) bool {
	return strings.HasPrefix(line, "○ Thought for ") || strings.HasPrefix(line, "▸ Thought for ")
}

// 输入框夹在两条横线之间。上面用户消息的 > 和横线只成对出现一次。
func antigravityInputTop(screen []string) int {
	bottom := -1
	for i := len(screen) - 1; i >= 0; i-- {
		line := screen[i]
		if line == "" || strings.Trim(line, "─") != "" {
			continue
		}
		if bottom < 0 {
			bottom = i
			continue
		}
		if line == screen[bottom] && i+1 < bottom && (screen[i+1] == ">" || strings.HasPrefix(screen[i+1], "> ")) {
			return i
		}
		break
	}
	return -1
}

func antigravityFooterLine(line string) bool {
	if line == "" || strings.HasPrefix(line, "└ Tip: ") {
		return true
	}
	r, n := utf8.DecodeRuneInString(line)
	return r >= 0x2800 && r <= 0x28FF && strings.HasPrefix(line[n:], "  ")
}

func antigravityPromptNorm(text string) string {
	return strings.Map(func(r rune) rune {
		if unicode.IsSpace(r) {
			return -1
		}
		return r
	}, text)
}

func antigravityAfterPrompt(rows []string, prompt string) (int, bool) {
	if prompt == "" {
		return 0, false
	}
	for i, line := range rows {
		if i == 0 {
			line = strings.TrimPrefix(line, "> ")
		}
		var ok bool
		prompt, ok = strings.CutPrefix(prompt, antigravityPromptNorm(line))
		if !ok {
			return 0, false
		}
		if prompt == "" {
			return i + 1, true
		}
	}
	return 0, false
}

// TUI 表格的各列独立折行，逐行取到的词序可能和 Markdown 不同。
// 正文有其他文字时用这些文字去重，只有表格时仍比较全文。
func antigravityDraftNorm(text string, screen bool) string {
	if !strings.ContainsAny(text, "|│") {
		return draftNorm(text)
	}
	var prose strings.Builder
	for line := range strings.Lines(text) {
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "|") || screen && strings.ContainsRune(line, '│') {
			continue
		}
		prose.WriteString(line)
	}
	if s := draftNorm(prose.String()); s != "" {
		return s
	}
	return draftNorm(text)
}
