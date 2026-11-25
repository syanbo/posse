// Package lark 实现 Posse 原生的飞书多机器人网关：
// 托管本机 lark-cli 的多 profile 长连接，将飞书单聊与同事群映射为 Posse 讨论，
// 提供 Card 2.0 实时进度、权限审批、选项问答与停止按钮闭环。
package lark

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

const (
	SessionChat   = "chat"   // 一个飞书群对应一个 Posse 讨论（适合一键拉群的报障群）
	SessionThread = "thread" // 群里每个话题/回复串对应一个独立讨论（适合长期同事大群）

	TriggerSelf      = "self"      // 仅本机主人（ownerOpenId）可触发
	TriggerAllowlist = "allowlist" // 本机主人 + allowedUsers 名单可触发
	TriggerGroup     = "group"     // 群内任意成员可触发（仅建议只读角色）

	PermAsk  = "ask"  // 敏感工具走飞书/手机审批卡片（超时退回终端）
	PermSkip = "skip" // 跳过工具审批
)

// Config 存在 $POSSE_HOME/lark-bots.json，描述所有已配置的飞书机器人与全局主人身份。
type Config struct {
	OwnerOpenID string      `json:"ownerOpenId,omitempty"`
	Bots        []BotConfig `json:"bots,omitempty"`
}

// BotConfig 对应一个飞书自建应用（一个 lark-cli profile）及其默认工作区、Agent 与权限策略。
type BotConfig struct {
	ID                string   `json:"id"`
	Name              string   `json:"name"`
	Profile           string   `json:"profile"`
	Enabled           bool     `json:"enabled"`
	WorkspaceID       string   `json:"workspaceId,omitempty"`
	Agent             string   `json:"agent,omitempty"`
	Model             string   `json:"model,omitempty"`
	Effort            string   `json:"effort,omitempty"`
	PermissionMode    string   `json:"permissionMode,omitempty"` // ask | skip
	Role              string   `json:"role,omitempty"`
	SessionMode       string   `json:"sessionMode,omitempty"`       // chat | thread
	TriggerPolicy     string   `json:"triggerPolicy,omitempty"`     // self | allowlist | group
	GroupContextCount int      `json:"groupContextCount,omitempty"` // 群聊 @ 触发时自动附带的最近 N 条群消息（0=关闭，最多 50）
	AllowedUsers      []string `json:"allowedUsers,omitempty"`
	AllowedChats      []string `json:"allowedChats,omitempty"`
	ReadOnlyHosts     []string `json:"readOnlyHosts,omitempty"`  // 允许 curl /api/0/ 只读查询的域名白名单（默认空 = 不放行任何 curl）
	TrustedScripts    []string `json:"trustedScripts,omitempty"` // 主人显式信任的脚本绝对路径或目录（目录以 / 结尾）
}

// ProfileInfo 来自 `lark-cli profile list` 的已配置飞书应用概览。
type ProfileInfo struct {
	Name        string `json:"name"`
	AppID       string `json:"appId"`
	Brand       string `json:"brand"`
	Active      bool   `json:"active"`
	User        string `json:"user,omitempty"`
	TokenStatus string `json:"tokenStatus,omitempty"`
}

// BotStatus 报告单个机器人在常驻进程里的运行状态。
type BotStatus struct {
	ID            string `json:"id"`
	Name          string `json:"name"`
	Profile       string `json:"profile"`
	Enabled       bool   `json:"enabled"`
	Running       bool   `json:"running"`
	MsgReady      bool   `json:"msgReady"`
	CardReady     bool   `json:"cardReady"`
	LastError     string `json:"lastError,omitempty"`
	ActiveThreads int    `json:"activeThreads,omitempty"`
}

func ConfigPath(home string) string { return filepath.Join(home, "lark-bots.json") }

func LoadConfig(home string) (Config, error) {
	b, err := os.ReadFile(ConfigPath(home))
	if errors.Is(err, os.ErrNotExist) {
		return Config{}, nil
	}
	if err != nil {
		return Config{}, err
	}
	var cfg Config
	if err := json.Unmarshal(b, &cfg); err != nil {
		return Config{}, err
	}
	for i := range cfg.Bots {
		cfg.Bots[i] = NormalizeBot(cfg.Bots[i])
	}
	return cfg, nil
}

func SaveConfig(home string, cfg Config) error {
	for i := range cfg.Bots {
		cfg.Bots[i] = NormalizeBot(cfg.Bots[i])
	}
	b, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		return err
	}
	path := ConfigPath(home)
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, b, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

func NormalizeBot(b BotConfig) BotConfig {
	b.ID = strings.TrimSpace(b.ID)
	b.Name = strings.TrimSpace(b.Name)
	b.Profile = strings.TrimSpace(b.Profile)
	b.WorkspaceID = strings.TrimSpace(b.WorkspaceID)
	b.Agent = strings.TrimSpace(b.Agent)
	if b.Agent == "" {
		b.Agent = "antigravity"
	}
	if b.Name == "" {
		b.Name = b.Profile
	}
	if b.PermissionMode != PermSkip {
		b.PermissionMode = PermAsk
	}
	if b.SessionMode != SessionThread {
		b.SessionMode = SessionChat
	}
	switch b.TriggerPolicy {
	case TriggerAllowlist, TriggerGroup:
	default:
		b.TriggerPolicy = TriggerSelf
	}
	if b.GroupContextCount < 0 {
		b.GroupContextCount = 0
	} else if b.GroupContextCount > 50 {
		b.GroupContextCount = 50
	}
	b.AllowedUsers = cleanIDs(b.AllowedUsers)
	b.AllowedChats = cleanIDs(b.AllowedChats)
	b.ReadOnlyHosts = cleanHosts(b.ReadOnlyHosts)
	b.TrustedScripts = cleanScriptPaths(b.TrustedScripts)
	return b
}

func cleanHosts(in []string) []string {
	var out []string
	seen := map[string]bool{}
	for _, v := range in {
		v = strings.ToLower(strings.TrimSpace(v))
		if v == "" || seen[v] {
			continue
		}
		seen[v] = true
		out = append(out, v)
	}
	return out
}

func cleanScriptPaths(in []string) []string {
	var out []string
	seen := map[string]bool{}
	for _, v := range in {
		v = strings.TrimSpace(v)
		if v == "" || !filepath.IsAbs(v) {
			continue
		}
		isDir := strings.HasSuffix(v, "/")
		clean := filepath.Clean(v)
		if isDir && clean != "/" {
			clean += "/"
		}
		if seen[clean] {
			continue
		}
		seen[clean] = true
		out = append(out, clean)
	}
	return out
}

func cleanIDs(in []string) []string {
	var out []string
	seen := map[string]bool{}
	for _, v := range in {
		v = strings.TrimSpace(v)
		if v == "" || seen[v] {
			continue
		}
		seen[v] = true
		out = append(out, v)
	}
	return out
}

// ValidateBotPolicy 校验机器人级的 readOnlyHosts 与 trustedScripts 原始入参（全英文报错，拒绝相对路径、非法字符与临时目录重叠）。
func ValidateBotPolicy(readOnlyHosts, trustedScripts []string) error {
	for _, raw := range readOnlyHosts {
		if !isValidReadOnlyHostEntry(raw) {
			return fmt.Errorf("invalid read-only host %q", raw)
		}
	}
	for _, raw := range trustedScripts {
		if !isValidTrustedScriptEntry(raw) {
			return fmt.Errorf("invalid trusted script path %q", raw)
		}
	}
	return nil
}

func isValidReadOnlyHostEntry(raw string) bool {
	v := strings.TrimSpace(raw)
	if v == "" || strings.Contains(v, "://") || strings.ContainsAny(v, "/\\, \t\r\n") {
		return false
	}
	for i := 0; i < len(v); i++ {
		if v[i] < 0x20 || v[i] == 0x7f {
			return false
		}
	}
	host := v
	if strings.Contains(v, ":") {
		h, portStr, ok := strings.Cut(v, ":")
		if !ok || strings.Contains(portStr, ":") || portStr == "" || len(portStr) > 5 || portStr[0] == '0' {
			return false
		}
		for i := 0; i < len(portStr); i++ {
			if portStr[i] < '0' || portStr[i] > '9' {
				return false
			}
		}
		port, err := strconv.Atoi(portStr)
		if err != nil || port < 1 || port > 65535 {
			return false
		}
		host = h
	}
	return isValidPolicyHostname(host)
}

func isValidPolicyHostname(host string) bool {
	if host == "" || len(host) > 253 || strings.HasPrefix(host, ".") || strings.HasSuffix(host, ".") ||
		strings.HasPrefix(host, "-") || strings.HasSuffix(host, "-") || strings.Contains(host, "..") {
		return false
	}
	for _, label := range strings.Split(host, ".") {
		if label == "" || len(label) > 63 || strings.HasPrefix(label, "-") || strings.HasSuffix(label, "-") {
			return false
		}
		for i := 0; i < len(label); i++ {
			c := label[i]
			if !((c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') || (c >= '0' && c <= '9') || c == '-') {
				return false
			}
		}
	}
	return true
}

func isValidTrustedScriptEntry(raw string) bool {
	v := strings.TrimSpace(raw)
	if v == "" || !filepath.IsAbs(v) || strings.ContainsAny(v, ":$~") {
		return false
	}
	for i := 0; i < len(v); i++ {
		if v[i] <= 0x20 || v[i] == 0x7f {
			return false
		}
	}
	for _, seg := range strings.Split(filepath.ToSlash(v), "/") {
		if seg == ".." {
			return false
		}
	}
	isDir := strings.HasSuffix(v, "/")
	clean := filepath.Clean(v)
	if isForbiddenScriptLocation(clean, isDir) {
		return false
	}
	resolved, ok := evalPathWithMissingSuffix(clean)
	if !ok {
		return false
	}
	if isForbiddenScriptLocation(resolved, isDir) {
		return false
	}
	return true
}

func isForbiddenScriptLocation(clean string, isDir bool) bool {
	clean = filepath.Clean(clean)
	if clean == "/" || clean == "/tmp" || strings.HasPrefix(clean, "/tmp/") ||
		clean == "/private/tmp" || strings.HasPrefix(clean, "/private/tmp/") {
		return true
	}
	if isDir && clean == "/private" {
		return true
	}
	return false
}

func evalPathWithMissingSuffix(clean string) (string, bool) {
	cur := clean
	for {
		_, err := os.Lstat(cur)
		if err == nil {
			resolved, err := filepath.EvalSymlinks(cur)
			if err != nil {
				return "", false
			}
			rel, err := filepath.Rel(cur, clean)
			if err != nil {
				return "", false
			}
			if rel == "." {
				return filepath.Clean(resolved), true
			}
			return filepath.Clean(filepath.Join(resolved, rel)), true
		}
		if !os.IsNotExist(err) {
			return "", false
		}
		parent := filepath.Dir(cur)
		if parent == cur {
			return "", false
		}
		cur = parent
	}
}

func (b BotConfig) AllowsTrigger(senderOpenID, ownerOpenID, chatID string) bool {
	if senderOpenID == "" {
		return false
	}
	if len(b.AllowedChats) > 0 && chatID != "" {
		ok := false
		for _, c := range b.AllowedChats {
			if c == chatID {
				ok = true
				break
			}
		}
		if !ok {
			return false
		}
	}
	if ownerOpenID != "" && senderOpenID == ownerOpenID {
		return true
	}
	switch b.TriggerPolicy {
	case TriggerGroup:
		return true
	case TriggerAllowlist:
		for _, u := range b.AllowedUsers {
			if u == senderOpenID {
				return true
			}
		}
		return false
	default:
		// 未配置 ownerOpenID 时安全拒绝，防止误放行
		return false
	}
}
