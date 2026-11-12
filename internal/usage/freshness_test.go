package usage

import (
	"testing"
	"time"
)

func TestCodexResetRequiresReading(t *testing.T) {
	home := t.TempDir()
	s := Open(home)
	u := Usage{Agent: "codex", UpdatedAt: 1000, Windows: []Window{
		{Percent: 65, Minutes: 300, ResetsAt: 5000},
		{Percent: 85, Minutes: 10080, ResetsAt: 9000},
	}}
	s.Update(u)
	got := s.List(time.UnixMilli(6000))[0]
	if !got.Windows[0].Unknown || got.Windows[0].Percent != 65 || got.Windows[0].ResetsAt != 5000 || got.Windows[1].Unknown || got.UpdatedAt != 1000 {
		t.Fatalf("expired snapshot: %+v", got)
	}
	// 未知只是返回值的状态，不能把推测写回上次成功读数。
	if persisted := Open(home).List(time.UnixMilli(4000))[0]; persisted.Windows[0].Unknown || persisted.Windows[0].Percent != 65 {
		t.Fatalf("stored reading changed: %+v", persisted)
	}
	s.Update(Usage{Agent: "codex", UpdatedAt: 7000, Windows: []Window{{Percent: 12, Minutes: 300, ResetsAt: 15000}}})
	if current := s.List(time.UnixMilli(8000))[0]; current.Windows[0].Unknown || current.Windows[0].Percent != 12 {
		t.Fatalf("new reading still unknown: %+v", current)
	}
}
