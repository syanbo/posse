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
