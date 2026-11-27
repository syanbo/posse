package remote

import (
	"testing"

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
		{"v0.1.55", "0.1.54", 1},
	}
	for _, tc := range cases {
		if got := compareSemver(tc.a, tc.b); got != tc.want {
			t.Errorf("compareSemver(%q, %q) = %d, want %d", tc.a, tc.b, got, tc.want)
		}
	}
}

func TestShouldAutoUpgradeHeadless(t *testing.T) {
	base := rpc.RemoteInfo{
		Version: "0.1.54",
		Build:   "8370eac 2026-11-27T10:09:08Z",
		Edition: "headless",
	}
	if !shouldAutoUpgradeHeadless(base, "0.1.55", "8370eac") {
		t.Fatal("newer semver did not trigger upgrade")
	}
	if shouldAutoUpgradeHeadless(base, "0.1.53", "9999999") {
		t.Fatal("older semver attempted downgrade")
	}
	if !shouldAutoUpgradeHeadless(base, "0.1.54", "49f7b8f") {
		t.Fatal("same version with different commit did not trigger upgrade")
	}
	if shouldAutoUpgradeHeadless(base, "0.1.54", "8370eac") {
		t.Fatal("same version and same commit triggered redundant upgrade")
	}
	if shouldAutoUpgradeHeadless(base, "0.1.54", "") {
		t.Fatal("empty target commit triggered upgrade on same version")
	}
	full := base
	full.Edition = "full"
	if shouldAutoUpgradeHeadless(full, "0.1.55", "49f7b8f") {
		t.Fatal("full edition triggered headless auto-upgrade")
	}
	noCommit := base
	noCommit.Build = "2026-11-27T10:09:08Z"
	if shouldAutoUpgradeHeadless(noCommit, "0.1.54", "49f7b8f") {
		t.Fatal("timestamp-only build triggered commit mismatch on same version")
	}
}
