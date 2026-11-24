package lark

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"posse/internal/store"
)

type fakeClient struct {
	mu          sync.Mutex
	sent        []fakeCardCall
	patched     []fakeCardCall
	files       []string
	nextID      int
	fetchedMsgs map[string]FetchedMessage
	recentMsgs  []FetchedMessage
	recentCalls int
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

func (f *fakeClient) ReplyCard(_ context.Context, profile, msgID string, inThread bool, cardJSON string) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.nextID++
	f.sent = append(f.sent, fakeCardCall{profile: profile, target: msgID, inThread: inThread, cardJSON: cardJSON})
	return "om_reply_" + string(rune('0'+f.nextID)), nil
}

func (f *fakeClient) PatchCard(_ context.Context, profile, cardMsgID, cardJSON string) error {
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

func (f *fakeClient) DownloadResource(context.Context, string, string, string, string, string) (string, error) {
	return "", nil
}

func (f *fakeClient) GetMessage(_ context.Context, _, messageID, downloadDir string) (FetchedMessage, error) {
	f.mu.Lock()
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

type fakeHost struct {
	mu          sync.Mutex
	bindings    map[string]store.LarkBinding
	cards       map[string]store.LarkCard
	events      map[string]bool
	posted      []string
	postedAtts  [][]string
	answered    map[string]string
	stopped     []string
	decided     map[string]string
	switched    string
	switchedWS  string
	execSnaps   map[string]ExecSnap
	termBinding map[string]store.LarkBinding
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
	h.execSnaps[id] = ExecSnap{ID: id, Discussion: b.DiscussionKey, Agent: b.Agent, Workspace: "Works/main", State: "running", StartedAt: &now}
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
	for _, s := range h.execSnaps {
		if s.Discussion == discKey {
			return s, true
		}
	}
	return ExecSnap{}, false
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
