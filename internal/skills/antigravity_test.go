package skills

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

func TestAntigravitySkills(t *testing.T) {
	home := t.TempDir()
	repo := filepath.Join(home, "repo")
	sub := filepath.Join(repo, "nested")
	os.MkdirAll(filepath.Join(repo, ".git"), 0o755)
	os.MkdirAll(sub, 0o755)
	skill(t, filepath.Join(home, ".agents", "skills", "shared"), "shared", "")
	skill(t, filepath.Join(home, ".gemini", "config", "skills", "global"), "global", "")
	skill(t, filepath.Join(home, ".gemini", "config", "skills", "same"), "same", "")
	skill(t, filepath.Join(home, ".gemini", "antigravity-cli", "builtin", "skills", "built-in"), "built-in", "")
	skill(t, filepath.Join(repo, ".agents", "skills", "same"), "same", "")
	skill(t, filepath.Join(sub, ".agents", "skills", "same"), "same", "")
	skill(t, filepath.Join(home, "team", "keep"), "keep", "")
	skill(t, filepath.Join(home, "team", "skip"), "skip", "")
	write(t, filepath.Join(repo, ".agents", "skills.json"), `{"inherits":[{"path":"`+filepath.ToSlash(filepath.Join(home, "manifest.json"))+`","include_only":["keep"]}]}`)
	write(t, filepath.Join(home, "manifest.json"), `{"inherits":[{"path":"`+filepath.ToSlash(filepath.Join(home, "manifest.json"))+`"}],"entries":[{"path":"`+filepath.ToSlash(filepath.Join(home, "team"))+`","exclude":["skip"]}]}`)
	cat := Scan(Options{Home: home, Dir: sub})
	if got := find(t, cat, "shared", ScopeUser).Agents[Antigravity].State; got != Unseen {
		t.Fatal(got)
	}
	if got := find(t, cat, "global", ScopeUser).Agents[Antigravity].State; got != On {
		t.Fatal(got)
	}
	if got := find(t, cat, "built-in", ScopeSystem).Agents[Antigravity].State; got != On {
		t.Fatal(got)
	}
	if got := find(t, cat, "keep", ScopeProject).Agents[Antigravity].State; got != On {
		t.Fatal(got)
	}
	var active int
	for _, s := range cat.Skills {
		if s.Name == "skip" {
			t.Fatal("manifest exclusion ignored")
		}
		if s.Name == "same" && s.Agents[Antigravity].State == On {
			active++
			if s.Dir != realDir(filepath.Join(sub, ".agents", "skills", "same")) {
				t.Fatal(s.Dir)
			}
		}
	}
	if active != 1 {
		t.Fatal(active)
	}
}

func TestAntigravityManifestPaths(t *testing.T) {
	home := t.TempDir()
	repo := filepath.Join(home, "repo")
	os.MkdirAll(filepath.Join(repo, ".git"), 0o755)
	for _, dir := range []string{"tools/direct", "tools/nested/deep", ".agents/skills/hidden", ".agent/skills/alias"} {
		skill(t, filepath.Join(repo, dir), filepath.Base(dir), "")
	}
	skill(t, filepath.Join(home, ".gemini", "config", "skills", "hidden"), "hidden", "")
	skill(t, filepath.Join(home, ".gemini", "skills", "hidden"), "hidden", "")
	write(t, filepath.Join(repo, ".agents", "skills.json"), `{"exclude":["hidden"],"entries":[{"path":"tools/direct/SKILL.md"},{"path":"tools","include_only":["nested/deep"]}]}`)
	cat := Scan(Options{Home: home, Dir: repo})
	for _, name := range []string{"direct", "deep", "alias"} {
		if find(t, cat, name, ScopeProject).Agents[Antigravity].State != On {
			t.Fatal(name)
		}
	}
	for _, s := range cat.Skills {
		if s.Name == "hidden" && s.Agents[Antigravity].State == On && s.Dir != realDir(filepath.Join(home, ".gemini", "skills", "hidden")) {
			t.Fatal(s)
		}
	}
}

func TestAntigravityPersonalInheritsProject(t *testing.T) {
	home := t.TempDir()
	shared := filepath.Join(home, "shared")
	skill(t, filepath.Join(shared, "custom", "review"), "review", "")
	manifest := filepath.Join(shared, ".agents", "skills.json")
	write(t, manifest, `{"entries":[{"path":"custom"}]}`)
	write(t, filepath.Join(home, ".gemini", "config", "skills.json"), `{"inherits":[{"path":"`+filepath.ToSlash(manifest)+`"}]}`)
	if got := find(t, Scan(Options{Home: home}), "review", ScopeUser).Agents[Antigravity].State; got != On {
		t.Fatal(got)
	}
}

func TestAntigravityMCP(t *testing.T) {
	home := t.TempDir()
	repo := filepath.Join(home, "repo")
	os.MkdirAll(filepath.Join(repo, ".git"), 0o755)
	write(t, filepath.Join(home, ".gemini", "config", "mcp_config.json"), `{"mcpServers":{"one":{"command":"server","env":{"TOKEN":"secret"}},"remote":{"serverUrl":"https://example.com/mcp"}}}`)
	write(t, filepath.Join(repo, ".agents", "mcp_config.json"), `{"mcpServers":{"one":{"command":"project-server","disabled":true}}}`)
	cat := ScanMCP(context.Background(), MCPOptions{Home: home, Dir: repo})
	if len(cat.Servers) != 2 {
		t.Fatal(cat)
	}
	for _, s := range cat.Servers {
		if s.Name == "one" && s.Agents[Antigravity].State != Off {
			t.Fatal(s)
		}
		if s.Name == "remote" && (s.Defs[0].URL != "https://example.com/mcp" || s.Defs[0].Transport != "http") {
			t.Fatal(s)
		}
	}
}

func TestPosseAntigravityLinks(t *testing.T) {
	home := t.TempDir()
	owned := filepath.Join(home, ".gemini", "config", "skills", RenameSkill)
	write(t, filepath.Join(owned, "SKILL.md"), "user owned")
	if err := CopyPosse(home, PosseNames(), false); err != nil {
		t.Fatal(err)
	}
	at := filepath.Join(home, ".gemini", "config", "skills", TasksSkill)
	target, err := os.Readlink(at)
	if err != nil || filepath.IsAbs(target) {
		t.Fatal(target, err)
	}
	if got := realDir(at); got != realDir(filepath.Join(home, ".agents", "skills", TasksSkill)) {
		t.Fatal(got)
	}
	b, _ := os.ReadFile(filepath.Join(owned, "SKILL.md"))
	if string(b) != "user owned" {
		t.Fatal(string(b))
	}
}
