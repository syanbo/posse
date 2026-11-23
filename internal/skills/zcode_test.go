package skills

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestZCodeSkillsToggleAndFallback(t *testing.T) {
	t.Setenv("ZCODE_STORAGE_DIR", "")
	home, repo := t.TempDir(), t.TempDir()
	o := Options{Home: home, Dir: repo}
	skill(t, filepath.Join(home, ".zcode", "skills", "review"), "review", "")
	skill(t, filepath.Join(repo, ".agents", "skills", "review"), "review", "")
	config := zcodeUserConfig(home)
	write(t, config, `{"hooks":{"enabled":true},"skills":{"enabled":true}}`)
	cat := Scan(o)
	user := find(t, cat, "review", ScopeUser)
	if user.Agents[ZCode].State != On || find(t, cat, "review", ScopeProject).Agents[ZCode].State != Shadowed {
		t.Fatal("expected the user skill to take precedence")
	}
	change, err := PlanToggle(o, user, ZCode, false)
	if err != nil {
		t.Fatal(err)
	}
	if err := change.Apply(); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(config)
	if err != nil || !strings.Contains(string(data), `"hooks":{"enabled":true}`) {
		t.Fatalf("unrelated configuration changed: %s, %v", data, err)
	}
	cat = Scan(o)
	user = find(t, cat, "review", ScopeUser)
	if user.Agents[ZCode].State != Off || find(t, cat, "review", ScopeProject).Agents[ZCode].State != On {
		t.Fatal("disabled user skill should not shadow an enabled project skill")
	}
	change, err = PlanToggle(o, user, ZCode, true)
	if err != nil {
		t.Fatal(err)
	}
	if err := change.Apply(); err != nil {
		t.Fatal(err)
	}
	if find(t, Scan(o), "review", ScopeUser).Agents[ZCode].State != On {
		t.Fatal("skill remained disabled")
	}
}

func TestZCodeDiscoversSkillAtConfiguredRoot(t *testing.T) {
	t.Setenv("ZCODE_STORAGE_DIR", "")
	home, repo := t.TempDir(), t.TempDir()
	dir := filepath.Join(repo, "review")
	skill(t, dir, "review", "")
	write(t, zcodeUserConfig(home), `{"skills":{"roots":["review"]}}`)
	s := find(t, Scan(Options{Home: home, Dir: repo}), "review", ScopeProject)
	if s.Agents[ZCode].State != On || s.Agents[Codex].State != Unseen {
		t.Fatalf("unexpected skill visibility: %+v", s.Agents)
	}
}
