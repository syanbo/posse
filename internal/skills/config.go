package skills

import (
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/BurntSushi/toml"
)

// configs 是各家和技能开关有关的配置，扫描时读一次。
type configs struct {
	// claude 是 skillOverrides：用户的 settings.json，工作区的 settings.json、settings.local.json 依次盖上去。
	claude map[string]string
	codex  []codexEntry
	grok   grokConfig
	lock   map[string]string
}

// codexEntry 是 Codex config.toml 里的一条 [[skills.config]]：按名字或路径选中技能。
type codexEntry struct {
	Name    string `toml:"name"`
	Path    string `toml:"path"`
	Enabled *bool  `toml:"enabled"`
}

type grokConfig struct {
	paths, ignore, disabled    []string
	claudeSkills, cursorSkills bool
}

func readConfigs(home, dir string) configs {
	c := configs{claude: map[string]string{}, lock: readLock(home)}
	files := []string{filepath.Join(home, ".claude", "settings.json")}
	if dir != "" {
		files = append(files, filepath.Join(dir, ".claude", "settings.json"), filepath.Join(dir, ".claude", "settings.local.json"))
	}
	for _, f := range files {
		for k, v := range claudeOverrides(f) {
			c.claude[k] = v
		}
	}
	var codex struct {
		Skills struct {
			Config []codexEntry `toml:"config"`
		} `toml:"skills"`
	}
	if _, err := toml.DecodeFile(filepath.Join(codexHome(home), "config.toml"), &codex); err == nil {
		c.codex = codex.Skills.Config
	}
	c.grok = readGrok(home)
	return c
}

// codexHome 是 Codex 的配置目录，照它的规则认 CODEX_HOME。
func codexHome(home string) string {
	if h := os.Getenv("CODEX_HOME"); h != "" {
		return h
	}
	return filepath.Join(home, ".codex")
}

func claudeOverrides(file string) map[string]string {
	b, err := os.ReadFile(file)
	if err != nil {
		return nil
	}
	var s struct {
		SkillOverrides map[string]string `json:"skillOverrides"`
	}
	if json.Unmarshal(b, &s) != nil {
		return nil
	}
	return s.SkillOverrides
}

func readGrok(home string) grokConfig {
	g := grokConfig{claudeSkills: true, cursorSkills: true}
	var f struct {
		Skills struct {
			Paths    []string `toml:"paths"`
			Ignore   []string `toml:"ignore"`
			Disabled []string `toml:"disabled"`
		} `toml:"skills"`
		Compat struct {
			Claude struct {
				Skills *bool `toml:"skills"`
			} `toml:"claude"`
			Cursor struct {
				Skills *bool `toml:"skills"`
			} `toml:"cursor"`
		} `toml:"compat"`
	}
	if _, err := toml.DecodeFile(filepath.Join(home, ".grok", "config.toml"), &f); err == nil {
		g.paths = expandAll(home, f.Skills.Paths)
		g.ignore = expandAll(home, f.Skills.Ignore)
		g.disabled = f.Skills.Disabled
		if p := f.Compat.Claude.Skills; p != nil {
			g.claudeSkills = *p
		}
		if p := f.Compat.Cursor.Skills; p != nil {
			g.cursorSkills = *p
		}
	}
	// 环境变量优先于配置文件（Grok 的文档）
	if v := os.Getenv("GROK_CLAUDE_SKILLS_ENABLED"); v != "" {
		g.claudeSkills = v != "false"
	}
	if v := os.Getenv("GROK_CURSOR_SKILLS_ENABLED"); v != "" {
		g.cursorSkills = v != "false"
	}
	return g
}

func expandAll(home string, ps []string) []string {
	out := make([]string, 0, len(ps))
	for _, p := range ps {
		if rest, ok := strings.CutPrefix(p, "~/"); ok {
			p = filepath.Join(home, rest)
		} else if p == "~" {
			p = home
		}
		out = append(out, filepath.Clean(p))
	}
	return out
}

// hidden 报告 a 是不是被配置藏掉了 path 这一项（Grok 的 [skills] ignore：按放链接的目录比，不按链接指向的）。
func (c configs) hidden(a Agent, path string) bool {
	if a != Grok {
		return false
	}
	for _, ig := range c.grok.ignore {
		if path == ig || strings.HasPrefix(path, ig+string(filepath.Separator)) {
			return true
		}
	}
	return false
}

// state 是 a 读得到、同名里生效的 s 的状态。
func (c configs) state(a Agent, s *Skill) (State, string) {
	switch a {
	case Claude:
		switch c.claude[s.Name] {
		case "off":
			return Off, "settings.json 的 skillOverrides 设为 off"
		case "user-invocable-only":
			return Manual, "settings.json 的 skillOverrides 设为 user-invocable-only"
		case "name-only":
			return On, "settings.json 的 skillOverrides 设为 name-only：只列名字，不带说明"
		}
		if s.Meta.ModelOff {
			return Manual, "SKILL.md 设了 disable-model-invocation"
		}
	case Codex:
		// 后写的盖过先写的
		for i := len(c.codex) - 1; i >= 0; i-- {
			e := c.codex[i]
			if e.Enabled == nil || !(e.Name == s.Name || e.Path != "" && (realDir(e.Path) == s.File || realDir(e.Path) == s.Dir)) {
				continue
			}
			if !*e.Enabled {
				return Off, "config.toml 的 [[skills.config]] 设为 enabled = false"
			}
			break
		}
	case Grok:
		if slices.Contains(c.grok.disabled, s.Name) {
			return Off, "config.toml 的 [skills] disabled 里有它"
		}
		if s.Meta.ModelOff {
			return Manual, "SKILL.md 设了 disable-model-invocation"
		}
	case Cursor:
		if s.Meta.ModelOff {
			return Manual, "SKILL.md 设了 disable-model-invocation"
		}
	}
	return On, ""
}
