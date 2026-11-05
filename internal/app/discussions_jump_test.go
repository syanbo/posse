package app

import (
	"context"
	"encoding/json"
	"fmt"
	"testing"
	"time"

	"posse/internal/rpc"
)

// latestView 是讨论最新的一页（非增量）：序号 101-150。
func latestView(key string, rev int64) discView {
	v := discView{Discussion: discSummary{Key: key, Status: "active", Rev: rev, MsgSeq: 150}, More: true}
	for i := int64(101); i <= 150; i++ {
		v.Messages = append(v.Messages, discMessage{ID: fmt.Sprintf("n%d", i), Seq: i, Kind: "user", Body: "latest"})
	}
	return v
}

// 跳到最新之后，在点击之前就带着旧版本发出的轮询结果晚到：不能把「取全量」的意图写回旧版本，
// 下一次轮询仍取最新一页，窗口回到最新。
func TestDiscussionJumpLatestSurvivesStalePoll(t *testing.T) {
	a, tt, _ := discTester(t)
	d := a.disc.d
	d.moreAfter = true // 停在远处的一段
	rev := d.rev
	key := d.key

	// 一次轮询已经带着旧版本取了增量（点击之前发出）
	var r pollResult
	var sent []int64
	a.pollDiscussions(&r, func(method string, params, out any) error {
		switch method {
		case "discussion.list":
			return nil
		case "discussion.show":
			p := params.(map[string]any)
			sent = append(sent, p["since"].(int64))
			*(out.(*discView)) = discView{Discussion: d.info, Delta: true, Live: map[string]string{}}
			out.(*discView).Discussion.Rev = rev
		}
		return nil
	})
	if len(sent) != 1 || sent[0] != rev {
		t.Fatalf("first poll since %v", sent)
	}

	a.jumpLatest()
	if _, since := a.wantedDisc(); since != 0 {
		t.Fatalf("jump did not ask for the latest page: since %d", since)
	}
	// 窗口照常出帧：页面不能把版本写回去
	tt.Frame()
	if _, since := a.wantedDisc(); since != 0 {
		t.Fatalf("a frame wrote the old version back: since %d", since)
	}
	// 旧的轮询结果晚到
	a.applyDiscussions(r.disc)
	tt.Frame()
	if _, since := a.wantedDisc(); since != 0 {
		t.Fatalf("stale poll result wrote the old version back: since %d", since)
	}
	// 下一次轮询取全量，换上最新一页
	var r2 pollResult
	a.pollDiscussions(&r2, func(method string, params, out any) error {
		if method == "discussion.show" {
			p := params.(map[string]any)
			if p["since"].(int64) != 0 {
				t.Fatalf("next poll asked for a delta since %v", p["since"])
			}
			*(out.(*discView)) = latestView(key, rev+1)
		}
		return nil
	})
	a.applyDiscussions(r2.disc)
	if a.disc.d.moreAfter || a.disc.d.msgs[len(a.disc.d.msgs)-1].Seq != 150 {
		t.Fatalf("not at the latest page: moreAfter=%v last=%d", a.disc.d.moreAfter, a.disc.d.msgs[len(a.disc.d.msgs)-1].Seq)
	}
	if _, since := a.wantedDisc(); since != rev+1 {
		t.Fatalf("after the full page, polls continue from its version: %d", since)
	}
}

// 切到别的讨论之后，上一个讨论晚到的轮询结果不能覆盖。
func TestDiscussionStalePollAfterSwitch(t *testing.T) {
	a, _, _ := discTester(t)
	old := a.disc.d
	var r pollResult
	a.pollDiscussions(&r, func(method string, params, out any) error {
		if method == "discussion.show" {
			*(out.(*discView)) = discView{Discussion: old.info}
		}
		return nil
	})
	a.selectDiscussion("fp/d_2")
	a.applyDiscussions(r.disc)
	if a.disc.d != nil && a.disc.d.key == old.key {
		t.Fatal("stale result of the previous discussion was shown")
	}
	if key, since := a.wantedDisc(); key != "fp/d_2" || since != 0 {
		t.Fatalf("wanted %s since %d", key, since)
	}
}

// 搜索定位（取以某条为中心的一页）的结果晚于「跳到最新」到达：不能把窗口拉回那一页。
func TestDiscussionStaleAroundPageAfterJumpLatest(t *testing.T) {
	release := make(chan struct{})
	a, tt, _ := discTester(t, map[string]rpc.Handler{"discussion.show": func(ctx context.Context, raw json.RawMessage) (any, error) {
		var p map[string]any
		json.Unmarshal(raw, &p)
		if p["around"] != nil {
			<-release
			v := discView{Discussion: discSummary{Key: p["discussion"].(string)}, MoreAfter: true}
			for i := int64(1); i <= 10; i++ {
				v.Messages = append(v.Messages, discMessage{ID: fmt.Sprintf("o%d", i), Seq: i, Kind: "user", Body: "old"})
			}
			return v, nil
		}
		return nil, rpc.Errorf(rpc.CodeNotFound, "not in this test")
	}})
	key := a.disc.d.key
	a.discJump(a.disc.d, "o5", 5)
	a.jumpLatest()
	// 「跳到最新」的全量结果先到
	a.applyDiscussions(&discPoll{view: ptr(latestView(key, a.disc.d.rev+1)), key: key, gen: a.discGen()})
	close(release)
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) && a.disc.older {
		tt.Frame()
		time.Sleep(10 * time.Millisecond)
	}
	tt.Frame()
	if d := a.disc.d; d.moreAfter || d.msgs[len(d.msgs)-1].Seq != 150 {
		t.Fatalf("late search page replaced the latest page: moreAfter=%v last=%d", d.moreAfter, d.msgs[len(d.msgs)-1].Seq)
	}
}

func ptr[T any](v T) *T { return &v }
