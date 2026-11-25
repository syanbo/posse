package lark

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"posse/internal/store"
)

type fakeClient struct {
	mu            sync.Mutex
	sent          []fakeCardCall
	patched       []fakeCardCall
	patchErr      error
	patchHook     func(ctx context.Context, cardMsgID, cardJSON string) error
	replyHook     func(ctx context.Context, msgID string, inThread bool, cardJSON string) error
	files         []string
	nextID        int
	fetchedMsgs   map[string]FetchedMessage
	recentMsgs    []FetchedMessage
	threadMsgs    []FetchedMessage
	recentCalls   int
	threadCalls   int
	getMsgCalls   int
	downloadCalls []string
}

type fakeCardCall struct {
	profile  string
	target   string
	inThread bool
	cardJSON string
}

func (f *fakeClient) Profiles(context.Context) ([]ProfileInfo, error) {
	return []ProfileInfo{{Name: "bot-a", AppID: "cli_a", Brand: "feishu", Active: true}}, nil
}

func (f *fakeClient) OwnerOpenID(context.Context, string) (string, error) {
	return "ou_owner", nil
}

func (f *fakeClient) Consume(ctx context.Context, _, _ string, onReady func(), _ func([]byte)) error {
	if onReady != nil {
		onReady()
	}
	<-ctx.Done()
	return nil
}

func (f *fakeClient) SendCard(_ context.Context, profile, chatID, cardJSON string) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.nextID++
	f.sent = append(f.sent, fakeCardCall{profile: profile, target: chatID, cardJSON: cardJSON})
	return "om_card_" + string(rune('0'+f.nextID)), nil
}

func (f *fakeClient) ReplyCard(ctx context.Context, profile, msgID string, inThread bool, cardJSON string) (string, error) {
	f.mu.Lock()
	hook := f.replyHook
	f.mu.Unlock()
	if hook != nil {
		if err := hook(ctx, msgID, inThread, cardJSON); err != nil {
			return "", err
		}
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	f.nextID++
	f.sent = append(f.sent, fakeCardCall{profile: profile, target: msgID, inThread: inThread, cardJSON: cardJSON})
	return "om_reply_" + string(rune('0'+f.nextID)), nil
}

func (f *fakeClient) PatchCard(ctx context.Context, profile, cardMsgID, cardJSON string) error {
	f.mu.Lock()
	hook := f.patchHook
	patchErr := f.patchErr
	f.mu.Unlock()
	if hook != nil {
		if err := hook(ctx, cardMsgID, cardJSON); err != nil {
			return err
		}
	} else if patchErr != nil {
		return patchErr
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	f.patched = append(f.patched, fakeCardCall{profile: profile, target: cardMsgID, cardJSON: cardJSON})
	return nil
}

func (f *fakeClient) SendFile(_ context.Context, _, _, path string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.files = append(f.files, path)
	return nil
}

func (f *fakeClient) DownloadResource(_ context.Context, _, msgID, fileKey, _, outDir string) (string, error) {
	f.mu.Lock()
	f.downloadCalls = append(f.downloadCalls, msgID+":"+fileKey)
	f.mu.Unlock()
	if outDir == "" || fileKey == "" {
		return "", nil
	}
	if err := os.MkdirAll(outDir, 0o700); err != nil {
		return "", err
	}
	p := filepath.Join(outDir, filepath.Base(fileKey))
	if err := os.WriteFile(p, []byte("fake-"+fileKey), 0o600); err != nil {
		return "", err
	}
	return p, nil
}

func (f *fakeClient) GetMessage(_ context.Context, _, messageID, downloadDir string) (FetchedMessage, error) {
	f.mu.Lock()
	f.getMsgCalls++
	m, ok := f.fetchedMsgs[messageID]
	f.mu.Unlock()
	if !ok {
		return FetchedMessage{}, store.ErrNotFound
	}
	if downloadDir != "" {
		for i := range m.Resources {
			if m.Resources[i].LocalPath != "" && !filepath.IsAbs(m.Resources[i].LocalPath) {
				p := filepath.Join(downloadDir, m.Resources[i].LocalPath)
				_ = os.MkdirAll(filepath.Dir(p), 0o700)
				_ = os.WriteFile(p, []byte("fake-image"), 0o600)
				m.Resources[i].LocalPath = p
			}
		}
	}
	return m, nil
}

func (f *fakeClient) ListRecentMessages(_ context.Context, _, _ string, _ int) ([]FetchedMessage, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.recentCalls++
	return append([]FetchedMessage(nil), f.recentMsgs...), nil
}

func (f *fakeClient) ListRecentThreadMessages(_ context.Context, _, _ string, _ int) ([]FetchedMessage, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.threadCalls++
	return append([]FetchedMessage(nil), f.threadMsgs...), nil
}

type fakeHost struct {
	mu           sync.Mutex
	bindings     map[string]store.LarkBinding
	cards        map[string]store.LarkCard
	events       map[string]bool
	posted       []string
	postedAtts   [][]string
	answered     map[string]string
	stopped      []string
	decided      map[string]string
	switched     string
	switchedWS   string
	execSnaps    map[string]ExecSnap
	termBinding  map[string]store.LarkBinding
	nextExecID   string
	nextExecStat string
}

func newFakeHost() *fakeHost {
	return &fakeHost{
		bindings:    map[string]store.LarkBinding{},
		cards:       map[string]store.LarkCard{},
		events:      map[string]bool{},
		answered:    map[string]string{},
		decided:     map[string]string{},
		execSnaps:   map[string]ExecSnap{},
		termBinding: map[string]store.LarkBinding{},
	}
}

func (h *fakeHost) bkey(bot, chat, thread string) string { return bot + "|" + chat + "|" + thread }
func (h *fakeHost) EnabledAgents() []string              { return []string{"antigravity", "claude", "codex"} }
func (h *fakeHost) Workspaces() []WorkspaceRef {
	return []WorkspaceRef{{ID: "w_works", Label: "Works/main", Path: "/Works"}, {ID: "w_posse", Label: "posse/main", Path: "/posse"}}
}

func (h *fakeHost) EnsureDiscussion(bot BotConfig, chatID, chatType, threadKey, _ string) (store.LarkBinding, error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	k := h.bkey(bot.ID, chatID, threadKey)
	if b, ok := h.bindings[k]; ok && b.Active {
		return b, nil
	}
	b := store.LarkBinding{
		ID: "lb_1", BotID: bot.ID, ChatID: chatID, ChatType: chatType, ThreadKey: threadKey,
		DiscussionKey: "self/d_" + chatID, MemberID: "mb_1", WorkspaceID: bot.WorkspaceID,
		Agent: bot.Agent, PermissionMode: bot.PermissionMode, Active: true,
	}
	h.bindings[k] = b
	return b, nil
}

func (h *fakeHost) ActiveBinding(botID, chatID, threadKey string) (store.LarkBinding, error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	b, ok := h.bindings[h.bkey(botID, chatID, threadKey)]
	if !ok {
		return store.LarkBinding{}, store.ErrNotFound
	}
	return b, nil
}

func (h *fakeHost) BindingByDiscussion(discKey string) (store.LarkBinding, error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	for _, b := range h.bindings {
		if b.DiscussionKey == discKey {
			return b, nil
		}
	}
	return store.LarkBinding{}, store.ErrNotFound
}

func (h *fakeHost) BindingByTerminal(termID string) (store.LarkBinding, bool) {
	h.mu.Lock()
	defer h.mu.Unlock()
	b, ok := h.termBinding[termID]
	return b, ok
}

func (h *fakeHost) SwitchAgent(b store.LarkBinding, agentName, _ string) (store.LarkBinding, error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	b.Agent = agentName
	h.switched = agentName
	h.bindings[h.bkey(b.BotID, b.ChatID, b.ThreadKey)] = b
	return b, nil
}

func (h *fakeHost) SwitchWorkspace(b store.LarkBinding, query string) (store.LarkBinding, error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	b.WorkspaceID = query
	h.switchedWS = query
	h.bindings[h.bkey(b.BotID, b.ChatID, b.ThreadKey)] = b
	return b, nil
}

func (h *fakeHost) ResetBinding(botID, chatID, threadKey string) error {
	h.mu.Lock()
	defer h.mu.Unlock()
	delete(h.bindings, h.bkey(botID, chatID, threadKey))
	return nil
}

func (h *fakeHost) PostFromLark(b store.LarkBinding, body, metaHeader string, atts []string) (string, error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.posted = append(h.posted, metaHeader+"\n"+body)
	h.postedAtts = append(h.postedAtts, append([]string(nil), atts...))
	now := time.Now()
	id := "x_1"
	if h.nextExecID != "" {
		id = h.nextExecID
	}
	st := "running"
	if h.nextExecStat != "" {
		st = h.nextExecStat
	}
	h.execSnaps[id] = ExecSnap{ID: id, Discussion: b.DiscussionKey, Agent: b.Agent, Workspace: "Works/main", State: st, StartedAt: &now}
	return id, nil
}

func (h *fakeHost) AnswerQuestion(qID, body string) error {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.answered[qID] = body
	return nil
}

func (h *fakeHost) StopExecution(execID string) error {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.stopped = append(h.stopped, execID)
	return nil
}

func (h *fakeHost) RetryExecution(execID string) (string, error) { return "x_retry", nil }
func (h *fakeHost) DecidePermission(permID, behavior, _ string) bool {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.decided[permID] = behavior
	return true
}
func (h *fakeHost) ExecutionSnapshot(execID string) (ExecSnap, bool) {
	h.mu.Lock()
	defer h.mu.Unlock()
	s, ok := h.execSnaps[execID]
	return s, ok
}
func (h *fakeHost) ActiveExecutionFor(discKey string) (ExecSnap, bool) {
	h.mu.Lock()
	defer h.mu.Unlock()
	var fallback ExecSnap
	var found bool
	for _, s := range h.execSnaps {
		if s.Discussion != discKey || !isActiveExecState(s.State) {
			continue
		}
		if s.State == "running" {
			return s, true
		}
		fallback = s
		found = true
	}
	return fallback, found
}
func (h *fakeHost) SaveCard(c store.LarkCard) error {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.cards[c.Kind+":"+c.RefID] = c
	return nil
}
func (h *fakeHost) GetCard(kind, refID string) (store.LarkCard, error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	c, ok := h.cards[kind+":"+refID]
	if !ok {
		return store.LarkCard{}, store.ErrNotFound
	}
	return c, nil
}
func (h *fakeHost) CardByMessageID(cardMsgID string) (store.LarkCard, error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	for _, c := range h.cards {
		if c.CardMsgID == cardMsgID {
			return c, nil
		}
	}
	return store.LarkCard{}, store.ErrNotFound
}
func (h *fakeHost) MarkEvent(botID, eventKey string) (bool, error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	k := botID + ":" + eventKey
	if h.events[k] {
		return false, nil
	}
	h.events[k] = true
	return true, nil
}

func TestGatewayMessageSecurityCommandsAndReverseSync(t *testing.T) {
	fc := &fakeClient{}
	fh := newFakeHost()
	bot := NormalizeBot(BotConfig{
		ID: "lbot_1", Name: "Works Triage", Profile: "bot-a", Enabled: true,
		WorkspaceID: "w_works", Agent: "antigravity", PermissionMode: PermAsk,
		SessionMode: SessionChat, TriggerPolicy: TriggerSelf,
	})
	gw := NewGateway(t.TempDir(), fc, fh)
	gw.Reload(Config{OwnerOpenID: "ou_owner", Bots: []BotConfig{bot}})

	// 1. 同事在群里 @ 机器人被 self 策略拦截；主人在群里 @ 别的同事也不应触发本机器人
	gw.HandleMessage(context.Background(), bot, MessageEvent{
		MessageID: "om_other", ChatID: "oc_grp1", ChatType: "group",
		SenderID: "ou_colleague", Content: "@Works Triage check", Mentions: []MessageMention{{Name: "Works Triage"}},
	})
	gw.HandleMessage(context.Background(), bot, MessageEvent{
		MessageID: "om_other_mention", ChatID: "oc_grp1", ChatType: "group",
		SenderID: "ou_owner", Content: "@Alice 帮我看下这个接口", Mentions: []MessageMention{{ID: "ou_alice", Name: "Alice"}},
	})
	if len(fh.posted) != 0 {
		t.Fatalf("colleague message or @other mention should be ignored, posted=%v", fh.posted)
	}

	// 2. 主人在群里 @ 机器人触发排查，并按 message_id 去重
	ev := MessageEvent{
		MessageID: "om_1", ChatID: "oc_grp1", ChatType: "group", ParentID: "om_parent",
		SenderID: "ou_owner", Content: "@Works Triage 查一下 20261124-001",
		Mentions: []MessageMention{{Name: "Works Triage"}},
	}
	gw.HandleMessage(context.Background(), bot, ev)
	gw.HandleMessage(context.Background(), bot, ev) // 重复事件忽略
	if len(fh.posted) != 1 || !strings.Contains(fh.posted[0], "chat_id=oc_grp1") || !strings.Contains(fh.posted[0], "parent_id=om_parent") {
		t.Fatalf("unexpected posted payload: %v", fh.posted)
	}
	if len(fc.sent) != 1 || !strings.Contains(fc.sent[0].cardJSON, `"schema":"2.0"`) {
		t.Fatalf("expected initial Card 2.0 progress reply, got %+v", fc.sent)
	}

	// 3. 群聊反向同步边界：桌面端内部追问不刷到同事群，只有 Agent 最终回复同步回群
	beforeSend := len(fc.sent)
	gw.OnDesktopUserMessage("self/d_oc_grp1", "internal follow-up from desktop")
	if len(fc.sent) != beforeSend {
		t.Fatalf("group chat should NOT mirror desktop internal user prompts")
	}

	// 单聊（p2p）双向全同步：桌面发的消息会同步到飞书单聊
	_, _ = fh.EnsureDiscussion(bot, "oc_p2p", "p2p", "", "dm")
	gw.OnDesktopUserMessage("self/d_oc_p2p", "desktop prompt in p2p")
	if len(fc.sent) != beforeSend+1 {
		t.Fatalf("p2p chat SHOULD mirror desktop user prompt")
	}

	// 4. 命令切换 Agent（/agent codex）
	gw.HandleMessage(context.Background(), bot, MessageEvent{
		MessageID: "om_cmd", ChatID: "oc_grp1", ChatType: "group",
		SenderID: "ou_owner", Content: "@Works Triage /agent codex",
		Mentions: []MessageMention{{Name: "Works Triage"}},
	})
	if fh.switched != "codex" {
		t.Fatalf("expected agent switch to codex, got %q", fh.switched)
	}

	// 5. 权限审批与停止按钮强校验：同事点无效，主人点生效
	fh.termBinding["t_1"] = store.LarkBinding{BotID: bot.ID, ChatID: "oc_grp1", DiscussionKey: "self/d_oc_grp1", PermissionMode: PermAsk}
	if !gw.WatchPermission(PermNotice{ID: "perm_1", TerminalID: "t_1", Agent: "antigravity", Tool: "run_command", Summary: "rm -rf /tmp/x"}) {
		t.Fatal("WatchPermission should accept active ask binding")
	}
	allowVal, _ := json.Marshal(map[string]string{"action": "perm_allow", "botId": bot.ID, "permId": "perm_1"})
	gw.HandleCardAction(context.Background(), bot, CardActionEvent{EventID: "ev_colleague", OperatorID: "ou_colleague", ActionValue: string(allowVal)})
	if fh.decided["perm_1"] != "" {
		t.Fatal("colleague must NOT be able to approve permission")
	}
	gw.HandleCardAction(context.Background(), bot, CardActionEvent{EventID: "ev_owner", OperatorID: "ou_owner", ActionValue: string(allowVal)})
	if fh.decided["perm_1"] != "allow" {
		t.Fatalf("owner approval should decide allow, got %q", fh.decided["perm_1"])
	}
	gw.PermissionResolved("perm_1", "allow", "feishu")
	if len(fc.patched) == 0 {
		t.Fatal("expected resolved permission card patch")
	}

	// 6. 问答卡片表单提交
	gw.OnQuestion("self/d_oc_grp1", "x_1", "q_1", "antigravity", "Which env?", []string{"prod", "staging"})
	qCard, err := fh.GetCard("question", "q_1")
	if err != nil {
		t.Fatal(err)
	}
	gw.HandleCardAction(context.Background(), bot, CardActionEvent{
		EventID: "ev_form", OperatorID: "ou_owner", MessageID: qCard.CardMsgID,
		FormValue: `{"answer_text":"canary"}`,
	})
	if fh.answered["q_1"] != "canary" {
		t.Fatalf("expected form answer canary, got %q", fh.answered["q_1"])
	}

	// 7. 执行完成：先收拢最初的进度卡，再把最终结果作为新卡片发到对话底部（回复原提问消息）
	sentBeforeFinish := len(fc.sent)
	patchedBeforeFinish := len(fc.patched)
	gw.OnExecFinished("self/d_oc_grp1", "x_1", "done", "Root cause: null pointer in login.")
	if len(fc.patched) != patchedBeforeFinish+1 {
		t.Fatalf("expected progress card to be patched into compact closed receipt, got %d patches", len(fc.patched)-patchedBeforeFinish)
	}
	if strings.Contains(fc.patched[len(fc.patched)-1].cardJSON, "null pointer in login") {
		t.Fatalf("progress card should NOT contain full result body")
	}
	if len(fc.sent) != sentBeforeFinish+1 {
		t.Fatalf("expected a NEW result card at bottom of chat, got %d new sends", len(fc.sent)-sentBeforeFinish)
	}
	lastSent := fc.sent[len(fc.sent)-1]
	if lastSent.target != "om_1" || !strings.Contains(lastSent.cardJSON, "null pointer in login") {
		t.Fatalf("unexpected final result card: %+v", lastSent)
	}
}

func TestGatewayParentExpansionAndGroupContext(t *testing.T) {
	parentMsg := FetchedMessage{
		MessageID:  "om_parent_err",
		MsgType:    "post",
		CreateTime: "2026-11-24 22:10",
		Content:    "Android 710 login crash stacktrace",
	}
	parentMsg.Sender.Name = "Alice"
	parentMsg.Resources = append(parentMsg.Resources, struct {
		Key       string `json:"key"`
		Type      string `json:"type"`
		LocalPath string `json:"local_path"`
		Error     bool   `json:"error"`
	}{Key: "img_1", Type: "image", LocalPath: "lark-im-resources/crash.png"})

	mOld := FetchedMessage{MessageID: "om_hist_1", CreateTime: "2026-11-24 22:08", Content: "First user report"}
	mOld.Sender.Name = "Bob"
	mDel := FetchedMessage{MessageID: "om_hist_del", CreateTime: "2026-11-24 22:09", Content: "recalled", Deleted: true}
	mMid := FetchedMessage{MessageID: "om_hist_2", CreateTime: "2026-11-24 22:11", Content: "Reproduced on Xiaomi 14"}
	mMid.Sender.Name = "Carol"
	mCur := FetchedMessage{MessageID: "om_trigger_ctx", CreateTime: "2026-11-24 22:12", Content: "@Works Triage"}
	mCur.Sender.Name = "Owner"

	fc := &fakeClient{
		fetchedMsgs: map[string]FetchedMessage{"om_parent_err": parentMsg},
		// API returns desc (newest first)
		recentMsgs: []FetchedMessage{mCur, mMid, parentMsg, mDel, mOld},
	}
	fh := newFakeHost()

	// 1. GroupContextCount == 0: expands quoted parent (including bare @bot reply with empty text) without calling ListRecentMessages
	botOff := NormalizeBot(BotConfig{
		ID: "lbot_off", Name: "Works Triage", Profile: "bot-a", Enabled: true,
		WorkspaceID: "w_works", Agent: "antigravity", TriggerPolicy: TriggerSelf, GroupContextCount: 0,
	})
	gw := NewGateway(t.TempDir(), fc, fh)
	gw.Reload(Config{OwnerOpenID: "ou_owner", Bots: []BotConfig{botOff}})

	gw.HandleMessage(context.Background(), botOff, MessageEvent{
		MessageID: "om_bare_reply", ChatID: "oc_grp1", ChatType: "group", ParentID: "om_parent_err",
		SenderID: "ou_owner", Content: "@Works Triage", Mentions: []MessageMention{{Name: "Works Triage"}},
	})
	if fc.recentCalls != 0 {
		t.Fatalf("expected 0 recent list calls when GroupContextCount=0, got %d", fc.recentCalls)
	}
	if len(fh.posted) != 1 || !strings.Contains(fh.posted[0], "[Quoted message om_parent_err · Alice · 2026-11-24 22:10]") ||
		!strings.Contains(fh.posted[0], "Android 710 login crash stacktrace") {
		t.Fatalf("expected expanded quoted parent in posted body, got %v", fh.posted)
	}
	if len(fh.postedAtts) != 1 || len(fh.postedAtts[0]) != 1 || !strings.HasSuffix(fh.postedAtts[0][0], "crash.png") {
		t.Fatalf("expected parent image attachment passed to PostFromLark, got %v", fh.postedAtts)
	}

	// 2. GroupContextCount == 10: attaches chronological recent messages, skipping current msg, deleted msg, and already-expanded parent
	botOn := NormalizeBot(BotConfig{
		ID: "lbot_on", Name: "Works Triage", Profile: "bot-a", Enabled: true,
		WorkspaceID: "w_works", Agent: "antigravity", TriggerPolicy: TriggerSelf, GroupContextCount: 10,
	})
	gw.Reload(Config{OwnerOpenID: "ou_owner", Bots: []BotConfig{botOn}})
	gw.HandleMessage(context.Background(), botOn, MessageEvent{
		MessageID: "om_trigger_ctx", ChatID: "oc_grp1", ChatType: "group", ParentID: "om_parent_err",
		SenderID: "ou_owner", Content: "@Works Triage check this", Mentions: []MessageMention{{Name: "Works Triage"}},
	})
	if fc.recentCalls != 1 {
		t.Fatalf("expected 1 recent list call when GroupContextCount=10, got %d", fc.recentCalls)
	}
	got := fh.posted[1]
	idxOld := strings.Index(got, "Bob: First user report")
	idxMid := strings.Index(got, "Carol: Reproduced on Xiaomi 14")
	idxQuoted := strings.Index(got, "[Quoted message om_parent_err")
	if idxOld < 0 || idxMid < 0 || idxQuoted < 0 || !(idxOld < idxMid && idxMid < idxQuoted) {
		t.Fatalf("expected chronological recent messages before quoted block, got:\n%s", got)
	}
	if strings.Contains(got, "recalled") {
		t.Fatalf("deleted message should be excluded: %s", got)
	}
	if strings.Count(got, "Android 710 login crash stacktrace") != 1 {
		t.Fatalf("parent message should not be duplicated in recent group context: %s", got)
	}
}

func TestGatewayOnExecFinishedInPlaceVsInterleaved(t *testing.T) {
	bot := NormalizeBot(BotConfig{
		ID: "lbot_1", Name: "Works Triage", Profile: "bot-a", Enabled: true,
		WorkspaceID: "w_works", Agent: "antigravity", PermissionMode: PermAsk,
		SessionMode: SessionChat, TriggerPolicy: TriggerSelf,
	})

	// 1. 中间没有任何新消息：直接原地更新到进度卡，不新开结果卡
	t.Run("in place when no interleaved message", func(t *testing.T) {
		fc := &fakeClient{}
		fh := newFakeHost()
		gw := NewGateway(t.TempDir(), fc, fh)
		gw.Reload(Config{OwnerOpenID: "ou_owner", Bots: []BotConfig{bot}})

		gw.HandleMessage(context.Background(), bot, MessageEvent{
			MessageID: "om_q1", ChatID: "oc_p2p", ChatType: "p2p",
			SenderID: "ou_owner", Content: "这些改动的作者是谁",
		})
		if len(fc.sent) != 1 {
			t.Fatalf("expected 1 progress card, got %d", len(fc.sent))
		}
		gw.OnExecFinished("self/d_oc_p2p", "x_1", "done", "作者是 xukai")
		if len(fc.sent) != 1 {
			t.Fatalf("expected NO new result card when no interleaved message, got %d sent", len(fc.sent))
		}
		if len(fc.patched) != 1 || fc.patched[0].target != "om_reply_1" || !strings.Contains(fc.patched[0].cardJSON, "作者是 xukai") {
			t.Fatalf("expected progress card patched in place with result body, got %+v", fc.patched)
		}
	})

	// 2. 群聊中间收到同事未 @ 机器人的普通消息：视为插队，收拢旧卡 + 底部发新卡
	t.Run("new card when group message interleaved", func(t *testing.T) {
		fc := &fakeClient{}
		fh := newFakeHost()
		gw := NewGateway(t.TempDir(), fc, fh)
		gw.Reload(Config{OwnerOpenID: "ou_owner", Bots: []BotConfig{bot}})

		gw.HandleMessage(context.Background(), bot, MessageEvent{
			MessageID: "om_q2", ChatID: "oc_grp", ChatType: "group",
			SenderID: "ou_owner", Content: "@Works Triage 查一下", Mentions: []MessageMention{{Name: "Works Triage"}},
		})
		gw.HandleMessage(context.Background(), bot, MessageEvent{
			MessageID: "om_chatter", ChatID: "oc_grp", ChatType: "group",
			SenderID: "ou_colleague", Content: "顺便问下下午开会吗",
		})
		gw.OnExecFinished("self/d_oc_grp", "x_1", "done", "已查明原因")
		if len(fc.patched) != 1 || strings.Contains(fc.patched[0].cardJSON, "已查明原因") {
			t.Fatalf("expected compact closed progress card patch, got %+v", fc.patched)
		}
		if len(fc.sent) != 2 || !strings.Contains(fc.sent[1].cardJSON, "已查明原因") {
			t.Fatalf("expected new result card at bottom, got %+v", fc.sent)
		}
	})

	// 3. 本地未收到插队事件，但远端最新消息已不是进度卡：兜底识别插队并发送新卡
	t.Run("new card when remote latest message differs", func(t *testing.T) {
		fc := &fakeClient{
			recentMsgs: []FetchedMessage{{MessageID: "om_newer_msg"}},
		}
		fh := newFakeHost()
		gw := NewGateway(t.TempDir(), fc, fh)
		gw.Reload(Config{OwnerOpenID: "ou_owner", Bots: []BotConfig{bot}})

		gw.HandleMessage(context.Background(), bot, MessageEvent{
			MessageID: "om_q3", ChatID: "oc_grp2", ChatType: "group",
			SenderID: "ou_owner", Content: "@Works Triage 查一下", Mentions: []MessageMention{{Name: "Works Triage"}},
		})
		gw.OnExecFinished("self/d_oc_grp2", "x_1", "done", "远端检测到插队")
		if len(fc.sent) != 2 || !strings.Contains(fc.sent[1].cardJSON, "远端检测到插队") {
			t.Fatalf("expected fallback to new card when remote latest message differs, got %+v", fc.sent)
		}
	})

	// 4. 远端最新消息仍是进度卡（且跳过已撤回消息）：原地更新
	t.Run("in place when remote latest non-deleted message matches progress card", func(t *testing.T) {
		fc := &fakeClient{
			recentMsgs: []FetchedMessage{
				{MessageID: "om_recalled", Deleted: true},
				{MessageID: "om_reply_1"},
			},
		}
		fh := newFakeHost()
		gw := NewGateway(t.TempDir(), fc, fh)
		gw.Reload(Config{OwnerOpenID: "ou_owner", Bots: []BotConfig{bot}})

		gw.HandleMessage(context.Background(), bot, MessageEvent{
			MessageID: "om_q4", ChatID: "oc_p2p2", ChatType: "p2p",
			SenderID: "ou_owner", Content: "查一下",
		})
		gw.OnExecFinished("self/d_oc_p2p2", "x_1", "done", "直接更新")
		if len(fc.sent) != 1 || len(fc.patched) != 1 || !strings.Contains(fc.patched[0].cardJSON, "直接更新") {
			t.Fatalf("expected in-place patch when latest non-deleted message matches progress card, sent=%d patched=%+v", len(fc.sent), fc.patched)
		}
	})

	// 5. 原地 PatchCard 失败时自动降级发送新结果卡
	t.Run("fallback to new card when in-place patch fails", func(t *testing.T) {
		fc := &fakeClient{patchErr: errors.New("patch failed")}
		fh := newFakeHost()
		gw := NewGateway(t.TempDir(), fc, fh)
		gw.Reload(Config{OwnerOpenID: "ou_owner", Bots: []BotConfig{bot}})

		gw.HandleMessage(context.Background(), bot, MessageEvent{
			MessageID: "om_q5", ChatID: "oc_p2p3", ChatType: "p2p",
			SenderID: "ou_owner", Content: "查一下",
		})
		gw.OnExecFinished("self/d_oc_p2p3", "x_1", "done", "降级发送结果")
		if len(fc.sent) != 2 || !strings.Contains(fc.sent[1].cardJSON, "降级发送结果") {
			t.Fatalf("expected fallback new result card on patch error, got %+v", fc.sent)
		}
	})

	// 6. 在途 tickProgress 补丁不得覆写 OnExecFinished 的最终结果卡
	t.Run("in-flight progress patch cannot overwrite final result", func(t *testing.T) {
		tickStarted := make(chan struct{})
		releaseTick := make(chan struct{})
		fc := &fakeClient{
			patchHook: func(_ context.Context, _, cardJSON string) error {
				if strings.Contains(cardJSON, "exec_stop") {
					select {
					case <-tickStarted:
					default:
						close(tickStarted)
					}
					<-releaseTick
				}
				return nil
			},
		}
		fh := newFakeHost()
		gw := NewGateway(t.TempDir(), fc, fh)
		gw.Reload(Config{OwnerOpenID: "ou_owner", Bots: []BotConfig{bot}})

		gw.HandleMessage(context.Background(), bot, MessageEvent{
			MessageID: "om_q6", ChatID: "oc_p2p4", ChatType: "p2p",
			SenderID: "ou_owner", Content: "查一下并发",
		})
		// 修改 snapshot 触发 tickProgress 真正发出 PatchCard
		fh.mu.Lock()
		snap := fh.execSnaps["x_1"]
		snap.Tool = "grep"
		fh.execSnaps["x_1"] = snap
		fh.mu.Unlock()

		tickDone := make(chan struct{})
		go func() {
			defer close(tickDone)
			gw.tickProgress(context.Background())
		}()
		<-tickStarted

		finishDone := make(chan struct{})
		go func() {
			defer close(finishDone)
			gw.OnExecFinished("self/d_oc_p2p4", "x_1", "done", "最终唯一结果")
		}()
		time.Sleep(30 * time.Millisecond)
		close(releaseTick)
		<-tickDone
		<-finishDone

		fc.mu.Lock()
		defer fc.mu.Unlock()
		if len(fc.patched) < 2 {
			t.Fatalf("expected both tick and final patches, got %d", len(fc.patched))
		}
		last := fc.patched[len(fc.patched)-1]
		if !strings.Contains(last.cardJSON, "最终唯一结果") {
			t.Fatalf("final patch must be the result card, got: %s", last.cardJSON)
		}
	})

	// 7. 原地补丁超时后，降级发送新结果卡仍拥有独立未过期上下文
	t.Run("fallback reply keeps fresh context after in-place patch timeout", func(t *testing.T) {
		var replyCtxErr error
		fc := &fakeClient{
			patchHook: func(_ context.Context, _, _ string) error {
				return context.DeadlineExceeded
			},
			replyHook: func(ctx context.Context, _ string, _ bool, _ string) error {
				replyCtxErr = ctx.Err()
				return nil
			},
		}
		fh := newFakeHost()
		gw := NewGateway(t.TempDir(), fc, fh)
		gw.Reload(Config{OwnerOpenID: "ou_owner", Bots: []BotConfig{bot}})

		gw.HandleMessage(context.Background(), bot, MessageEvent{
			MessageID: "om_q7", ChatID: "oc_p2p5", ChatType: "p2p",
			SenderID: "ou_owner", Content: "查一下超时降级",
		})
		gw.OnExecFinished("self/d_oc_p2p5", "x_1", "done", "超时降级结果")
		if replyCtxErr != nil {
			t.Fatalf("expected fresh context for fallback reply, got %v", replyCtxErr)
		}
		if len(fc.sent) != 2 || !strings.Contains(fc.sent[1].cardJSON, "超时降级结果") {
			t.Fatalf("expected fallback result card sent, got %+v", fc.sent)
		}
	})

	// 8. 话题模式重试保留 inThread / threadKey，且按话题列表检查最新消息
	t.Run("thread retry preserves thread scope", func(t *testing.T) {
		threadBot := NormalizeBot(BotConfig{
			ID: "lbot_t", Name: "Works Triage", Profile: "bot-a", Enabled: true,
			WorkspaceID: "w_works", Agent: "antigravity", PermissionMode: PermAsk,
			SessionMode: SessionThread, TriggerPolicy: TriggerSelf,
		})
		fc := &fakeClient{
			threadMsgs: []FetchedMessage{{MessageID: "om_reply_1"}},
		}
		fh := newFakeHost()
		gw := NewGateway(t.TempDir(), fc, fh)
		gw.Reload(Config{OwnerOpenID: "ou_owner", Bots: []BotConfig{threadBot}})

		gw.HandleMessage(context.Background(), threadBot, MessageEvent{
			MessageID: "om_t1", ChatID: "oc_grp_t", ChatType: "group", ThreadID: "omt_1",
			SenderID: "ou_owner", Content: "@Works Triage 查话题", Mentions: []MessageMention{{Name: "Works Triage"}},
		})
		now := time.Now()
		fh.mu.Lock()
		fh.execSnaps["x_retry"] = ExecSnap{ID: "x_retry", Discussion: "self/d_oc_grp_t", Agent: "antigravity", Workspace: "Works/main", State: "running", StartedAt: &now}
		fh.mu.Unlock()

		retryVal, _ := json.Marshal(map[string]string{"action": "exec_retry", "botId": threadBot.ID, "execId": "x_1"})
		gw.HandleCardAction(context.Background(), threadBot, CardActionEvent{
			EventID: "ev_retry_1", OperatorID: "ou_owner", ChatID: "oc_grp_t", MessageID: "om_reply_1", ActionValue: string(retryVal),
		})
		gw.OnExecFinished("self/d_oc_grp_t", "x_retry", "done", "话题重试完成")
		if fc.threadCalls == 0 {
			t.Fatalf("expected thread fetcher to be consulted for thread retry")
		}
		if len(fc.sent) != 1 || !strings.Contains(fc.patched[len(fc.patched)-1].cardJSON, "话题重试完成") {
			t.Fatalf("expected in-place thread result update, sent=%d patched=%+v", len(fc.sent), fc.patched)
		}
	})
}

func TestGatewayP2PStagedAttachments(t *testing.T) {
	bot := NormalizeBot(BotConfig{
		ID: "lbot_1", Name: "Works Triage", Profile: "bot-a", Enabled: true,
		WorkspaceID: "w_works", Agent: "antigravity", PermissionMode: PermAsk,
		SessionMode: SessionChat, TriggerPolicy: TriggerSelf,
	})

	t.Run("file then reply instruction merges once without duplicate parent fetch", func(t *testing.T) {
		fc := &fakeClient{
			fetchedMsgs: map[string]FetchedMessage{
				"om_file_1": {MessageID: "om_file_1", MsgType: "file", Content: "[file] helper.zip"},
			},
		}
		fh := newFakeHost()
		gw := NewGateway(t.TempDir(), fc, fh)
		gw.Reload(Config{OwnerOpenID: "ou_owner", Bots: []BotConfig{bot}})

		// 1. 单聊先发纯文件：只暂存并回暂存卡，不触发执行也不提前下载
		gw.HandleMessage(context.Background(), bot, MessageEvent{
			MessageID:   "om_file_1",
			ChatID:      "oc_p2p_1",
			ChatType:    "p2p",
			SenderID:    "ou_owner",
			MessageType: "file",
			RawContent:  json.RawMessage(`{"file_key":"file_k1","file_name":"helper-2.1.10.zip"}`),
		})
		if len(fh.posted) != 0 {
			t.Fatalf("standalone file in p2p must not trigger execution immediately, posted=%v", fh.posted)
		}
		if len(fc.downloadCalls) != 0 {
			t.Fatalf("standalone file should defer download until submission, downloads=%v", fc.downloadCalls)
		}
		if len(fc.sent) != 1 || !strings.Contains(fc.sent[0].cardJSON, "helper-2.1.10.zip") || !strings.Contains(fc.sent[0].cardJSON, "staged_send") {
			t.Fatalf("expected staged attachment card with filename and actions, got %+v", fc.sent)
		}

		// 2. 紧接着引用回复该文件下达指令：合并附件、保留原始文件名、不重复拉取父消息、收拢暂存卡
		gw.HandleMessage(context.Background(), bot, MessageEvent{
			MessageID:   "om_text_1",
			ParentID:    "om_file_1",
			ChatID:      "oc_p2p_1",
			ChatType:    "p2p",
			SenderID:    "ou_owner",
			MessageType: "text",
			Content:     "Analyze the plugin mechanism",
		})
		if len(fh.posted) != 1 || !strings.Contains(fh.posted[0], "Analyze the plugin mechanism") {
			t.Fatalf("expected single merged post, got %v", fh.posted)
		}
		if len(fh.postedAtts) != 1 || len(fh.postedAtts[0]) != 1 || filepath.Base(fh.postedAtts[0][0]) != "helper-2.1.10.zip" {
			t.Fatalf("expected attachment preserving original filename, got %+v", fh.postedAtts)
		}
		if fc.getMsgCalls != 0 {
			t.Fatalf("parent file already covered by staged batch should not be re-fetched, getMsgCalls=%d", fc.getMsgCalls)
		}
		if len(fc.patched) != 1 || strings.Contains(fc.patched[0].cardJSON, "staged_send") {
			t.Fatalf("expected staged card to be closed after submission, patched=%+v", fc.patched)
		}
	})

	t.Run("multiple attachments patch one card, slash command preserves staged, plain text merges all", func(t *testing.T) {
		fc := &fakeClient{}
		fh := newFakeHost()
		gw := NewGateway(t.TempDir(), fc, fh)
		gw.Reload(Config{OwnerOpenID: "ou_owner", Bots: []BotConfig{bot}})

		gw.HandleMessage(context.Background(), bot, MessageEvent{
			MessageID: "om_f1", ChatID: "oc_p2p_2", ChatType: "p2p", SenderID: "ou_owner",
			MessageType: "file", RawContent: json.RawMessage(`{"file_key":"fk_a","file_name":"crash.log"}`),
		})
		gw.HandleMessage(context.Background(), bot, MessageEvent{
			MessageID: "om_img1", ChatID: "oc_p2p_2", ChatType: "p2p", SenderID: "ou_owner",
			MessageType: "image", RawContent: json.RawMessage(`{"image_key":"ik_b"}`),
		})
		if len(fc.sent) != 1 {
			t.Fatalf("second attachment should patch existing staged card instead of spamming, sent=%d", len(fc.sent))
		}
		if len(fc.patched) != 1 || !strings.Contains(fc.patched[0].cardJSON, "crash.log") {
			t.Fatalf("expected staged card patch containing crash.log, patched=%+v", fc.patched)
		}

		// /status 不消费暂存附件
		gw.HandleMessage(context.Background(), bot, MessageEvent{
			MessageID: "om_status", ChatID: "oc_p2p_2", ChatType: "p2p", SenderID: "ou_owner",
			MessageType: "text", Content: "/status",
		})
		if len(fh.posted) != 0 {
			t.Fatalf("/status should not submit staged attachments")
		}

		// 直接发文字指令（不引用回复）合并全部 2 个附件
		gw.HandleMessage(context.Background(), bot, MessageEvent{
			MessageID: "om_go", ChatID: "oc_p2p_2", ChatType: "p2p", SenderID: "ou_owner",
			MessageType: "text", Content: "Compare log and screenshot",
		})
		if len(fh.posted) != 1 || len(fh.postedAtts[0]) != 2 {
			t.Fatalf("expected 2 merged attachments, posted=%d atts=%+v", len(fh.posted), fh.postedAtts)
		}
		if filepath.Base(fh.postedAtts[0][0]) != "crash.log" || filepath.Base(fh.postedAtts[0][1]) != "ik_b" {
			t.Fatalf("unexpected merged attachment names: %+v", fh.postedAtts[0])
		}
	})

	t.Run("send now button submits staged attachments in place", func(t *testing.T) {
		fc := &fakeClient{}
		fh := newFakeHost()
		gw := NewGateway(t.TempDir(), fc, fh)
		gw.Reload(Config{OwnerOpenID: "ou_owner", Bots: []BotConfig{bot}})

		gw.HandleMessage(context.Background(), bot, MessageEvent{
			MessageID: "om_img_only", ChatID: "oc_p2p_3", ChatType: "p2p", SenderID: "ou_owner",
			MessageType: "image", RawContent: json.RawMessage(`{"image_key":"ik_only"}`),
		})
		// 提取 batchId
		var batchID string
		gw.mu.Lock()
		for _, b := range gw.staged {
			batchID = b.id
		}
		gw.mu.Unlock()
		if batchID == "" {
			t.Fatal("expected staged batch to exist")
		}

		sendVal, _ := json.Marshal(map[string]string{"action": "staged_send", "botId": bot.ID, "batchId": batchID})
		gw.HandleCardAction(context.Background(), bot, CardActionEvent{
			EventID: "ev_send_now", OperatorID: "ou_owner", ChatID: "oc_p2p_3",
			MessageID: "om_reply_1", ActionValue: string(sendVal),
		})
		if len(fh.posted) != 1 || len(fh.postedAtts[0]) != 1 {
			t.Fatalf("expected staged_send to post 1 attachment, posted=%d atts=%+v", len(fh.posted), fh.postedAtts)
		}
		if len(fc.patched) != 1 || !strings.Contains(fc.patched[0].cardJSON, "exec_stop") {
			t.Fatalf("expected staged card to be patched into progress card, patched=%+v", fc.patched)
		}
	})

	t.Run("cancel button and TTL expiry prevent stale merge", func(t *testing.T) {
		fc := &fakeClient{}
		fh := newFakeHost()
		now := time.Date(2026, 11, 25, 20, 0, 0, 0, time.UTC)
		gw := NewGateway(t.TempDir(), fc, fh)
		gw.nowFn = func() time.Time { return now }
		gw.Reload(Config{OwnerOpenID: "ou_owner", Bots: []BotConfig{bot}})

		// 1. 取消按钮清空暂存
		gw.HandleMessage(context.Background(), bot, MessageEvent{
			MessageID: "om_c1", ChatID: "oc_p2p_4", ChatType: "p2p", SenderID: "ou_owner",
			MessageType: "file", RawContent: json.RawMessage(`{"file_key":"fk_c1","file_name":"wrong.zip"}`),
		})
		var batchID string
		gw.mu.Lock()
		for _, b := range gw.staged {
			batchID = b.id
		}
		gw.mu.Unlock()
		cancelVal, _ := json.Marshal(map[string]string{"action": "staged_cancel", "botId": bot.ID, "batchId": batchID})
		gw.HandleCardAction(context.Background(), bot, CardActionEvent{
			EventID: "ev_cancel_1", OperatorID: "ou_owner", ChatID: "oc_p2p_4",
			MessageID: "om_reply_1", ActionValue: string(cancelVal),
		})
		gw.HandleMessage(context.Background(), bot, MessageEvent{
			MessageID: "om_t1", ChatID: "oc_p2p_4", ChatType: "p2p", SenderID: "ou_owner",
			MessageType: "text", Content: "Normal question",
		})
		if len(fh.posted) != 1 || len(fh.postedAtts[0]) != 0 {
			t.Fatalf("cancelled attachment must not be merged, atts=%+v", fh.postedAtts)
		}

		// 2. 超过 TTL 后自动过期，不合并到后续消息
		gw.HandleMessage(context.Background(), bot, MessageEvent{
			MessageID: "om_exp1", ChatID: "oc_p2p_4", ChatType: "p2p", SenderID: "ou_owner",
			MessageType: "file", RawContent: json.RawMessage(`{"file_key":"fk_exp","file_name":"old.zip"}`),
		})
		now = now.Add(pendingAttachTTL + time.Minute)
		gw.HandleMessage(context.Background(), bot, MessageEvent{
			MessageID: "om_t2", ChatID: "oc_p2p_4", ChatType: "p2p", SenderID: "ou_owner",
			MessageType: "text", Content: "Later unrelated question",
		})
		if len(fh.posted) != 2 || len(fh.postedAtts[1]) != 0 {
			t.Fatalf("expired attachment must not be merged, atts=%+v", fh.postedAtts)
		}
	})

	t.Run("concurrent stage and immediate text leaves no orphan card", func(t *testing.T) {
		replyEntered := make(chan struct{})
		releaseReply := make(chan struct{})
		fc := &fakeClient{}
		fc.replyHook = func(_ context.Context, msgID string, _ bool, _ string) error {
			if msgID == "om_race_file" {
				close(replyEntered)
				<-releaseReply
			}
			return nil
		}
		fh := newFakeHost()
		gw := NewGateway(t.TempDir(), fc, fh)
		gw.Reload(Config{OwnerOpenID: "ou_owner", Bots: []BotConfig{bot}})

		var wg sync.WaitGroup
		wg.Add(1)
		go func() {
			defer wg.Done()
			gw.HandleMessage(context.Background(), bot, MessageEvent{
				MessageID: "om_race_file", ChatID: "oc_p2p_race", ChatType: "p2p", SenderID: "ou_owner",
				MessageType: "file", RawContent: json.RawMessage(`"{\"file_key\":\"fk_race\",\"file_name\":\"race.zip\"}"`),
			})
		}()
		<-replyEntered

		// 文件暂存卡尚未返回时，文字消息到达并消费该暂存批次
		textDone := make(chan struct{})
		go func() {
			defer close(textDone)
			gw.HandleMessage(context.Background(), bot, MessageEvent{
				MessageID: "om_race_txt", ParentID: "om_race_file", ChatID: "oc_p2p_race", ChatType: "p2p", SenderID: "ou_owner",
				MessageType: "text", Content: "Analyze race.zip",
			})
		}()
		// 放行第一次 ReplyCard，closeStagedCard 应等 cardMu 拿到 cardMsgID 后将其收拢
		close(releaseReply)
		wg.Wait()
		<-textDone

		if len(fh.posted) != 1 || len(fh.postedAtts[0]) != 1 || filepath.Base(fh.postedAtts[0][0]) != "race.zip" {
			t.Fatalf("expected double-encoded JSON raw_content to merge with filename race.zip, posted=%v atts=%+v", fh.posted, fh.postedAtts)
		}
		if len(fc.patched) == 0 || strings.Contains(fc.patched[len(fc.patched)-1].cardJSON, "staged_send") {
			t.Fatalf("expected staged card to be closed after race, patched=%+v", fc.patched)
		}
	})

	t.Run("fallback to GetMessage when event lacks file_key and stale button closes card", func(t *testing.T) {
		fc := &fakeClient{
			fetchedMsgs: map[string]FetchedMessage{
				"om_nokey": {
					MessageID: "om_nokey",
					MsgType:   "file",
					Content:   "[file] dump.bin",
					Resources: []struct {
						Key       string `json:"key"`
						Type      string `json:"type"`
						LocalPath string `json:"local_path"`
						Error     bool   `json:"error"`
					}{{Key: "fk_mget", Type: "file", LocalPath: "dump.bin"}},
				},
			},
		}
		fh := newFakeHost()
		gw := NewGateway(t.TempDir(), fc, fh)
		gw.Reload(Config{OwnerOpenID: "ou_owner", Bots: []BotConfig{bot}})

		// 仅含渲染文本、无 raw_content/file_key 时也应暂存，提交时通过 GetMessage 拉取
		gw.HandleMessage(context.Background(), bot, MessageEvent{
			MessageID: "om_nokey", ChatID: "oc_p2p_5", ChatType: "p2p", SenderID: "ou_owner",
			MessageType: "file", Content: "[file] dump.bin",
		})
		if len(fh.posted) != 0 {
			t.Fatalf("file without raw_content should still stage instead of running immediately")
		}
		gw.HandleMessage(context.Background(), bot, MessageEvent{
			MessageID: "om_nokey_go", ChatID: "oc_p2p_5", ChatType: "p2p", SenderID: "ou_owner",
			MessageType: "text", Content: "Inspect it",
		})
		if len(fh.posted) != 1 || len(fh.postedAtts[0]) != 1 || filepath.Base(fh.postedAtts[0][0]) != "dump.bin" {
			t.Fatalf("expected GetMessage fallback attachment dump.bin, atts=%+v", fh.postedAtts)
		}

		// 重启后内存暂存丢失时，点击旧卡片按钮应能收拢旧卡
		patchedBefore := len(fc.patched)
		staleVal, _ := json.Marshal(map[string]string{"action": "staged_send", "botId": bot.ID, "batchId": "sb_missing"})
		gw.HandleCardAction(context.Background(), bot, CardActionEvent{
			EventID: "ev_stale_btn", OperatorID: "ou_owner", ChatID: "oc_p2p_5",
			MessageID: "om_old_staged_card", ActionValue: string(staleVal),
		})
		if len(fc.patched) != patchedBefore+1 || strings.Contains(fc.patched[len(fc.patched)-1].cardJSON, "staged_send") {
			t.Fatalf("expected stale staged card button to close old card, patched=%+v", fc.patched)
		}
	})
}

func TestGatewayTextDebounceAndStageCoalescing(t *testing.T) {
	bot := NormalizeBot(BotConfig{
		ID: "lbot_1", Name: "Works Triage", Profile: "bot-a", Enabled: true,
		WorkspaceID: "w_works", Agent: "antigravity", PermissionMode: PermAsk,
		SessionMode: SessionChat, TriggerPolicy: TriggerSelf,
	})

	t.Run("rapid text messages coalesce into one turn with staged file", func(t *testing.T) {
		fc := &fakeClient{}
		fh := newFakeHost()
		gw := NewGateway(t.TempDir(), fc, fh)
		gw.debounceWindow = 40 * time.Millisecond
		gw.debounceMax = 300 * time.Millisecond
		gw.Reload(Config{OwnerOpenID: "ou_owner", Bots: []BotConfig{bot}})
		defer gw.Stop()

		// 先发一个纯文件进入暂存，再连发两句文字
		gw.HandleMessage(context.Background(), bot, MessageEvent{
			MessageID: "om_df1", ChatID: "oc_p2p_deb", ChatType: "p2p", SenderID: "ou_owner",
			MessageType: "file", RawContent: json.RawMessage(`{"file_key":"fk_deb","file_name":"trace.log"}`),
		})
		gw.HandleMessage(context.Background(), bot, MessageEvent{
			MessageID: "om_dt1", ParentID: "om_df1", ChatID: "oc_p2p_deb", ChatType: "p2p", SenderID: "ou_owner",
			MessageType: "text", Content: "First line",
		})
		gw.HandleMessage(context.Background(), bot, MessageEvent{
			MessageID: "om_dt2", ChatID: "oc_p2p_deb", ChatType: "p2p", SenderID: "ou_owner",
			MessageType: "text", Content: "Second line",
		})

		deadline := time.Now().Add(time.Second)
		for time.Now().Before(deadline) {
			fc.mu.Lock()
			sentN := len(fc.sent)
			fc.mu.Unlock()
			if sentN >= 2 {
				break
			}
			time.Sleep(10 * time.Millisecond)
		}

		fh.mu.Lock()
		posted := append([]string(nil), fh.posted...)
		atts := append([][]string(nil), fh.postedAtts...)
		fh.mu.Unlock()
		if len(posted) != 1 || !strings.Contains(posted[0], "First line\nSecond line") || !strings.Contains(posted[0], "parent_id=om_df1") {
			t.Fatalf("expected coalesced prompt preserving first parent_id, got %v", posted)
		}
		if len(atts) != 1 || len(atts[0]) != 1 || filepath.Base(atts[0][0]) != "trace.log" {
			t.Fatalf("expected staged trace.log merged with debounced text, got %+v", atts)
		}
		fc.mu.Lock()
		lastSent := fc.sent[len(fc.sent)-1]
		fc.mu.Unlock()
		if lastSent.target != "om_dt2" {
			t.Fatalf("expected progress card replying to latest message om_dt2, got %q", lastSent.target)
		}
	})

	t.Run("slash /new cancels pending debounced text", func(t *testing.T) {
		fc := &fakeClient{}
		fh := newFakeHost()
		gw := NewGateway(t.TempDir(), fc, fh)
		gw.debounceWindow = 60 * time.Millisecond
		gw.Reload(Config{OwnerOpenID: "ou_owner", Bots: []BotConfig{bot}})
		defer gw.Stop()

		gw.HandleMessage(context.Background(), bot, MessageEvent{
			MessageID: "om_cancel_t1", ChatID: "oc_p2p_deb2", ChatType: "p2p", SenderID: "ou_owner",
			MessageType: "text", Content: "Abort me",
		})
		gw.HandleMessage(context.Background(), bot, MessageEvent{
			MessageID: "om_cancel_new", ChatID: "oc_p2p_deb2", ChatType: "p2p", SenderID: "ou_owner",
			MessageType: "text", Content: "/new",
		})
		time.Sleep(120 * time.Millisecond)

		fh.mu.Lock()
		n := len(fh.posted)
		fh.mu.Unlock()
		if n != 0 {
			t.Fatalf("expected /new to cancel pending debounced text, posted=%d", n)
		}
	})

	t.Run("non-debounceable post message prepends pending debounced text in order", func(t *testing.T) {
		fc := &fakeClient{}
		fh := newFakeHost()
		gw := NewGateway(t.TempDir(), fc, fh)
		gw.debounceWindow = 80 * time.Millisecond
		gw.Reload(Config{OwnerOpenID: "ou_owner", Bots: []BotConfig{bot}})
		defer gw.Stop()

		gw.HandleMessage(context.Background(), bot, MessageEvent{
			MessageID: "om_ord_1", ChatID: "oc_p2p_deb3", ChatType: "p2p", SenderID: "ou_owner",
			MessageType: "text", Content: "Context line",
		})
		gw.HandleMessage(context.Background(), bot, MessageEvent{
			MessageID: "om_ord_2", ChatID: "oc_p2p_deb3", ChatType: "p2p", SenderID: "ou_owner",
			MessageType: "post", Content: "Rich post body",
		})
		time.Sleep(120 * time.Millisecond)

		fh.mu.Lock()
		posted := append([]string(nil), fh.posted...)
		fh.mu.Unlock()
		if len(posted) != 1 || !strings.Contains(posted[0], "Context line\nRich post body") {
			t.Fatalf("expected post message to prepend pending text in order, got %v", posted)
		}
	})
}

func TestGatewayQueuedInterruptPrevious(t *testing.T) {
	bot := NormalizeBot(BotConfig{
		ID: "lbot_1", Name: "Works Triage", Profile: "bot-a", Enabled: true,
		WorkspaceID: "w_works", Agent: "antigravity", PermissionMode: PermAsk,
		SessionMode: SessionChat, TriggerPolicy: TriggerSelf,
	})
	fc := &fakeClient{}
	fh := newFakeHost()
	gw := NewGateway(t.TempDir(), fc, fh)
	gw.Reload(Config{OwnerOpenID: "ou_owner", Bots: []BotConfig{bot}})

	// 先建立已有讨论与正在运行的前序轮次 x_prev
	_, _ = fh.EnsureDiscussion(bot, "oc_p2p_q", "p2p", "", "dm")
	now := time.Now()
	fh.mu.Lock()
	fh.execSnaps["x_prev"] = ExecSnap{ID: "x_prev", Discussion: "self/d_oc_p2p_q", Agent: "antigravity", Workspace: "Works/main", State: "running", StartedAt: &now}
	fh.nextExecID = "x_2"
	fh.nextExecStat = "queued"
	fh.mu.Unlock()

	// 新消息进入排队态，初始进度卡应展示「中止前序并执行」按钮
	gw.HandleMessage(context.Background(), bot, MessageEvent{
		MessageID: "om_q_new", ChatID: "oc_p2p_q", ChatType: "p2p", SenderID: "ou_owner",
		MessageType: "text", Content: "Urgent new task",
	})
	if len(fc.sent) != 1 || !strings.Contains(fc.sent[0].cardJSON, "exec_interrupt_prev") {
		t.Fatalf("expected queued card to show interrupt-previous button, sent=%+v", fc.sent)
	}

	// 同事点击无权中止前序
	intVal, _ := json.Marshal(map[string]string{"action": "exec_interrupt_prev", "botId": bot.ID, "execId": "x_2"})
	gw.HandleCardAction(context.Background(), bot, CardActionEvent{
		EventID: "ev_int_colleague", OperatorID: "ou_colleague", ChatID: "oc_p2p_q", ActionValue: string(intVal),
	})
	if len(fh.stopped) != 0 {
		t.Fatalf("colleague must not interrupt previous execution, stopped=%v", fh.stopped)
	}

	// 主人点击后中止前序轮次 x_prev
	gw.HandleCardAction(context.Background(), bot, CardActionEvent{
		EventID: "ev_int_owner", OperatorID: "ou_owner", ChatID: "oc_p2p_q", ActionValue: string(intVal),
	})
	if len(fh.stopped) != 1 || fh.stopped[0] != "x_prev" {
		t.Fatalf("expected owner click to stop x_prev, stopped=%v", fh.stopped)
	}

	// 前序停止、x_2 开始运行后，tickProgress 应自动移除「中止前序并执行」按钮
	fh.mu.Lock()
	fh.execSnaps["x_prev"] = ExecSnap{ID: "x_prev", Discussion: "self/d_oc_p2p_q", Agent: "antigravity", Workspace: "Works/main", State: "stopped"}
	fh.execSnaps["x_2"] = ExecSnap{ID: "x_2", Discussion: "self/d_oc_p2p_q", Agent: "antigravity", Workspace: "Works/main", State: "running", StartedAt: &now}
	fh.mu.Unlock()
	gw.tickProgress(context.Background())
	if len(fc.patched) == 0 || strings.Contains(fc.patched[len(fc.patched)-1].cardJSON, "exec_interrupt_prev") {
		t.Fatalf("expected interrupt-previous button to disappear once running, patched=%+v", fc.patched)
	}
}

type fakeChatOverrideHost struct {
	*fakeHost
	mutated []ChatOverride
}

func (h *fakeChatOverrideHost) MutateChatOverride(botID, chatID string, mutate func(*ChatOverride)) (BotConfig, error) {
	ov := ChatOverride{ChatID: chatID}
	mutate(&ov)
	ov = NormalizeChatOverride(ov)
	h.mutated = append(h.mutated, ov)
	return BotConfig{ID: botID, Chats: []ChatOverride{ov}}, nil
}

func TestGatewayChatOverridesAndSecurity(t *testing.T) {
	customRole := "Focus on Posse development"
	emptyRole := ""
	bot := NormalizeBot(BotConfig{
		ID: "lbot_1", Name: "Works Triage", Profile: "bot-a", Enabled: true,
		WorkspaceID: "w_works", Agent: "antigravity", Model: "gemini-4", Effort: "high",
		PermissionMode: PermAsk, SessionMode: SessionChat, TriggerPolicy: TriggerSelf,
		Role: "Default Works triage role",
		Chats: []ChatOverride{
			{
				ChatID: "oc_private", Name: "Posse Dev", WorkspaceID: "w_posse",
				Agent: "codex", Role: &customRole, PermissionMode: PermSkip, NoMention: true,
			},
			{
				ChatID: "oc_open_grp", Name: "Team Helper", Role: &emptyRole,
				TriggerPolicy: TriggerGroup, NoMention: true,
			},
		},
	})

	// 1. EffectiveForChat 继承/覆写/无角色/切换 Agent 清空 Model&Effort
	eff, noMention := bot.EffectiveForChat("oc_private")
	if !noMention || eff.Name != "Posse Dev" || eff.WorkspaceID != "w_posse" || eff.Agent != "codex" ||
		eff.Model != "" || eff.Effort != "" || eff.Role != customRole || eff.PermissionMode != PermSkip {
		t.Fatalf("unexpected effective config for oc_private: noMention=%v eff=%+v", noMention, eff)
	}
	effOpen, noMentionOpen := bot.EffectiveForChat("oc_open_grp")
	if !noMentionOpen || effOpen.Role != "" || effOpen.Agent != "antigravity" || effOpen.Model != "gemini-4" {
		t.Fatalf("unexpected effective config for oc_open_grp: noMention=%v eff=%+v", noMentionOpen, effOpen)
	}

	// 2. 免 @ 私有群：无需 @ 直发消息、纯附件走暂存合并、@ 他人不抢话、卡片标题用群分身名
	fc := &fakeClient{}
	fh := &fakeChatOverrideHost{fakeHost: newFakeHost()}
	gw := NewGateway(t.TempDir(), fc, fh)
	gw.Reload(Config{OwnerOpenID: "ou_owner", Bots: []BotConfig{bot}})
	gw.mu.Lock()
	gw.runners[bot.ID] = &botRunner{cfg: bot, ident: BotIdentity{BotOpenID: "ou_bot_real", BotAppName: "Works Triage"}}
	gw.mu.Unlock()

	// 显式 @ 同事（即使同事名字碰巧与分身名相同）必须忽略
	gw.HandleMessage(context.Background(), bot, MessageEvent{
		MessageID: "om_other_colleague", ChatID: "oc_private", ChatType: "group", SenderID: "ou_owner",
		MessageType: "text", Content: "@Posse Dev please check",
		Mentions: []MessageMention{{ID: "ou_colleague_same_name", Name: "Posse Dev"}},
	})
	if len(fh.posted) != 0 {
		t.Fatalf("mentioning colleague by ID in noMention group must be ignored, posted=%v", fh.posted)
	}

	// 免 @ 私有群先发文件 -> 暂存不抢跑，再直接发文字 -> 合并执行且卡片标题显示群分身名 Posse Dev
	gw.HandleMessage(context.Background(), bot, MessageEvent{
		MessageID: "om_priv_file", ChatID: "oc_private", ChatType: "group", SenderID: "ou_owner",
		MessageType: "file", RawContent: json.RawMessage(`{"file_key":"fk_priv","file_name":"spec.md"}`),
	})
	if len(fh.posted) != 0 {
		t.Fatalf("standalone file in noMention group should stage, posted=%v", fh.posted)
	}
	gw.HandleMessage(context.Background(), bot, MessageEvent{
		MessageID: "om_priv_txt", ChatID: "oc_private", ChatType: "group", SenderID: "ou_owner",
		MessageType: "text", Content: "Review this spec",
	})
	if len(fh.posted) != 1 || len(fh.postedAtts[0]) != 1 || filepath.Base(fh.postedAtts[0][0]) != "spec.md" {
		t.Fatalf("expected merged file + text in noMention group, posted=%v atts=%+v", fh.posted, fh.postedAtts)
	}
	if !strings.Contains(fc.sent[len(fc.sent)-1].cardJSON, "Posse Dev") {
		t.Fatalf("expected progress card title to use group persona name, got %s", fc.sent[len(fc.sent)-1].cardJSON)
	}

	// 3. 安全边界：全群可触发的群里，非主人不能执行 /agent /ws /new /stop /mention /role
	for _, cmd := range []string{"/agent codex", "/ws w_posse", "/new", "/stop", "/mention on", "/role hack"} {
		gw.HandleMessage(context.Background(), bot, MessageEvent{
			MessageID: "om_nonowner_" + cmd, ChatID: "oc_open_grp", ChatType: "group", SenderID: "ou_colleague",
			MessageType: "text", Content: cmd,
		})
	}
	if fh.switched != "" || fh.switchedWS != "" || len(fh.mutated) != 0 {
		t.Fatalf("non-owner must not execute privileged slash commands, switched=%q ws=%q mutated=%+v", fh.switched, fh.switchedWS, fh.mutated)
	}

	// 4. 主人在群内执行 /mention off 与 /role clear 成功写入
	gw.HandleMessage(context.Background(), bot, MessageEvent{
		MessageID: "om_owner_mention", ChatID: "oc_open_grp", ChatType: "group", SenderID: "ou_owner",
		MessageType: "text", Content: "/mention off",
	})
	gw.HandleMessage(context.Background(), bot, MessageEvent{
		MessageID: "om_owner_role", ChatID: "oc_open_grp", ChatType: "group", SenderID: "ou_owner",
		MessageType: "text", Content: "/role clear",
	})
	if len(fh.mutated) != 2 || !fh.mutated[0].NoMention || fh.mutated[1].Role == nil || *fh.mutated[1].Role != "" {
		t.Fatalf("expected owner /mention off and /role clear mutations, got %+v", fh.mutated)
	}

	// 5. parseBotChats 解析真实 +chat-list 输出
	res, err := parseBotChats([]byte(`{"ok":true,"data":{"chats":[{"chat_id":"oc_1","name":"Group A"},{"chat_id":"oc_1","name":"Dup"},{"chat_id":"oc_2","name":""}],"has_more":true}}`))
	if err != nil || !res.HasMore || len(res.Chats) != 2 || res.Chats[0].Name != "Group A" || res.Chats[1].Name != "oc_2" {
		t.Fatalf("unexpected parseBotChats result: %+v err=%v", res, err)
	}

	// 6. 仅文本事件：句中 @同事 不抢话；@OpsSupport 不误匹配名为 Ops 的机器人
	opsBot := NormalizeBot(BotConfig{ID: "lbot_ops", Name: "Ops", Profile: "bot-ops", Enabled: true})
	if isMentioningBot(opsBot, BotIdentity{BotAppName: "Ops"}, MessageEvent{Content: "@OpsSupport help"}) {
		t.Fatalf("@OpsSupport must not match bot named Ops")
	}
	if !isMentioningOthersOnly(bot, BotIdentity{BotAppName: "Works Triage"}, MessageEvent{Content: "Thanks, @Alice please check"}) {
		t.Fatalf("mid-sentence @Alice without Mentions array must be treated as mentioning others")
	}
	if isMentioningOthersOnly(bot, BotIdentity{BotAppName: "Works Triage"}, MessageEvent{Content: "email test@example.com please check"}) {
		t.Fatalf("email address must not be treated as @mention")
	}

	// 7. 删除机器人后，待防抖文本消息必须丢弃，不得沿用旧快照派发
	fcDel := &fakeClient{}
	fhDel := newFakeHost()
	gwDel := NewGateway(t.TempDir(), fcDel, fhDel)
	gwDel.debounceWindow = 50 * time.Millisecond
	gwDel.Reload(Config{OwnerOpenID: "ou_owner", Bots: []BotConfig{bot}})
	defer gwDel.Stop()
	gwDel.HandleMessage(context.Background(), bot, MessageEvent{
		MessageID: "om_del_1", ChatID: "oc_private", ChatType: "group", SenderID: "ou_owner",
		MessageType: "text", Content: "Should drop after bot deleted",
	})
	gwDel.Reload(Config{OwnerOpenID: "ou_owner", Bots: nil})
	time.Sleep(100 * time.Millisecond)
	fhDel.mu.Lock()
	delPosted := len(fhDel.posted)
	fhDel.mu.Unlock()
	if delPosted != 0 {
		t.Fatalf("debounced text must be dropped when bot is deleted, posted=%d", delPosted)
	}

	// 8. 旧选择器卡在无活跃绑定时不重建失效绑定，改为过期提示
	staleSwitchVal, _ := json.Marshal(map[string]string{"action": "switch_ws", "botId": bot.ID, "chatId": "oc_expired_chat", "threadKey": "omt_old", "workspaceId": "w_posse"})
	patchedBefore := len(fc.patched)
	gw.HandleCardAction(context.Background(), bot, CardActionEvent{
		EventID: "ev_stale_sw", OperatorID: "ou_owner", ChatID: "oc_expired_chat", MessageID: "om_picker_old", ActionValue: string(staleSwitchVal),
	})
	if len(fc.patched) != patchedBefore+1 || fh.switchedWS != "" {
		t.Fatalf("stale picker must expire without creating binding, switchedWS=%q patched=%+v", fh.switchedWS, fc.patched)
	}
}
