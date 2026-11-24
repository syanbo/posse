package app

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"slices"
	"testing"
	"time"

	"posse/internal/rpc"
)

func switchDraft(a *app, key string) {
	a.refreshQueued.Store(true)
	a.selectDiscussion(key)
}

func waitDraftUpdate(t *testing.T, a *app, ready func() bool) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		a.runPending()
		if ready() {
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatal("draft update did not arrive")
}

func TestDiscussionDraftSwitchPreservesContentAndRequest(t *testing.T) {
	a := &app{}
	switchDraft(a, "a")
	file, err := os.CreateTemp("", pastePrefix+"*.png")
	if err != nil {
		t.Fatal(err)
	}
	file.Close()
	t.Cleanup(func() { os.Remove(file.Name()) })
	want := discDraftContent{text: "draft", targets: []string{"member"}, quote: "message", atts: []string{file.Name()}, answerQ: "question", mention: "person"}
	a.disc.discDraftContent = want
	request := a.disc.beginSend("", "draft", false)
	switchDraft(a, "b")
	a.disc.text = "another draft"
	switchDraft(a, "a")
	if !a.disc.discDraftContent.equal(want) || !a.disc.sending || a.disc.request != request || a.disc.clientID != request.clientID {
		t.Fatalf("draft/request lost after switch: %+v", a.disc.discDraft)
	}
	if _, err := os.Stat(file.Name()); err != nil {
		t.Fatalf("switch deleted pasted attachment: %v", err)
	}
	// 详情还没取回来时重复选中，也不能把草稿换成空值。
	switchDraft(a, "a")
	if !a.disc.discDraftContent.equal(want) {
		t.Fatal("reselect during loading lost draft")
	}
	switchDraft(a, "b")
	if a.disc.text != "another draft" || a.disc.sending {
		t.Fatalf("another discussion inherited send state: %+v", a.disc.discDraft)
	}
}

func TestDiscussionDraftSendCallbacksStayWithOrigin(t *testing.T) {
	for _, mode := range []string{"message", "typed", "choice"} {
		for _, edited := range []bool{false, true} {
			t.Run(mode+map[bool]string{false: "/unchanged", true: "/edited"}[edited], func(t *testing.T) {
				started, release, refreshed := make(chan struct{}), make(chan struct{}), make(chan struct{})
				method := "discussion.post"
				if mode != "message" {
					method = "discussion.answer"
				}
				_, c := startFakeDaemon(t, map[string]rpc.Handler{
					method: func(ctx context.Context, raw json.RawMessage) (any, error) {
						var params map[string]any
						if err := json.Unmarshal(raw, &params); err != nil {
							return nil, err
						}
						if params["clientId"] == "" || params["clientId"] == nil {
							t.Error("send has no retry identity")
						}
						close(started)
						select {
						case <-release:
						case <-ctx.Done():
							return nil, ctx.Err()
						}
						return map[string]any{"message": map[string]any{"seq": 10}}, nil
					},
					"project.list": func(context.Context, json.RawMessage) (any, error) {
						close(refreshed)
						return nil, errors.New("test refresh complete")
					},
				})
				a := &app{c: c}
				switchDraft(a, "a")
				a.disc.text, a.disc.quote = "original", "quoted"
				if mode == "message" {
					a.sendDiscMessage("a", map[string]any{"discussion": "a", "body": "original"})
				} else {
					a.disc.answerQ = "q1"
					a.sendDiscAnswer("q1", "original", mode == "typed")
				}
				if !a.disc.sending {
					t.Fatal("request was not captured before returning to UI")
				}
				if edited {
					a.disc.text, a.disc.answerQ = "new edit", "q2"
				}
				switchDraft(a, "b")
				a.disc.text, a.disc.answerQ = "other draft", "other question"
				other := a.disc.beginSend("", "other draft", false)
				select {
				case <-started:
				case <-time.After(3 * time.Second):
					t.Fatal("send did not reach test server")
				}
				close(release)
				waitDraftUpdate(t, a, func() bool { return !a.disc.drafts["a"].sending })
				if a.disc.text != "other draft" || a.disc.answerQ != "other question" || !a.disc.sending || a.disc.request != other {
					t.Fatalf("late result changed another draft: %+v", a.disc.discDraft)
				}
				select {
				case <-refreshed:
				case <-time.After(3 * time.Second):
					t.Fatal("send refresh did not finish")
				}
				a.refreshMu.Lock()
				a.refreshMu.Unlock()
				switchDraft(a, "a")
				want := ""
				if edited {
					want = "new edit"
				} else if mode == "choice" {
					want = "original"
				}
				if a.disc.text != want || a.disc.sending || a.disc.clientID != "" {
					t.Fatalf("origin draft after result: %+v; want text %q", a.disc.discDraft, want)
				}
				if edited && a.disc.answerQ != "q2" {
					t.Fatal("late answer cleared a newly selected question")
				}
			})
		}
	}
}

func TestDiscussionDraftClosedPageDoesNotMarkRead(t *testing.T) {
	release := make(chan struct{})
	_, c := startFakeDaemon(t, map[string]rpc.Handler{
		"discussion.post": func(ctx context.Context, _ json.RawMessage) (any, error) {
			select {
			case <-release:
			case <-ctx.Done():
				return nil, ctx.Err()
			}
			return map[string]any{"message": map[string]any{"seq": 10}}, nil
		},
		"project.list": func(context.Context, json.RawMessage) (any, error) {
			return nil, errors.New("offline test")
		},
	})
	a := &app{c: c, selected: discBoardID}
	switchDraft(a, "a")
	a.disc.text = "message"
	a.state.DiscRead = map[string]int64{"a": 3}
	a.sendDiscMessage("a", map[string]any{"discussion": "a", "body": "message"})
	a.closeBoard()
	close(release)
	waitDraftUpdate(t, a, func() bool { return !a.disc.sending })
	if a.disc.text != "" || a.state.DiscRead["a"] != 3 {
		t.Fatalf("closed page result: text=%q read=%d", a.disc.text, a.state.DiscRead["a"])
	}
}

func TestDiscussionDraftAnswerPollBeforeReply(t *testing.T) {
	for _, edited := range []bool{false, true} {
		a := &app{}
		switchDraft(a, "a")
		a.disc.text, a.disc.answerQ = "answer", "q1"
		r := a.disc.beginSend("q1", "answer", true)
		a.disc.answerQ = ""
		if edited {
			a.disc.text = "new message"
		}
		a.disc.finishSend("a", r, true)
		want := ""
		if edited {
			want = "new message"
		}
		if a.disc.text != want {
			t.Fatalf("edited=%v text=%q want=%q", edited, a.disc.text, want)
		}
	}
}

func TestDiscussionDraftRetryIdentity(t *testing.T) {
	a := &app{}
	switchDraft(a, "a")
	a.disc.text, a.disc.atts = "first", []string{"a.png"}
	first := a.disc.beginSend("", "first", false)
	switchDraft(a, "b")
	a.disc.finishSend("a", first, false)
	switchDraft(a, "a")
	if a.disc.sending || a.disc.clientID != first.clientID || a.disc.text != "first" {
		t.Fatal("failure lost draft or request ID")
	}
	retry := a.disc.beginSend("", "first", false)
	if retry.clientID != first.clientID {
		t.Fatal("unchanged retry got another ID")
	}
	a.disc.finishSend("a", first, true)
	if !a.disc.sending || a.disc.text != "first" {
		t.Fatal("superseded callback completed a newer attempt")
	}
	a.disc.finishSend("a", retry, false)
	a.disc.atts[0] = "b.png"
	edited := a.disc.beginSend("", "first", false)
	if edited.clientID == first.clientID || !slices.Equal(first.content.atts, []string{"a.png"}) {
		t.Fatal("edited retry reused old ID or changed sent snapshot")
	}
}

func TestDiscussionDraftModuleSwitch(t *testing.T) {
	a := previewApp(t)
	switchDraft(a, "a")
	a.selected = discBoardID
	a.disc.text, a.disc.answerQ = "unfinished answer", "q1"
	request := a.disc.beginSend("q1", "unfinished answer", true)
	a.closeBoard()
	if a.selected == discBoardID {
		t.Fatal("discussion module did not close")
	}
	a.refreshQueued.Store(true)
	a.openDiscussion("a")
	if a.disc.text != "unfinished answer" || a.disc.answerQ != "q1" || !a.disc.sending || a.disc.request != request {
		t.Fatalf("module navigation lost draft: %+v", a.disc.discDraft)
	}
}

func TestDiscussionDraftAttachmentDialogReturnsToOrigin(t *testing.T) {
	calls, answer := fakeOpenDialog(t)
	a := &app{}
	switchDraft(a, "a")
	a.pickAttachments(L(nil))
	waitDraftUpdate(t, a, func() bool { return calls.Load() == 1 })
	switchDraft(a, "b")
	answer <- []string{"/tmp/a.png"}
	waitDraftUpdate(t, a, func() bool { return !a.dialogOpen })
	if len(a.disc.atts) != 0 {
		t.Fatal("attachment picker changed another draft")
	}
	switchDraft(a, "a")
	if !slices.Equal(a.disc.atts, []string{"/tmp/a.png"}) {
		t.Fatalf("attachment picker lost files after switching: %v", a.disc.atts)
	}
}

func TestDiscussionDraftPasteReturnsToOrigin(t *testing.T) {
	a := &app{}
	switchDraft(a, "a")
	a.pasteDiscImage([]byte("test image"))
	switchDraft(a, "b")
	waitDraftUpdate(t, a, func() bool { return len(a.disc.drafts["a"].atts) == 1 })
	paths := a.disc.drafts["a"].atts
	t.Cleanup(func() { removePasted(paths) })
	if len(a.disc.atts) != 0 {
		t.Fatal("paste went into current discussion")
	}
	switchDraft(a, "a")
	got, err := os.ReadFile(a.disc.atts[0])
	if err != nil || string(got) != "test image" {
		t.Fatalf("pasted attachment not retained: %q, %v", got, err)
	}
	request := a.disc.beginSend("", "", false)
	a.disc.finishSend("a", request, true)
	if _, err := os.Stat(paths[0]); !os.IsNotExist(err) {
		t.Fatalf("successful send did not remove pasted file: %v", err)
	}
}
