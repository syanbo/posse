package store

import (
	"errors"
	"sync"
	"testing"
	"time"
)

// 房主一侧的协作事务：邀请单次使用、事件幂等和冲突、放弃后的迟到结果、撤销后的拒收。
func TestCollabHostTransactions(t *testing.T) {
	s := openTestStore(t)
	d, _ := newDiscussion(t, s)
	if err := s.SetShared(d.Key, true, "Hana", time.Now()); err != nil {
		t.Fatal(err)
	}
	if err := s.CreateInvite(d.Key, "inv1", "h1", time.Now().Add(10*time.Minute), time.Now()); err != nil {
		t.Fatal(err)
	}
	// 两份申请抢同一份邀请：只有一份成功
	var wg sync.WaitGroup
	var oks int
	var mu sync.Mutex
	for i, fp := range []string{"fpA", "fpB"} {
		wg.Go(func() {
			if _, err := s.SubmitJoin(d.Key, "h1", "rq"+fp, fp, "p"+string(rune('0'+i)), "", time.Now()); err == nil {
				mu.Lock()
				oks++
				mu.Unlock()
			}
		})
	}
	wg.Wait()
	if oks != 1 {
		t.Fatalf("%d joins with one invite", oks)
	}
	reqs, _ := s.JoinRequests(d.Key, true)
	r, err := s.ApproveRequest(reqs[0].ID, time.Now())
	if err != nil || r.Generation != 1 {
		t.Fatalf("approve %+v %v", r, err)
	}
	if again, _ := s.ApproveRequest(r.ID, time.Now()); again.Generation != 1 {
		t.Fatal("approving twice bumped the generation")
	}
	fp := r.FP
	m := Member{ID: "dmb_remote", Discussion: d.Key, Name: "Bob", Agent: "codex", Owner: fp, Generation: 1, CreatedAt: time.Now()}
	if err := s.AddMember(m); err != nil {
		t.Fatal(err)
	}
	_, x, _, err := s.PostMessage(PostInput{Discussion: d.Key, Body: "go", Target: m.ID, ClientID: "c1", Author: fp, AuthorName: "Bob's owner", Generation: 1, At: time.Now()})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.HostClaim(d.Key, fp, 1, x.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := s.HostClaim(d.Key, fp, 1, x.ID); err != nil {
		t.Fatal("repeated claim should be idempotent:", err)
	}
	if _, err := s.HostClaim(d.Key, "fpOther", 1, x.ID); err == nil {
		t.Fatal("another person claimed")
	}
	ev := OwnerEvent{Kind: EventState, State: ExecRunning}
	he := HostEvent{Room: d.Key, FP: fp, Gen: 1, Exec: x.ID, Seq: 1, Event: ev, Digest: EventDigest(ev)}
	if err := s.ApplyOwnerEvent(he); err != nil {
		t.Fatal(err)
	}
	if err := s.ApplyOwnerEvent(he); err != nil {
		t.Fatal("resent event should be accepted:", err)
	}
	other := OwnerEvent{Kind: EventState, State: ExecFailed}
	if err := s.ApplyOwnerEvent(HostEvent{Room: d.Key, FP: fp, Gen: 1, Exec: x.ID, Seq: 1, Event: other, Digest: EventDigest(other)}); !errors.Is(err, ErrConflict) {
		t.Fatalf("same seq, different content: %v", err)
	}
	// 放弃这一轮、另发起：迟到的结果只补到旧轮次
	nx, err := s.OwnerRetry(d.Key, fp, 1, x.ID, "x_new", true, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	late := OwnerEvent{Kind: EventReply, Body: "late but real", Outcome: TaskDone}
	if err := s.ApplyOwnerEvent(HostEvent{Room: d.Key, FP: fp, Gen: 1, Exec: x.ID, Seq: 2, Event: late, Digest: EventDigest(late)}); err != nil {
		t.Fatal(err)
	}
	if old, _ := s.Execution(x.ID); old.State != ExecDone || old.Reply == "" {
		t.Fatalf("old round %+v", old)
	}
	if n, _ := s.Execution(nx.ID); n.State != ExecQueued || n.RetryOf != x.ID {
		t.Fatalf("new round %+v", n)
	}
	// 撤销：执行记访问已撤销，之后的事件、发言都拒收
	if _, err := s.RevokePerson(d.Key, fp, PersonRemoved, time.Now()); err != nil {
		t.Fatal(err)
	}
	if n, _ := s.Execution(nx.ID); n.State != ExecRevoked {
		t.Fatalf("after revoke %s", n.State)
	}
	ev3 := OwnerEvent{Kind: EventState, State: ExecRunning}
	if err := s.ApplyOwnerEvent(HostEvent{Room: d.Key, FP: fp, Gen: 1, Exec: nx.ID, Seq: 1, Event: ev3, Digest: EventDigest(ev3)}); !errors.Is(err, ErrRevoked) {
		t.Fatalf("event after revoke: %v", err)
	}
	if _, _, _, err := s.PostMessage(PostInput{Discussion: d.Key, Body: "x", ClientID: "c2", Author: fp, Generation: 1, At: time.Now()}); !errors.Is(err, ErrRevoked) {
		t.Fatalf("post after revoke: %v", err)
	}
	// 撤销后的执行不挡归档
	if err := s.SetArchived(d.Key, true, time.Now()); err != nil {
		t.Fatal(err)
	}
}
