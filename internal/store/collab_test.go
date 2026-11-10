package store

import (
	"errors"
	"fmt"
	"path/filepath"
	"sync"
	"testing"
	"time"
)

// 房主一侧的协作事务：邀请单次使用、事件幂等和冲突、放弃后的迟到结果、撤销后的拒收。
func TestCollabHostTransactions(t *testing.T) {
	s := openTestStore(t)
	d, _ := newDiscussion(t, s)
	if err := s.SetShared(d.Key, true, Profile{Name: "Hana"}, time.Now()); err != nil {
		t.Fatal(err)
	}
	if err := s.CreateInvite(d.Key, "inv1", "h1", "", time.Now().Add(10*time.Minute), time.Now()); err != nil {
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

// 增量被截断后，截断点之后只有成员改动：More 为真，下一批带上改动。
func TestCollabSyncTrailingChange(t *testing.T) {
	s := openTestStore(t)
	d, _ := newDiscussion(t, s)
	m := Member{ID: "dmb_t", Discussion: d.Key, Name: "Alice", Agent: "claude", CreatedAt: time.Now()}
	if err := s.AddMember(m); err != nil {
		t.Fatal(err)
	}
	for i := range 200 {
		if _, _, _, err := s.PostMessage(PostInput{Discussion: d.Key, Body: fmt.Sprintf("m%d", i), At: time.Now()}); err != nil {
			t.Fatal(err)
		}
	}
	m.Name = "Alice renamed"
	if _, err := s.UpdateMember(m); err != nil {
		t.Fatal(err)
	}
	cur, _ := s.Discussion(d.Key)
	b, err := s.SyncSince(d.Key, 0, 200)
	if err != nil {
		t.Fatal(err)
	}
	if !b.More || b.Rev >= cur.Rev {
		t.Fatalf("first batch more=%v rev=%d/%d", b.More, b.Rev, cur.Rev)
	}
	b2, err := s.SyncSince(d.Key, b.Rev, 200)
	if err != nil || b2.More || b2.Rev != cur.Rev || len(b2.Members) != 1 || b2.Members[0].Name != "Alice renamed" {
		t.Fatalf("second batch %+v %v", b2, err)
	}
}

// 未发布的报告路径（房主本机文件）不同步给成员。
func TestCollabSyncStripsReport(t *testing.T) {
	s := openTestStore(t)
	d, _ := newDiscussion(t, s)
	_, _, _, err := s.PostMessage(PostInput{Discussion: d.Key, Body: "x", At: time.Now()})
	if err != nil {
		t.Fatal(err)
	}
	s.db.Exec(`UPDATE disc_messages SET report='/Users/host/secret.md' WHERE discussion=?`, d.Key)
	s.db.Exec(`UPDATE discussions SET rev=rev+1 WHERE key=?`, d.Key)
	b, _ := s.SyncSince(d.Key, 0, 0)
	for _, m := range b.Messages {
		if m.Report != "" {
			t.Fatalf("report path synced: %q", m.Report)
		}
	}
}

// 配额把在传的也算进去：两个并发上传合计不能超额。
func TestCollabQuotaReservesInFlight(t *testing.T) {
	s := openTestStore(t)
	d, _ := newDiscussion(t, s)
	s.SetCollabQuota(150)
	if err := s.BeginUpload(Upload{ID: "u1", Room: d.Key, FP: "fp", Name: "a", Size: 100, SHA: "x"}, 2, time.Now()); err != nil {
		t.Fatal(err)
	}
	if err := s.BeginUpload(Upload{ID: "u2", Room: d.Key, FP: "fp", Name: "b", Size: 100, SHA: "y"}, 2, time.Now()); !errors.Is(err, ErrQuota) {
		t.Fatalf("second in-flight upload over quota: %v", err)
	}
}

// 本机没有确认过的配置（旧版本加的成员）：不猜哪份可信，暂停到拥有者保存；重新启动不解除。
func TestCollabLegacyOwnConfigNeedsConfirm(t *testing.T) {
	s := openTestStore(t)
	s.SetSelf("me")
	room := "hostfp/d_x"
	if err := s.CreateMirror(Discussion{Key: room, Host: "hostfp", LocalID: "d_x", Title: "t", CreatedAt: time.Now()},
		Joined{Room: room, Host: "hostfp", State: JoinedActive, JoinedAt: time.Now()}); err != nil {
		t.Fatal(err)
	}
	m := Member{ID: "dmb_me", Name: "Bob", Agent: "codex", Model: "m1", Owner: "me", Generation: 1, ConfigVersion: 1, CreatedAt: time.Now()}
	b := SyncBatch{Rev: 1, Discussion: SyncDiscussion{Title: "t", Status: DiscussionActive, Shared: true}, Members: []Member{m}}
	if _, err := s.ApplySync(room, "me", b); err != nil {
		t.Fatal(err)
	}
	if got, _ := s.Member(m.ID); got.Paused != NeedsConfirm {
		t.Fatalf("own agent from sync not paused: %q", got.Paused)
	}
	m.WorkspaceID = "w1"
	if err := s.SetOwnConfig(m); err != nil {
		t.Fatal(err)
	}
	if got, _ := s.Member(m.ID); got.Paused != "" {
		t.Fatalf("confirmed agent still paused: %q", got.Paused)
	}
	// 旧版本留下的：own_config 为空
	s.db.Exec(`UPDATE disc_members SET own_config='' WHERE id=?`, m.ID)
	m.Model = "from-host"
	b = SyncBatch{Rev: 2, Discussion: b.Discussion, Members: []Member{m}}
	if _, err := s.ApplySync(room, "me", b); err != nil {
		t.Fatal(err)
	}
	if got, _ := s.Member(m.ID); got.Paused != NeedsConfirm {
		t.Fatalf("legacy agent not paused: %q", got.Paused)
	}
	if _, err := s.ReleaseWorker(m.ID, true); err != nil {
		t.Fatal(err)
	}
	if got, _ := s.Member(m.ID); got.Paused != NeedsConfirm {
		t.Fatalf("restart cleared the confirmation pause: %q", got.Paused)
	}
}

// 成员电脑经房主加 Agent 的应答丢了，用同一个请求 ID 重发：返回已经加好的那个，不加第二个；
// 同一个 ID 换了设置、Agent 已经移除、换了一代资格都报冲突，别人用同样的 ID 互不相干。
func TestCollabAddMemberOnce(t *testing.T) {
	s := openTestStore(t)
	d, _ := newDiscussion(t, s)
	if err := s.SetShared(d.Key, true, Profile{Name: "Hana"}, time.Now()); err != nil {
		t.Fatal(err)
	}
	join := func(inv, fp string) JoinRequest {
		t.Helper()
		if err := s.CreateInvite(d.Key, inv, "h-"+inv, "", time.Now().Add(10*time.Minute), time.Now()); err != nil {
			t.Fatal(err)
		}
		if _, err := s.SubmitJoin(d.Key, "h-"+inv, "rq-"+inv, fp, fp, "", time.Now()); err != nil {
			t.Fatal(err)
		}
		r, err := s.ApproveRequest("rq-"+inv, time.Now())
		if err != nil {
			t.Fatal(err)
		}
		return r
	}
	active := func() (n int) {
		ms, _ := s.Members(d.Key)
		for _, m := range ms {
			if m.RemovedAt == nil {
				n++
			}
		}
		return n
	}
	base := active()
	a := join("inv1", "fpA")
	bob := Member{ID: "dmb_1", Discussion: d.Key, Name: "Bob", Role: "review", Agent: "codex", Owner: "fpA", Generation: a.Generation, CreatedAt: time.Now()}
	if id, dup, err := s.AddMemberOnce(bob, "c1"); err != nil || dup || id != "dmb_1" {
		t.Fatalf("first add: %s %v %v", id, dup, err)
	}
	// 应答丢了，重发同一次添加（房主每次给新 ID）
	again := bob
	again.ID = "dmb_2"
	if id, dup, err := s.AddMemberOnce(again, "c1"); err != nil || !dup || id != "dmb_1" {
		t.Fatalf("resend: %s %v %v", id, dup, err)
	}
	if n := active() - base; n != 1 {
		t.Fatalf("%d agents after a resend", n)
	}
	// 同一个 ID 换了设置：不覆盖、不新加
	changed := again
	changed.ID, changed.Model = "dmb_3", "gpt-5"
	if _, _, err := s.AddMemberOnce(changed, "c1"); !errors.Is(err, ErrAddChanged) {
		t.Fatalf("same id, other settings: %v", err)
	}
	if m, _ := s.Member("dmb_1"); m.Model != "" {
		t.Fatalf("settings overwritten: %+v", m)
	}
	// 别人用同样的 ID：各加各的
	b := join("inv2", "fpB")
	carol := Member{ID: "dmb_4", Discussion: d.Key, Name: "Bob", Role: "review", Agent: "codex", Owner: "fpB", Generation: b.Generation, CreatedAt: time.Now()}
	if id, dup, err := s.AddMemberOnce(carol, "c1"); err != nil || dup || id != "dmb_4" {
		t.Fatalf("other person, same id: %s %v %v", id, dup, err)
	}
	// 移除之后同一个 ID 不把它复活
	if err := s.OwnerMemberRemove(d.Key, "fpA", a.Generation, "dmb_1", time.Now()); err != nil {
		t.Fatal(err)
	}
	again.ID = "dmb_5"
	if _, _, err := s.AddMemberOnce(again, "c1"); !errors.Is(err, ErrAddGone) {
		t.Fatalf("resend after removal: %v", err)
	}
	// 移出再加入是新的一代：上一代的请求 ID 不算数
	if _, err := s.RevokePerson(d.Key, "fpB", PersonRemoved, time.Now()); err != nil {
		t.Fatal(err)
	}
	b2 := join("inv3", "fpB")
	if b2.Generation == b.Generation {
		t.Fatalf("rejoin kept generation %d", b2.Generation)
	}
	carol.ID, carol.Generation = "dmb_6", b2.Generation
	if _, _, err := s.AddMemberOnce(carol, "c1"); !errors.Is(err, ErrAddGone) {
		t.Fatalf("resend from an earlier generation: %v", err)
	}
	if n := active() - base; n != 0 {
		ms, _ := s.Members(d.Key)
		t.Fatalf("%d agents of members still active, want 0: %+v", n, ms)
	}
}

// 旧库（没有 client_id 列和它的唯一索引）打开时补上；同一个请求 ID 并发添加只加一个，重开库后仍认得；
// 别的讨论里同一个 ID 互不相干；旧客户端不带 ID 时照旧每次都加。
func TestCollabAddMemberOnceMigrationAndConcurrency(t *testing.T) {
	path := filepath.Join(t.TempDir(), "old.db")
	s, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	d, _ := newDiscussion(t, s)
	if _, err := s.db.Exec(`DROP INDEX disc_members_client; ALTER TABLE disc_members DROP COLUMN client_id`); err != nil {
		t.Fatal(err)
	}
	s.Close()
	if s, err = Open(path); err != nil {
		t.Fatal(err)
	}
	defer func() { s.Close() }()
	before, _ := s.Members(d.Key)
	var wg sync.WaitGroup
	for i := range 12 {
		wg.Go(func() {
			m := Member{ID: fmt.Sprintf("dmb_c%d", i), Discussion: d.Key, Owner: d.Host, Name: "Bob", Agent: "codex", CreatedAt: time.Now()}
			if _, _, err := s.AddMemberOnce(m, "same"); err != nil {
				t.Error(err)
			}
		})
	}
	wg.Wait()
	if ms, _ := s.Members(d.Key); len(ms) != len(before)+1 {
		t.Fatalf("%d members after 12 concurrent adds with one id, want %d", len(ms), len(before)+1)
	}
	s.Close()
	if s, err = Open(path); err != nil {
		t.Fatal(err)
	}
	m := Member{ID: "dmb_reopen", Discussion: d.Key, Owner: d.Host, Name: "Bob", Agent: "codex", CreatedAt: time.Now()}
	if _, dup, err := s.AddMemberOnce(m, "same"); err != nil || !dup {
		t.Fatalf("after reopening: dup=%v %v", dup, err)
	}
	d2, _ := newDiscussion(t, s)
	m.ID, m.Discussion = "dmb_other_room", d2.Key
	if _, dup, err := s.AddMemberOnce(m, "same"); err != nil || dup {
		t.Fatalf("other discussion: dup=%v %v", dup, err)
	}
	for _, id := range []string{"dmb_legacy1", "dmb_legacy2"} {
		m.ID = id
		if _, dup, err := s.AddMemberOnce(m, ""); err != nil || dup {
			t.Fatalf("no request id: dup=%v %v", dup, err)
		}
	}
}

// 列表带着最近一条 @ 本机这个人的消息的序号：后面又来了别的消息也不丢。
func TestSummaryMentionSeq(t *testing.T) {
	s := openTestStore(t)
	s.SetSelf("me")
	d, _ := newDiscussion(t, s)
	for i, mention := range []string{"", "me", "other", ""} {
		msg, _, _, err := s.PostMessage(PostInput{Discussion: d.Key, Body: fmt.Sprint(i), At: time.Now()})
		if err != nil {
			t.Fatal(err)
		}
		// 加入者的缓存里 @ 本机的消息由同步写入，这里直接改 mention
		s.db.Exec(`UPDATE disc_messages SET mention=? WHERE id=?`, mention, msg.ID)
	}
	ss, err := s.DiscussionSummaries()
	if err != nil {
		t.Fatal(err)
	}
	if len(ss) != 1 || ss[0].MentionSeq != 2 || ss[0].MsgSeq != 4 {
		t.Fatalf("summary %+v", ss)
	}
}

func TestMemberAccess(t *testing.T) {
	if got := NormalizeAccess(" b , a,owner,a,all", "owner"); got != "a,b" {
		t.Fatalf("normalize %q", got)
	}
	m := Member{Owner: "o"}
	if !m.Allows("o", "h") || m.Allows("a", "h") || m.Allows("", "h") {
		t.Fatal("only owner")
	}
	if !(Member{}).Allows("h", "h") {
		t.Fatal("host's own agent")
	}
	m.Access = "a,b"
	if !m.Allows("b", "h") || m.Allows("c", "h") {
		t.Fatal("specific people")
	}
	m.Access = AccessAll
	if !m.Allows("c", "h") || m.Allows("", "h") {
		t.Fatal("everyone")
	}
	// 访问是空时确认过的配置摘要和加访问设置之前一样
	if ownConfig(Member{Name: "n", Role: "r", Agent: "codex"}) != digest("n", "r", "codex", "", "") {
		t.Fatal("ownConfig changed for agents without access")
	}
}

// 房主在同步里把本机的 Agent 改成没有拥有者、放开给所有人：不接受归属变化，暂停，核对不通过；不调度。
func TestSyncCannotTakeOwnAgent(t *testing.T) {
	s := openTestStore(t)
	s.SetSelf("me")
	room := "hostfp/d_x"
	if err := s.CreateMirror(Discussion{Key: room, Host: "hostfp", LocalID: "d_x", Title: "t", CreatedAt: time.Now()},
		Joined{Room: room, Host: "hostfp", State: JoinedActive, Generation: 1, JoinedAt: time.Now()}); err != nil {
		t.Fatal(err)
	}
	if err := s.SetJoined(room, JoinedActive, 1, nil, ""); err != nil {
		t.Fatal(err)
	}
	m := Member{ID: "dmb_me", Name: "Bob", Agent: "codex", Owner: "me", Generation: 1, ConfigVersion: 1, CreatedAt: time.Now()}
	b := SyncBatch{Rev: 1, Discussion: SyncDiscussion{Title: "t", Status: DiscussionActive, Shared: true}, Members: []Member{m}}
	if _, err := s.ApplySync(room, "me", b); err != nil {
		t.Fatal(err)
	}
	m.WorkspaceID = "w1"
	if err := s.SetOwnConfig(m); err != nil {
		t.Fatal(err)
	}
	if !s.OwnConfigHolds(m.ID) {
		t.Fatal("confirmed agent does not hold")
	}
	forged := m
	forged.Owner, forged.Access = "", AccessAll
	b = SyncBatch{Rev: 2, Discussion: b.Discussion, Members: []Member{forged}}
	if _, err := s.ApplySync(room, "me", b); err != nil {
		t.Fatal(err)
	}
	got, _ := s.Member(m.ID)
	if got.Owner != "me" || got.Access != "" || got.Paused != ConfigConflict {
		t.Fatalf("forged ownership applied: %+v", got)
	}
	// 换代际、删了又被房主恢复：都不接受
	regen := m
	regen.Generation = 2
	if _, err := s.ApplySync(room, "me", SyncBatch{Rev: 3, Discussion: b.Discussion, Members: []Member{regen}}); err != nil {
		t.Fatal(err)
	}
	if got, _ := s.Member(m.ID); got.Generation != 1 || got.Paused != ConfigConflict {
		t.Fatalf("generation rewritten: %+v", got)
	}
	gone := time.Now()
	removed := m
	removed.RemovedAt = &gone
	if _, err := s.ApplySync(room, "me", SyncBatch{Rev: 4, Discussion: b.Discussion, Members: []Member{removed}}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.ApplySync(room, "me", SyncBatch{Rev: 5, Discussion: b.Discussion, Members: []Member{m}}); err != nil {
		t.Fatal(err)
	}
	if got, _ := s.Member(m.ID); got.RemovedAt == nil || s.OwnConfigHolds(m.ID) {
		t.Fatalf("removed agent resurrected: %+v", got)
	}
	// 本机经房主删除后马上记下：房主不下发删除标记、照旧同步它，也不复活
	other0 := Member{ID: "dmb_me2", Name: "Bob2", Agent: "codex", Owner: "me", Generation: 1, ConfigVersion: 1, CreatedAt: time.Now()}
	if _, err := s.ApplySync(room, "me", SyncBatch{Rev: 5, Discussion: b.Discussion, Members: []Member{other0}}); err != nil {
		t.Fatal(err)
	}
	other0.WorkspaceID = "w1"
	s.SetOwnConfig(other0)
	if err := s.MarkOwnRemoved(other0.ID, time.Now()); err != nil {
		t.Fatal(err)
	}
	if _, err := s.ApplySync(room, "me", SyncBatch{Rev: 6, Discussion: b.Discussion, Members: []Member{other0}}); err != nil {
		t.Fatal(err)
	}
	if got, _ := s.Member(other0.ID); got.RemovedAt == nil || s.OwnConfigHolds(other0.ID) {
		t.Fatalf("locally removed agent revived: %+v", got)
	}
	// 拥有者为空的成员在加入的讨论里不调度（那是房主的）
	other := Member{ID: "dmb_host", Name: "Alice", Agent: "claude", ConfigVersion: 1, CreatedAt: time.Now()}
	if _, err := s.ApplySync(room, "me", SyncBatch{Rev: 7, Discussion: b.Discussion, Members: []Member{other}}); err != nil {
		t.Fatal(err)
	}
	ms, _ := s.ActiveMembers()
	for _, x := range ms {
		if x.ID == other.ID {
			t.Fatal("host's agent with empty owner scheduled on a member's computer")
		}
	}
}

// 清理邀请：过期又没用过的删掉；过期但用过的（被申请引用）和没过期的留着。
func TestPruneInvites(t *testing.T) {
	s := openTestStore(t)
	d, _ := newDiscussion(t, s)
	if err := s.SetShared(d.Key, true, Profile{Name: "Hana"}, time.Now()); err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	for _, inv := range []struct {
		id      string
		expires time.Time
	}{{"stale", now.Add(-time.Hour)}, {"used", now.Add(-time.Hour)}, {"fresh", now.Add(10 * time.Minute)}} {
		if err := s.CreateInvite(d.Key, inv.id, "h-"+inv.id, "", inv.expires, now.Add(-2*time.Hour)); err != nil {
			t.Fatal(err)
		}
	}
	s.db.Exec(`UPDATE collab_invites SET request='r1' WHERE id='used'`)
	n, err := s.PruneInvites(now)
	if err != nil || n != 1 {
		t.Fatalf("pruned %d, %v", n, err)
	}
	var left []string
	rows, _ := s.db.Query(`SELECT id FROM collab_invites ORDER BY id`)
	for rows.Next() {
		var id string
		rows.Scan(&id)
		left = append(left, id)
	}
	rows.Close()
	if fmt.Sprint(left) != "[fresh used]" {
		t.Fatalf("left %v", left)
	}
}
