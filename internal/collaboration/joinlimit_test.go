package collaboration

import (
	"fmt"
	"testing"
	"time"
)

// 每个申请过的地址都留一条记录：过了一分钟没再来的要清掉，不然一直攒着。
func TestAllowJoinForgetsOldAddresses(t *testing.T) {
	now := time.Unix(1_800_000_000, 0)
	s := &Server{joins: map[string][]time.Time{}, Now: func() time.Time { return now }}
	for i := range 100 {
		if !s.AllowJoin(fmt.Sprintf("10.0.%d.%d:5000", i/250, i%250+1)) {
			t.Fatalf("join %d refused", i)
		}
	}
	now = now.Add(2 * time.Minute)
	s.AllowJoin("10.9.9.9:5000")
	if len(s.joins) != 1 {
		t.Fatalf("%d addresses kept", len(s.joins))
	}
	// 一分钟内还在限流的地址照样记着
	for range joinPerMinute {
		s.AllowJoin("10.9.9.9:5000")
	}
	now = now.Add(30 * time.Second)
	if s.AllowJoin("10.9.9.9:5000") {
		t.Fatal("limit lost")
	}
}

// 按单个地址计（有意不按 /64 归并，见 AllowJoin）：同一 /64 的另一台设备不受前一台的限流影响。
func TestAllowJoinPerAddress(t *testing.T) {
	s := &Server{joins: map[string][]time.Time{}}
	for range joinPerMinute {
		s.AllowJoin("[2001:db8::1]:5000")
	}
	if s.AllowJoin("[2001:db8::1]:5000") {
		t.Fatal("over the limit")
	}
	if !s.AllowJoin("[2001:db8::2]:5000") {
		t.Fatal("a neighbour in the same /64 was refused")
	}
}
