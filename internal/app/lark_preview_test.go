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
				},
				{
					ID: "lbot_docs", Name: "文档整理助手", Profile: "cli_docs_bot",
					Enabled: false, WorkspaceID: "w1", Agent: "claude",
					PermissionMode: lark.PermSkip, SessionMode: lark.SessionThread, TriggerPolicy: lark.TriggerAllowlist,
				},
			},
			statuses: map[string]lark.BotStatus{
				"lbot_works": {ID: "lbot_works", Running: true, MsgReady: true, CardReady: true},
				"lbot_docs":  {ID: "lbot_docs", Running: false},
			},
			editing: true,
			draft: lark.BotConfig{
				Name: "Works 排查助手", Profile: "cli_a945f8f32b38dcd4",
				WorkspaceID: "w7", Agent: "antigravity",
				PermissionMode: lark.PermAsk, SessionMode: lark.SessionChat, TriggerPolicy: lark.TriggerSelf,
				Role: "负责 Works 报障排查，结合多维表格和日志定位根因并给出话术。",
			},
		}
		savePNG(t, newPreviewTester(a, dark), "settings-lark-"+suffix+".png")
	}
}
