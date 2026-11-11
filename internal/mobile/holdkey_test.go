package mobile

import (
	"runtime"
	"testing"
)

// 一路流还看着时，之后开的流不能拿到同一个 key：否则先结束的会放掉另一路的尺寸占用。
func TestHoldKeyUniqueAcrossGC(t *testing.T) {
	for range 20 {
		live := holdKey()
		for i := range 2000 {
			if i%100 == 0 {
				runtime.GC()
			}
			if holdKey() == live {
				t.Fatalf("key %s reused after %d keys", live, i)
			}
		}
	}
}
