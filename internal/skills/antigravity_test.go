package skills

import (
	"context"
	"os"
	"path/filepath"
	"strings"
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

func TestAntigravityPersonalAndAdditionalRoots(t *testing.T) {
	t.Setenv("USER", "alex")
	home := t.TempDir()
	repo, extra := filepath.Join(home, "repo"), filepath.Join(home, "extra")
	write(t, filepath.Join(repo, ".git"), "")
	write(t, filepath.Join(repo, ".gemini", "config.json"), `{"personal_customization_dir":"people/{user}"}`)
	personal := filepath.Join(repo, "people", "alex", ".agents")
	write(t, filepath.Join(personal, "skills.json"), `{"entries":[{"path":"team-skills"}]}`)
	skill(t, filepath.Join(repo, "team-skills", "team"), "team", "")
	skill(t, filepath.Join(personal, "skills", "review"), "review", "")
	skill(t, filepath.Join(repo, ".agents", "skills", "review"), "review", "")
	skill(t, filepath.Join(extra, ".agents", "skills", "extra"), "extra", "")
	cat := Scan(Options{Home: home, Dir: repo, AntigravityDirs: []string{extra}})
	winners := 0
	for _, sk := range cat.Skills {
		if sk.Name == "review" && sk.Agents[Antigravity].State == On {
			winners++
			if sk.Dir != realDir(filepath.Join(personal, "skills", "review")) {
				t.Fatalf("personal skill did not win: %s", sk.Dir)
			}
		}
	}
	if winners != 1 {
		t.Fatalf("enabled review skills = %d", winners)
	}
	if find(t, cat, "team", ScopeProject).Agents[Antigravity].State != On {
		t.Fatal("personal manifest relative path did not resolve from repo")
	}
	s := find(t, cat, "extra", ScopeProject)
	if s.Agents[Antigravity].State != On || s.Agents[Codex].State != Unseen {
		t.Fatalf("additional workspace leaked to another agent: %+v", s.Agents)
	}
}

func TestAntigravityPluginSkillsAndMCP(t *testing.T) {
	home := t.TempDir()
	repo := filepath.Join(home, "repo")
	cfg := filepath.Join(home, ".gemini", "config")
	global := filepath.Join(cfg, "plugins", "team")
	project := filepath.Join(repo, ".agents", "plugins", "project")
	registered := filepath.Join(repo, "bundles", "registered")
	write(t, filepath.Join(cfg, "config.json"), `{"plugins":{"team":{"enabled":false}}}`)
	write(t, filepath.Join(global, "plugin.json"), "{ // Native JSONC\n\"name\": \"Display Name\",}")
	write(t, filepath.Join(project, ".antigravity-plugin", "plugin.json"), `{}`)
	write(t, filepath.Join(registered, "plugin.json"), `{}`)
	write(t, filepath.Join(repo, ".agents", "plugins.json"), `{"entries":[{"path":"bundles/registered"}]}`)
	for _, dir := range []string{global, project, registered} {
		skill(t, filepath.Join(dir, "skills", "review"), "review", "")
		write(t, filepath.Join(dir, "mcp_config.json"), `{"mcpServers":{"local":{"command":"server"}}}`)
	}
	cat := Scan(Options{Home: home, Dir: repo})
	if st := find(t, cat, "team:review", ScopePlugin).Agents[Antigravity]; st.State != Off {
		t.Fatalf("disabled plugin: %+v", st)
	}
	for _, name := range []string{"project:review", "registered:review"} {
		if st := find(t, cat, name, ScopeProject).Agents[Antigravity]; st.State != On {
			t.Fatalf("project plugin %s: %+v", name, st)
		}
	}
	statuses := map[string]State{}
	for _, sv := range ScanMCP(context.Background(), MCPOptions{Home: home, Dir: repo}).Servers {
		if len(sv.Defs) != 1 || !sv.Defs[0].Plugin {
			t.Fatalf("bad plugin definition: %+v", sv.Defs)
		}
		statuses[sv.Name] = sv.Agents[Antigravity].State
	}
	if len(statuses) != 3 || statuses["team_local"] != Off || statuses["project_local"] != On || statuses["registered_local"] != On {
		t.Fatal(statuses)
	}
}

func TestAntigravityToggleKeepsManifestSettings(t *testing.T) {
	home := t.TempDir()
	repo := filepath.Join(home, "repo")
	cfg := filepath.Join(repo, ".agents", "skills.json")
	write(t, cfg, "{\n  \"entries\": [],\n  \"exclude\": [\"other\"]\n}\n")
	skill(t, filepath.Join(repo, ".agents", "skills", "my-folder"), "my-name", "")
	o := Options{Home: home, Dir: repo}
	for _, enable := range []bool{false, true} {
		s := find(t, Scan(o), "my-name", ScopeProject)
		ch, err := PlanToggle(o, s, Antigravity, enable)
		if err != nil || ch.File != cfg || !strings.Contains(ch.After, `"entries": []`) || strings.Contains(ch.After, `"my-folder"`) == enable {
			t.Fatalf("toggle %v: %+v, %v", enable, ch, err)
		}
		if err := ch.Apply(); err != nil {
			t.Fatal(err)
		}
		s = find(t, Scan(o), "my-name", ScopeProject)
		if (s.Agents[Antigravity].State == On) != enable {
			t.Fatalf("toggle %v resulted in %v", enable, s.Agents[Antigravity].State)
		}
	}
}

func TestAntigravityPluginSkillDirectoryLinks(t *testing.T) {
	home := t.TempDir()
	shared := filepath.Join(home, ".agents", "skills", "review")
	skill(t, shared, "review", "")
	plugin := filepath.Join(home, ".gemini", "config", "plugins", "kit")
	write(t, filepath.Join(plugin, "plugin.json"), `{}`)
	link(t, shared, filepath.Join(plugin, "skills", "review"))
	skill(t, filepath.Join(plugin, "skills", "check"), "check", "")
	cat := Scan(Options{Home: home})
	if got := find(t, cat, "review", ScopeUser).Agents[Antigravity].State; got != Unseen {
		t.Fatalf("plugin directory link made a shared skill available as %v", got)
	}
	if got := find(t, cat, "kit:check", ScopePlugin).Agents[Antigravity].State; got != On {
		t.Fatalf("regular plugin skill = %v", got)
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
