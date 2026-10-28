package daemon

import (
	"testing"

	"posse/internal/agent"
)

func TestUnread(t *testing.T) {
	s := &session{}
	s.noteStatus(agent.StatusStarting, agent.StatusIdle)
	if s.isUnread() {
		t.Fatal("unread after starting, nothing was done yet")
	}
	s.noteStatus(agent.StatusWorking, agent.StatusIdle)
	if !s.isUnread() {
		t.Fatal("finished turn not unread")
	}
	// 停着时再来一次空闲的事件不改
	s.noteStatus(agent.StatusIdle, agent.StatusIdle)
	if !s.isUnread() {
		t.Fatal("repeated idle cleared unread")
	}
	s.noteStatus(agent.StatusIdle, agent.StatusWorking)
	if s.isUnread() {
		t.Fatal("still unread after the next turn started")
	}
	s.noteStatus(agent.StatusWorking, agent.StatusIdle)
	s.markRead()
	if s.isUnread() {
		t.Fatal("unread after seen")
	}

	// 手机看着时完成的不算没看；不看了之后照常记
	s.noteStatus(agent.StatusIdle, agent.StatusWorking)
	done := s.view()
	s.noteStatus(agent.StatusWorking, agent.StatusIdle)
	if s.isUnread() {
		t.Fatal("unread while a phone is watching")
	}
	done()
	done()
	if s.viewers != 0 {
		t.Fatalf("viewers = %d after release", s.viewers)
	}
	s.noteStatus(agent.StatusIdle, agent.StatusWorking)
	s.noteStatus(agent.StatusWorking, agent.StatusIdle)
	if !s.isUnread() {
		t.Fatal("not unread after the phone left")
	}
	// 打开就算看过
	s.view()()
	if s.isUnread() {
		t.Fatal("unread after the phone opened it")
	}
}
