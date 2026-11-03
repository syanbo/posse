package daemon

import (
	"testing"

	"posse/internal/agent"
	"posse/internal/mobile"
)

func TestPushKind(t *testing.T) {
	cases := []struct {
		prev, next string
		unread     bool
		want       string
	}{
		{agent.StatusWorking, agent.StatusWaiting, false, mobile.PushWaiting},
		{agent.StatusIdle, agent.StatusWaiting, false, mobile.PushWaiting},
		{agent.StatusWaiting, agent.StatusWaiting, false, ""},
		{agent.StatusWorking, agent.StatusIdle, true, mobile.PushDone},
		// 手机看着时完成的不记没看，不推
		{agent.StatusWorking, agent.StatusIdle, false, ""},
		{agent.StatusWaiting, agent.StatusIdle, true, ""},
		{agent.StatusIdle, agent.StatusWorking, false, ""},
	}
	for _, c := range cases {
		if got := pushKind(c.prev, c.next, c.unread); got != c.want {
			t.Errorf("pushKind(%s, %s, %v) = %q, want %q", c.prev, c.next, c.unread, got, c.want)
		}
	}
}

func TestHIDIdle(t *testing.T) {
	if _, ok := hidIdle(); !ok {
		t.Skip("no IOHIDSystem here")
	}
}
