package skills

import (
	"encoding/json"
	"os"
	"os/user"
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

type antigravityLocation struct {
	base, repo, relativeTo string
	project                bool
}

type antigravityExclude struct {
	file  string
	names []string
}

func antigravityExclusions(excluded []antigravityExclude, base, path string) []antigravityExclude {
	var matches []antigravityExclude
	for _, ex := range excluded {
		if antigravityMatches(ex.names, base, path) {
			matches = append(matches, ex)
		}
	}
	return matches
}

// 额外工作区与主目录同样向上找；同一个仓库的个人目录只取一次。
func antigravityLocations(home, dir string, extra []string) []antigravityLocation {
	var personal, project []antigravityLocation
	seen, repos := map[string]bool{}, map[string]bool{}
	for _, dir := range append([]string{dir}, extra...) {
		if !filepath.IsAbs(dir) {
			continue
		}
		dirs := upToRepo(dir)
		repo := dirs[len(dirs)-1]
		if key := realDir(repo); !repos[key] {
			repos[key] = true
			var cfg struct {
				Personal string `json:"personal_customization_dir"`
			}
			if readAntigravityJSON(filepath.Join(repo, ".gemini", "config.json"), &cfg) && cfg.Personal != "" {
				name := os.Getenv("USER")
				if name == "" {
					if u, err := user.Current(); err == nil {
						name = u.Username
					}
				}
				path := strings.ReplaceAll(cfg.Personal, "{user}", name)
				path = filepath.Clean(path)
				if !filepath.IsAbs(path) && !strings.HasPrefix(path, "~") && path != ".." && !strings.HasPrefix(path, ".."+string(filepath.Separator)) {
					base := filepath.Join(repo, path, ".agents")
					personal = append(personal, antigravityLocation{base: base, repo: repo, relativeTo: repo, project: true})
				}
			}
		}
		for _, dir := range dirs {
			for _, folder := range antigravityFolders {
				base := filepath.Join(dir, folder)
				if key := realDir(base); !seen[key] {
					seen[key] = true
					project = append(project, antigravityLocation{base: base, repo: repo, project: true})
				}
			}
		}
	}
	return append(append(personal, project...), antigravityLocation{base: filepath.Join(home, ".gemini", "config")})
}

func antigravityRoots(o Options) []root {
	out := []root{
		{dir: filepath.Join(o.Home, ".gemini", "antigravity-cli", "skills"), scope: ScopeUser, prio: prios(-1, -1, -1, -1, 0)},
		{dir: filepath.Join(o.Home, ".gemini", "skills"), scope: ScopeUser, prio: prios(-1, -1, -1, -1, 1)},
	}
	dir := o.AntigravityDir
	if dir == "" {
		dir = o.Dir
	}
	locations := antigravityLocations(o.Home, dir, o.AntigravityDirs)
	var excluded []antigravityExclude
	for i, loc := range locations {
		scope := ScopeUser
		if loc.project {
			scope = ScopeProject
		}
		prio := prios(-1, -1, -1, -1, i+2)
		base := filepath.Join(loc.base, "skills")
		direct := root{dir: base, scope: scope, prio: prio}
		if !loc.project || filepath.Base(loc.base) == ".agents" {
			file := filepath.Join(loc.base, "skills.json")
			direct.agySwitch = func(path string) (bool, string) {
				matches := antigravityExclusions(excluded, base, path)
				for _, ex := range matches {
					if realDir(ex.file) != realDir(file) {
						return true, ""
					}
				}
				return len(matches) > 0, file
			}
		}
		out = append(out, direct)
		excluded = append(excluded, loc.manifest("skills", antigravityCandidates, func(base string, paths []string, allow func(string) bool) {
			out = append(out, root{dir: base, paths: paths, scope: scope, prio: prio, allow: allow})
		})...)
	}
	prio := prios(-1, -1, -1, -1, len(locations)+2)
	out = append(out, root{dir: filepath.Join(o.Home, ".gemini", "antigravity-cli", "builtin", "skills"), scope: ScopeSystem, prio: prio})
	// CLI 专属目录不受 manifest exclude 影响，其余位置共享顶层排除表。
	for i := 2; i < len(out); i++ {
		if out[i].agySwitch != nil {
			continue
		}
		base, allow := out[i].dir, out[i].allow
		out[i].allow = func(path string) bool {
			return len(antigravityExclusions(excluded, base, path)) == 0 && (allow == nil || allow(path))
		}
	}
	for _, p := range antigravityPlugins(locations) {
		scope := ScopePlugin
		if p.project {
			scope = ScopeProject
		}
		out = append(out, root{dir: filepath.Join(p.dir, "skills"), scope: scope, prio: prio, prefix: p.name + ":", off: !p.enabled})
	}
	return out
}

func readAntigravityJSON(path string, out any) bool {
	b, err := os.ReadFile(path)
	return err == nil && json.Unmarshal(b, out) == nil
}

func (loc antigravityLocation) manifest(kind string, candidates func(string, []antigravityEntry) ([]string, string), add func(string, []string, func(string) bool)) []antigravityExclude {
	var excluded []antigravityExclude
	active := map[string]bool{}
	visits := 0
	var visit func(string, []antigravityEntry, int)
	visit = func(file string, filters []antigravityEntry, depth int) {
		key := realDir(file)
		if active[key] || depth > 32 || visits >= 1024 {
			return
		}
		visits++
		active[key] = true
		defer delete(active, key)
		var config struct {
			Entries  []antigravityEntry `json:"entries"`
			Inherits []antigravityEntry `json:"inherits"`
			Exclude  []string           `json:"exclude"`
		}
		if !readAntigravityJSON(file, &config) {
			return
		}
		if len(config.Exclude) > 0 {
			excluded = append(excluded, antigravityExclude{file: file, names: config.Exclude})
		}
		resolve := func(path string, inherit bool) string {
			if path == "" || strings.HasPrefix(path, "~") {
				return ""
			}
			if filepath.IsAbs(path) {
				return filepath.Clean(path)
			}
			base := filepath.Dir(file)
			switch {
			case depth == 0 && loc.relativeTo != "":
				base = loc.relativeTo
			case slices.Contains(antigravityFolders, filepath.Base(base)):
				base = filepath.Dir(base)
			case !loc.project:
				if inherit {
					return ""
				}
				base = loc.base
			}
			local := filepath.Join(base, path)
			if _, err := os.Stat(local); err == nil || loc.repo == "" {
				return local
			}
			return filepath.Join(loc.repo, path)
		}
		for _, e := range config.Inherits {
			if path := resolve(e.Path, true); path != "" {
				visit(path, append(slices.Clone(filters), e), depth+1)
			}
		}
		for _, e := range config.Entries {
			path := resolve(e.Path, false)
			if path == "" {
				continue
			}
			fs := append(slices.Clone(filters), e)
			paths, base := candidates(path, fs)
			add(base, paths, func(path string) bool {
				for _, f := range fs {
					if len(f.Include) > 0 && !antigravityMatches(f.Include, base, path) || antigravityMatches(f.Exclude, base, path) {
						return false
					}
				}
				return true
			})
		}
	}
	visit(filepath.Join(loc.base, kind+".json"), nil, 0)
	return excluded
}

func antigravityCandidates(path string, filters []antigravityEntry) ([]string, string) {
	if filepath.Base(path) == "SKILL.md" {
		path = filepath.Dir(path)
	}
	return antigravityManifestCandidates(path, filters, func(path string) bool {
		_, err := os.Stat(filepath.Join(path, "SKILL.md"))
		return err == nil
	})
}

func antigravityManifestCandidates(path string, filters []antigravityEntry, single func(string) bool) ([]string, string) {
	if single(path) {
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
