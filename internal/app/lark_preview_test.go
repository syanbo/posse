package app

import (
	"os"
	"testing"

	"posse/internal/lark"
)

func TestPreviewLarkSettings(t *testing.T) {
	if os.Getenv("POSSE_PREVIEW_DIR") == "" {
		t.Skip("set POSSE_PREVIEW_DIR to render previews")
	}
	for _, dark := range []bool{false, true} {
		suffix := "light"
		if dark {
			suffix = "dark"
		}
		a := previewApp(t)
		a.selected = settingsBoardID
		a.settingsTab = "lark"
		customRole := "专注 Posse 仓库开发与代码审查"
		emptyRole := ""
		a.lark = larkUIState{
			loaded: true,
			owner:  "ou_bd37ce5c61504b94bbb6499cd7b1b6bf",
			profiles: []lark.ProfileInfo{
				{Name: "cli_a945f8f32b38dcd4", AppID: "cli_a945f8f32b38dcd4", Brand: "feishu", Active: true, User: "Owner"},
			},
			bots: []lark.BotConfig{
				{
					ID: "lbot_works", Name: "Works 排查助手", Profile: "cli_a945f8f32b38dcd4",
					Enabled: true, WorkspaceID: "w7", Agent: "antigravity",
					PermissionMode: lark.PermAsk, SessionMode: lark.SessionChat, TriggerPolicy: lark.TriggerSelf,
					GroupContextCount: 20,
					Role:              "优先按 works-incident-triage 技能排查 Works 线上报障，只读定位代码与日志，给出结论与回复话术。",
					Chats: []lark.ChatOverride{
						{ChatID: "oc_posse_dev_1234", Name: "Posse 开发助手", WorkspaceID: "w1", Agent: "codex", Role: &customRole, NoMention: true},
						{ChatID: "oc_daily_5678", Name: "日报机器人", Role: &emptyRole, NoMention: true},
					},
				},
			},
			statuses: map[string]lark.BotStatus{
				"lbot_works": {ID: "lbot_works", Running: true, MsgReady: true, CardReady: true},
			},
			chatEditing:    true,
			chatEditIsNew:  true,
			chatBotID:      "lbot_works",
			chatRoleMode:   1,
			chatRoleText:   customRole,
			chatListBotID:  "lbot_works",
			chatListLoaded: true,
			chatList: []lark.ChatInfo{
				{ChatID: "oc_posse_dev_1234", Name: "Posse 开发助手"},
				{ChatID: "oc_daily_5678", Name: "日报机器人"},
				{ChatID: "oc_reverse_9999", Name: "外挂逆向分析"},
			},
			chatDraft: lark.ChatOverride{
				ChatID: "oc_reverse_9999", Name: "外挂逆向分析", WorkspaceID: "w7", Agent: "antigravity", NoMention: true,
			},
		}
		savePNG(t, newPreviewTesterSize(a, dark, 1200, 1060), "settings-lark-"+suffix+".png")
	}
}
