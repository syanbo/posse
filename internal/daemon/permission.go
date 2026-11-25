package daemon

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"strings"
	"sync"
	"time"

	"posse/internal/agent"
)

// PermRequest 是一条挂起等待用户决策的工具权限请求（兼容 Antigravity PreToolUse 与 Claude PermissionRequest）。
type PermRequest struct {
	ID         string          `json:"id"`
	TerminalID string          `json:"terminalId"`
	Agent      string          `json:"agent"`
	Tool       string          `json:"tool"`
	Summary    string          `json:"summary"`
	RuleKeys   []string        `json:"-"` // 用于「本会话允许同类」精确匹配（复合命令需每段均命中）
	RuleLabel  string          `json:"ruleLabel,omitempty"`
	Payload    json.RawMessage `json:"-"`
	CreatedAt  time.Time       `json:"createdAt"`
}

// PermWatcher 由飞书网关等外部审批端实现，监听权限挂起与解除。
type PermWatcher interface {
	WatchPermission(req PermRequest) bool
	PermissionResolved(reqID, behavior, source string)
}

type permWaiter struct {
	req PermRequest
	ch  chan permOutcome
}

type permOutcome struct {
	behavior string
	source   string
}

// permissionHub 在常驻进程内统一协调手机与飞书的权限审批：任意一端先决定即放行，并通知其余端刷新卡片。
type permissionHub struct {
	mu           sync.Mutex
	waits        map[string]*permWaiter
	sessionRules map[string]map[string]bool // terminalID -> 已在本会话放行的同类规则键集合
	watcher      PermWatcher
	rootsFor     func(terminalID string) agent.ScratchRoots // 按终端读取对应飞书机器人的只读白名单配置
	timeout      time.Duration                              // 飞书审批挂起时长，默认 3 分钟（测试可覆写）
}

func (h *permissionHub) ClearTerminal(terminalID string) {
	if terminalID == "" {
		return
	}
	h.mu.Lock()
	delete(h.sessionRules, terminalID)
	h.mu.Unlock()
}

func (h *permissionHub) isSessionAllowed(terminalID string, keys []string) bool {
	if terminalID == "" || len(keys) == 0 {
		return false
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	rules := h.sessionRules[terminalID]
	if len(rules) == 0 {
		return false
	}
	for _, k := range keys {
		if !rules[k] {
			return false
		}
	}
	return true
}

func (h *permissionHub) SetWatcher(w PermWatcher) {
	h.mu.Lock()
	h.watcher = w
	h.mu.Unlock()
}

func (h *permissionHub) SetRootsResolver(fn func(terminalID string) agent.ScratchRoots) {
	h.mu.Lock()
	h.rootsFor = fn
	h.mu.Unlock()
}

func (h *permissionHub) rootsForTerminal(terminalID string) agent.ScratchRoots {
	h.mu.Lock()
	fn := h.rootsFor
	h.mu.Unlock()
	if fn != nil && terminalID != "" {
		return fn(terminalID)
	}
	return agent.DefaultScratchRoots()
}

func (h *permissionHub) larkTimeout() time.Duration {
	h.mu.Lock()
	t := h.timeout
	h.mu.Unlock()
	if t > 0 {
		return t
	}
	return 3 * time.Minute
}

// Decide 由飞书卡片回调或测试提交审批决策（allow | allow_session | deny）。
func (h *permissionHub) Decide(reqID, behavior, source string) bool {
	if behavior != "allow" && behavior != "allow_session" && behavior != "deny" {
		return false
	}
	h.mu.Lock()
	w := h.waits[reqID]
	if w != nil && behavior == "allow_session" && w.req.TerminalID != "" && len(w.req.RuleKeys) > 0 {
		if h.sessionRules == nil {
			h.sessionRules = map[string]map[string]bool{}
		}
		if h.sessionRules[w.req.TerminalID] == nil {
			h.sessionRules[w.req.TerminalID] = map[string]bool{}
		}
		for _, k := range w.req.RuleKeys {
			h.sessionRules[w.req.TerminalID][k] = true
		}
	}
	h.mu.Unlock()
	if w == nil {
		return false
	}
	outBehavior := behavior
	outSource := source
	if behavior == "allow_session" {
		outBehavior = "allow"
		if outSource != "" {
			outSource += ":session"
		} else {
			outSource = "session"
		}
	}
	select {
	case w.ch <- permOutcome{behavior: outBehavior, source: outSource}:
		return true
	default:
		return false
	}
}

// Hold 同时协调飞书审批与手机审批。
// - 若当前终端已经通过「本会话允许同类」记住了该请求的全部规则键，立即返回 allow，不再弹飞书卡片；
// - 当飞书网关认领该终端时，挂起最多 3 分钟，期间手机、飞书卡片按钮或桌面键入任一触发即结束；
// - 飞书未认领时，Claude 回退到原有手机 30 秒挂起逻辑；Antigravity 不挂起直接返回空串。
func (h *permissionHub) Hold(
	ctx context.Context,
	terminalID, agentName string,
	payload json.RawMessage,
	mobileHold func(context.Context) string,
	lastInput func() time.Time,
) string {
	roots := h.rootsForTerminal(terminalID)
	tool, summary, ruleKeys, ruleLabel := parsePermPayloadWithRoots(agentName, payload, roots)
	if h.isSessionAllowed(terminalID, ruleKeys) {
		return "allow"
	}
	var buf [12]byte
	_, _ = rand.Read(buf[:])
	req := PermRequest{
		ID:         "perm_" + hex.EncodeToString(buf[:]),
		TerminalID: terminalID,
		Agent:      agentName,
		Tool:       tool,
		Summary:    summary,
		RuleKeys:   ruleKeys,
		RuleLabel:  ruleLabel,
		Payload:    payload,
		CreatedAt:  time.Now(),
	}

	h.mu.Lock()
	watcher := h.watcher
	h.mu.Unlock()

	watchedByLark := watcher != nil && watcher.WatchPermission(req)
	if !watchedByLark {
		if mobileHold != nil {
			return mobileHold(ctx)
		}
		return ""
	}

	base := time.Time{}
	if lastInput != nil {
		base = lastInput()
	}

	ch := make(chan permOutcome, 1)
	h.mu.Lock()
	if h.waits == nil {
		h.waits = map[string]*permWaiter{}
	}
	h.waits[req.ID] = &permWaiter{req: req, ch: ch}
	h.mu.Unlock()
	defer func() {
		h.mu.Lock()
		delete(h.waits, req.ID)
		h.mu.Unlock()
	}()

	holdCtx, cancel := context.WithTimeout(ctx, h.larkTimeout())
	defer cancel()

	// 若手机也连着同一终端，并行竞态等待，任意一端先批即生效
	if mobileHold != nil {
		go func() {
			if b := mobileHold(holdCtx); b == "allow" || b == "deny" {
				h.Decide(req.ID, b, "mobile")
			}
		}()
	}

	tick := time.NewTicker(50 * time.Millisecond)
	defer tick.Stop()

	out := permOutcome{source: "timeout"}
	for {
		select {
		case res := <-ch:
			out = res
			goto done
		case <-holdCtx.Done():
			if ctx.Err() != nil {
				out.source = "cancelled"
			}
			goto done
		case <-tick.C:
			if lastInput != nil && !lastInput().Equal(base) {
				out.source = "desktop"
				goto done
			}
		}
	}
done:
	if watcher != nil {
		watcher.PermissionResolved(req.ID, out.behavior, out.source)
	}
	return out.behavior
}

// parsePermPayload 从 Claude PermissionRequest 或 Antigravity PreToolUse 负载中提取工具名、摘要与本会话同类规则键。
func parsePermPayload(agentName string, payload json.RawMessage) (tool, summary string, ruleKeys []string, ruleLabel string) {
	return parsePermPayloadWithRoots(agentName, payload, agent.DefaultScratchRoots())
}

func parsePermPayloadWithRoots(agentName string, payload json.RawMessage, roots agent.ScratchRoots) (tool, summary string, ruleKeys []string, ruleLabel string) {
	if len(payload) == 0 {
		return "", "", nil, ""
	}
	var args map[string]any
	if agentName == "antigravity" {
		var p struct {
			ToolCall struct {
				Name string         `json:"name"`
				Args map[string]any `json:"args"`
			} `json:"toolCall"`
		}
		if json.Unmarshal(payload, &p) == nil && p.ToolCall.Name != "" {
			tool, args = p.ToolCall.Name, p.ToolCall.Args
		}
	}
	if tool == "" {
		var p struct {
			ToolName  string         `json:"tool_name"`
			ToolInput map[string]any `json:"tool_input"`
		}
		if json.Unmarshal(payload, &p) == nil && p.ToolName != "" {
			tool, args = p.ToolName, p.ToolInput
		}
	}
	if tool == "" {
		return "", "", nil, ""
	}
	summary = permArgsSummary(args)
	ruleKeys, ruleLabel = derivePermRuleWithRoots(tool, args, roots)
	return tool, summary, ruleKeys, ruleLabel
}

func derivePermRule(tool string, args map[string]any) ([]string, string) {
	return derivePermRuleWithRoots(tool, args, agent.DefaultScratchRoots())
}

func derivePermRuleWithRoots(tool string, args map[string]any, roots agent.ScratchRoots) ([]string, string) {
	switch tool {
	case "run_command", "Bash", "bash":
		cmdLine := ""
		for _, k := range []string{"CommandLine", "command", "cmd"} {
			if v, ok := args[k].(string); ok && strings.TrimSpace(v) != "" {
				cmdLine = strings.TrimSpace(v)
				break
			}
		}
		return deriveCommandRuleKeysWithRoots(cmdLine, roots)
	case "call_mcp_tool":
		srv, _ := args["ServerName"].(string)
		tn, _ := args["ToolName"].(string)
		if srv != "" && tn != "" {
			lbl := srv + "/" + tn
			return []string{"mcp:" + lbl}, lbl
		}
		if tn != "" {
			return []string{"mcp:" + tn}, tn
		}
		return []string{"tool:call_mcp_tool"}, "call_mcp_tool"
	case "write_file", "write_to_file", "Write":
		return []string{"tool:write"}, "write_file"
	case "edit_file", "replace_file_content", "multi_replace_file_content", "Edit":
		return []string{"tool:edit"}, "edit_file"
	default:
		return []string{"tool:" + tool}, tool
	}
}

// noSessionRuleCommands 列出通用解释器/包装命令：执行任意代码或子命令，绝不生成「本会话允许同类」规则键。
var noSessionRuleCommands = map[string]bool{
	"bash": true, "sh": true, "zsh": true, "fish": true,
	"python": true, "python3": true,
	"node": true, "deno": true, "bun": true,
	"ruby": true, "perl": true, "php": true,
	"osascript": true, "eval": true, "sudo": true, "xargs": true,
	"npx": true, "uvx": true, "pipx": true, "env": true,
}

func deriveCommandRuleKeys(cmdLine string) ([]string, string) {
	return deriveCommandRuleKeysWithRoots(cmdLine, agent.DefaultScratchRoots())
}

func deriveCommandRuleKeysWithRoots(cmdLine string, roots agent.ScratchRoots) ([]string, string) {
	normalized := agent.NormalizeShellLine(cmdLine)
	if agent.HasUnsafeShellSyntax(normalized) {
		return nil, ""
	}
	segs, ok := agent.SplitShellSegments(normalized)
	if !ok || len(segs) == 0 {
		return nil, ""
	}
	var keys []string
	var labels []string
	seen := map[string]bool{}
	for _, seg := range segs {
		// 跳过本身已由只读安全白名单（含当前机器人配置的 readOnlyHosts/trustedScripts）放行的段，仅记录需要审批的段
		if agent.IsSafeReadOnlySegmentWithRoots(seg, roots) {
			continue
		}
		// 需审批的段若无法可靠解析、带有任意重定向、包含前置环境变量赋值、程序以带 / 的路径给出、
		// 属于通用解释器/包装命令，或指向非临时输出目的地，整条命令不生成同类规则键，只能单次审批。
		fields, hasRedir, ok := agent.ParseSegmentForRuleKey(seg)
		if !ok || len(fields) == 0 || hasRedir {
			return nil, ""
		}
		if strings.Contains(fields[0], "=") || strings.Contains(fields[0], "/") {
			return nil, ""
		}
		if noSessionRuleCommands[fields[0]] {
			return nil, ""
		}
		if agent.HasUnsafeRuleOutputFlag(fields) {
			return nil, ""
		}
		lbl := commandSegmentLabelArgs(fields)
		if lbl == "" {
			return nil, ""
		}
		k := "cmd:" + lbl
		if !seen[k] {
			seen[k] = true
			keys = append(keys, k)
			labels = append(labels, lbl)
		}
	}
	if len(keys) == 0 {
		return nil, ""
	}
	return keys, strings.Join(labels, " + ")
}

func commandSegmentLabelArgs(fields []string) string {
	if len(fields) == 0 || strings.Contains(fields[0], "/") {
		return ""
	}
	cmd := fields[0]
	rest := fields[1:]
	switch cmd {
	case "lark-cli":
		var parts []string
		for j := 0; j < len(rest) && len(parts) < 2; j++ {
			a := rest[j]
			if a == "--" {
				break
			}
			if a == "--profile" || a == "--as" || a == "--format" || a == "--jq" || a == "-q" || a == "--output" || a == "--output-dir" || a == "-o" {
				j++
				continue
			}
			if strings.HasPrefix(a, "-") {
				continue
			}
			parts = append(parts, a)
		}
		if len(parts) > 0 {
			return cmd + " " + strings.Join(parts, " ")
		}
		return cmd
	case "posse":
		var parts []string
		for j := 0; j < len(rest) && len(parts) < 2; j++ {
			a := rest[j]
			if a == "--" {
				break
			}
			if strings.HasPrefix(a, "-") {
				continue
			}
			parts = append(parts, a)
		}
		if len(parts) > 0 {
			return cmd + " " + strings.Join(parts, " ")
		}
		return cmd
	case "git", "go", "npm", "pnpm", "yarn", "docker", "kubectl", "adb":
		for j := 0; j < len(rest); j++ {
			a := rest[j]
			if a == "--" {
				break
			}
			if a == "-C" || a == "--git-dir" || a == "--work-tree" || a == "-c" {
				j++
				continue
			}
			if strings.HasPrefix(a, "-") {
				continue
			}
			return cmd + " " + a
		}
		return cmd
	default:
		return cmd
	}
}

func permArgsSummary(args map[string]any) string {
	if len(args) == 0 {
		return ""
	}
	for _, key := range []string{"CommandLine", "command", "cmd", "Path", "TargetFile", "file_path", "path", "ToolName", "ServerName", "url", "query"} {
		if v, ok := args[key].(string); ok && strings.TrimSpace(v) != "" {
			s := strings.Join(strings.Fields(strings.TrimSpace(v)), " ")
			if key == "ToolName" {
				if srv, ok := args["ServerName"].(string); ok && srv != "" {
					s = srv + " / " + s
				}
			}
			r := []rune(s)
			if len(r) > 600 {
				return string(r[:599]) + "…"
			}
			return s
		}
	}
	b, _ := json.Marshal(args)
	s := strings.TrimSpace(string(b))
	r := []rune(s)
	if len(r) > 600 {
		return string(r[:599]) + "…"
	}
	return s
}
