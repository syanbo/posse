package skills

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
)

func TestCheckUpdates(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/repos/me/kit/git/trees/HEAD", func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"sha":"root1","tree":[{"path":"skills/a","type":"tree","sha":"a-new"},{"path":"skills/a/SKILL.md","type":"blob"},` +
			`{"path":"skills/b","type":"tree","sha":"b-old"},{"path":"skills/b/SKILL.md","type":"blob"},` +
			`{"path":"new/m","type":"tree","sha":"m-moved"},{"path":"new/m/SKILL.md","type":"blob"},` +
			`{"path":"x/d/SKILL.md","type":"blob"},{"path":"y/d/SKILL.md","type":"blob"}]}`))
	})
	gone := 0
	mux.HandleFunc("/repos/me/gone/git/trees/", func(w http.ResponseWriter, r *http.Request) { gone++; http.NotFound(w, r) })
	// 限流：不该再换分支多试
	limited := 0
	mux.HandleFunc("/repos/me/limited/git/trees/", func(w http.ResponseWriter, r *http.Request) {
		limited++
		w.Header().Set("X-Ratelimit-Remaining", "0")
		w.WriteHeader(http.StatusForbidden)
	})
	mux.HandleFunc("/.well-known/skills/index.json", func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"skills":[{"name":"wk","files":["SKILL.md","ref/x.md"]},{"name":"flaky","files":["SKILL.md"]},{"name":"other","files":["SKILL.md"]}]}`))
	})
	// 每次下载内容不同的文件（飞书把机房代号写进 HTML）：头一次和本地那份不同，再下一次相同，算最新
	flaky := 0
	mux.HandleFunc("/.well-known/skills/flaky/SKILL.md", func(w http.ResponseWriter, r *http.Request) {
		flaky++
		w.Write([]byte(fmt.Sprint("idc", flaky%2)))
	})
	mux.HandleFunc("/.well-known/skills/wk/SKILL.md", func(w http.ResponseWriter, r *http.Request) { w.Write([]byte("skill")) })
	mux.HandleFunc("/.well-known/skills/wk/ref/x.md", func(w http.ResponseWriter, r *http.Request) { w.Write([]byte("x")) })
	srv := httptest.NewServer(mux)
	defer srv.Close()
	old := githubAPI
	githubAPI = srv.URL
	defer func() { githubAPI = old }()
	t.Setenv("GITHUB_TOKEN", "t")

	h := t.TempDir()
	write(t, filepath.Join(h, ".agents", "skills", "flaky", "SKILL.md"), "idc0")
	write(t, filepath.Join(h, ".agents", ".skill-lock.json"), `{"version":3,"skills":{
		"a":{"source":"me/kit","sourceType":"github","skillPath":"skills/a/SKILL.md","skillFolderHash":"a-old"},
		"b":{"source":"me/kit","sourceType":"github","skillPath":"skills/b/SKILL.md","skillFolderHash":"b-old"},
		"m":{"source":"me/kit","sourceType":"github","skillPath":"skills/m/SKILL.md","skillFolderHash":"m-old"},
		"r":{"source":"me/kit","sourceType":"github","skillPath":"SKILL.md","skillFolderHash":"r-old"},
		"d":{"source":"me/kit","sourceType":"github","skillPath":"skills/d/SKILL.md","skillFolderHash":"d-old"},
		"g":{"source":"me/gone","sourceType":"github","skillPath":"SKILL.md","skillFolderHash":"g"},
		"lim":{"source":"me/limited","sourceType":"github","skillPath":"SKILL.md","skillFolderHash":"l"},
		"wk":{"source":"local.test","sourceType":"well-known","sourceBaseUrl":"`+srv.URL+`","wellKnownDigest":"sha256:old"},
		"flaky":{"source":"local.test","sourceType":"well-known","sourceBaseUrl":"`+srv.URL+`","wellKnownDigest":"`+digest("SKILL.md\x00idc0\x00")+`"},
		"loc":{"source":"./x","sourceType":"local"}}}`)
	u := CheckUpdates(context.Background(), h)

	want := map[string]string{"a": "a-new", "b": "b-old", "m": "m-moved", "wk": digest("SKILL.md\x00skill\x00ref/x.md\x00x\x00"), "flaky": digest("SKILL.md\x00idc0\x00")}
	if len(u.Latest) != len(want) {
		t.Fatalf("查到 %v", u.Latest)
	}
	for k, v := range want {
		if u.Latest[k] != v {
			t.Errorf("%s：%q，要 %q", k, u.Latest[k], v)
		}
	}
	// 根目录的 SKILL.md 删了（上游改名挪走）、同名目录有两个（npx 不知道是哪个）：不算新版本，npx 更新不了
	if len(u.Gone) != 2 || !u.Gone["d"] || !u.IsGone(Skill{Name: "r", LockHash: "r-old"}) || u.HasUpdate(Skill{Name: "r", LockHash: "r-old"}) {
		t.Errorf("来源里找不到的 %v", u.Gone)
	}
	if len(u.Failed) != 2 || !strings.HasPrefix(u.Failed[0], "me/gone") || !strings.HasPrefix(u.Failed[1], "me/limited") {
		t.Errorf("查不了的来源 %v", u.Failed)
	}
	if gone != 3 || limited != 1 {
		t.Errorf("分支不存在才换下一个：404 试了 %d 次（要 3），限流试了 %d 次（要 1）", gone, limited)
	}
	if !u.HasUpdate(Skill{Name: "a", LockHash: "a-old"}) || u.HasUpdate(Skill{Name: "b", LockHash: "b-old"}) || u.HasUpdate(Skill{Name: "loc"}) {
		t.Error("HasUpdate 判断不对")
	}
}

func digest(s string) string {
	sum := sha256.Sum256([]byte(s))
	return "sha256:" + hex.EncodeToString(sum[:])
}

func TestSkillDir(t *testing.T) {
	for in, want := range map[string]string{"SKILL.md": "", "skills/a/SKILL.md": "skills/a", `x\y\skill.md`: "x/y"} {
		if got := skillDir(in); got != want {
			t.Errorf("%q：%q，要 %q", in, got, want)
		}
	}
}
