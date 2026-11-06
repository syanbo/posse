package app

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/egoist/mygo/ui"

	"posse/internal/client"
	"posse/internal/rpc"
)

// fakeDaemon 记下窗口发来的讨论操作；别的方法没有处理函数，轮询报错不影响这里看的东西。
type fakeDaemon struct {
	mu    sync.Mutex
	calls []fakeCall
}

type fakeCall struct {
	method string
	params map[string]any
}

func (f *fakeDaemon) count() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.calls)
}

func (f *fakeDaemon) find(method string) (map[string]any, bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, c := range f.calls {
		if c.method == method {
			return c.params, true
		}
	}
	return nil, false
}

func startFakeDaemon(t *testing.T, extra ...map[string]rpc.Handler) (*fakeDaemon, *client.Client) {
	t.Helper()
	dir, err := os.MkdirTemp("/tmp", "pd")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })
	sock := filepath.Join(dir, "s.sock")
	ln, err := net.Listen("unix", sock)
	if err != nil {
		t.Fatal(err)
	}
	f := &fakeDaemon{}
	srv := rpc.NewServer()
	for _, m := range []string{"discussion.post", "discussion.answer", "discussion.confirm", "discussion.retry", "discussion.cancel", "discussion.key",
		"discussion.member.restart", "discussion.archive", "discussion.isolate"} {
		srv.Handle(m, func(ctx context.Context, raw json.RawMessage) (any, error) {
			var p map[string]any
			json.Unmarshal(raw, &p)
			f.mu.Lock()
			f.calls = append(f.calls, fakeCall{m, p})
			f.mu.Unlock()
			return map[string]any{"message": map[string]any{"seq": 10}}, nil
		})
	}
	for _, hs := range extra {
		for m, h := range hs {
			srv.Handle(m, func(ctx context.Context, raw json.RawMessage) (any, error) {
				var p map[string]any
				json.Unmarshal(raw, &p)
				f.mu.Lock()
				f.calls = append(f.calls, fakeCall{m, p})
				f.mu.Unlock()
				return h(ctx, raw)
			})
		}
	}
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	go srv.Serve(ctx, ln)
	return f, &client.Client{Home: dir, Socket: sock}
}

// waitCall 一边出帧（跑完后台协程交回主线程的更新）一边等窗口发出这个调用。
func waitCall(t *testing.T, tt *ui.Tester, f *fakeDaemon, method string) map[string]any {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if p, ok := f.find(method); ok {
			return p
		}
		tt.Frame()
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("window did not call %s; calls: %+v", method, f.calls)
	return nil
}

func discTester(t *testing.T, extra ...map[string]rpc.Handler) (*app, *ui.Tester, *fakeDaemon) {
	t.Helper()
	f, c := startFakeDaemon(t, extra...)
	a := previewApp(t)
	previewDiscussion(a)
	a.c = c
	a.disc.target, a.disc.quote, a.disc.panel = "", "", false
	// 成员现在的名字和历史消息里的 @ 快照不同，菜单项和消息上的标签分得开
	a.disc.d.members[1].Name = "Codex 复核"
	// 暂停的成员还有一条排队的请求：输入框上面给「重新启动并继续」
	x := a.disc.d.execs["x4"]
	x.ID, x.State, x.Reason, x.CreatedAt = "x6", "queued", "", time.Now()
	a.disc.d.execs["x6"] = x
	// 窗口够高，所有消息都在可见区里，点得到
	tt := newTester(a.build, 1400, 3200)
	for range 3 {
		tt.Frame()
	}
	return a, tt, f
}

// 选接收者、打字、⌘↩ 发送：发出的请求带着成员 ID 和客户端请求 ID；发送失败重试沿用同一个 ID。
func TestDiscussionComposerSendsToMember(t *testing.T) {
	a, tt, f := discTester(t)
	if err := tt.Click("所有人"); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	if err := tt.Click("@Codex 复核"); err != nil {
		t.Fatalf("recipient menu: %v; texts %v", err, tt.Texts())
	}
	tt.Frame()
	if a.disc.target != "mb2" {
		t.Fatalf("target %q", a.disc.target)
	}
	if err := tt.Click("给 Codex 复核 的请求（⌘↩ 发送）"); err != nil {
		t.Fatal(err)
	}
	tt.Type("请复核方案")
	tt.Key(ui.Cmd, ui.KeyEnter)
	p := waitCall(t, tt, f, "discussion.post")
	if p["target"] != "mb2" || p["body"] != "请复核方案" || p["discussion"] != "fp/d_1" || p["clientId"] == "" {
		t.Fatalf("post params %+v", p)
	}
	for range 5 {
		tt.Frame()
	}
	if a.disc.text != "" || a.disc.clientID != "" {
		t.Fatalf("draft not cleared after send: %q %q", a.disc.text, a.disc.clientID)
	}
}

// 「回答…」切到回答模式，发出的是 discussion.answer，绑定问题；普通发言不会被当作答案。
func TestDiscussionAnswerFromMessage(t *testing.T) {
	a, tt, f := discTester(t)
	if err := tt.Click("回答…"); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	if a.disc.answerQ != "q1" || !tt.HasText("回答 Codex 审查 的问题") {
		t.Fatalf("answer mode: %q", a.disc.answerQ)
	}
	if err := tt.Click("你的回答"); err != nil {
		t.Fatal(err)
	}
	tt.Type("保留")
	tt.Key(ui.Cmd, ui.KeyEnter)
	p := waitCall(t, tt, f, "discussion.answer")
	if p["question"] != "q1" || p["body"] != "保留" {
		t.Fatalf("answer params %+v", p)
	}
	if _, ok := f.find("discussion.post"); ok {
		t.Fatal("answer went out as a plain message")
	}
}

// 执行卡上的按钮：同目录待确认的「仍然继续」、失败一轮的「重试」、暂停队列的「重新启动并继续」。
func TestDiscussionExecCardButtons(t *testing.T) {
	_, tt, f := discTester(t)
	if err := tt.Click("仍然继续"); err != nil {
		t.Fatalf("%v; texts %v", err, tt.Texts())
	}
	if p := waitCall(t, tt, f, "discussion.confirm"); p["execution"] != "x5" {
		t.Fatalf("confirm %+v", p)
	}
	if err := tt.Click("重试"); err != nil {
		t.Fatal(err)
	}
	if p := waitCall(t, tt, f, "discussion.retry"); p["execution"] != "x3" && p["execution"] != "x4" {
		t.Fatalf("retry %+v", p)
	}
	if err := tt.Click("重新启动并继续"); err != nil {
		t.Fatal(err)
	}
	if p := waitCall(t, tt, f, "discussion.member.restart"); p["member"] != "mb1" {
		t.Fatalf("restart %+v", p)
	}
}

// 右键引用一条消息，再发送：请求带着引用；有进行中的请求时归档先列出要处理的请求，不发归档。
func TestDiscussionQuoteAndArchiveBlocked(t *testing.T) {
	a, tt, f := discTester(t)
	// 不悬停时没有引用按钮（不留看不见又点不动的按钮）；悬停在这条消息上才出现它自己的那一个
	if tt.HasText("引用") {
		t.Fatal("hover buttons exist without hover")
	}
	r, ok := tt.Find("先读一下 LoginView.swift，列出要改的地方。")
	if !ok {
		t.Fatal("message not shown")
	}
	tt.Move(r.X+20, r.Y+r.H/2)
	tt.Frame()
	if err := tt.Click("引用"); err != nil {
		t.Fatalf("%v; texts %v", err, tt.Texts())
	}
	tt.Frame()
	if a.disc.quote != "m2" {
		t.Fatalf("quote %q", a.disc.quote)
	}
	if err := tt.Click("消息（⌘↩ 发送）"); err != nil {
		t.Fatal(err)
	}
	tt.Type("同意")
	tt.Key(ui.Cmd, ui.KeyEnter)
	if p := waitCall(t, tt, f, "discussion.post"); p["quote"] != "m2" || p["target"] != "" {
		t.Fatalf("post %+v", p)
	}
	if err := tt.Click("更多"); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	if err := tt.Click("归档"); err != nil {
		t.Fatalf("%v; texts %v", err, tt.Texts())
	}
	tt.Frame()
	if !tt.HasText("还不能归档") {
		t.Fatalf("archive not blocked; texts %v", tt.Texts())
	}
	if _, ok := f.find("discussion.archive"); ok {
		t.Fatal("archive sent with active requests")
	}
}

// msgPages 是假常驻进程的 discussion.show：总共 n 条旧消息（序号 1..n），按 before/after/around 给一页。
func msgPages(n int) rpc.Handler {
	return func(ctx context.Context, raw json.RawMessage) (any, error) {
		var p struct{ Before, After, Around, Limit int64 }
		json.Unmarshal(raw, &p)
		var lo, hi int64
		switch {
		case p.Before > 0:
			lo, hi = max(1, p.Before-p.Limit), p.Before-1
		case p.After > 0:
			lo, hi = p.After+1, p.After+p.Limit
		case p.Around > 0:
			lo = max(1, p.Around-p.Limit/2)
			hi = lo + p.Limit - 1
		}
		hi = min(hi, int64(n))
		var msgs []discMessage
		for seq := lo; seq <= hi; seq++ {
			msgs = append(msgs, discMessage{ID: fmt.Sprintf("old%d", seq), Seq: seq, Kind: "reply", AuthorName: "Claude 方案", AuthorAgent: "claude",
				Body: fmt.Sprintf("旧消息 %d\n\n- 第一点\n- 第二点", seq), CreatedAt: time.Now().Add(-48 * time.Hour)})
		}
		v := discView{Messages: msgs}
		if len(msgs) > 0 {
			v.More = msgs[0].Seq > 1
			v.MoreAfter = msgs[len(msgs)-1].Seq < int64(n)
		}
		return v, nil
	}
}

// shiftSeqs 让读进来的几页从序号 101 开始，前面还有 100 条没读进来。
func shiftSeqs(a *app) {
	for i := range a.disc.d.msgs {
		a.disc.d.msgs[i].Seq += 100
	}
	a.disc.d.more = true
}

// 搜索命中还没读进来的旧消息：先往前取到它，再定位、高亮。
func TestDiscussionSearchJumpsToOlder(t *testing.T) {
	hit := discMessage{ID: "old20", Seq: 20, Kind: "user", Body: "旧消息 20", CreatedAt: time.Now().Add(-48 * time.Hour)}
	a, tt, f := discTester(t, map[string]rpc.Handler{
		"discussion.search": func(context.Context, json.RawMessage) (any, error) { return []discMessage{hit}, nil },
		"discussion.show":   msgPages(100),
	})
	shiftSeqs(a)
	tt.Frame()
	if err := tt.Click("搜索"); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	tt.Type("旧消息 20")
	tt.Key(0, ui.KeyEnter)
	waitCall(t, tt, f, "discussion.search")
	for range 5 {
		tt.Frame()
	}
	if len(a.disc.hits) != 1 {
		t.Fatalf("hits %+v", a.disc.hits)
	}
	if err := tt.Click("旧消息 20"); err != nil {
		t.Fatal(err)
	}
	p := waitCall(t, tt, f, "discussion.show")
	if p["around"] != float64(20) {
		t.Fatalf("jump request %+v", p)
	}
	deadline := time.Now().Add(3 * time.Second)
	for a.disc.d.msg("old20") == nil && time.Now().Before(deadline) {
		tt.Frame()
		time.Sleep(10 * time.Millisecond)
	}
	tt.Frame()
	if a.disc.d.msg("old20") == nil || a.disc.revealed != "old20" || a.disc.flash != "old20" {
		t.Fatalf("not jumped: loaded %v revealed %q flash %q", a.disc.d.msg("old20") != nil, a.disc.revealed, a.disc.flash)
	}
	tt.Frame()
	if !tt.HasText("旧消息 20") {
		t.Fatal("jumped message not shown")
	}
}

// 「加载更早的消息」：取前一页插到前面，停在原来最上面那条（不跳到底）。
func TestDiscussionLoadOlderKeepsPlace(t *testing.T) {
	a, tt, f := discTester(t, map[string]rpc.Handler{"discussion.show": msgPages(100)})
	shiftSeqs(a)
	tt.Frame()
	first := a.disc.d.msgs[0].ID
	if err := tt.Click("加载更早的消息"); err != nil {
		t.Fatal(err)
	}
	p := waitCall(t, tt, f, "discussion.show")
	if p["before"] != float64(101) || p["limit"] != float64(discPage) {
		t.Fatalf("page request %+v", p)
	}
	deadline := time.Now().Add(3 * time.Second)
	for a.disc.d.msgs[0].ID == first && time.Now().Before(deadline) {
		tt.Frame()
		time.Sleep(10 * time.Millisecond)
	}
	if n := len(a.disc.d.msgs); n != 9+discPage {
		t.Fatalf("%d messages after one page", n)
	}
	tt.Frame()
	// 测试窗口放得下全部消息，follow 会照实变回 true；这里只看原来最上面那条被滚回视野、不高亮
	if a.disc.revealed != first || a.disc.flash != "" {
		t.Fatalf("reading place: revealed %q flash %q", a.disc.revealed, a.disc.flash)
	}
	if !a.disc.d.more {
		t.Fatal("more pages lost")
	}
}

// 已归档讨论清理附件：先列出文件和大小，取消不动文件；确认后移到废纸篓（只用这里建的临时文件），消息里的附件信息还在。
func TestDiscussionCleanAttachments(t *testing.T) {
	// 照常驻进程的布局：attachments/<附件 ID>/<文件名>
	dir := filepath.Join(t.TempDir(), "attachments")
	var files []string
	for i, body := range []string{"一", "二二"} {
		p := filepath.Join(dir, fmt.Sprint("a", i), fmt.Sprintf("posse-disc-clean-%d-%d.txt", time.Now().UnixNano(), i))
		os.MkdirAll(filepath.Dir(p), 0o755)
		os.WriteFile(p, []byte(body), 0o644)
		files = append(files, p)
	}
	a, tt, f := discTester(t, map[string]rpc.Handler{
		"discussion.attachments": func(context.Context, json.RawMessage) (any, error) {
			var items []map[string]any
			for i, p := range files {
				_, err := os.Stat(p)
				items = append(items, map[string]any{"id": fmt.Sprint("a", i), "name": filepath.Base(p), "size": 3, "abs": p, "exists": err == nil})
			}
			return map[string]any{"dir": dir, "items": items}, nil
		},
	})
	a.disc.d.info.Status = "archived"
	a.disc.list[0].Status = "archived"
	// 测试里没有主线程的消息循环，换掉移废纸篓的函数：平时挪进临时目录，
	// POSSE_TRASH_TEST=1 时用系统的 trash 命令真的移到废纸篓（只有这里建的临时文件）
	bin := t.TempDir()
	a.trashItem = func(p string) error { return os.Rename(p, filepath.Join(bin, filepath.Base(p))) }
	if os.Getenv("POSSE_TRASH_TEST") == "1" {
		a.trashItem = func(p string) error { return exec.Command("/usr/bin/trash", p).Run() }
	}
	open := func() {
		t.Helper()
		f.mu.Lock()
		f.calls = nil
		f.mu.Unlock()
		tt.Frame()
		if err := tt.Click("更多"); err != nil {
			t.Fatal(err)
		}
		tt.Frame()
		if err := tt.Click("清理附件…"); err != nil {
			t.Fatalf("%v; texts %v", err, tt.Texts())
		}
		waitCall(t, tt, f, "discussion.attachments")
		deadline := time.Now().Add(3 * time.Second)
		for !a.confirming && time.Now().Before(deadline) {
			tt.Frame()
			time.Sleep(10 * time.Millisecond)
		}
		tt.Frame()
		if !tt.HasText("把 2 个附件（6 B）移到废纸篓？") {
			t.Fatalf("confirm not shown; texts %v", tt.Texts())
		}
	}
	open()
	if err := tt.Click("取消"); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	for _, p := range files {
		if _, err := os.Stat(p); err != nil {
			t.Fatalf("cancel removed %s", p)
		}
	}
	open()
	if err := tt.Click("移到废纸篓"); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		tt.Frame()
		gone := 0
		for _, p := range files {
			if _, err := os.Stat(p); err != nil {
				gone++
			}
		}
		if gone == len(files) {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	for _, p := range files {
		if _, err := os.Stat(p); err == nil {
			t.Fatalf("%s still there: %s", p, a.err)
		}
	}
	// 附件目录整个移走，attachments/ 空了也移走，不留空目录
	if _, err := os.Stat(dir); err == nil {
		t.Fatalf("attachments dir left behind: %s", a.err)
	}
	if len(a.disc.d.msgs[3].Attachments) != 1 {
		t.Fatal("attachment info dropped from the message")
	}
}

// 远处的搜索结果（1200 条里的第 5 条）：直接取以它为中心的一页，读进来的不超过窗口；给「跳到最新」。
func TestDiscussionJumpFar(t *testing.T) {
	a, tt, f := discTester(t, map[string]rpc.Handler{"discussion.show": msgPages(1200)})
	for i := range a.disc.d.msgs {
		a.disc.d.msgs[i].Seq += 1200
	}
	a.disc.d.more = true
	tt.Frame()
	a.discJump(a.disc.d, "old5", 5)
	if p := waitCall(t, tt, f, "discussion.show"); p["around"] != float64(5) {
		t.Fatalf("request %+v", p)
	}
	deadline := time.Now().Add(3 * time.Second)
	for a.disc.d.msg("old5") == nil && time.Now().Before(deadline) {
		tt.Frame()
		time.Sleep(10 * time.Millisecond)
	}
	tt.Frame()
	d := a.disc.d
	if d.msg("old5") == nil || a.disc.revealed != "old5" || !d.moreAfter || len(d.msgs) > discWindow {
		t.Fatalf("far jump: loaded %v revealed %q moreAfter %v len %d", d.msg("old5") != nil, a.disc.revealed, d.moreAfter, len(d.msgs))
	}
	if !tt.HasText("跳到最新") || !tt.HasText("加载更新的消息") {
		t.Fatal("no way back to the latest messages")
	}
	// 看着远处时来的新消息不接上来
	d.merge(discView{Discussion: d.info, Messages: []discMessage{{ID: "new", Seq: 5000, Kind: "user", Body: "new"}}})
	if d.msg("new") != nil {
		t.Fatal("new message appended to an earlier window")
	}
}

// 一直往前翻：读进来的消息不超过 discWindow，丢掉的那头的 Markdown 缓存一起放掉，之后能往后翻回来。
func TestDiscussionWindowBounded(t *testing.T) {
	a, tt, f := discTester(t, map[string]rpc.Handler{"discussion.show": msgPages(2000)})
	for i := range a.disc.d.msgs {
		a.disc.d.msgs[i].Seq += 2000
	}
	a.disc.d.more = true
	tt.Frame()
	for range 14 {
		before := f.count()
		a.loadOlder(a.disc.d.key)
		deadline := time.Now().Add(3 * time.Second)
		for (f.count() == before || a.disc.older) && time.Now().Before(deadline) {
			tt.Frame()
			time.Sleep(5 * time.Millisecond)
		}
		tt.Frame()
	}
	d := a.disc.d
	if len(d.msgs) != discWindow || !d.moreAfter || !d.more {
		t.Fatalf("window: %d msgs, more %v, moreAfter %v", len(d.msgs), d.more, d.moreAfter)
	}
	if len(d.md) > len(d.msgs) {
		t.Fatalf("markdown cache kept %d entries for %d messages", len(d.md), len(d.msgs))
	}
	for id := range d.md {
		if d.msg(id) == nil {
			t.Fatalf("markdown cache kept a dropped message %s", id)
		}
	}
	last := d.msgs[len(d.msgs)-1].Seq
	a.loadNewer(d.key)
	deadline := time.Now().Add(3 * time.Second)
	for d.msgs[len(d.msgs)-1].Seq == last && time.Now().Before(deadline) {
		tt.Frame()
		time.Sleep(5 * time.Millisecond)
	}
	if d.msgs[len(d.msgs)-1].Seq != last+discPage || len(d.msgs) != discWindow {
		t.Fatalf("after newer: last %d len %d", d.msgs[len(d.msgs)-1].Seq, len(d.msgs))
	}
}

// 执行卡的「打开终端」按 worker 终端的实时状态：终端不在了就不给。
func TestDiscussionLiveTerminal(t *testing.T) {
	a, tt, _ := discTester(t)
	n := countText(tt, "打开终端")
	if n == 0 {
		t.Fatal("no open-terminal button")
	}
	a.disc.d.live = map[string]string{"t9": "gone", "t5": "working"}
	tt.Frame()
	// t9（已完成，一行里的链接）和 t5（进行中，卡片上的按钮）各有一个；关掉 t9 后只剩 t5 那张的
	if m := countText(tt, "打开终端"); m == 0 || m >= n {
		t.Fatalf("open-terminal buttons %d → %d after t9 closed", n, m)
	}
}

// 粘贴图片写的临时文件：发出去或从草稿去掉后删掉；别的文件不碰。
func TestRemovePasted(t *testing.T) {
	pasted := filepath.Join(os.TempDir(), fmt.Sprintf("%s%d.png", pastePrefix, time.Now().UnixNano()))
	other := filepath.Join(t.TempDir(), pastePrefix+"x.png")
	os.WriteFile(pasted, []byte("x"), 0o600)
	os.WriteFile(other, []byte("x"), 0o600)
	removePasted([]string{pasted, other})
	if _, err := os.Stat(pasted); err == nil {
		t.Fatal("pasted temp file kept")
	}
	if _, err := os.Stat(other); err != nil {
		t.Fatal("removed a file that was not pasted")
	}
}

// 加成员失败（加入的讨论里房主没回应）：表单和请求 ID 都留着，原样重试用同一个 ID；成功才关掉，新开表单换 ID。
func TestDiscussionMemberFormKeepsRequestOnFailure(t *testing.T) {
	var mu sync.Mutex
	var ids []string
	fail := true
	a, tt, _ := discTester(t, map[string]rpc.Handler{"discussion.member.add": func(ctx context.Context, raw json.RawMessage) (any, error) {
		var p struct{ ClientID string }
		json.Unmarshal(raw, &p)
		mu.Lock()
		defer mu.Unlock()
		ids = append(ids, p.ClientID)
		if fail {
			fail = false
			return nil, rpc.Errorf(rpc.CodeConflict, "the host did not answer (connection to the host closed); it may or may not have done this")
		}
		return map[string]any{"id": "dmb_new"}, nil
	}})
	submit := func() {
		t.Helper()
		if err := tt.Click("添加"); err != nil {
			t.Fatalf("%v; texts %v", err, tt.Texts())
		}
		deadline := time.Now().Add(3 * time.Second)
		for tt.Frame(); a.disc.form.busy && time.Now().Before(deadline); tt.Frame() {
			time.Sleep(5 * time.Millisecond)
		}
		tt.Frame()
	}
	a.openMemberForm(a.disc.d, "")
	a.disc.form.name = "Win Agent"
	tt.Frame()
	submit()
	if !a.disc.form.open || a.disc.form.name != "Win Agent" || a.err == "" {
		t.Fatalf("form after a failure: open=%v name=%q err=%q", a.disc.form.open, a.disc.form.name, a.err)
	}
	submit()
	if a.disc.form.open {
		t.Fatal("form still open after success")
	}
	mu.Lock()
	got := slices.Clone(ids)
	mu.Unlock()
	if len(got) != 2 || got[0] == "" || got[0] != got[1] {
		t.Fatalf("request ids %v, want the same one twice", got)
	}
	first := a.disc.form.client
	a.openMemberForm(a.disc.d, "")
	if a.disc.form.client == "" || a.disc.form.client == first {
		t.Fatalf("new form reused request id %q", first)
	}
}

// 提交了还没结果时表单不能改；结果回来时表单若已经不是发出去的那份（比如没被禁用的途径改了），成功也不关，
// 免得关掉没发出去的修改。
func TestDiscussionMemberFormLockedWhileSubmitting(t *testing.T) {
	release := make(chan struct{})
	a, tt, _ := discTester(t, map[string]rpc.Handler{"discussion.member.add": func(ctx context.Context, raw json.RawMessage) (any, error) {
		<-release
		return map[string]any{"id": "dmb_new"}, nil
	}})
	a.openMemberForm(a.disc.d, "")
	a.disc.form.name = "Win Agent"
	tt.Frame()
	if err := tt.Click("添加"); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	if !a.disc.form.busy {
		t.Fatal("not submitting")
	}
	// 提交期间输入框禁用：点职责框打字改不了
	if err := tt.Click("在讨论里的职责（可选），如：负责安全审查"); err != nil {
		t.Fatal(err)
	}
	tt.Type("edited")
	tt.Frame()
	if a.disc.form.role != "" {
		t.Fatalf("role changed while submitting: %q", a.disc.form.role)
	}
	// 绕过界面改了表单：成功回来也不关，改动留在表单里
	a.disc.form.role = "changed after sending"
	close(release)
	deadline := time.Now().Add(3 * time.Second)
	for tt.Frame(); a.disc.form.busy && time.Now().Before(deadline); tt.Frame() {
		time.Sleep(5 * time.Millisecond)
	}
	if !a.disc.form.open || a.disc.form.role != "changed after sending" {
		t.Fatalf("late success closed a form holding unsent changes: open=%v role=%q", a.disc.form.open, a.disc.form.role)
	}
}

// 在输入框里打 @ 弹出候选：上下选、回车选中自己的 Agent，@ 和打的字从正文里去掉、接收者换成它；Esc 关掉后不再弹。
func TestDiscussionAtSuggestions(t *testing.T) {
	a, tt, f := discTester(t)
	if err := tt.Click("消息（⌘↩ 发送）"); err != nil {
		t.Fatal(err)
	}
	// 候选弹出来时名字比平时多出现一次
	base := map[string]int{"Claude 方案": countText(tt, "Claude 方案"), "Codex 复核": countText(tt, "Codex 复核")}
	tt.Type("请 @co")
	tt.Frame()
	if countText(tt, "Codex 复核") != base["Codex 复核"]+1 || countText(tt, "Claude 方案") != base["Claude 方案"] {
		t.Fatalf("suggestions after @co: %v", tt.Texts())
	}
	tt.Key(0, ui.KeyEnter)
	for range 3 {
		tt.Frame()
	}
	if a.disc.target != "mb2" || a.disc.text != "请 " {
		t.Fatalf("after pick: target %q text %q", a.disc.target, a.disc.text)
	}
	tt.Type("看下")
	tt.Key(ui.Cmd, ui.KeyEnter)
	if p := waitCall(t, tt, f, "discussion.post"); p["target"] != "mb2" || p["body"] != "请 看下" {
		t.Fatalf("post params %+v", p)
	}
	for range 3 {
		tt.Frame()
	}
	// Esc 关掉这一次 @，接着打字也不再弹；新打一个 @ 又弹
	tt.Type("@")
	tt.Frame()
	if countText(tt, "Claude 方案") != base["Claude 方案"]+1 {
		t.Fatalf("suggestions after @: %v", tt.Texts())
	}
	tt.Key(0, ui.KeyEscape)
	tt.Type("c")
	tt.Frame()
	if countText(tt, "Claude 方案") != base["Claude 方案"] {
		t.Fatal("suggestions reopened after Esc")
	}
	tt.Type(" @")
	tt.Frame()
	if countText(tt, "Claude 方案") != base["Claude 方案"]+1 {
		t.Fatalf("suggestions after new @: %v", tt.Texts())
	}
}

// 别人的 Agent：拥有者放开给你时 @ 候选里作为请求执行；没放开时选它只提醒拥有者，名字留在正文里。
func TestDiscussionAtOthersAgent(t *testing.T) {
	a, tt, f := discTester(t)
	previewCollab(a, true)
	for range 2 {
		tt.Frame()
	}
	if err := tt.Click("消息（⌘↩ 发送）"); err != nil {
		t.Fatal(err)
	}
	tt.Type("@Codex")
	tt.Frame()
	if tt.HasText("提醒 Mo 安排") {
		t.Fatalf("allowed agent shown as owner-only: %v", tt.Texts())
	}
	tt.Key(0, ui.KeyEnter)
	for range 3 {
		tt.Frame()
	}
	if a.disc.target != "mb2" {
		t.Fatalf("target %q", a.disc.target)
	}
	tt.Type("看下")
	tt.Key(ui.Cmd, ui.KeyEnter)
	if p := waitCall(t, tt, f, "discussion.post"); p["target"] != "mb2" {
		t.Fatalf("post %+v", p)
	}
	for range 3 {
		tt.Frame()
	}

	// 收回之后：提示提醒拥有者，选中后 @ 拥有者，名字留在正文里
	a.disc.d.members[1].Access = ""
	tt.Type("@Codex")
	tt.Frame()
	if !tt.HasText("提醒 Mo 安排") {
		t.Fatalf("owner hint missing: %v", tt.Texts())
	}
	tt.Key(0, ui.KeyEnter)
	for range 3 {
		tt.Frame()
	}
	if a.disc.target != "" || a.disc.mention != strings.Repeat("b2", 32) || a.disc.text != "@Codex 复核 " {
		t.Fatalf("after pick: target %q mention %q text %q", a.disc.target, a.disc.mention, a.disc.text)
	}
}
