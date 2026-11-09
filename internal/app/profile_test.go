package app

import (
	"testing"
	"time"
)

// 读资料失败（旧常驻进程不认）后不每帧重试；读到过的隔一阵再读。
func TestProfileDueBacksOff(t *testing.T) {
	var p profileState
	if !p.due(30 * time.Second) {
		t.Fatal("never loaded but not due")
	}
	p.at = time.Now()
	if p.due(30 * time.Second) {
		t.Fatal("retried right after a failure")
	}
	p.at = time.Now().Add(-6 * time.Second)
	if !p.due(30 * time.Second) {
		t.Fatal("failure not retried after the backoff")
	}
	p.loaded = true
	if p.due(30 * time.Second) {
		t.Fatal("loaded profile reread too early")
	}
	p.loading = true
	p.at = time.Time{}
	if p.due(30 * time.Second) {
		t.Fatal("due while a read is in flight")
	}
}
