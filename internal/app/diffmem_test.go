package app

import (
	"runtime"
	"testing"
	"time"
)

// 大 diff 没人用了被回收后从计数里去掉，定时 GC 的协程跟着停。
func TestBigDiffReleased(t *testing.T) {
	mem.mu.Lock()
	before := mem.big
	mem.mu.Unlock()
	func() {
		df := &diffDoc{}
		trackBigDiff(df)
		trackBigDiff(df) // 同一个只记一次
	}()
	mem.mu.Lock()
	if mem.big != before+1 {
		t.Fatalf("big = %d, want %d", mem.big, before+1)
	}
	mem.mu.Unlock()
	deadline := time.Now().Add(5 * time.Second)
	for {
		runtime.GC()
		mem.mu.Lock()
		n := mem.big
		mem.mu.Unlock()
		if n == before {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("big diff never released: %d", n)
		}
		time.Sleep(10 * time.Millisecond)
	}
}
