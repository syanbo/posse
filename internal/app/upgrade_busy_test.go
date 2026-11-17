package app

import (
	"testing"
	"time"
)

func TestUpgradeBusyRetriesAfterDelay(t *testing.T) {
	var u upgradeState
	if !u.begin(123, true) {
		t.Fatal("first attempt rejected")
	}
	u.deferBusy(123)
	if u.begin(123, true) {
		t.Fatal("busy retry did not back off")
	}
	u.mu.Lock()
	u.retryAt[123] = time.Now().Add(-time.Second)
	u.mu.Unlock()
	if !u.begin(123, true) {
		t.Fatal("busy attempt permanently suppressed retry")
	}
	u.end(123, false)
	if u.begin(123, true) {
		t.Fatal("non-busy failure retried automatically")
	}
}
