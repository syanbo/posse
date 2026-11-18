package skills

import (
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"strings"
)

type antigravityEntry struct {
	Path    string   `json:"path"`
	Include []string `json:"include_only"`
	Exclude []string `json:"exclude"`
}

var antigravityFolders = []string{".agents", ".agent", "_agents", "_agent"}

func antigravityRoots(o Options) []root {
	global := filepath.Join(o.Home, ".gemini", "config")
	out := []root{
		{dir: filepath.Join(o.Home, ".gemini", "antigravity-cli", "skills"), scope: ScopeUser, prio: prios(-1, -1, -1, -1, 0)},
		{dir: filepath.Join(o.Home, ".gemini", "skills"), scope: ScopeUser, prio: prios(-1, -1, -1, -1, 1)},
		{dir: filepath.Join(global, "skills"), scope: ScopeUser, prio: prios(-1, -1, -1, -1, 10000)},
		{dir: filepath.Join(o.Home, ".gemini", "antigravity-cli", "builtin", "skills"), scope: ScopeSystem, prio: prios(-1, -1, -1, -1, 20000)},
	}
	var repo string
	if o.Dir != "" {
		dirs := upToRepo(o.Dir)
		repo = dirs[len(dirs)-1]
	}
	var excluded []string
	active := map[string]bool{}
	var visit func(string, Scope, int, []antigravityEntry, int)
	visit = func(file string, scope Scope, prio int, filters []antigravityEntry, depth int) {
		key := realDir(file)
		if active[key] || depth > 32 || len(out) > 1024 {
			return
		}
		active[key] = true
		defer delete(active, key)
		b, err := os.ReadFile(file)
		if err != nil {
			return
		}
		var config struct {
			Entries  []antigravityEntry `json:"entries"`
			Inherits []antigravityEntry `json:"inherits"`
			Exclude  []string           `json:"exclude"`
		}
		if json.Unmarshal(b, &config) != nil {
			return
		}
		excluded = append(excluded, config.Exclude...)
		resolve := func(path string, inherit bool) string {
			if path == "" || strings.HasPrefix(path, "~") {
				return ""
			}
			if filepath.IsAbs(path) {
				return filepath.Clean(path)
			}
			base := filepath.Dir(file)
			projectManifest := slices.Contains(antigravityFolders, filepath.Base(base))
			if projectManifest {
				base = filepath.Dir(base)
			} else if scope == ScopeUser {
				if inherit {
					return ""
				}
				base = global
			}
			local := filepath.Join(base, path)
			if _, err := os.Stat(local); err == nil || repo == "" || scope == ScopeUser {
				return local
			}
			return filepath.Join(repo, path)
		}
		for _, e := range config.Inherits {
			if path := resolve(e.Path, true); path != "" {
				visit(path, scope, prio, append(append([]antigravityEntry{}, filters...), e), depth+1)
			}
		}
		for _, e := range config.Entries {
			path := resolve(e.Path, false)
			if path == "" {
				continue
			}
			fs := append(append([]antigravityEntry{}, filters...), e)
			paths, base := antigravityCandidates(path, fs)
			out = append(out, root{dir: base, paths: paths, scope: scope, prio: prios(-1, -1, -1, -1, prio), allow: func(path string) bool {
				for _, f := range fs {
					if len(f.Include) > 0 && !antigravityMatches(f.Include, base, path) || antigravityMatches(f.Exclude, base, path) {
						return false
					}
				}
				return true
			}})
		}
	}
	visit(filepath.Join(global, "skills.json"), ScopeUser, 10001, nil, 0)
	if o.Dir != "" {
		for i, dir := range upToRepo(o.Dir) {
			for _, folder := range antigravityFolders {
				base := filepath.Join(dir, folder)
				out = append(out, root{dir: filepath.Join(base, "skills"), scope: ScopeProject, prio: prios(-1, -1, -1, -1, i+10)})
				visit(filepath.Join(base, "skills.json"), ScopeProject, i+10, nil, 0)
			}
		}
	}
	// CLI 专属目录不受 manifest exclude 影响，其余位置共享顶层排除表。
	for i := 2; i < len(out); i++ {
		base, allow := out[i].dir, out[i].allow
		out[i].allow = func(path string) bool {
			return !antigravityMatches(excluded, base, path) && (allow == nil || allow(path))
		}
	}
	return out
}

func antigravityCandidates(path string, filters []antigravityEntry) ([]string, string) {
	if filepath.Base(path) == "SKILL.md" {
		path = filepath.Dir(path)
	}
	if _, err := os.Stat(filepath.Join(path, "SKILL.md")); err == nil {
		return []string{path}, filepath.Dir(path)
	}
	paths := candidates(root{dir: path})
	if paths == nil {
		paths = []string{}
	}
	for _, f := range filters {
		for _, name := range f.Include {
			if filepath.IsAbs(name) {
				continue
			}
			p := filepath.Join(path, name)
			rel, err := filepath.Rel(path, p)
			if err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)) && !slices.Contains(paths, p) {
				paths = append(paths, p)
			}
		}
	}
	return paths, path
}

func antigravityMatches(patterns []string, base, path string) bool {
	rel, _ := filepath.Rel(base, path)
	return slices.Contains(patterns, filepath.Base(path)) || slices.Contains(patterns, filepath.ToSlash(rel))
}

// LinkPosseAntigravity 只链接已安装的托管技能；用户自己的同名目录或链接保留。
func LinkPosseAntigravity(home string, names []string) error {
	for _, name := range names {
		if !contains(PosseNames(), name) {
			continue
		}
		dir := filepath.Join(home, ".agents", "skills", name)
		b, err := os.ReadFile(filepath.Join(dir, "SKILL.md"))
		if err != nil || !isPosseFile(string(b)) {
			continue
		}
		at := filepath.Join(home, ".gemini", "config", "skills", name)
		if _, err := os.Lstat(at); err == nil {
			continue
		} else if !os.IsNotExist(err) {
			return err
		}
		if err := MakeLink(at, dir); err != nil {
			return err
		}
	}
	return nil
}
