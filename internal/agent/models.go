package agent

import (
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"strings"
)

// ModelOption 描述某家 agent 的一个可选模型及其支持的思考深度档位。
type ModelOption struct {
	ID            string   `json:"id"`
	Label         string   `json:"label"`
	Efforts       []string `json:"efforts,omitempty"`
	DefaultEffort string   `json:"defaultEffort,omitempty"`
	Default       bool     `json:"default,omitempty"`
}

// ModelList 是本机发现的某家 agent 可用模型与兜底思考深度列表。
type ModelList struct {
	Agent   string        `json:"agent"`
	Models  []ModelOption `json:"models,omitempty"`
	Efforts []string      `json:"efforts,omitempty"`
	Source  string        `json:"source,omitempty"` // "cli" | "cache" | "static"
	Error   string        `json:"error,omitempty"`
}

var (
	effortOrder    = []string{"low", "medium", "high", "xhigh", "max", "ultra"}
	agyEfforts     = []string{"low", "medium", "high", "xhigh", "max"}
	claudeEfforts  = []string{"low", "medium", "high", "xhigh", "max"}
	codexEfforts   = []string{"low", "medium", "high", "xhigh", "max", "ultra"}
	grokEfforts    = []string{"low", "medium", "high", "xhigh"}
	agyEffortLabel = map[string]string{
		"low":    "(low)",
		"medium": "(medium)",
		"high":   "(high)",
		"xhigh":  "(xhigh)",
		"max":    "(max)",
	}
)

// NormalizeName 统一规范化 agent 标识（去空白、小写，agy 映射为 antigravity）。
func NormalizeName(name string) string {
	n := strings.ToLower(strings.TrimSpace(name))
	if n == "agy" {
		return "antigravity"
	}
	return n
}

// DefaultEfforts 返回某家 agent 在使用自定义模型或未指定模型时可选的思考深度列表。
// cursor 与 zcode 不提供独立思考深度，返回 nil。
func DefaultEfforts(agentName string) []string {
	switch NormalizeName(agentName) {
	case "antigravity":
		return slices.Clone(agyEfforts)
	case "claude":
		return slices.Clone(claudeEfforts)
	case "codex":
		return slices.Clone(codexEfforts)
	case "grok":
		return slices.Clone(grokEfforts)
	default:
		return nil
	}
}

// SplitAntigravityModelEffort 将 agy 带档位后缀的模型 ID（如 gemini-4-neon-high）
// 拆为基础模型 ID 与档位；不带已知档位后缀时返回原值与 false。
func SplitAntigravityModelEffort(id string) (base, effort string, ok bool) {
	id = strings.TrimSpace(id)
	if id == "" {
		return "", "", false
	}
	low := strings.ToLower(id)
	for _, ef := range agyEfforts {
		suffix := "-" + ef
		if strings.HasSuffix(low, suffix) && len(id) > len(suffix) {
			return id[:len(id)-len(suffix)], ef, true
		}
	}
	return id, "", false
}

// ParseAntigravityModels 解析 `agy models` 输出并按基础模型 ID 聚合档位。
// 仅当 ID 本身带档位后缀时才从显示名末尾去掉对应的 `(Low)` 等括号，保留 `Orchestrator (Neon)` 等非档位括号。
func ParseAntigravityModels(out string) []ModelOption {
	var order []string
	byBase := map[string]*ModelOption{}
	for _, rawLine := range strings.Split(out, "\n") {
		line := strings.TrimRight(rawLine, "\r")
		if strings.TrimSpace(line) == "" {
			continue
		}
		id, label, ok := strings.Cut(line, "\t")
		if !ok {
			continue
		}
		id = strings.TrimSpace(id)
		label = strings.TrimSpace(label)
		if id == "" {
			continue
		}
		base, ef, hasEffort := SplitAntigravityModelEffort(id)
		if hasEffort {
			label = stripTrailingEffortLabel(label, ef)
		}
		if label == "" {
			label = base
		}
		opt, exists := byBase[base]
		if !exists {
			opt = &ModelOption{ID: base, Label: label}
			byBase[base] = opt
			order = append(order, base)
		} else if opt.Label == "" && label != "" {
			opt.Label = label
		}
		if hasEffort && !slices.Contains(opt.Efforts, ef) {
			opt.Efforts = append(opt.Efforts, ef)
		}
	}
	models := make([]ModelOption, 0, len(order))
	for _, base := range order {
		opt := byBase[base]
		if len(opt.Efforts) > 1 {
			sortEfforts(opt.Efforts)
		}
		models = append(models, *opt)
	}
	return models
}

func stripTrailingEffortLabel(label, effort string) string {
	want := agyEffortLabel[effort]
	if want == "" {
		return label
	}
	trimmed := strings.TrimSpace(label)
	if len(trimmed) > len(want) && strings.EqualFold(trimmed[len(trimmed)-len(want):], want) {
		return strings.TrimSpace(trimmed[:len(trimmed)-len(want)])
	}
	return label
}

func sortEfforts(efforts []string) {
	slices.SortFunc(efforts, func(a, b string) int {
		ia := slices.Index(effortOrder, a)
		ib := slices.Index(effortOrder, b)
		if ia == -1 {
			ia = len(effortOrder)
		}
		if ib == -1 {
			ib = len(effortOrder)
		}
		if ia != ib {
			return ia - ib
		}
		return strings.Compare(a, b)
	})
}

// ParseCodexModelsCache 解析 `~/.codex/models_cache.json`，仅保留 visibility=="list" 的模型。
func ParseCodexModelsCache(data []byte) ([]ModelOption, error) {
	var doc struct {
		Models []struct {
			Slug                     string `json:"slug"`
			DisplayName              string `json:"display_name"`
			DefaultReasoningLevel    string `json:"default_reasoning_level"`
			Visibility               string `json:"visibility"`
			SupportedReasoningLevels []struct {
				Effort string `json:"effort"`
			} `json:"supported_reasoning_levels"`
		} `json:"models"`
	}
	if err := json.Unmarshal(data, &doc); err != nil {
		return nil, err
	}
	out := make([]ModelOption, 0, len(doc.Models))
	for _, m := range doc.Models {
		if strings.TrimSpace(m.Visibility) != "list" {
			continue
		}
		slug := strings.TrimSpace(m.Slug)
		if slug == "" {
			continue
		}
		label := strings.TrimSpace(m.DisplayName)
		if label == "" {
			label = slug
		}
		var efforts []string
		for _, lv := range m.SupportedReasoningLevels {
			ef := strings.ToLower(strings.TrimSpace(lv.Effort))
			if ef != "" && !slices.Contains(efforts, ef) {
				efforts = append(efforts, ef)
			}
		}
		out = append(out, ModelOption{
			ID:            slug,
			Label:         label,
			Efforts:       efforts,
			DefaultEffort: strings.ToLower(strings.TrimSpace(m.DefaultReasoningLevel)),
		})
	}
	return out, nil
}

// CodexModels 从本机 `$CODEX_HOME/models_cache.json`（默认 `~/.codex`）读取 Codex 可用模型列表。
func CodexModels(homeDir string) ModelList {
	codexDir := strings.TrimSpace(os.Getenv("CODEX_HOME"))
	if codexDir == "" && homeDir != "" {
		codexDir = filepath.Join(homeDir, ".codex")
	}
	list := ModelList{
		Agent:   "codex",
		Efforts: DefaultEfforts("codex"),
		Source:  "cache",
	}
	if codexDir == "" {
		return list
	}
	data, err := os.ReadFile(filepath.Join(codexDir, "models_cache.json"))
	if err != nil {
		if !os.IsNotExist(err) {
			list.Error = err.Error()
		}
		return list
	}
	models, err := ParseCodexModelsCache(data)
	if err != nil {
		list.Error = err.Error()
		return list
	}
	list.Models = models
	return list
}

// ClaudeModels 返回 Claude 官方稳定别名，并结合 `~/.claude/settings.json` 中的当前默认模型标注 Default。
func ClaudeModels(homeDir string) ModelList {
	presets := []ModelOption{
		{ID: "opus", Label: "Opus", Efforts: slices.Clone(claudeEfforts)},
		{ID: "sonnet", Label: "Sonnet", Efforts: slices.Clone(claudeEfforts)},
		{ID: "haiku", Label: "Haiku", Efforts: slices.Clone(claudeEfforts)},
		{ID: "fable", Label: "Fable", Efforts: slices.Clone(claudeEfforts)},
	}
	claudeDir := strings.TrimSpace(os.Getenv("CLAUDE_CONFIG_DIR"))
	if claudeDir == "" && homeDir != "" {
		claudeDir = filepath.Join(homeDir, ".claude")
	}
	if claudeDir != "" {
		if raw, err := os.ReadFile(filepath.Join(claudeDir, "settings.json")); err == nil {
			var cfg struct {
				Model       string `json:"model"`
				EffortLevel string `json:"effortLevel"`
			}
			if json.Unmarshal(raw, &cfg) == nil {
				defModel := strings.TrimSpace(cfg.Model)
				defEffort := strings.ToLower(strings.TrimSpace(cfg.EffortLevel))
				if defModel != "" {
					matched := false
					for i := range presets {
						if strings.EqualFold(presets[i].ID, defModel) {
							presets[i].Default = true
							if defEffort != "" {
								presets[i].DefaultEffort = defEffort
							}
							matched = true
							break
						}
					}
					if !matched {
						presets = append([]ModelOption{{
							ID:            defModel,
							Label:         defModel,
							Efforts:       slices.Clone(claudeEfforts),
							DefaultEffort: defEffort,
							Default:       true,
						}}, presets...)
					}
				}
			}
		}
	}
	return ModelList{
		Agent:   "claude",
		Models:  presets,
		Efforts: DefaultEfforts("claude"),
		Source:  "static",
	}
}

// ParseGrokModels 解析 `grok models` 输出，仅提取 `Available models:` 下方的缩进行。
func ParseGrokModels(out string) []ModelOption {
	inList := false
	var outModels []ModelOption
	seen := map[string]bool{}
	for _, rawLine := range strings.Split(out, "\n") {
		trimmed := strings.TrimSpace(rawLine)
		if strings.EqualFold(trimmed, "Available models:") {
			inList = true
			continue
		}
		if !inList {
			continue
		}
		if trimmed == "" {
			if len(outModels) > 0 {
				break
			}
			continue
		}
		if !strings.HasPrefix(rawLine, " ") && !strings.HasPrefix(rawLine, "\t") {
			break
		}
		item := strings.TrimSpace(strings.TrimPrefix(trimmed, "*"))
		isDef := false
		if strings.HasSuffix(item, "(default)") {
			isDef = true
			item = strings.TrimSpace(strings.TrimSuffix(item, "(default)"))
		}
		if item == "" || seen[item] {
			continue
		}
		seen[item] = true
		outModels = append(outModels, ModelOption{
			ID:      item,
			Label:   item,
			Efforts: slices.Clone(grokEfforts),
			Default: isDef,
		})
	}
	return outModels
}

// ParseCursorModels 解析 `cursor-agent models` 输出为平铺列表，清理零宽字符并跳过标题行。
func ParseCursorModels(out string) []ModelOption {
	var models []ModelOption
	seen := map[string]bool{}
	for _, rawLine := range strings.Split(out, "\n") {
		line := cleanZeroWidth(strings.TrimSpace(rawLine))
		if line == "" || strings.EqualFold(line, "Available models") {
			continue
		}
		id, label, ok := strings.Cut(line, " - ")
		if !ok {
			continue
		}
		id = strings.TrimSpace(id)
		label = strings.TrimSpace(label)
		if id == "" || seen[id] {
			continue
		}
		seen[id] = true
		isDef := strings.HasSuffix(strings.ToLower(label), "(default)")
		models = append(models, ModelOption{
			ID:      id,
			Label:   label,
			Default: isDef,
		})
	}
	return models
}

func cleanZeroWidth(s string) string {
	return strings.NewReplacer("\u200b", "", "\u200c", "", "\u200d", "", "\ufeff", "").Replace(s)
}
