package history

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestSkillStats(t *testing.T) {
	h := t.TempDir()
	dir := filepath.Join(h, ".claude", "projects", "-x")
	os.MkdirAll(dir, 0o755)
	a := filepath.Join(dir, "a.jsonl")
	b := filepath.Join(dir, "b.jsonl")
	use := func(at, name string) string {
		return `{"type":"user","timestamp":"` + at + `","message":{"content":"Base directory for this skill: /x/skills/` + name + `"}}` + "\n"
	}
	os.WriteFile(a, []byte(use("2026-10-01T08:00:00.000Z", "lint")+use("2026-10-02T08:00:00.000Z", "lint")), 0o644)
	os.WriteFile(b, []byte(use("2026-10-05T08:00:00.000Z", "lint")+use("2026-10-03T08:00:00.000Z", "deploy")), 0o644)
	cache := filepath.Join(t.TempDir(), "skill-usage.json")

	st, err := SkillStats(context.Background(), Roots{Home: h}, cache, nil)
	if err != nil {
		t.Fatal(err)
	}
	lint := st["lint"]
	if lint == nil || lint.Count != 3 || lint.Sessions != 2 || lint.ByAgent["claude"] != 3 || !lint.Last.Equal(time.Date(2026, 10, 5, 8, 0, 0, 0, time.UTC)) {
		t.Fatalf("lint %+v", lint)
	}
	if st["deploy"] == nil || st["deploy"].Count != 1 {
		t.Fatalf("deploy %+v", st["deploy"])
	}

	// 没变的文件用缓存：改掉缓存里的次数，再算还是缓存的；文件变了才重读
	b2, _ := os.ReadFile(cache)
	if len(b2) == 0 {
		t.Fatal("没写缓存")
	}
	var c skillCache
	readJSON(t, cache, &c)
	f := c.Files[a]
	f.Skills["lint"] = skillUses{N: 10, Last: f.Skills["lint"].Last}
	c.Files[a] = f
	writeJSON(t, cache, c)
	st, _ = SkillStats(context.Background(), Roots{Home: h}, cache, nil)
	if st["lint"].Count != 11 {
		t.Errorf("没变的文件应读缓存：%d", st["lint"].Count)
	}
	os.WriteFile(a, []byte(use("2026-10-01T08:00:00.000Z", "lint")), 0o644)
	st, _ = SkillStats(context.Background(), Roots{Home: h}, cache, nil)
	if st["lint"].Count != 2 {
		t.Errorf("文件变了应重读：%d", st["lint"].Count)
	}
	// 文件删了，缓存里也去掉
	os.Remove(b)
	st, _ = SkillStats(context.Background(), Roots{Home: h}, cache, nil)
	if st["deploy"] != nil || st["lint"].Sessions != 1 {
		t.Errorf("删掉的会话不该再算：%+v %+v", st["deploy"], st["lint"])
	}
}

func readJSON(t *testing.T, path string, v any) {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(b, v); err != nil {
		t.Fatal(err)
	}
}

func writeJSON(t *testing.T, path string, v any) {
	t.Helper()
	b, _ := json.Marshal(v)
	if err := os.WriteFile(path, b, 0o600); err != nil {
		t.Fatal(err)
	}
}
