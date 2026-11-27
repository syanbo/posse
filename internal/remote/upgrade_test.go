package remote

import (
	"testing"
	"time"

	"posse/internal/rpc"
)

func TestCompareSemver(t *testing.T) {
	cases := []struct {
		a, b string
		want int
	}{
		{"0.1.55", "0.1.54", 1},
		{"0.1.54", "0.1.55", -1},
		{"0.1.54", "0.1.54", 0},
		{"0.2.0", "0.1.99", 1},
		{"1.0.0", "1.0.0-beta.1", 1},
		{"1.0.0-beta.10", "1.0.0-beta.9", 1},
		{"1.0.0-beta.2", "1.0.0-beta.10", -1},
		{"1.0.0-1", "1.0.0-alpha", -1},
		{"v0.1.55", "0.1.54", 1},
	}
	for _, tc := range cases {
		if got := compareSemver(tc.a, tc.b); got != tc.want {
			t.Errorf("compareSemver(%q, %q) = %d, want %d", tc.a, tc.b, got, tc.want)
		}
	}
}

func TestShouldAutoUpgradeHeadless(t *testing.T) {
	baseTime := time.Date(2026, 11, 27, 10, 9, 8, 0, time.UTC)
	newerTime := baseTime.Add(5 * time.Minute)
	olderTime := baseTime.Add(-5 * time.Minute)
	base := rpc.RemoteInfo{
		Version: "0.1.54",
		Build:   "8370eac " + baseTime.Format(time.RFC3339),
		Edition: "headless",
		Managed: true,
		Home:    "/Users/pur/.posse",
	}
	if !shouldAutoUpgradeHeadless(base, "0.1.55", "8370eac", olderTime) {
		t.Fatal("newer semver did not trigger upgrade")
	}
	if shouldAutoUpgradeHeadless(base, "0.1.53", "9999999", newerTime) {
		t.Fatal("older semver attempted downgrade")
	}
	if !shouldAutoUpgradeHeadless(base, "0.1.54", "49f7b8f", newerTime) {
		t.Fatal("same version with strictly newer build time and different commit did not trigger upgrade")
	}
	if shouldAutoUpgradeHeadless(base, "0.1.54", "49f7b8f", olderTime) {
		t.Fatal("same version with older build time triggered backward upgrade")
	}
	if shouldAutoUpgradeHeadless(base, "0.1.54", "49f7b8f", time.Time{}) {
		t.Fatal("same version without builtAt timestamp triggered unverified upgrade")
	}
	if shouldAutoUpgradeHeadless(base, "0.1.54", "8370eac", newerTime) {
		t.Fatal("same version and same commit triggered redundant upgrade")
	}
	// 非托管安装（Managed=false）绝不自动接管升级
	unmanaged := base
	unmanaged.Managed = false
	if shouldAutoUpgradeHeadless(unmanaged, "0.1.55", "49f7b8f", newerTime) {
		t.Fatal("unmanaged headless executable triggered auto-upgrade")
	}
	full := base
	full.Edition = "full"
	if shouldAutoUpgradeHeadless(full, "0.1.55", "49f7b8f", newerTime) {
		t.Fatal("full edition triggered headless auto-upgrade")
	}
}
