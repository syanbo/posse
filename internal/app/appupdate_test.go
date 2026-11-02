package app

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestUpdateDue(t *testing.T) {
	now := time.Now()
	cases := []struct {
		checked time.Time
		manual  bool
		want    bool
	}{
		{time.Time{}, false, true},
		{now.Add(-time.Hour), false, false},
		{now.Add(-time.Hour), true, true},
		{now.Add(-updateEvery), false, true},
	}
	for _, tc := range cases {
		u := selfUpdate{checkedAt: tc.checked}
		if got := u.due(tc.manual, now); got != tc.want {
			t.Errorf("due(checked %v, manual %v) = %v, want %v", now.Sub(tc.checked), tc.manual, got, tc.want)
		}
	}
}

func TestInApplications(t *testing.T) {
	home, _ := os.UserHomeDir()
	for path, want := range map[string]bool{
		"/Applications/Posse.app":                        true,
		filepath.Join(home, "Applications", "Posse.app"): true,
		"/Applications Old/Posse.app":                    false,
		"/Users/x/posse/build/darwin-arm64/Posse.app":    false,
		"/Volumes/Posse/Posse.app":                       false,
	} {
		if got := inApplications(path); got != want {
			t.Errorf("inApplications(%q) = %v, want %v", path, got, want)
		}
	}
}

// 测试二进制不带更新地址，和 PosseDev、go build 的一样不显示更新。
func TestUpdateHiddenWithoutFeed(t *testing.T) {
	a := &app{exe: exeWatch{bundle: "/Applications/Posse.app"}}
	if reason, hidden := a.updateUnavailable(); !hidden || reason != "" {
		t.Fatalf("updateUnavailable() = %q, %v; want hidden", reason, hidden)
	}
}

// 从源码打的包是临时签名，读不出团队；发布证书签的包读得出 releaseTeam。
func TestSigningTeam(t *testing.T) {
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	if got := signingTeam(exe); got != "" {
		t.Errorf("signingTeam(test binary) = %q, want empty (linker adhoc signature)", got)
	}
	if got := signingTeam("/nonexistent"); got != "" {
		t.Errorf("signingTeam(missing) = %q", got)
	}
	// 本机有发布证书签的包时顺便核对（只读）
	for _, p := range []string{"../../build/darwin-arm64/Posse.app/Contents/MacOS/Posse", "../../build/dev/PosseDev.app/Contents/MacOS/Posse"} {
		if _, err := os.Stat(p); err != nil {
			continue
		}
		if got := signingTeam(p); got != releaseTeam {
			t.Errorf("signingTeam(%s) = %q, want %q", p, got, releaseTeam)
		}
	}
}

// 更新说明按界面语言截取：中文去掉 English 小节，英文只取它，没写英文的旧版本整节照旧。
func TestLocalNotes(t *testing.T) {
	both := "- 分栏\n- 拖动\n\n### English\n\n- Split panes\n- Drag tabs"
	old := "- 分栏\n- 拖动"
	for _, c := range []struct{ notes, lang, want string }{
		{both, "zh-Hans", "- 分栏\n- 拖动"},
		{both, "zh", "- 分栏\n- 拖动"},
		{both, "en", "- Split panes\n- Drag tabs"},
		{both, "ja", "- Split panes\n- Drag tabs"},
		{old, "en", old},
		{old, "zh-Hans", old},
		// English 之后还有别的小节：中文里留着，英文不取
		{"- 甲\n### English\n- A\n### 已知问题\n- 乙", "zh-Hans", "- 甲\n### 已知问题\n- 乙"},
		{"- 甲\n### English\n- A\n### 已知问题\n- 乙", "en", "- A"},
		{"", "en", ""},
	} {
		if got := localNotes(c.notes, c.lang); got != c.want {
			t.Errorf("localNotes(%q, %s) = %q，要 %q", c.notes, c.lang, got, c.want)
		}
	}
}
