package mobile

import (
	"regexp"
	"strings"
	"unicode"

	"posse/internal/history"
)

// 草稿是 agent 正在写、会话文件里还没有的那段回复，从终端屏幕上取（posse.draft.v1）。
// Claude 的正文一块写完才进会话文件，Codex 要等一轮结束，手机不看屏幕就要干等。
//
// 屏幕上是 TUI 排过版的文字：按终端宽度折行、续行缩进 2 格，Markdown 符号被吃掉或换了样子，
// 所以草稿只当纯文本，正式消息到了由手机换掉。
//
// 两家都整屏重画（Claude 每次重画连回滚区一起清掉，Codex 在备用屏幕上），回复一长开头就滚出屏幕、
// 回滚区里也找不到。所以每一帧只取看得见的那段，和上一帧拼好的接起来（drafter.rows）。

var (
	// Codex 的工具条目："• Ran ls"、"• Explored"，下面跟 "  └ …"
	codexToolRe = regexp.MustCompile(`^• (Working|Waiting|Running|Ran|Explored|Exploring|Edited|Editing|Called|Calling|Searched|Searching|Updated Plan|Applied|Added|Deleted)\b`)
	// Claude 的工具调用开头："⏺ Bash(ls)"
	claudeToolRe = regexp.MustCompile(`^⏺ [A-Z][\w-]*\(`)
	// Codex 一轮结束后接在回复下面的 "  Worked for 1m 33s • 02:02"，有的版本是 "─ Worked for … ─"
	codexWorkedRe = regexp.MustCompile(`^[\s─]*Worked for \d`)
	listItemRe    = regexp.MustCompile(`^([-*•◦]|\d+[.)]) `)
)

// draftHold 是同一块草稿缩回去时最多按住的帧数：Codex 写完一段重排时，
// 偶尔有一帧把正在写的那行画成下一段的开头，下一帧又好了
const draftHold = 4

// draftMutable 是拼接时允许和这一帧对不上的末尾行数：正在写的几行会被重排
// （Claude 的表格写完前是原样的竖线，写完换成框线；列表项先只画一个 "-"）。
const draftMutable = 6

type drafter struct {
	agent string
	// rows 是当前这块回复拼到现在的行，已去掉块的缩进；head 是块头那行
	rows []string
	head string
	// known 是会话文件里最近一条助手正文，规范成只剩字母和数字
	known string
	sent  string
	held  int
}

func newDrafter(agent string) *drafter {
	if agent != "claude" && agent != "codex" {
		return nil
	}
	return &drafter{agent: agent}
}

// note 记下会话文件里新出现的助手正文：屏幕上同一段之后不再当草稿。
func (d *drafter) note(msgs []history.ChatMessage) {
	if d == nil {
		return
	}
	for i := len(msgs) - 1; i >= 0; i-- {
		if msgs[i].Role != "assistant" {
			continue
		}
		var b strings.Builder
		for _, blk := range msgs[i].Blocks {
			if blk.Type == "text" {
				b.WriteString(blk.Text)
			}
		}
		if t := draftNorm(b.String()); t != "" {
			d.known = t
			return
		}
	}
}

// next 用这一帧屏幕算出草稿。working 是 agent 在干活（hooks 报的状态，或屏幕上有状态行）：
// Claude、Codex 流式写正文时都不画状态行，不能只看它。changed 为 false 表示和上次推的一样。
func (d *drafter) next(screen []string, cols int, working bool) (text string, changed bool) {
	if d == nil {
		return "", false
	}
	if !working {
		d.rows = nil
		return d.emit("")
	}
	prev, head := d.rows, d.head
	body, start, ok := replyBlock(d.agent, screen)
	// same 是这一帧还是上一帧那块：块头那行还在写时会变长
	same := false
	switch {
	case !ok:
		d.rows, d.head = nil, ""
	case start:
		same = head != "" && (strings.HasPrefix(body[0], head) || strings.HasPrefix(head, body[0]))
		d.rows, d.head = body, body[0]
	case len(d.rows) == 0:
		d.rows = body
	default:
		same = true
		d.rows = stitch(d.rows, body)
	}
	text = draftText(d.rows, cols)
	n := draftNorm(text)
	if n == "" || strings.Contains(d.known, n) {
		text = ""
	}
	// 同一块缩回去了，当作重排的过渡帧
	if old := draftNorm(d.sent); same && text != "" && old != "" && !strings.HasPrefix(n, old) && d.held < draftHold {
		d.held++
		d.rows, d.head = prev, head
		return d.sent, false
	}
	d.held = 0
	return d.emit(text)
}

func (d *drafter) emit(text string) (string, bool) {
	if text == d.sent {
		return text, false
	}
	d.sent = text
	return text, true
}

// replyBlock 找出屏幕上最后一块，是回复正文时返回它去掉缩进的各行。start 表示块的开头
// （Claude 的 "⏺"、Codex 的 "•"）在屏幕上；不在时 body 是从屏幕顶上开始的续行。
// 最后一块是工具调用、用户的话、"完成"行这类时 ok 为 false。
func replyBlock(agent string, screen []string) (body []string, start, ok bool) {
	end := inputTop(agent, screen)
	top := 0
	// Codex 回复滚出屏幕时，顶上一行钉着截断的用户提示："› 只读…"
	if agent == "codex" && end > 0 && strings.HasPrefix(screen[0], "› ") && strings.HasSuffix(screen[0], "…") {
		top = 1
	}
	// 从下往上找最后一个块头（第一列有字的行），跳过状态行那一块
	head := -1
	for i := end - 1; i >= top; i-- {
		line := screen[i]
		if line == "" || line[0] == ' ' {
			continue
		}
		if agentStatus([]string{line}) != "" {
			end = i
			continue
		}
		head = i
		break
	}
	rows := screen[max(head, top):end]
	from := 0
	if head >= 0 {
		h := rows[0]
		switch agent {
		case "claude":
			if !strings.HasPrefix(h, "⏺ ") || claudeToolRe.MatchString(h) {
				return nil, false, false
			}
		case "codex":
			if !strings.HasPrefix(h, "• ") || codexToolRe.MatchString(h) {
				return nil, false, false
			}
		}
		_, first, _ := strings.Cut(h, " ")
		body, from = []string{first}, 1
	}
	for i := from; i < len(rows); i++ {
		t := strings.TrimLeft(rows[i], " ")
		// Claude 工具的输出挂在 "⎿" 下面。进行中的工具块头上的 "⏺" 会闪，闪灭时看着像正文的续行
		if agent == "claude" && strings.HasPrefix(t, "⎿") {
			return nil, false, false
		}
		// Codex 的工具自带 "•" 块头，正文下面的 "└ Tip: …" 和 "Worked for …" 是一轮结束后加的
		if agent == "codex" && (strings.HasPrefix(t, "└") || codexWorkedRe.MatchString(rows[i])) {
			rows = rows[:i]
			break
		}
	}
	body = append(body, dedent(rows[from:])...)
	filled := nonEmpty(body)
	if len(filled) == 0 {
		return nil, false, false
	}
	// Claude 刚开始调工具时只有一行 "⏺ Reading 1 file…"，下面的 "⎿" 稍后才画
	if agent == "claude" && head >= 0 && len(filled) == 1 && strings.HasSuffix(body[0], "…") {
		return nil, false, false
	}
	return body, head >= 0, true
}

// inputTop 是输入框上沿：Claude 是 "❯" 输入行上面那条横线，Codex 是最下面的 "›" 输入行。
func inputTop(agent string, screen []string) int {
	for i := len(screen) - 1; i > 0; i-- {
		switch agent {
		case "claude":
			if strings.HasPrefix(screen[i], "❯") && strings.HasPrefix(screen[i-1], "─") {
				return i - 1
			}
		case "codex":
			if strings.HasPrefix(screen[i], "› ") || screen[i] == "›" {
				return i
			}
		}
	}
	return len(screen)
}

func dedent(rows []string) []string {
	out := make([]string, len(rows))
	for i, l := range rows {
		n := 0
		for n < 2 && n < len(l) && l[n] == ' ' {
			n++
		}
		out[i] = l[n:]
	}
	return out
}

// stitch 把这一帧看得见的行接到之前拼好的 acc 后面：在 acc 里从后往前找这一帧第一行非空行，
// 之后的非空行也对得上（末尾 draftMutable 行内可以不同）就从那里换成这一帧的。
// 空行不比：Codex 写下一段时才在两段之间补空行。对不上说明两帧之间滚过了一整屏，中间空一截。
func stitch(acc, body []string) []string {
	first := -1
	for i, l := range body {
		if strings.TrimSpace(l) != "" {
			first = i
			break
		}
	}
	if first < 0 {
		return acc
	}
	body = body[first:]
	filled := nonEmpty(acc)
	for k := len(filled) - 1; k >= 0; k-- {
		p := filled[k]
		if !sameRow(acc[p], body[0], k == len(filled)-1) {
			continue
		}
		if !follows(acc, filled[k+1:], body[1:], len(filled)-draftMutable) {
			continue
		}
		return append(acc[:p:p], body...)
	}
	return append(append(acc[:len(acc):len(acc)], "", "⋯", ""), body...)
}

// follows 比较 acc 里 rest 这些非空行和 body 的非空行，下标到 loose 以后的不比。
func follows(acc []string, rest []int, body []string, loose int) bool {
	j := 0
	for n, p := range rest {
		for j < len(body) && strings.TrimSpace(body[j]) == "" {
			j++
		}
		if j == len(body) {
			return true
		}
		if !sameRow(acc[p], body[j], n == len(rest)-1) && p < loose {
			return false
		}
		j++
	}
	return true
}

// sameRow 比较两行；last 是 acc 最后一行非空行，它可能还没写完，是这一帧那行的开头也算。
func sameRow(old, now string, last bool) bool {
	if old == now {
		return true
	}
	return last && strings.HasPrefix(now, old)
}

func nonEmpty(rows []string) []int {
	var out []int
	for i, l := range rows {
		if strings.TrimSpace(l) != "" {
			out = append(out, i)
		}
	}
	return out
}

// draftText 把屏幕上的行接回成段：TUI 折出来的行接回上一行，空行保留成段落分隔，
// 框线表格和代码这类不折的行原样保留。
func draftText(rows []string, cols int) string {
	avail := cols - 2
	var b strings.Builder
	prev, blank := "", false
	for _, row := range rows {
		if strings.TrimSpace(row) == "" {
			blank = prev != ""
			prev = ""
			continue
		}
		switch {
		case prev != "" && !boxRow(prev) && !boxRow(row) && wrapped(prev, row, avail):
			row = strings.TrimLeft(row, " ")
			if !tight(lastRune(prev)) || !tight(firstRune(row)) {
				b.WriteByte(' ')
			}
		case b.Len() > 0 && blank:
			b.WriteString("\n\n")
		case b.Len() > 0:
			b.WriteByte('\n')
		}
		b.WriteString(row)
		prev, blank = row, false
	}
	return b.String()
}

// wrapped 判断 next 是不是 prev 放不下折过来的：next 的第一个词接在 prev 后面会超出终端宽度。
// 两家都按空格断词，中文连成一串算一个词。
// 列表项自己起一行，续行另有缩进，不会以列表符号开头。
func wrapped(prev, next string, avail int) bool {
	next = strings.TrimLeft(next, " ")
	if listItemRe.MatchString(next) {
		return false
	}
	word, _, _ := strings.Cut(next, " ")
	return textWidth(prev)+1+textWidth(word) > avail
}

func boxRow(s string) bool {
	return strings.ContainsAny(s, "│┌┐└┘├┤┬┴┼━─")
}

// tight 是两边不用空格隔开的字：中日韩文字和全角标点，以及中文里常贴着用的引号、省略号
func tight(r rune) bool {
	return wide(r) || strings.ContainsRune("“”‘’…—", r)
}

func firstRune(s string) rune {
	for _, r := range s {
		return r
	}
	return 0
}

func lastRune(s string) rune {
	r := []rune(s)
	if len(r) == 0 {
		return 0
	}
	return r[len(r)-1]
}

func textWidth(s string) int {
	n := 0
	for _, r := range s {
		if wide(r) {
			n += 2
		} else {
			n++
		}
	}
	return n
}

// wide 是终端里占两格的字，够折行判断用
func wide(r rune) bool {
	switch {
	case r < 0x1100:
		return false
	case r <= 0x115F, r >= 0x2E80 && r <= 0xA4CF, r >= 0xAC00 && r <= 0xD7A3,
		r >= 0xF900 && r <= 0xFAFF, r >= 0xFE30 && r <= 0xFE4F, r >= 0xFF00 && r <= 0xFF60,
		r >= 0xFFE0 && r <= 0xFFE6, r >= 0x1F300 && r <= 0x1F64F, r >= 0x1F900 && r <= 0x1F9FF,
		r >= 0x20000 && r <= 0x3FFFD:
		return true
	}
	return false
}

// draftNorm 只留字母和数字：屏幕上的 Markdown 符号、折行和表格框线都和会话文件里的不一样，
// 代码围栏那一行（连同语言名）屏幕上不画
func draftNorm(s string) string {
	var b strings.Builder
	for line := range strings.Lines(s) {
		if t := strings.TrimSpace(line); strings.HasPrefix(t, "```") || strings.HasPrefix(t, "~~~") {
			continue
		}
		for _, r := range line {
			if unicode.IsLetter(r) || unicode.IsNumber(r) {
				b.WriteRune(unicode.ToLower(r))
			}
		}
	}
	return b.String()
}
