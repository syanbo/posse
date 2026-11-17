package app

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"posse/internal/rpc"
)

func localDiscussionFixture(t *testing.T, handlers map[string]rpc.Handler) (*hostViews, *app, *app, *fakeDaemon, *fakeDaemon) {
	t.Helper()
	v, a, b := hostViewsFixture(t)
	local, c := startFakeDaemon(t, handlers)
	a.c = c
	remote, c := startFakeDaemon(t, map[string]rpc.Handler{
		"desktop.status": func(context.Context, json.RawMessage) (any, error) {
			return rpc.DesktopState{InstanceID: "remote", Epoch: 1}, nil
		},
		"desktop.call": func(_ context.Context, raw json.RawMessage) (any, error) {
			var p rpc.DesktopCall
			if err := rpc.Decode(raw, &p); err != nil {
				return nil, err
			}
			switch p.Method {
			case "project.list":
				return []project{{ID: "remote-p", Name: "Remote project"}}, nil
			case "daemon.status":
				return daemonStatus{}, nil
			case "workspace.list":
				return []workspace{{ID: "remote-w", ProjectID: "remote-p", Kind: "main", Path: "/remote/project"}}, nil
			case "task.list":
				return []taskInfo{}, nil
			default:
				return nil, rpc.Errorf(rpc.CodeUnknown, "unexpected remote method")
			}
		},
	})
	c.Remote = true
	b.c = c
	v.activate("remote")
	if err := b.host.connect(context.Background(), c); err != nil {
		t.Fatal(err)
	}
	if b.host.snapshot().Controlled {
		t.Fatal("remote fixture unexpectedly controls the host")
	}
	return v, a, b, local, remote
}

func assertNoRemoteDiscussion(t *testing.T, remote *fakeDaemon, attachment string) {
	t.Helper()
	remote.mu.Lock()
	defer remote.mu.Unlock()
	for _, c := range remote.calls {
		method := c.method
		if c.method == "desktop.call" {
			method, _ = c.params["method"].(string)
		}
		raw, err := json.Marshal(c.params)
		if err != nil {
			t.Fatal(err)
		}
		if strings.HasPrefix(method, "discussion.") || attachment != "" && strings.Contains(string(raw), attachment) {
			t.Fatalf("local discussion reached remote: %s %s", method, raw)
		}
	}
}

func TestDiscussionLocalPollWhileRemoteDisplayed(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	_, a, b, local, remote := localDiscussionFixture(t, map[string]rpc.Handler{
		"discussion.list": func(context.Context, json.RawMessage) (any, error) {
			cancel()
			return discList{Seq: 8, Self: "local", Items: []discSummary{{Key: "room", MsgSeq: 7, Last: &discMessage{Kind: "reply", Seq: 7}}}}, nil
		},
	})
	a.state.DiscRead = map[string]int64{"room": 3}
	a.disc.sel, a.selected = "room", discBoardID
	a.setWantedDisc("room", 1)
	// 即使隐藏对象留下旧详情意图，后台轮询也只能取列表。
	a.poll(ctx)
	a.runPending()
	b.runPending()
	if _, ok := local.find("discussion.list"); !ok || a.discSeq.Load() != 8 || len(a.disc.list) != 1 {
		t.Fatalf("local list did not update: seq=%d list=%+v", a.discSeq.Load(), a.disc.list)
	}
	if _, ok := local.find("discussion.show"); ok {
		t.Fatal("hidden discussion polled details")
	}
	if _, unread := a.discCounts(); unread != 1 || a.discUnread(a.disc.list[0]) != 4 {
		t.Fatalf("local unread not updated: discussions=%d", unread)
	}
	if len(b.disc.list) != 0 || b.discSeq.Load() != 0 || b.discLoaded || len(b.state.DiscRead) != 0 {
		t.Fatal("local discussion result entered remote app")
	}
	if len(b.projects) != 1 || b.projects[0].ID != "remote-p" || a.lastPoll != nil {
		t.Fatal("workspace poll did not stay with remote app")
	}
	assertNoRemoteDiscussion(t, remote, "")
}

func TestDiscussionLocalCallsIgnoreRemoteViewer(t *testing.T) {
	methods := []string{"discussion.post", "discussion.answer", "discussion.attachments", "discussion.search", "discussion.create", "discussion.update", "discussion.archive", "discussion.member.add", "discussion.member.update", "discussion.member.remove", "discussion.share", "discussion.invite", "discussion.join", "discussion.leave", "discussion.decide", "discussion.person.remove"}
	handlers := map[string]rpc.Handler{}
	for _, method := range methods {
		handlers[method] = func(context.Context, json.RawMessage) (any, error) { return nil, nil }
	}
	_, a, b, local, remote := localDiscussionFixture(t, handlers)
	path := filepath.Join(t.TempDir(), "local-attachment.png")
	for _, method := range methods {
		err := b.discussionRequest().call(method, map[string]any{"discussion": "room", "attachments": []map[string]string{{"path": path}}}, nil, time.Second)
		if err != nil {
			t.Fatalf("%s rejected through remote viewer: %v", method, err)
		}
		if p, ok := local.find(method); !ok || !strings.Contains(string(discussionJSON(t, p)), path) {
			t.Fatalf("%s did not reach local client with its attachment", method)
		}
	}
	if b.discussionRequest().app != a || b.discussionRequest().client != a.c || a.c.Desktop != nil {
		t.Fatal("discussion request borrowed remote or desktop identity")
	}
	assertNoRemoteDiscussion(t, remote, path)
}

func discussionJSON(t *testing.T, value any) []byte {
	t.Helper()
	data, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	return data
}

func TestDiscussionLocalDraftAndAttachmentSurviveHostSwitch(t *testing.T) {
	v, a, b, local, remote := localDiscussionFixture(t, map[string]rpc.Handler{
		"discussion.list": func(context.Context, json.RawMessage) (any, error) { return discList{}, nil },
	})
	v.activate(localHostID)
	switchDraft(a, "room")
	a.selected = discBoardID
	path := filepath.Join(t.TempDir(), "local.png")
	if err := os.WriteFile(path, []byte("attachment"), 0o600); err != nil {
		t.Fatal(err)
	}
	a.disc.text, a.disc.atts = "local draft", []string{path}
	a.state.DiscRead = map[string]int64{"room": 4}
	v.activate("remote")
	if key, _ := a.wantedDisc(); key != "" {
		t.Fatal("leaving local discussion retained detail subscription")
	}
	if err := b.host.connect(context.Background(), b.c); err != nil {
		t.Fatal(err)
	}
	b.sendDiscMessage("room", map[string]any{"discussion": "room", "body": "local draft", "attachments": []map[string]string{{"path": path}}})
	waitDraftUpdate(t, a, func() bool { return !a.disc.sending })
	if p, ok := local.find("discussion.post"); !ok || !strings.Contains(string(discussionJSON(t, p)), path) {
		t.Fatal("send from remote app did not use local attachment/client")
	}
	v.activate(localHostID)
	if a.disc.text != "local draft" || !slices.Equal(a.disc.atts, []string{path}) || a.state.DiscRead["room"] != 4 {
		t.Fatalf("host switch lost local draft/read state: %+v", a.disc.discDraft)
	}
	if b.disc.text != "" || len(b.disc.atts) != 0 || b.disc.sending || len(b.state.DiscRead) != 0 {
		t.Fatal("remote app inherited local draft/read state")
	}
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("switch removed local attachment: %v", err)
	}
	assertNoRemoteDiscussion(t, remote, path)
}

func TestDiscussionLocalRequestExpiresOnViewChange(t *testing.T) {
	var seq atomic.Int64
	seq.Store(4)
	v, a, b, local, _ := localDiscussionFixture(t, map[string]rpc.Handler{
		"discussion.list": func(context.Context, json.RawMessage) (any, error) {
			return discList{Seq: seq.Load(), Items: []discSummary{{Key: "room"}}}, nil
		},
	})
	old := b.discussionRequest()
	old.refreshDiscussions()
	old.update(func() { a.disc.search = "hidden result" })
	a.runPending()
	if len(a.disc.list) != 1 || a.disc.search != "" {
		t.Fatal("hidden request applied more than the local list")
	}
	seq.Store(5)
	old.refreshDiscussions()
	v.activate(localHostID)
	v.activate("remote")
	a.disc.list = nil
	a.runPending()
	if a.disc.list != nil {
		t.Fatal("queued old generation list survived switching back")
	}
	before := local.count()
	if err := old.call("discussion.post", nil, nil, time.Second); !errors.Is(err, context.Canceled) || local.count() != before {
		t.Fatalf("expired write reached local client: %v", err)
	}
	b.discussionRequest().refreshDiscussions()
	a.runPending()
	if len(a.disc.list) != 1 {
		t.Fatal("new view generation failed to reapply identical list")
	}
	v.activate(localHostID)
	r := a.discussionRequest()
	r.update(func() { a.disc.search = "old intent" })
	a.setWantedDisc("another", 0)
	a.runPending()
	if a.disc.search != "" {
		t.Fatal("expired discussion intent applied a result")
	}
}

func TestDiscussionLocalLateRemotePollCannotChangeDiscussions(t *testing.T) {
	v, a, b, _, remote := localDiscussionFixture(t, nil)
	a.disc.list = []discSummary{{Key: "room", Title: "Local", MsgSeq: 5}}
	a.discSeq.Store(5)
	before := slices.Clone(a.disc.list)
	b.refresh()
	v.activate(localHostID)
	b.runPending()
	a.runPending()
	if !reflect.DeepEqual(a.disc.list, before) || a.discSeq.Load() != 5 || b.discLoaded {
		t.Fatal("late remote workspace result changed local discussions")
	}
	assertNoRemoteDiscussion(t, remote, "")
}
