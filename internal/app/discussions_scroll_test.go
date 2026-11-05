package app

import (
	"fmt"
	"testing"
	"time"

	"github.com/egoist/mygo/ui"

	"posse/internal/rpc"
)

// scrollTester 是停在底部的 60 条消息、窗口 1200×800，返回指针放在消息区上的坐标。
func scrollTester(t *testing.T, extra ...map[string]rpc.Handler) (*app, *ui.Tester, float32, float32) {
	t.Helper()
	a, tt, _ := discTester(t, extra...)
	d := a.disc.d
	d.msgs = nil
	for i := 1; i <= 60; i++ {
		d.msgs = append(d.msgs, discMessage{ID: fmt.Sprintf("p%d", i), Seq: int64(i), Kind: "user", Body: fmt.Sprintf("scroll message %d", i), CreatedAt: time.Now()})
	}
	d.info.MsgSeq = 60
	d.more, d.moreAfter = true, false
	a.disc.panel, a.disc.quote, a.disc.target = false, "", ""
	a.disc.stickBottom()
	tt.SetSize(1200, 800)
	for range 4 {
		tt.Frame()
	}
	s := &a.disc.scroll
	if s.MaxY <= 0 || s.Y != s.MaxY || !a.disc.follow {
		t.Fatalf("not at the bottom: Y=%v MaxY=%v follow=%v", s.Y, s.MaxY, a.disc.follow)
	}
	r, ok := tt.Find("scroll message 58")
	if !ok {
		t.Fatal("message not shown")
	}
	return a, tt, r.X + r.W/2, r.Y + r.H/2
}

// 新来一条消息（增量）。
func arrive(a *app, seq int64) {
	d := a.disc.d
	v := discView{Discussion: d.info, Delta: true, Live: map[string]string{},
		Messages: []discMessage{{ID: fmt.Sprintf("p%d", seq), Seq: seq, Kind: "user", Body: fmt.Sprintf("scroll message %d", seq), CreatedAt: time.Now()}}}
	v.Discussion.Rev, v.Discussion.MsgSeq = d.rev+1, seq
	a.applyDiscussions(&discPoll{view: &v, key: d.key, since: d.rev, gen: a.discGen()})
}

// 触控板、滚轮离开底部之后留在原处，新消息不把人拉走，给「N 条新消息」。
func TestDiscussionScrollLeavesBottom(t *testing.T) {
	a, tt, x, y := scrollTester(t)
	s := &a.disc.scroll
	bottom := s.MaxY
	tt.Scroll(x, y, 0, -300)
	settle(tt)
	if s.Y > bottom-250 || a.disc.follow {
		t.Fatalf("trackpad scroll was pulled back: Y=%v of %v follow=%v", s.Y, bottom, a.disc.follow)
	}
	tt.Wheel(x, y, 0, -300)
	tt.Advance(time.Second)
	settle(tt)
	if s.Y > bottom-550 {
		t.Fatalf("wheel did not move further up: Y=%v", s.Y)
	}
	at := s.Y
	arrive(a, 61)
	settle(tt)
	if s.Y != at || a.disc.follow {
		t.Fatalf("new message moved the reader: Y=%v want %v follow=%v", s.Y, at, a.disc.follow)
	}
	if !tt.HasText("1 条新消息") {
		t.Fatalf("no new-message pill; texts %v", tt.Texts())
	}
	// 点胶囊回到底部，接着跟随
	if err := tt.Click("1 条新消息"); err != nil {
		t.Fatal(err)
	}
	tt.Advance(time.Second)
	settle(tt)
	if s.Y != s.MaxY || !a.disc.follow {
		t.Fatalf("pill did not return to the bottom: Y=%v MaxY=%v follow=%v", s.Y, s.MaxY, a.disc.follow)
	}
}

// 停在底部时第一下就是鼠标滚轮：滚轮分帧缓出，事件后的第一帧还没动多少（无头测试里一点没动），
// 那一帧照常跟随也不能把这一格滚轮清掉。
func TestDiscussionScrollWheelFromBottom(t *testing.T) {
	a, tt, x, y := scrollTester(t)
	s := &a.disc.scroll
	bottom := s.MaxY
	tt.Wheel(x, y, 0, -300)
	tt.Advance(time.Second)
	settle(tt)
	if s.Y > bottom-250 || a.disc.follow {
		t.Fatalf("wheel from the bottom was swallowed: Y=%v of %v follow=%v", s.Y, bottom, a.disc.follow)
	}
}

// 触控板慢慢滚，每帧只动几像素，也能离开底部。
func TestDiscussionScrollSlowTrackpad(t *testing.T) {
	a, tt, x, y := scrollTester(t)
	s := &a.disc.scroll
	bottom := s.MaxY
	for range 8 {
		tt.Scroll(x, y, 0, -3)
	}
	if s.Y != bottom-24 || a.disc.follow {
		t.Fatalf("slow trackpad was pulled back: Y=%v of %v follow=%v", s.Y, bottom, a.disc.follow)
	}
}

// 停在底部时新消息照常跟上。
func TestDiscussionScrollFollowsAtBottom(t *testing.T) {
	a, tt, _, _ := scrollTester(t)
	s := &a.disc.scroll
	old := s.MaxY
	arrive(a, 61)
	settle(tt)
	if s.MaxY <= old || s.Y != s.MaxY || !a.disc.follow {
		t.Fatalf("did not follow: Y=%v MaxY=%v (was %v) follow=%v", s.Y, s.MaxY, old, a.disc.follow)
	}
}

// 往上翻着时主动回到底部：发出消息、跳到最新、整页换上都回到底部并接着跟随。
func TestDiscussionScrollReturnsToBottom(t *testing.T) {
	a, tt, x, y := scrollTester(t)
	s := &a.disc.scroll
	up := func() {
		tt.Scroll(x, y, 0, -600)
		settle(tt)
		if s.Y >= s.MaxY-4 {
			t.Fatalf("could not scroll up: Y=%v MaxY=%v", s.Y, s.MaxY)
		}
	}
	up()
	a.disc.text = "hello"
	tt.Frame()
	if err := tt.Click("发送"); err != nil {
		t.Fatal(err)
	}
	waitFor := func(what string, cond func() bool) {
		deadline := time.Now().Add(3 * time.Second)
		for time.Now().Before(deadline) {
			tt.Frame()
			if cond() {
				return
			}
			time.Sleep(10 * time.Millisecond)
		}
		t.Fatalf("%s: Y=%v MaxY=%v follow=%v", what, s.Y, s.MaxY, a.disc.follow)
	}
	waitFor("send returns to the bottom", func() bool { return !a.disc.sending && s.Y == s.MaxY && a.disc.follow })

	up()
	a.jumpLatest()
	settle(tt)
	if s.Y != s.MaxY || !a.disc.follow {
		t.Fatalf("jump to latest: Y=%v MaxY=%v", s.Y, s.MaxY)
	}

	up()
	full := discView{Discussion: a.disc.d.info, Messages: a.disc.d.msgs, More: true, Live: map[string]string{}}
	full.Discussion.Rev = a.disc.d.rev + 1
	a.applyDiscussions(&discPoll{view: &full, key: a.disc.d.key, gen: a.discGen()})
	settle(tt)
	if s.Y != s.MaxY || !a.disc.follow {
		t.Fatalf("full page did not return to the bottom: Y=%v MaxY=%v", s.Y, s.MaxY)
	}
}

// 滚到顶上点「加载更早的消息」：新的一页接在前面，原来最上面那条留在视野里，不跳到底部。
func TestDiscussionScrollLoadOlderKeepsAnchor(t *testing.T) {
	a, tt, x, y := scrollTester(t, map[string]rpc.Handler{"discussion.show": msgPages(200)})
	s := &a.disc.scroll
	// 消息 p1..p60 在 msgPages 里对应序号 1..60，把它们挪到 141..200 好往前翻
	for i := range a.disc.d.msgs {
		m := &a.disc.d.msgs[i]
		m.Seq += 140
		m.ID = fmt.Sprintf("m%d", m.Seq)
	}
	settle(tt)
	for range 20 {
		tt.Scroll(x, y, 0, -1000)
		tt.Frame()
	}
	settle(tt)
	if s.Y != 0 || a.disc.follow {
		t.Fatalf("not at the top: Y=%v follow=%v", s.Y, a.disc.follow)
	}
	if err := tt.Click("加载更早的消息"); err != nil {
		t.Fatalf("load older not reachable: %v", err)
	}
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) && (a.disc.older || a.disc.d.msgs[0].Seq == 141) {
		tt.Frame()
		time.Sleep(10 * time.Millisecond)
	}
	settle(tt)
	if a.disc.d.msgs[0].Seq >= 141 {
		t.Fatalf("older page not loaded: first %d", a.disc.d.msgs[0].Seq)
	}
	if a.disc.follow || s.Y >= s.MaxY-4 {
		t.Fatalf("jumped to the bottom after loading older: Y=%v MaxY=%v", s.Y, s.MaxY)
	}
	r, ok := tt.Find("scroll message 1")
	if !ok || r.Y < 0 || r.Y > 800 {
		t.Fatalf("previous top message not kept in view: %+v %v", r, ok)
	}
}
