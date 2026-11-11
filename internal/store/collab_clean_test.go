package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"
)

const cleanRoom = "host/d1"

// cleanFixture 是一个加入的讨论里本机几轮执行的 outbox：结束了 40 天的（全部交出、被拒收、还有一条没交出）、
// 结束了 10 天的、还在跑的；另有一个讨论已经不在了的房间留下的各种行。
func cleanFixture(t *testing.T) *Store {
	t.Helper()
	s, err := Open(filepath.Join(t.TempDir(), "t.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	now := time.Now()
	exec := func(q string, args ...any) {
		t.Helper()
		if _, err := s.db.Exec(q, args...); err != nil {
			t.Fatalf("%s: %v", q, err)
		}
	}
	exec(`INSERT INTO discussions(key,host,local_id,title,project_id,status,created_at,mirror) VALUES(?,?,?,?,?,?,?,1)`, cleanRoom, "host", "d1", "t", "", DiscussionActive, ms(now))
	exec(`INSERT INTO disc_members(id,discussion,name,agent,workspace_id,created_at) VALUES('m1',?,'a','claude','',?)`, cleanRoom, ms(now))
	event := func(x string, ev OwnerEvent) {
		t.Helper()
		if err := s.withTx(func(tx *sql.Tx) error { return addOutbox(tx, cleanRoom, x, ev) }); err != nil {
			t.Fatal(err)
		}
	}
	reply := strings.Repeat("回复正文", 2000)
	for _, x := range []string{"x_old_done", "x_old_rej", "x_old_pending", "x_recent", "x_live"} {
		exec(`INSERT INTO disc_executions(id,discussion,member,message,state,created_at) VALUES(?,?,'m1',?,'running',?)`, x, cleanRoom, "msg-"+x, ms(now))
		exec(`INSERT INTO collab_accepts(exec,room,state,created_at) VALUES(?,?,'accepted',?)`, x, cleanRoom, ms(now))
		event(x, OwnerEvent{Kind: EventState, State: ExecRunning, At: ms(now)})
	}
	for _, x := range []string{"x_old_done", "x_old_rej", "x_old_pending", "x_recent"} {
		event(x, OwnerEvent{Kind: EventReply, Body: reply + x, Outcome: "done", At: ms(now)})
	}
	event("x_old_done", OwnerEvent{Kind: EventState, State: ExecDone, At: ms(now)})
	event("x_recent", OwnerEvent{Kind: EventState, State: ExecDone, At: ms(now)})
	// 交出去的、被拒收的；x_old_pending 的回复一直没交出去（成员离线很久）
	exec(`UPDATE collab_outbox SET state='sent' WHERE exec IN ('x_old_done','x_recent','x_live')`)
	exec(`UPDATE collab_outbox SET state='sent' WHERE exec='x_old_pending' AND seq=1`)
	if err := s.MarkOutbox("x_old_rej", 1, OutboxRejected, "access revoked"); err != nil {
		t.Fatal(err)
	}
	old, recent := ms(now.Add(-40*24*time.Hour)), ms(now.Add(-10*24*time.Hour))
	exec(`UPDATE disc_executions SET state='done', ended_at=? WHERE id='x_old_done'`, old)
	exec(`UPDATE disc_executions SET state='revoked', ended_at=? WHERE id='x_old_rej'`, old)
	exec(`UPDATE collab_accepts SET state='revoked' WHERE exec='x_old_rej'`)
	exec(`UPDATE disc_executions SET state='interrupted', ended_at=? WHERE id='x_old_pending'`, old)
	exec(`UPDATE disc_executions SET state='done', ended_at=? WHERE id='x_recent'`, recent)

	// 讨论已经不在了的房间
	gone := "host/gone"
	exec(`INSERT INTO collab_outbox(exec,seq,room,kind,payload,digest,state,created_at) VALUES('g1',1,?,'state','{}','d','sent',0)`, gone)
	exec(`INSERT INTO collab_events(exec,seq,room,digest) VALUES('g1',1,?,'d')`, gone)
	exec(`INSERT INTO collab_accepts(exec,room,state,created_at) VALUES('g1',?,'accepted',0)`, gone)
	exec(`INSERT INTO collab_joined(room,host,state,joined_at,updated_at) VALUES(?,'host','active',0,0)`, gone)
	exec(`INSERT INTO collab_people(room,fp,state) VALUES(?,'fp','active')`, gone)
	exec(`INSERT INTO collab_intents(room,client_id,member,digest,created_at) VALUES(?,'c','m','d',0)`, gone)
	// 还在的讨论里房主一侧的去重键
	exec(`INSERT INTO collab_events(exec,seq,room,digest) VALUES('h1',1,?,'d')`, cleanRoom)
	return s
}

type outboxRow struct {
	Exec, Kind, Digest, State, Error string
	Seq                              int64
}

func outboxRows(t *testing.T, s *Store) []outboxRow {
	t.Helper()
	rows, err := s.db.Query(`SELECT exec,seq,kind,digest,state,error FROM collab_outbox ORDER BY exec,seq`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var out []outboxRow
	for rows.Next() {
		var r outboxRow
		rows.Scan(&r.Exec, &r.Seq, &r.Kind, &r.Digest, &r.State, &r.Error)
		out = append(out, r)
	}
	return out
}

// pendingView 是补发时交给房主的内容。两边建库的时刻不同：时间戳去掉，按执行和序号排（PendingOutbox 先按
// 写入时间排，同一执行里的先后和按序号一样）。
func pendingView(t *testing.T, s *Store) string {
	t.Helper()
	items, err := s.PendingOutbox(cleanRoom)
	if err != nil {
		t.Fatal(err)
	}
	var lines []string
	for _, it := range items {
		ev := it.Event
		ev.At = 0
		lines = append(lines, fmt.Sprintf("%s/%03d %s %+v", it.Exec, it.Seq, it.Digest, ev))
	}
	slices.Sort(lines)
	return strings.Join(lines, "\n")
}

// 清理之后补发、序号、状态事件去重、拒收、计数都和不清理时一样；结束 30 天内的、没交出的、每个执行最近一条状态事件不动；
// 讨论不在了的房间的行删掉，还在的讨论的去重键不动；再清一次什么都不改。
func TestCollabCleanKeepsOutboxBehaviour(t *testing.T) {
	a, b := cleanFixture(t), cleanFixture(t)
	n, err := a.PruneCollabOrphans(t.Context())
	if err != nil || n != 6 {
		t.Fatalf("PruneCollabOrphans = %d, %v; want 6", n, err)
	}
	n, err = a.ClearOutboxPayloads(t.Context(), time.Now().Add(-30*24*time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	// x_old_done 的 running 和回复、x_old_rej 被拒收的回复。各执行最近一条状态事件、x_old_pending 没交出的回复、
	// 10 天前结束的和还在跑的都留着
	if n != 3 {
		t.Fatalf("cleared %d rows, want 3", n)
	}
	var cleared []string
	rows, _ := a.db.Query(`SELECT exec||'/'||seq FROM collab_outbox WHERE payload='' ORDER BY exec,seq`)
	for rows.Next() {
		var k string
		rows.Scan(&k)
		cleared = append(cleared, k)
	}
	rows.Close()
	if want := []string{"x_old_done/1", "x_old_done/2", "x_old_rej/2"}; !reflect.DeepEqual(cleared, want) {
		t.Fatalf("cleared %v, want %v", cleared, want)
	}
	for _, q := range []string{
		`SELECT COUNT(*) FROM collab_events WHERE room='host/gone'`,
		`SELECT COUNT(*) FROM collab_joined WHERE room='host/gone'`,
		`SELECT COUNT(*) FROM collab_outbox WHERE room='host/gone'`,
	} {
		var c int
		a.db.QueryRow(q).Scan(&c)
		if c != 0 {
			t.Fatalf("%s = %d after prune", q, c)
		}
	}
	var hostKeys int
	a.db.QueryRow(`SELECT COUNT(*) FROM collab_events WHERE room=?`, cleanRoom).Scan(&hostKeys)
	if hostKeys != 1 {
		t.Fatalf("host dedupe keys of a live room = %d, want 1", hostKeys)
	}
	if n, err := a.ClearOutboxPayloads(t.Context(), time.Now().Add(-30*24*time.Hour)); err != nil || n != 0 {
		t.Fatalf("second clear = %d, %v", n, err)
	}
	if n, err := a.PruneCollabOrphans(t.Context()); err != nil || n != 0 {
		t.Fatalf("second prune = %d, %v", n, err)
	}

	// 两边做同样的事，结果一样
	b.PruneCollabOrphans(t.Context())
	same := func(step string) {
		t.Helper()
		if ra, rb := outboxRows(t, a), outboxRows(t, b); !reflect.DeepEqual(ra, rb) {
			t.Fatalf("%s: outbox rows differ\ncleaned: %v\nuncleaned: %v", step, ra, rb)
		}
		if pa, pb := pendingView(t, a), pendingView(t, b); pa != pb {
			t.Fatalf("%s: pending differ\ncleaned:\n%s\nuncleaned:\n%s", step, pa, pb)
		}
		p1, r1 := a.OutboxCount(cleanRoom)
		p2, r2 := b.OutboxCount(cleanRoom)
		if p1 != p2 || r1 != r2 {
			t.Fatalf("%s: OutboxCount %d/%d vs %d/%d", step, p1, r1, p2, r2)
		}
	}
	same("after clean")
	for _, s := range []*Store{a, b} {
		ev := func(x string, e OwnerEvent) {
			if err := s.withTx(func(tx *sql.Tx) error { return addOutbox(tx, cleanRoom, x, e) }); err != nil {
				t.Fatal(err)
			}
		}
		// 和最近一条状态一样：去重，不加
		ev("x_old_done", OwnerEvent{Kind: EventState, State: ExecDone, At: 1})
		ev("x_old_pending", OwnerEvent{Kind: EventState, State: ExecRunning, At: 1})
		// 不一样：接着最大的序号加
		ev("x_old_done", OwnerEvent{Kind: EventState, State: ExecStopped, At: 1})
		ev("x_old_pending", OwnerEvent{Kind: EventState, State: ExecInterrupted, Reason: "r", At: 1})
		ev("x_live", OwnerEvent{Kind: EventReply, Body: "late", Outcome: "done", At: 1})
		// 撤销了的不再加
		ev("x_old_rej", OwnerEvent{Kind: EventState, State: ExecDone, At: 1})
	}
	same("after new events")
	// 长时间离线的成员回来：按顺序补发，一条被拒收（同一执行后面的一起拒收），其余交出
	for _, s := range []*Store{a, b} {
		items, err := s.PendingOutbox(cleanRoom)
		if err != nil {
			t.Fatal(err)
		}
		for _, it := range items {
			if it.Exec == "x_old_pending" && it.Seq == 2 {
				s.MarkOutbox(it.Exec, it.Seq, OutboxRejected, "conflict")
				continue
			}
			s.MarkOutbox(it.Exec, it.Seq, OutboxSent, "")
		}
	}
	same("after resend")
	var seqs []int64
	rows, _ = a.db.Query(`SELECT seq FROM collab_outbox WHERE exec='x_old_done' ORDER BY seq`)
	for rows.Next() {
		var q int64
		rows.Scan(&q)
		seqs = append(seqs, q)
	}
	rows.Close()
	if !reflect.DeepEqual(seqs, []int64{1, 2, 3, 4}) {
		t.Fatalf("x_old_done seqs = %v, want 1..4", seqs)
	}
}

// 清理在批与批之间歇一下；ctx 结束了在批间停下，下次接着清。
func TestCollabCleanStopsBetweenBatches(t *testing.T) {
	s, err := Open(filepath.Join(t.TempDir(), "t.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	for i := range 1200 {
		if _, err := s.db.Exec(`INSERT INTO collab_outbox(exec,seq,room,kind,payload,digest,state,created_at) VALUES(?,1,'host/gone','state','{}','d','sent',0)`, fmt.Sprint("g", i)); err != nil {
			t.Fatal(err)
		}
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	n, err := s.PruneCollabOrphans(ctx)
	if !errors.Is(err, context.Canceled) || n != collabCleanBatch {
		t.Fatalf("cancelled prune = %d, %v; want one batch and context.Canceled", n, err)
	}
	if n, err := s.PruneCollabOrphans(t.Context()); err != nil || n != 1200-collabCleanBatch {
		t.Fatalf("prune after = %d, %v", n, err)
	}
}
