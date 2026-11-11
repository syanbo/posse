//go:build darwin

package desk

import (
	"testing"
	"time"
)

// 时间戳按真实时间走，同一时刻或时钟倒退时也严格递增。
func TestNextPTS(t *testing.T) {
	ms := int64(time.Millisecond)
	cases := []struct{ last, now, want int64 }{
		{0, 5 * ms, 5 * ms},
		{5 * ms, 105 * ms, 105 * ms},
		{105 * ms, 105 * ms, 106 * ms}, // 同一时刻
		{106 * ms, 100 * ms, 107 * ms}, // 倒退
	}
	for _, c := range cases {
		if got := nextPTS(c.last, c.now); got != c.want {
			t.Errorf("nextPTS(%d, %d) = %d, want %d", c.last, c.now, got, c.want)
		}
	}
}
