package skills

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
)

var antigravityPluginFiles = []string{"plugin.json", ".antigravity-plugin/plugin.json", ".gemini-plugin/plugin.json"}

type antigravityPlugin struct {
	name, dir string
	project   bool
	enabled   bool
}

func antigravityPlugins(locs []antigravityLocation) []antigravityPlugin {
	var config struct {
		Plugins map[string]struct {
			Enabled *bool `json:"enabled"`
		} `json:"plugins"`
	}
	b, err := os.ReadFile(filepath.Join(locs[len(locs)-1].base, "config.json"))
	if err == nil {
		if json.Unmarshal(antigravityJSONC(b), &config) != nil {
			return nil
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return nil
	}
	type candidatesAt struct {
		base    string
		paths   []string
		allow   func(string) bool
		project bool
	}
	var sets []candidatesAt
	var excluded []antigravityExclude
	for _, loc := range locs {
		base := filepath.Join(loc.base, "plugins")
		sets = append(sets, candidatesAt{base: base, paths: candidates(root{dir: base}), project: loc.project})
		excluded = append(excluded, loc.manifest("plugins", func(path string, filters []antigravityEntry) ([]string, string) {
			return antigravityManifestCandidates(path, filters, hasAntigravityPluginManifest)
		}, func(base string, paths []string, allow func(string) bool) {
			sets = append(sets, candidatesAt{base: base, paths: paths, allow: allow, project: loc.project})
		})...)
	}
	var out []antigravityPlugin
	seen := map[string]bool{}
	for _, set := range sets {
		for _, dir := range set.paths {
			name := filepath.Base(dir)
			if seen[name] || len(antigravityExclusions(excluded, set.base, dir)) != 0 || set.allow != nil && !set.allow(dir) {
				continue
			}
			var manifest struct {
				Disabled bool `json:"disabled"`
			}
			file := antigravityPluginManifest(dir)
			b, err := os.ReadFile(file)
			if err != nil || json.Unmarshal(antigravityJSONC(b), &manifest) != nil {
				continue
			}
			enabled := !manifest.Disabled
			if choice := config.Plugins[name].Enabled; choice != nil {
				enabled = *choice
			}
			seen[name] = true
			out = append(out, antigravityPlugin{name: name, dir: dir, enabled: enabled, project: set.project})
		}
	}
	return out
}

func hasAntigravityPluginManifest(dir string) bool { return antigravityPluginManifest(dir) != "" }

func antigravityPluginManifest(dir string) string {
	for _, name := range antigravityPluginFiles {
		file := filepath.Join(dir, name)
		if info, err := os.Stat(file); err == nil && info.Mode().IsRegular() {
			return file
		}
	}
	return ""
}

// 插件清单允许 JSONC。先去掉字符串外的注释，再判断逗号后是否关闭容器。
func antigravityJSONC(raw []byte) []byte {
	if json.Valid(raw) {
		return raw
	}
	src := string(raw)
	var clean strings.Builder
	for i := 0; i < len(src); {
		switch {
		case src[i] == '"':
			end := skipValue(src, i)
			clean.WriteString(src[i:end])
			i = end
		case strings.HasPrefix(src[i:], "//"):
			end := strings.IndexByte(src[i:], '\n')
			if end < 0 {
				i = len(src)
			} else {
				i += end
			}
		case strings.HasPrefix(src[i:], "/*"):
			end := strings.Index(src[i+2:], "*/")
			if end < 0 {
				return raw
			}
			clean.WriteByte(' ')
			i += end + 4
		default:
			clean.WriteByte(src[i])
			i++
		}
	}
	src = clean.String()
	clean.Reset()
	for i := 0; i < len(src); i++ {
		switch src[i] {
		case '"':
			end := skipValue(src, i)
			clean.WriteString(src[i:end])
			i = end - 1
		case ',':
			next := skipSpace(src, i+1)
			if next >= len(src) || src[next] != '}' && src[next] != ']' {
				clean.WriteByte(',')
			}
		default:
			clean.WriteByte(src[i])
		}
	}
	return []byte(clean.String())
}
