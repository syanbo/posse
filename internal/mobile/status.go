package mobile

import (
	"regexp"
	"strings"
)

// Claude Code 干活时屏幕底部有一行状态："✻ Puzzling… (1m 54s · ↓ 9.5k tokens · thought for 1s)"，
// 星号在几种符号间转。Codex 是 "• Working (12s • esc to interrupt)"。
// 都从第一列开始；回复正文有缩进，不会认错。
var (
	claudeStatusRe = regexp.MustCompile(`^[·✢✳✶✻✽*] ([A-Z][\w' -]{0,40}…)(?: \((.+)\))?\s*$`)
	codexStatusRe  = regexp.MustCompile(`^[•◦] ([A-Z][\w' -]{0,40}) \((.+)\)\s*$`)
)

// statusLines 是从最后一行有字的往上找状态行的范围：它在输入框上面，离底部不远。
const statusLines = 16

// agentStatus 从屏幕上找出 agent 的状态行，去掉转圈符号和按键提示，比如
// "Puzzling… (1m 54s · ↓ 9.5k tokens)"。没有时返回空串。
func agentStatus(screen []string) string {
	end := len(screen)
	for end > 0 && strings.TrimSpace(screen[end-1]) == "" {
		end--
	}
	stop := max(end-statusLines, 0)
	for i := end - 1; i >= stop; i-- {
		line := strings.TrimRight(screen[i], " ")
		if m := claudeStatusRe.FindStringSubmatch(line); m != nil {
			return statusText(m[1], m[2], " · ")
		}
		if m := codexStatusRe.FindStringSubmatch(line); m != nil && strings.Contains(m[2], "to interrupt") {
			return statusText(m[1], m[2], " • ")
		}
	}
	return ""
}

func statusText(verb, details, sep string) string {
	var keep []string
	for _, p := range strings.Split(details, sep) {
		p = strings.TrimSpace(p)
		// 按键提示和一闪而过的"思考了几秒"不要，免得这一行来回跳
		if p == "" || strings.Contains(p, "to interrupt") || strings.HasPrefix(p, "thought for") ||
			strings.HasPrefix(p, "thinking") {
			continue
		}
		keep = append(keep, p)
	}
	if len(keep) == 0 {
		return verb
	}
	return verb + " (" + strings.Join(keep, " · ") + ")"
}
