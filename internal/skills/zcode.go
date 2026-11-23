package skills

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"slices"

	"posse/internal/agent"
)

type zcodeOverride struct {
	Enable *bool `json:"enable"`
}

type zcodeConfig struct {
	roots     []string
	overrides map[string]zcodeOverride
	disabled  bool
}

func zcodeUserConfig(home string) string {
	return filepath.Join(agent.ZCodeStorageRoot(home), "cli", "config.json")
}

func zcodePriority(prio [NumAgents]int, value int) [NumAgents]int {
	prio[ZCode] = value
	return prio
}

func readZCodeConfig(home, dir string) zcodeConfig {
	file := zcodeUserConfig(home)
	paths := []string{file}
	if dir != "" {
		for _, d := range slices.Backward(upToRepo(dir)) {
			paths = append(paths, filepath.Join(d, "zcode.json"), filepath.Join(d, ".zcode", "config.json"))
		}
	}
	c := zcodeConfig{overrides: map[string]zcodeOverride{}}
	skillsEnabled, featureEnabled := true, true
	for _, path := range paths {
		var f struct {
			Skills   map[string]json.RawMessage `json:"skills"`
			Skill    map[string]zcodeOverride   `json:"skill"`
			Features struct {
				Skill *bool `json:"skill"`
			} `json:"features"`
		}
		data, err := os.ReadFile(path)
		if err != nil || json.Unmarshal(data, &f) != nil {
			continue
		}
		if f.Features.Skill != nil {
			featureEnabled = *f.Features.Skill
		}
		_ = json.Unmarshal(f.Skills["enabled"], &skillsEnabled)
		if raw := f.Skills["roots"]; len(raw) > 0 {
			var paths []string
			if json.Unmarshal(raw, &paths) == nil {
				c.roots = expandAll(home, paths)
			}
		}
		for p, e := range f.Skill {
			if filepath.IsAbs(p) {
				c.overrides[filepath.Clean(p)] = e
			}
		}
		for p, raw := range f.Skills {
			var e *zcodeOverride
			if filepath.IsAbs(p) && json.Unmarshal(raw, &e) == nil && e != nil {
				c.overrides[filepath.Clean(p)] = *e
			}
		}
	}
	c.disabled = !skillsEnabled || !featureEnabled
	c.roots = slices.DeleteFunc(c.roots, func(path string) bool { return dir == "" && !filepath.IsAbs(path) })
	for i, path := range c.roots {
		if !filepath.IsAbs(path) {
			c.roots[i] = filepath.Join(dir, path)
		}
	}
	return c
}

func (c zcodeConfig) state(file, via string) (State, string, bool) {
	if c.disabled {
		return Off, "skills are disabled in ZCode config.json", false
	}
	for p, e := range c.overrides {
		if e.Enable != nil && !*e.Enable && (p == via || realDir(p) == realDir(file)) {
			return Off, "set to enable = false in skills of ZCode config.json", true
		}
	}
	return On, "", true
}

func setZCodeSkill(src string, cfg zcodeConfig, s Skill, enable bool) (string, error) {
	file := realDir(s.File)
	paths := []string{file}
	if enable {
		for p, e := range cfg.overrides {
			if p != file && e.Enable != nil && !*e.Enable && realDir(p) == file {
				paths = append(paths, p)
			}
		}
		slices.Sort(paths)
	}
	value := json.RawMessage(fmt.Sprintf(`{"enable": %t}`, enable))
	var err error
	for _, path := range paths {
		src, err = setJSONObjectEntry(src, "config.json", "skills", path, value)
		if err != nil {
			return "", err
		}
	}
	return src, nil
}
