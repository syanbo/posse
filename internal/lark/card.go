package lark

import (
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"posse/internal/errtext"
)

const maxCardMarkdownBytes = 18000 // 给 Card 2.0 外壳和按钮留足余量，保证整卡远小于飞书 30 KB 限制

func tr(en string) string {
	return errtext.MessageIn(errtext.SystemLang(), en)
}

func card20(title, template string, elements []any) string {
	card := map[string]any{
		"schema": "2.0",
		"config": map[string]any{
			"wide_screen_mode": true,
			"update_multi":     true,
		},
		"header": map[string]any{
			"title": map[string]any{
				"tag":     "plain_text",
				"content": title,
			},
			"template": template,
		},
		"body": map[string]any{
			"elements": elements,
		},
	}
	b, _ := json.Marshal(card)
	return string(b)
}

func mdElement(content string) map[string]any {
	return map[string]any{
		"tag":     "markdown",
		"content": content,
	}
}

func cbButton(label, typ string, value map[string]any) map[string]any {
	return map[string]any{
		"tag":  "button",
		"type": typ,
		"size": "small",
		"text": map[string]any{
			"tag":     "plain_text",
			"content": label,
		},
		"behaviors": []any{
			map[string]any{
				"type":  "callback",
				"value": value,
			},
		},
	}
}

func buttonRow(buttons ...map[string]any) map[string]any {
	cols := make([]any, 0, len(buttons))
	for _, b := range buttons {
		cols = append(cols, map[string]any{
			"tag":      "column",
			"width":    "auto",
			"elements": []any{b},
		})
	}
	return map[string]any{
		"tag":                "column_set",
		"flex_mode":          "none",
		"horizontal_spacing": "small",
		"columns":            cols,
	}
}

// FormatDuration 格式化紧凑耗时（如 18s、2m 15s）。
func FormatDuration(d time.Duration) string {
	if d < 0 {
		d = 0
	}
	sec := int(d.Seconds())
	if sec < 60 {
		return fmt.Sprintf("%ds", sec)
	}
	mins := sec / 60
	rem := sec % 60
	if mins < 60 {
		if rem == 0 {
			return fmt.Sprintf("%dm", mins)
		}
		return fmt.Sprintf("%dm %ds", mins, rem)
	}
	hours := mins / 60
	m := mins % 60
	if m == 0 {
		return fmt.Sprintf("%dh", hours)
	}
	return fmt.Sprintf("%dh %dm", hours, m)
}

// FormatModelLabel 将 agent 名、模型名和思考档位格式化为飞书卡片展示标签：
// 优先显示模型名与思考档位；若模型名末尾已自带相同档位括号（如 "Gemini 4 Neon (Medium)"），
// 不重复追加；若配置了不同档位则替换末尾旧档位括号；无模型名时回退到 agent 名。
func FormatModelLabel(agentName, model, effort string) string {
	agentName = strings.TrimSpace(agentName)
	model = stripContextParen(strings.TrimSpace(model))
	effort = strings.TrimSpace(effort)
	if model == "" {
		if agentName != "" && effort != "" {
			return fmt.Sprintf("%s (%s)", agentName, effort)
		}
		return agentName
	}
	if effort == "" {
		return model
	}
	base, trailing := splitTrailingEffortParen(model)
	if trailing != "" {
		if strings.EqualFold(trailing, effort) {
			return model
		}
		return fmt.Sprintf("%s (%s)", base, effort)
	}
	return fmt.Sprintf("%s (%s)", model, effort)
}

func stripContextParen(s string) string {
	if !strings.HasSuffix(s, ")") {
		return s
	}
	open := strings.LastIndex(s, "(")
	if open < 0 {
		return s
	}
	inner := strings.ToLower(strings.TrimSpace(s[open+1 : len(s)-1]))
	if strings.Contains(inner, "context") {
		return strings.TrimSpace(s[:open])
	}
	return s
}

func splitTrailingEffortParen(s string) (base, effort string) {
	if !strings.HasSuffix(s, ")") {
		return s, ""
	}
	open := strings.LastIndex(s, "(")
	if open <= 0 {
		return s, ""
	}
	inner := strings.TrimSpace(s[open+1 : len(s)-1])
	switch strings.ToLower(inner) {
	case "low", "medium", "med", "high", "xhigh", "extra", "max", "ultra", "minimal", "none":
		return strings.TrimSpace(s[:open]), inner
	}
	return s, ""
}

func cardTitle(botName, agentName, wsLabel, state string) string {
	parts := []string{}
	if botName != "" {
		parts = append(parts, botName)
	}
	if agentName != "" {
		parts = append(parts, agentName)
	}
	if wsLabel != "" {
		parts = append(parts, wsLabel)
	}
	head := strings.Join(parts, " · ")
	if state != "" {
		if head != "" {
			return head + " — " + state
		}
		return state
	}
	return head
}

// BuildProgressCard 生成执行中的实时进度卡片（带耗时、当前工具/状态行与停止按钮；排队等待前序轮次时附加「中止前序并执行」按钮）。
func BuildProgressCard(botID, botName, agentName, wsLabel, execID, stateLabel, elapsed, tool, toolInput, phase string, subagents int, interruptPrevID string) string {
	var lines []string
	meta := stateLabel
	if elapsed != "" {
		meta += "  " + elapsed
	}
	if subagents > 0 {
		meta += fmt.Sprintf("  (+%d %s)", subagents, tr("subagents"))
	}
	lines = append(lines, "**"+meta+"**")
	switch {
	case tool != "" && toolInput != "":
		lines = append(lines, fmt.Sprintf("`%s` `%s`", tool, oneLine(toolInput, 120)))
	case tool != "":
		lines = append(lines, fmt.Sprintf("`%s`", tool))
	case phase != "":
		lines = append(lines, phase)
	default:
		lines = append(lines, tr("Working in Posse…"))
	}
	var btns []map[string]any
	if interruptPrevID != "" {
		btns = append(btns, cbButton(tr("Interrupt previous & run"), "primary_filled", map[string]any{
			"action":     "exec_interrupt_prev",
			"botId":      botID,
			"execId":     execID,
			"prevExecId": interruptPrevID,
		}))
	}
	btns = append(btns, cbButton(tr("Stop"), "danger", map[string]any{
		"action": "exec_stop",
		"botId":  botID,
		"execId": execID,
	}))
	elems := []any{
		mdElement(strings.Join(lines, "\n")),
		buttonRow(btns...),
	}
	return card20(cardTitle(botName, agentName, wsLabel, tr("Running")), "blue", elems)
}

// BuildProgressClosedCard 把最初的进度卡片收拢成一行完结回执（移除停止按钮），最终结论另发一条新卡片到对话底部。
func BuildProgressClosedCard(botName, agentName, wsLabel, outcome, elapsed string, traceCount int) string {
	label := tr("Done")
	note := tr("Reply sent below.")
	switch outcome {
	case "failed", "interrupted":
		label = tr("Failed")
		note = tr("Details sent below.")
	case "stopped", "cancelled":
		label = tr("Stopped")
		note = ""
	}
	parts := []string{label}
	if elapsed != "" {
		parts = append(parts, elapsed)
	}
	if traceCount > 0 {
		parts = append(parts, fmt.Sprintf("%d %s", traceCount, tr("tool calls")))
	}
	if note != "" {
		parts = append(parts, note)
	}
	return card20(cardTitle(botName, agentName, wsLabel, label), "grey", []any{
		mdElement(strings.Join(parts, " · ")),
	})
}

// BuildResultCard 生成执行结束后的最终结果卡片；若正文超长返回 truncated=true 供调用方补发完整 .md 文件。
func BuildResultCard(botID, botName, agentName, wsLabel, execID, outcome, elapsed string, traceCount int, body string) (card string, truncated bool) {
	template := "green"
	label := tr("Done")
	switch outcome {
	case "failed", "interrupted":
		template = "red"
		label = tr("Failed")
	case "stopped", "cancelled":
		template = "grey"
		label = tr("Stopped")
	}
	body = strings.TrimSpace(body)
	if body == "" {
		body = "(" + label + ")"
	}
	if len(body) > maxCardMarkdownBytes {
		truncated = true
		body = truncateUTF8(body, maxCardMarkdownBytes-160) + "\n\n---\n*" + tr("Output truncated; full report attached as Markdown file.") + "*"
	}
	var footerParts []string
	if elapsed != "" {
		footerParts = append(footerParts, elapsed)
	}
	if traceCount > 0 {
		footerParts = append(footerParts, fmt.Sprintf("%d %s", traceCount, tr("tool calls")))
	}
	elems := []any{mdElement(body)}
	if len(footerParts) > 0 {
		elems = append(elems, mdElement("<font color='grey'>"+strings.Join(footerParts, " · ")+"</font>"))
	}
	if outcome == "failed" || outcome == "interrupted" || outcome == "stopped" {
		elems = append(elems, buttonRow(cbButton(tr("Retry"), "primary_filled", map[string]any{
			"action": "exec_retry",
			"botId":  botID,
			"execId": execID,
		})))
	}
	return card20(cardTitle(botName, agentName, wsLabel, label), template, elems), truncated
}

// BuildPermissionCard 生成工具权限审批卡片（支持待审批与已决两种状态，含「本会话允许同类」按钮）。
func BuildPermissionCard(botID, botName, agentName, wsLabel, permID, tool, summary, ruleLabel, resolvedBehavior, resolvedSource string) string {
	var lines []string
	if tool != "" {
		lines = append(lines, fmt.Sprintf("**%s**: `%s`", tr("Tool"), tool))
	}
	if summary != "" {
		lines = append(lines, fmt.Sprintf("```\n%s\n```", summary))
	}
	if resolvedBehavior == "" && resolvedSource == "" {
		lines = append(lines, "<font color='grey'>"+tr("Only the Posse owner can approve or deny. Expires in 3 minutes.")+"</font>")
		btns := []map[string]any{
			cbButton(tr("Allow"), "primary_filled", map[string]any{
				"action": "perm_allow",
				"botId":  botID,
				"permId": permID,
			}),
		}
		if ruleLabel != "" {
			btns = append(btns, cbButton(tr("Allow similar (session)"), "default", map[string]any{
				"action": "perm_allow_session",
				"botId":  botID,
				"permId": permID,
			}))
		}
		btns = append(btns, cbButton(tr("Deny"), "danger", map[string]any{
			"action": "perm_deny",
			"botId":  botID,
			"permId": permID,
		}))
		elems := []any{
			mdElement(strings.Join(lines, "\n")),
			buttonRow(btns...),
		}
		return card20(cardTitle(botName, agentName, wsLabel, tr("Permission required")), "orange", elems)
	}
	template := "grey"
	status := tr("Timed out (returned to terminal)")
	sessionScoped := strings.HasSuffix(resolvedSource, ":session")
	cleanSource := strings.TrimSuffix(resolvedSource, ":session")
	switch resolvedBehavior {
	case "allow":
		template = "green"
		if sessionScoped {
			status = tr("Allowed for session")
		} else {
			status = tr("Allowed")
		}
	case "deny":
		template = "red"
		status = tr("Denied")
	default:
		if resolvedSource == "desktop" {
			status = tr("Handled in desktop terminal")
		}
	}
	if cleanSource != "" && (resolvedBehavior == "allow" || resolvedBehavior == "deny") {
		status += " (" + cleanSource + ")"
	}
	if sessionScoped && ruleLabel != "" {
		lines = append(lines, fmt.Sprintf("**%s** · `%s`", status, ruleLabel))
	} else {
		lines = append(lines, "**"+status+"**")
	}
	return card20(cardTitle(botName, agentName, wsLabel, status), template, []any{mdElement(strings.Join(lines, "\n"))})
}

// BuildQuestionCard 生成 Agent 提问卡片（支持选项按钮与自由文本表单，已回答时刷新为已答态）。
func BuildQuestionCard(botID, botName, agentName, questionID, question string, choices []string, answeredBody string) string {
	elems := []any{mdElement(strings.TrimSpace(question))}
	if answeredBody != "" {
		elems = append(elems, mdElement("**"+tr("Answered")+":** "+answeredBody))
		return card20(cardTitle(botName, agentName, "", tr("Answered")), "green", elems)
	}
	if len(choices) > 0 {
		btns := make([]map[string]any, 0, len(choices))
		for i, ch := range choices {
			typ := "default"
			if i == 0 {
				typ = "primary_filled"
			}
			btns = append(btns, cbButton(oneLine(ch, 60), typ, map[string]any{
				"action":     "answer_choice",
				"botId":      botID,
				"questionId": questionID,
				"choice":     ch,
			}))
		}
		elems = append(elems, buttonRow(btns...))
	}
	// 提供自定义回答表单，方便选项之外补充说明
	elems = append(elems, map[string]any{
		"tag":  "form",
		"name": "answer_form_" + safeFormName(questionID),
		"elements": []any{
			map[string]any{
				"tag":         "input",
				"name":        "answer_text",
				"required":    true,
				"placeholder": map[string]any{"tag": "plain_text", "content": tr("Type another answer…")},
			},
			map[string]any{
				"tag":              "button",
				"name":             "submit_answer",
				"form_action_type": "submit",
				"type":             "default",
				"size":             "small",
				"text":             map[string]any{"tag": "plain_text", "content": tr("Submit answer")},
			},
		},
	})
	return card20(cardTitle(botName, agentName, "", tr("Question")), "orange", elems)
}

// BuildNoticeCard 生成通用提示/状态/帮助卡片。
func BuildNoticeCard(title, template, body string) string {
	return card20(title, template, []any{mdElement(body)})
}

// BuildAgentPickerCard 生成 `/agent` 切换按钮卡片。
func BuildAgentPickerCard(botID, botName, chatID, threadKey, current string, agents []string) string {
	elems := []any{mdElement(fmt.Sprintf("**%s**: `%s`", tr("Current agent"), current))}
	if len(agents) > 0 {
		btns := make([]map[string]any, 0, len(agents))
		for _, a := range agents {
			typ := "default"
			if a == current {
				typ = "primary_filled"
			}
			btns = append(btns, cbButton(a, typ, map[string]any{
				"action":    "switch_agent",
				"botId":     botID,
				"chatId":    chatID,
				"threadKey": threadKey,
				"agent":     a,
			}))
		}
		elems = append(elems, buttonRow(btns...))
	}
	return card20(cardTitle(botName, "", "", tr("Switch Agent")), "blue", elems)
}

// WorkspaceRef 是给 `/ws` 卡片展示的精简工作区项。
type WorkspaceRef struct {
	ID    string `json:"id"`
	Label string `json:"label"`
	Path  string `json:"path"`
}

// BuildWorkspacePickerCard 生成 `/ws` 切换按钮卡片。
func BuildWorkspacePickerCard(botID, botName, chatID, threadKey, currentID string, workspaces []WorkspaceRef) string {
	curLabel := currentID
	for _, w := range workspaces {
		if w.ID == currentID {
			curLabel = w.Label
			break
		}
	}
	elems := []any{mdElement(fmt.Sprintf("**%s**: `%s`", tr("Current workspace"), curLabel))}
	limit := min(len(workspaces), 12)
	if limit > 0 {
		btns := make([]map[string]any, 0, limit)
		for _, w := range workspaces[:limit] {
			typ := "default"
			if w.ID == currentID {
				typ = "primary_filled"
			}
			btns = append(btns, cbButton(oneLine(w.Label, 40), typ, map[string]any{
				"action":      "switch_ws",
				"botId":       botID,
				"chatId":      chatID,
				"threadKey":   threadKey,
				"workspaceId": w.ID,
			}))
		}
		elems = append(elems, buttonRow(btns...))
	}
	return card20(cardTitle(botName, "", "", tr("Switch Workspace")), "blue", elems)
}

// BuildStagedAttachmentsCard 生成单聊纯附件暂存提示卡，等待用户补充指令或点击立即发送。
func BuildStagedAttachmentsCard(botID, botName, batchID string, labels []string) string {
	lines := []string{
		fmt.Sprintf("**%d %s**", len(labels), tr("attachment(s) staged")),
	}
	const maxShown = 5
	for i, name := range labels {
		if i >= maxShown {
			lines = append(lines, "- …")
			break
		}
		safe := strings.ReplaceAll(oneLine(name, 80), "`", "'")
		lines = append(lines, fmt.Sprintf("- `%s`", safe))
	}
	lines = append(lines, tr("Send your instruction next (or reply to the file) to run together."))
	elems := []any{
		mdElement(strings.Join(lines, "\n")),
		buttonRow(
			cbButton(tr("Send now"), "primary_filled", map[string]any{
				"action":  "staged_send",
				"botId":   botID,
				"batchId": batchID,
			}),
			cbButton(tr("Cancel"), "default", map[string]any{
				"action":  "staged_cancel",
				"botId":   botID,
				"batchId": batchID,
			}),
		),
	}
	return card20(cardTitle(botName, "", "", tr("Attachments staged")), "wathet", elems)
}

// BuildStagedAttachmentsClosedCard 将暂存提示卡收拢为已提交/已取消/已过期的灰色状态卡。
func BuildStagedAttachmentsClosedCard(botName, status string, count int) string {
	label := tr("Submitted")
	body := fmt.Sprintf("%d %s", count, tr("attachment(s) submitted with your message."))
	switch status {
	case "cancelled":
		label = tr("Cancelled")
		body = tr("Staged attachments cleared.")
	case "expired":
		label = tr("Expired")
		body = tr("Staged attachments expired.")
	}
	return card20(cardTitle(botName, "", "", label), "grey", []any{
		mdElement(body),
	})
}

func oneLine(s string, maxRunes int) string {
	s = strings.Join(strings.Fields(strings.TrimSpace(s)), " ")
	r := []rune(s)
	if len(r) <= maxRunes {
		return s
	}
	return string(r[:maxRunes-1]) + "…"
}

func truncateUTF8(s string, maxBytes int) string {
	if len(s) <= maxBytes {
		return s
	}
	for maxBytes > 0 && (s[maxBytes]&0xc0) == 0x80 {
		maxBytes--
	}
	return s[:maxBytes]
}

func safeFormName(s string) string {
	var b strings.Builder
	for _, r := range s {
		if (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') || r == '_' {
			b.WriteRune(r)
		}
	}
	if b.Len() == 0 {
		return "q"
	}
	return b.String()
}
