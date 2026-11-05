package app

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
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
	// 悬停在这条消息上，点它自己那行的引用按钮（行尾，和名字同一行）
	r, ok := tt.Find("先读一下 LoginView.swift，列出要改的地方。")
	if !ok {
		t.Fatal("message not shown")
	}
	tt.Move(r.X+20, r.Y+r.H/2)
	tt.Frame()
	q, _ := tt.Find("引用")
	tt.ClickAt(q.X+q.W/2, r.Y-30+q.H/2)
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

// olderPage 是假常驻进程给的更早一页：序号 1..n 的旧消息。
func olderPage(n int) rpc.Handler {
	return func(ctx context.Context, raw json.RawMessage) (any, error) {
		var p struct{ Before, Limit int64 }
		json.Unmarshal(raw, &p)
		var msgs []discMessage
		for seq := max(1, p.Before-p.Limit); seq < p.Before && seq <= int64(n); seq++ {
			msgs = append(msgs, discMessage{ID: fmt.Sprintf("old%d", seq), Seq: seq, Kind: "user", Body: fmt.Sprintf("旧消息 %d", seq), CreatedAt: time.Now().Add(-48 * time.Hour)})
		}
		return discView{Messages: msgs, More: len(msgs) > 0 && msgs[0].Seq > 1}, nil
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
		"discussion.show":   olderPage(100),
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
	if p["before"] != float64(101) || p["limit"].(float64) < 81 {
		t.Fatalf("older page request %+v", p)
	}
	deadline := time.Now().Add(3 * time.Second)
	for a.disc.d.msg("old20") == nil && time.Now().Before(deadline) {
		tt.Frame()
		time.Sleep(10 * time.Millisecond)
	}
	if a.disc.d.msg("old20") == nil || a.disc.jump != "old20" || a.disc.follow {
		t.Fatalf("not jumped: loaded %v jump %q", a.disc.d.msg("old20") != nil, a.disc.jump)
	}
	tt.Frame()
	if !tt.HasText("旧消息 20") {
		t.Fatal("jumped message not shown")
	}
}

// 「加载更早的消息」：取前一页插到前面，停在原来最上面那条（不跳到底）。
func TestDiscussionLoadOlderKeepsPlace(t *testing.T) {
	a, tt, f := discTester(t, map[string]rpc.Handler{"discussion.show": olderPage(100)})
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
	if a.disc.jump != first || a.disc.follow {
		t.Fatalf("reading place: jump %q follow %v", a.disc.jump, a.disc.follow)
	}
	if !a.disc.d.more {
		t.Fatal("more pages lost")
	}
}

// 已归档讨论清理附件：先列出文件和大小，取消不动文件；确认后移到废纸篓（只用这里建的临时文件），消息里的附件信息还在。
func TestDiscussionCleanAttachments(t *testing.T) {
	dir := t.TempDir()
	var files []string
	for i, body := range []string{"一", "二二"} {
		p := filepath.Join(dir, fmt.Sprintf("posse-disc-clean-%d-%d.txt", time.Now().UnixNano(), i))
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
	if len(a.disc.d.msgs[3].Attachments) != 1 {
		t.Fatal("attachment info dropped from the message")
	}
}
