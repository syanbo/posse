package lark

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"posse/internal/store"
)

func strPtr(s string) *string { return &s }

func TestChatOverrideModelEffortThreeState(t *testing.T) {
	bot := BotConfig{
		ID:      "lbot_1",
		Profile: "p1",
		Agent:   "claude",
		Model:   "opus",
		Effort:  "high",
		Chats: []ChatOverride{
			{ChatID: "oc_inherit"},
			{ChatID: "oc_default", Model: strPtr(""), Effort: strPtr("")},
			{ChatID: "oc_same_override", Agent: "claude", Model: strPtr("sonnet"), Effort: strPtr("low")},
			{ChatID: "oc_cross_agent", Agent: "codex", Model: strPtr("gpt-6.2-sol"), Effort: strPtr("xhigh")},
			{ChatID: "oc_cross_nil", Agent: "codex"},
		},
	}

	eff, _ := bot.EffectiveForChat("oc_inherit")
	if eff.Model != "opus" || eff.Effort != "high" {
		t.Fatalf("inherit failed: got (%q, %q)", eff.Model, eff.Effort)
	}

	eff, _ = bot.EffectiveForChat("oc_default")
	if eff.Model != "" || eff.Effort != "" {
		t.Fatalf("explicit empty should clear parent model/effort, got (%q, %q)", eff.Model, eff.Effort)
	}

	eff, _ = bot.EffectiveForChat("oc_same_override")
	if eff.Agent != "claude" || eff.Model != "sonnet" || eff.Effort != "low" {
		t.Fatalf("same-agent override failed: %+v", eff)
	}

	eff, _ = bot.EffectiveForChat("oc_cross_agent")
	if eff.Agent != "codex" || eff.Model != "gpt-6.2-sol" || eff.Effort != "xhigh" {
		t.Fatalf("cross-agent explicit override failed: %+v", eff)
	}

	eff, _ = bot.EffectiveForChat("oc_cross_nil")
	if eff.Agent != "codex" || eff.Model != "" || eff.Effort != "" {
		t.Fatalf("cross-agent nil should clear parent model/effort: %+v", eff)
	}
}

func TestCloneBotDeepCopiesModelEffortPointers(t *testing.T) {
	orig := BotConfig{
		ID:      "lbot_1",
		Profile: "p1",
		Agent:   "antigravity",
		Chats: []ChatOverride{
			{ChatID: "oc_1", Agent: "antigravity", Model: strPtr("gemini-4-neon"), Effort: strPtr("medium")},
		},
	}
	cloned := CloneBot(orig)
	*cloned.Chats[0].Model = "mutated"
	*cloned.Chats[0].Effort = "low"
	if *orig.Chats[0].Model != "gemini-4-neon" || *orig.Chats[0].Effort != "medium" {
		t.Fatalf("CloneBot leaked pointer mutation into original: %+v", orig.Chats[0])
	}
}

func TestChatOverrideModelEffortJSONRoundTrip(t *testing.T) {
	in := ChatOverride{
		ChatID: "oc_rt",
		Model:  strPtr(""),
		Effort: nil,
	}
	raw, err := json.Marshal(in)
	if err != nil {
		t.Fatal(err)
	}
	var out ChatOverride
	if err := json.Unmarshal(raw, &out); err != nil {
		t.Fatal(err)
	}
	if out.Model == nil || *out.Model != "" {
		t.Fatalf("explicit empty model lost in JSON round-trip: %+v", out.Model)
	}
	if out.Effort != nil {
		t.Fatalf("nil effort should stay nil in JSON round-trip: %+v", out.Effort)
	}
}

func TestValidateChatOverrideAndEffectiveBot(t *testing.T) {
	if err := ValidateChatOverride(ChatOverride{ChatID: "oc_1", Model: strPtr("opus")}); err == nil {
		t.Fatalf("expected error when model is set without agent")
	}
	if err := ValidateChatOverride(ChatOverride{ChatID: "oc_1", Effort: strPtr("high")}); err == nil {
		t.Fatalf("expected error when effort is set without agent")
	}
	if err := ValidateChatOverride(ChatOverride{ChatID: "oc_1", Model: strPtr(""), Effort: strPtr("")}); err != nil {
		t.Fatalf("explicit empty model/effort should be allowed without agent: %v", err)
	}
	if err := ValidateEffectiveBot(BotConfig{
		Profile: "p1",
		Agent:   "cursor",
		Model:   "gpt-5.3-codex",
		Effort:  "high",
	}); err == nil {
		t.Fatalf("cursor bot with effort should fail validation")
	}
	if err := ValidateEffectiveBot(BotConfig{
		Profile: "p1",
		Agent:   "claude",
		Chats: []ChatOverride{
			{ChatID: "oc_cur", Agent: "cursor", Model: strPtr("gpt-5.3-codex"), Effort: strPtr("high")},
		},
	}); err == nil {
		t.Fatalf("cursor chat override with effort should fail validation")
	}
}

func TestStatusIncludesModelLine(t *testing.T) {
	host := newFakeHost()
	cli := &fakeClient{}
	bot := BotConfig{
		ID:          "lbot_status",
		Name:        "StatusBot",
		Profile:     "p1",
		Enabled:     true,
		Agent:       "antigravity",
		Model:       "gemini-4-neon",
		Effort:      "high",
		WorkspaceID: "ws1",
	}
	gw := NewGateway(t.TempDir(), cli, host)
	gw.Reload(Config{OwnerOpenID: "ou_owner", Bots: []BotConfig{bot}})
	gw.HandleMessage(context.Background(), bot, MessageEvent{
		EventID:     "ev_status_1",
		MessageID:   "om_status_1",
		ChatID:      "oc_status",
		ChatType:    "p2p",
		SenderID:    "ou_owner",
		MessageType: "text",
		Content:     "/status",
	})
	if len(cli.sent) == 0 {
		t.Fatalf("expected status reply card")
	}
	cardJSON := cli.sent[len(cli.sent)-1].cardJSON
	if !strings.Contains(cardJSON, "gemini-4-neon (high)") {
		t.Fatalf("status card should contain configured model/effort, got: %s", cardJSON)
	}

	// 验证存在活跃绑定时也显示模型
	host.bindings[bot.ID+"|oc_status|"] = store.LarkBinding{
		ID:            "lb_1",
		BotID:         bot.ID,
		ChatID:        "oc_status",
		ChatType:      "p2p",
		DiscussionKey: "disc_1",
		MemberID:      "mb_1",
		WorkspaceID:   "ws1",
		Agent:         "antigravity",
		Active:        true,
	}
	gw.HandleMessage(context.Background(), bot, MessageEvent{
		EventID:     "ev_status_2",
		MessageID:   "om_status_2",
		ChatID:      "oc_status",
		ChatType:    "p2p",
		SenderID:    "ou_owner",
		MessageType: "text",
		Content:     "/status",
	})
	cardJSON = cli.sent[len(cli.sent)-1].cardJSON
	if !strings.Contains(cardJSON, "gemini-4-neon (high)") {
		t.Fatalf("active binding status card should contain model/effort, got: %s", cardJSON)
	}

	// I5 回归：仅配置 Effort 且模型为空时，/status 仍应展示档位而非丢弃
	botEffortOnly := bot
	botEffortOnly.ID = "lbot_effort_only"
	botEffortOnly.Model = ""
	botEffortOnly.Effort = "high"
	gw.HandleMessage(context.Background(), botEffortOnly, MessageEvent{
		EventID:     "ev_status_3",
		MessageID:   "om_status_3",
		ChatID:      "oc_effort_only",
		ChatType:    "p2p",
		SenderID:    "ou_owner",
		MessageType: "text",
		Content:     "/status",
	})
	cardJSON = cli.sent[len(cli.sent)-1].cardJSON
	if !strings.Contains(cardJSON, "(high)") {
		t.Fatalf("effort-only status card should keep effort label, got: %s", cardJSON)
	}
}

func TestCanUpdateProgressInPlaceIgnoresSystemMessages(t *testing.T) {
	cli := &fakeClient{
		recentMsgs: []FetchedMessage{
			{MessageID: "om_sys_photo", MsgType: "system", Content: "updated the group photo"},
			{MessageID: "om_progress_1", MsgType: "interactive"},
		},
	}
	host := newFakeHost()
	gw := NewGateway(t.TempDir(), cli, host)
	bot := BotConfig{ID: "lbot_1", Profile: "p1", Enabled: true}
	ac := &activeCard{chatID: "oc_1", cardMsgID: "om_progress_1"}
	binding := store.LarkBinding{BotID: "lbot_1", ChatID: "oc_1", ChatType: "group"}
	if !gw.canUpdateProgressInPlace(bot, binding, ac, "om_progress_1", false, "") {
		t.Fatalf("system messages (e.g. group photo change) should not count as interleaved user messages")
	}
}

func TestTickProgressClosesMergedCard(t *testing.T) {
	cli := &fakeClient{}
	host := newFakeHost()
	host.execSnaps["x_merged_1"] = ExecSnap{
		ID:         "x_merged_1",
		Discussion: "disc_1",
		Agent:      "antigravity",
		Model:      "gemini-4-neon",
		Effort:     "medium",
		Workspace:  "posse/main",
		State:      store.ExecMerged,
	}
	gw := NewGateway(t.TempDir(), cli, host)
	gw.mu.Lock()
	gw.activeCards["x_merged_1"] = &activeCard{
		botID:     "lbot_1",
		profile:   "p1",
		botName:   "Posse 迭代",
		execID:    "x_merged_1",
		chatID:    "oc_1",
		cardMsgID: "om_card_merged",
	}
	gw.mu.Unlock()

	gw.tickProgress(context.Background())

	if len(cli.patched) != 1 {
		t.Fatalf("expected merged progress card to be patched once, got %d", len(cli.patched))
	}
	patched := cli.patched[0].cardJSON
	if strings.Contains(patched, "exec_stop") {
		t.Fatalf("merged card should remove Stop button, got: %s", patched)
	}
	if !strings.Contains(patched, "grey") {
		t.Fatalf("merged card should use grey closed template, got: %s", patched)
	}
	gw.mu.Lock()
	_, stillActive := gw.activeCards["x_merged_1"]
	gw.mu.Unlock()
	if stillActive {
		t.Fatalf("merged card should be removed from activeCards after closing")
	}
}

func TestInterleavedReplyNoticeIncludesPromptBrief(t *testing.T) {
	brief := summarizePromptBrief("![Image](img_v3_abc)\n  先调研一下方案  \n再看细节", 28)
	if brief != "先调研一下方案 再看细节" {
		t.Fatalf("unexpected brief: %q", brief)
	}
	longBrief := summarizePromptBrief("这是一条非常长非常长的问题描述用于验证自动截断到一行简短摘要的效果", 12)
	if len([]rune(longBrief)) != 12 || !strings.HasSuffix(longBrief, "…") {
		t.Fatalf("unexpected truncated brief: %q", longBrief)
	}
	notice := interleavedReplyNotice("done", brief)
	if !strings.Contains(notice, " — 先调研一下方案 再看细节") {
		t.Fatalf("notice should include brief, got: %q", notice)
	}
	plain := interleavedReplyNotice("done", "")
	if strings.HasSuffix(plain, " — ") {
		t.Fatalf("empty brief should not append separator: %q", plain)
	}
}
