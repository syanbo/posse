package app

import (
	"context"
	"slices"
	"strings"
	"time"

	"github.com/egoist/mygo/ui"

	"posse/internal/agent"
	"posse/internal/errtext"
	"posse/internal/lark"
)

type larkUIState struct {
	loaded   bool
	loading  bool
	saving   bool
	loadedAt time.Time
	revision int64
	owner    string
	bots     []lark.BotConfig
	statuses map[string]lark.BotStatus
	profiles []lark.ProfileInfo
	err      string

	editing bool
	draft   lark.BotConfig

	// 群分身编辑状态
	chatEditing     bool
	chatEditIsNew   bool
	chatBotID       string
	chatDraft       lark.ChatOverride
	chatNameAuto    bool // 当前显示名是否由选群自动填入，切换选中群时自动同步
	chatRoleMode    int  // 0=继承默认 | 1=自定义 | 2=无角色
	chatRoleText    string
	chatSearch      string
	chatShowManual  bool
	chatListBotID   string
	chatListReqID   uint64
	chatListLoading bool
	chatListLoaded  bool
	chatListErr     string
	chatListHasMore bool
	chatList        []lark.ChatInfo
}

func (a *app) loadLark() {
	if a.lark.loading || a.c == nil {
		return
	}
	a.lark.loading = true
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		var listOut struct {
			Revision    int64            `json:"revision"`
			OwnerOpenID string           `json:"ownerOpenId"`
			Bots        []lark.BotConfig `json:"bots"`
			Statuses    []lark.BotStatus `json:"statuses"`
		}
		err := a.c.Call(ctx, "lark.bots.list", nil, &listOut)
		var profOut struct {
			OwnerOpenID string             `json:"ownerOpenId"`
			Profiles    []lark.ProfileInfo `json:"profiles"`
		}
		_ = a.c.Call(ctx, "lark.profiles", nil, &profOut)
		cancel()
		a.update(func() {
			a.lark.loading = false
			a.lark.loaded = true
			a.lark.loadedAt = time.Now()
			if err != nil {
				a.lark.err = errtext.Text(err)
				return
			}
			a.lark.err = ""
			a.lark.revision = listOut.Revision
			a.lark.owner = listOut.OwnerOpenID
			if a.lark.owner == "" {
				a.lark.owner = profOut.OwnerOpenID
			}
			a.lark.bots = listOut.Bots
			a.lark.statuses = map[string]lark.BotStatus{}
			for _, st := range listOut.Statuses {
				a.lark.statuses[st.ID] = st
			}
			a.lark.profiles = profOut.Profiles
		})
	}()
}

func (a *app) saveLarkBot(bot lark.BotConfig) {
	if a.lark.saving || a.c == nil {
		return
	}
	a.lark.saving = true
	owner := a.lark.owner
	rev := a.lark.revision
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		var saved lark.BotConfig
		err := a.c.Call(ctx, "lark.bots.save", map[string]any{
			"ownerOpenId":   owner,
			"bot":           bot,
			"preserveChats": true,
			"expectedRev":   rev,
		}, &saved)
		cancel()
		a.update(func() {
			a.lark.saving = false
			if err != nil {
				a.lark.err = errtext.Text(err)
				a.loadLark()
				return
			}
			a.lark.err = ""
			a.lark.editing = false
			a.loadLark()
		})
	}()
}

func (a *app) loadLarkBotChats(botID string) {
	if a.c == nil || botID == "" {
		return
	}
	a.lark.chatListReqID++
	reqID := a.lark.chatListReqID
	a.lark.chatListLoading = true
	a.lark.chatListBotID = botID
	a.lark.chatListErr = ""
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		var out lark.ChatListResult
		err := a.c.Call(ctx, "lark.bots.chats", map[string]any{"botId": botID}, &out)
		cancel()
		a.update(func() {
			if a.lark.chatListReqID != reqID || a.lark.chatListBotID != botID {
				return
			}
			a.lark.chatListLoading = false
			a.lark.chatListLoaded = true
			if err != nil {
				a.lark.chatListErr = errtext.Text(err)
				return
			}
			a.lark.chatListErr = ""
			a.lark.chatList = out.Chats
			a.lark.chatListHasMore = out.HasMore
		})
	}()
}

func (a *app) saveLarkChat(botID string, ov lark.ChatOverride, createOnly bool) {
	if a.lark.saving || a.c == nil {
		return
	}
	a.lark.saving = true
	rev := a.lark.revision
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		var saved lark.BotConfig
		err := a.c.Call(ctx, "lark.bots.chat.save", map[string]any{
			"botId":       botID,
			"chat":        ov,
			"createOnly":  createOnly,
			"expectedRev": rev,
		}, &saved)
		cancel()
		a.update(func() {
			a.lark.saving = false
			if err != nil {
				a.lark.err = errtext.Text(err)
				a.loadLark()
				return
			}
			a.lark.err = ""
			a.lark.chatEditing = false
			a.loadLark()
		})
	}()
}

func (a *app) deleteLarkChat(botID, chatID string) {
	if a.c == nil || botID == "" || chatID == "" {
		return
	}
	rev := a.lark.revision
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		var saved lark.BotConfig
		err := a.c.Call(ctx, "lark.bots.chat.delete", map[string]any{
			"botId":       botID,
			"chatId":      chatID,
			"expectedRev": rev,
		}, &saved)
		cancel()
		a.update(func() {
			if err != nil {
				a.lark.err = errtext.Text(err)
				a.loadLark()
				return
			}
			a.lark.err = ""
			if a.lark.chatEditing && a.lark.chatBotID == botID && a.lark.chatDraft.ChatID == chatID {
				a.lark.chatEditing = false
			}
			a.loadLark()
		})
	}()
}

func (a *app) openLarkChatEditor(botID string, ov lark.ChatOverride, isNew bool) {
	l := &a.lark
	l.editing = false
	l.chatEditing = true
	l.chatEditIsNew = isNew
	l.chatBotID = botID
	l.chatDraft = lark.NormalizeChatOverride(ov)
	l.chatNameAuto = isNew && strings.TrimSpace(ov.Name) == ""
	l.chatSearch = ""
	l.chatShowManual = !isNew
	if l.chatDraft.Role == nil {
		l.chatRoleMode = 0
		l.chatRoleText = ""
	} else if *l.chatDraft.Role == "" {
		l.chatRoleMode = 2
		l.chatRoleText = ""
	} else {
		l.chatRoleMode = 1
		l.chatRoleText = *l.chatDraft.Role
	}
	if l.chatListBotID != botID || !l.chatListLoaded {
		l.chatList = nil
		l.chatListLoaded = false
		l.chatListHasMore = false
		l.chatListErr = ""
		a.loadLarkBotChats(botID)
	}
}

func (a *app) toggleLarkBot(id string, on bool) {
	if a.c == nil {
		return
	}
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		var saved lark.BotConfig
		err := a.c.Call(ctx, "lark.bots.toggle", map[string]any{"id": id, "enabled": on}, &saved)
		cancel()
		a.update(func() {
			if err != nil {
				a.lark.err = errtext.Text(err)
				return
			}
			a.lark.err = ""
			a.loadLark()
		})
	}()
}

func (a *app) deleteLarkBot(id string) {
	if a.c == nil {
		return
	}
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		var out map[string]bool
		err := a.c.Call(ctx, "lark.bots.delete", map[string]any{"id": id}, &out)
		cancel()
		a.update(func() {
			if err != nil {
				a.lark.err = errtext.Text(err)
				return
			}
			a.lark.err = ""
			a.loadLark()
		})
	}()
}

func (a *app) larkSettings(c *ui.Context) {
	t, m := c.Theme(), L(c)
	l := &a.lark
	if !l.loaded || time.Since(l.loadedAt) > 10*time.Second {
		a.loadLark()
	}

	settingsCard(c, m.LarkTitle(), m.LarkNote(), func() {
		ui.Row(c).Gap(8).AlignItems(ui.Center).Children(func() {
			if l.owner != "" {
				ui.Text(c, m.LarkOwnerLabel()+": "+l.owner).FontSize(fsSmall).Font(monoFont).TextColor(t.TextMuted).Shrink(1)
			}
			ui.Spacer(c)
			if pressable(textButton(c.Key("lark-refresh"), m.LarkRefresh())).Clicked() {
				a.loadLark()
			}
			if !l.editing && pressable(accentButton(c.Key("lark-add"), m.LarkAddBot())).Clicked() {
				defProfile := ""
				if len(l.profiles) > 0 {
					defProfile = l.profiles[0].Name
				}
				defWS := ""
				if len(a.workspaces) > 0 {
					defWS = a.workspaces[0].ID
				}
				l.draft = lark.NormalizeBot(lark.BotConfig{
					Profile:        defProfile,
					Name:           defProfile,
					WorkspaceID:    defWS,
					Agent:          "antigravity",
					PermissionMode: lark.PermAsk,
					SessionMode:    lark.SessionChat,
					TriggerPolicy:  lark.TriggerSelf,
					Enabled:        true,
				})
				l.editing = true
			}
		})

		if l.err != "" {
			ui.Text(c, l.err).FontSize(fsSmall).TextColor(t.Danger)
		}

		if len(l.bots) == 0 && !l.editing {
			ui.Text(c, m.LarkEmpty()).FontSize(fsSmall).TextColor(t.TextMuted)
			return
		}

		for _, b := range l.bots {
			bot := b
			st := l.statuses[bot.ID]
			ui.Column(c.Key("lark-bot:" + bot.ID)).Padding(12).Gap(6).Background(colors(t).fill).Radius(t.Radius).Children(func() {
				ui.Row(c).Gap(8).AlignItems(ui.Center).Children(func() {
					agentIcon(c, bot.Agent, smallIcon())
					ui.Text(c, bot.Name).FontSize(fsBody).FontWeight(fwMedium).Shrink(1)
					stateLabel, stateColor := m.LarkStateOff(), t.TextMuted
					if bot.Enabled {
						if st.MsgReady && st.CardReady {
							stateLabel, stateColor = m.LarkStateReady(), colors(t).sage
						} else {
							stateLabel, stateColor = m.LarkStateConnecting(), colors(t).honey
						}
					}
					ui.Text(c, stateLabel).FontSize(fsSmall).TextColor(stateColor).Shrink(0)
					ui.Spacer(c)
					on := bot.Enabled
					if ui.Switch(c.Key("lark-en:"+bot.ID), &on).Label(bot.Name).Changed() {
						a.toggleLarkBot(bot.ID, on)
					}
					if pressable(textButton(c.Key("lark-ed:"+bot.ID), m.LarkEditBot())).Clicked() {
						l.chatEditing = false
						l.draft = lark.CloneBot(bot)
						l.editing = true
					}
					if pressable(textButton(c.Key("lark-rm:"+bot.ID), m.LarkDeleteBot())).Clicked() {
						a.deleteLarkBot(bot.ID)
					}
				})
				wsLabel := a.larkWorkspaceLabel(bot.WorkspaceID)
				ui.Row(c).Gap(12).AlignItems(ui.Center).Children(func() {
					ui.Text(c, bot.Profile).FontSize(fsSmall).Font(monoFont).TextColor(t.TextMuted)
					if wsLabel != "" {
						ui.Text(c, wsLabel).FontSize(fsSmall).TextColor(t.TextMuted)
					}
					ui.Text(c, agentTitle(bot.Agent)).FontSize(fsSmall).TextColor(t.TextMuted)
					if bot.GroupContextCount > 0 {
						ui.Text(c, m.LarkGroupContextCount(bot.GroupContextCount)).FontSize(fsSmall).TextColor(t.TextMuted)
					}
				})
				if strings.TrimSpace(bot.Role) != "" {
					ui.Text(c, strings.TrimSpace(bot.Role)).FontSize(fsSmall).TextColor(t.TextMuted).MaxLines(2)
				}
				if st.LastError != "" {
					ui.Text(c, st.LastError).FontSize(fsSmall).TextColor(t.Danger)
				}

				// 群分身小节：按飞书群独立覆写工作区、Agent、角色与免 @ 触发
				ui.Column(c.Key("lark-chats-sec:" + bot.ID)).Padding(8).Gap(6).Background(colors(t).card).Radius(rRow).Children(func() {
					ui.Row(c).Gap(8).AlignItems(ui.Center).Children(func() {
						ui.Text(c, m.LarkChatsTitle()).FontSize(fsSmall).FontWeight(fwMedium).Shrink(0)
						ui.Text(c, m.LarkChatsHint()).FontSize(fsSmall).TextColor(t.TextMuted).Shrink(1)
						ui.Spacer(c)
						if pressable(textButton(c.Key("lark-add-chat:"+bot.ID), m.LarkAddChat())).Clicked() {
							a.openLarkChatEditor(bot.ID, lark.ChatOverride{}, true)
						}
					})
					if len(bot.Chats) == 0 {
						ui.Text(c, m.LarkChatsEmpty()).FontSize(fsSmall).TextColor(t.TextMuted)
					}
					for _, ch := range bot.Chats {
						ov := ch
						eff, noMention := bot.EffectiveForChat(ov.ChatID)
						title := ov.Name
						if title == "" {
							title = ov.ChatID
						}
						ui.Row(c.Key("lark-chat:" + bot.ID + ":" + ov.ChatID)).Padding(8).Gap(8).AlignItems(ui.Center).Background(colors(t).fill).Radius(rRow).Children(func() {
							agentIcon(c, eff.Agent, smallIcon())
							ui.Column(c).Gap(2).Shrink(1).Children(func() {
								ui.Row(c).Gap(8).AlignItems(ui.Center).Children(func() {
									ui.Text(c, title).FontSize(fsBody).FontWeight(fwMedium).Shrink(1)
									if noMention {
										ui.Text(c, m.LarkChatNoMentionBadge()).FontSize(fsSmall).TextColor(colors(t).sage).Shrink(0)
									}
									if ov.Name != "" {
										ui.Text(c, shortChatID(ov.ChatID)).FontSize(fsSmall).Font(monoFont).TextColor(t.TextMuted).Shrink(0)
									}
								})
								ui.Row(c).Gap(8).AlignItems(ui.Center).Children(func() {
									if wl := a.larkWorkspaceLabel(eff.WorkspaceID); wl != "" {
										ui.Text(c, wl).FontSize(fsSmall).TextColor(t.TextMuted)
									}
									ui.Text(c, agentTitle(eff.Agent)).FontSize(fsSmall).TextColor(t.TextMuted)
									if ov.Role != nil {
										roleTag := m.LarkRoleCustom()
										if *ov.Role == "" {
											roleTag = m.LarkRoleNone()
										}
										ui.Text(c, roleTag).FontSize(fsSmall).TextColor(t.TextMuted)
									}
								})
							})
							ui.Spacer(c)
							if pressable(textButton(c.Key("lark-ed-chat:"+bot.ID+":"+ov.ChatID), m.LarkEditBot())).Clicked() {
								a.openLarkChatEditor(bot.ID, ov, false)
							}
							if pressable(textButton(c.Key("lark-rm-chat:"+bot.ID+":"+ov.ChatID), m.LarkDeleteBot())).Clicked() {
								a.deleteLarkChat(bot.ID, ov.ChatID)
							}
						})
					}
				})
			})
		}
	})

	if l.editing {
		a.larkBotEditor(c)
	}
	if l.chatEditing {
		a.larkChatEditor(c)
	}
}

func (a *app) larkWorkspaceLabel(id string) string {
	for _, w := range a.workspaces {
		if w.ID == id {
			return w.Label
		}
	}
	return id
}

func shortChatID(id string) string {
	r := []rune(strings.TrimSpace(id))
	if len(r) <= 10 {
		return string(r)
	}
	return string(r[:4]) + "…" + string(r[len(r)-4:])
}

func (a *app) larkBotEditor(c *ui.Context) {
	m := L(c)
	d := &a.lark.draft
	title := m.LarkAddBot()
	if d.ID != "" {
		title = m.LarkEditBot()
	}
	settingsCard(c, title, "", func() {
		ui.Row(c).Gap(8).AlignItems(ui.Center).Children(func() {
			ui.Text(c, m.LarkFieldName()).FontSize(fsBody).Shrink(0)
			ui.Spacer(c)
			fieldInputSized(c.Key("lark-f-name"), &d.Name, m.LarkFieldName(), sz(280), sz(120))
		})
		ui.Row(c).Gap(8).AlignItems(ui.Center).Children(func() {
			ui.Text(c, m.LarkFieldProfile()).FontSize(fsBody).Shrink(0)
			ui.Spacer(c)
			fieldInputSized(c.Key("lark-f-prof"), &d.Profile, m.LarkFieldProfile(), sz(280), sz(120))
		})
		if len(a.lark.profiles) > 0 {
			ui.Row(c).Gap(6).Wrap().Children(func() {
				for _, p := range a.lark.profiles {
					prof := p
					if pressable(textButton(c.Key("lark-pick-prof:"+prof.Name), prof.Name)).Clicked() {
						d.Profile = prof.Name
						if strings.TrimSpace(d.Name) == "" {
							d.Name = prof.Name
						}
					}
				}
			})
		}
		if len(a.workspaces) > 0 {
			wsLabels := make([]string, 0, len(a.workspaces))
			wsIDs := make([]string, 0, len(a.workspaces))
			labelSeen := map[string]int{}
			for _, w := range a.workspaces {
				labelSeen[w.Label]++
			}
			wsSel := ""
			for _, w := range a.workspaces {
				lbl := w.Label
				if labelSeen[w.Label] > 1 {
					lbl = w.Label + " (" + shortChatID(w.ID) + ")"
				}
				wsLabels = append(wsLabels, lbl)
				wsIDs = append(wsIDs, w.ID)
				if w.ID == d.WorkspaceID {
					wsSel = lbl
				}
			}
			if wsSel == "" {
				wsSel = wsLabels[0]
			}
			settingsRow(c, m.LarkFieldWorkspace(), func() {
				if ui.Select(c.Key("lark-f-ws"), &wsSel, wsLabels).Changed() {
					for i, lbl := range wsLabels {
						if lbl == wsSel {
							d.WorkspaceID = wsIDs[i]
							break
						}
					}
				}
			})
		}
		var agents []string
		for _, ag := range a.installedAgents() {
			if !slices.Contains(a.disabled, ag) && agent.CanJoinDiscussion(ag) {
				agents = append(agents, ag)
			}
		}
		if len(agents) > 0 {
			agLabels := make([]string, 0, len(agents))
			agByLabel := map[string]string{}
			agSel := ""
			for _, ag := range agents {
				lbl := agentTitle(ag)
				agLabels = append(agLabels, lbl)
				agByLabel[lbl] = ag
				if ag == d.Agent {
					agSel = lbl
				}
			}
			if agSel == "" {
				agSel = agLabels[0]
			}
			settingsRow(c, m.LarkFieldAgent(), func() {
				if ui.Select(c.Key("lark-f-agent"), &agSel, agLabels).Changed() {
					if ag := agByLabel[agSel]; ag != "" {
						d.Agent = ag
					}
				}
			})
		}
		permIdx := 0
		if d.PermissionMode == lark.PermSkip {
			permIdx = 1
		}
		settingsRow(c, m.LarkFieldPermission(), func() {
			if segmented(c.Key("lark-f-perm"), &permIdx, m.LarkFieldPermission(), m.SettingsPermissionAsk(), m.SettingsPermissionSkip()) {
				if permIdx == 1 {
					d.PermissionMode = lark.PermSkip
				} else {
					d.PermissionMode = lark.PermAsk
				}
			}
		})
		sessIdx := 0
		if d.SessionMode == lark.SessionThread {
			sessIdx = 1
		}
		settingsRow(c, m.LarkFieldSession(), func() {
			if segmented(c.Key("lark-f-sess"), &sessIdx, m.LarkFieldSession(), m.LarkSessionChat(), m.LarkSessionThread()) {
				if sessIdx == 1 {
					d.SessionMode = lark.SessionThread
				} else {
					d.SessionMode = lark.SessionChat
				}
			}
		})
		trigIdx := 0
		switch d.TriggerPolicy {
		case lark.TriggerAllowlist:
			trigIdx = 1
		case lark.TriggerGroup:
			trigIdx = 2
		}
		settingsRow(c, m.LarkFieldTrigger(), func() {
			if segmented(c.Key("lark-f-trig"), &trigIdx, m.LarkFieldTrigger(), m.LarkTriggerSelf(), m.LarkTriggerAllowlist(), m.LarkTriggerGroup()) {
				switch trigIdx {
				case 1:
					d.TriggerPolicy = lark.TriggerAllowlist
				case 2:
					d.TriggerPolicy = lark.TriggerGroup
				default:
					d.TriggerPolicy = lark.TriggerSelf
				}
			}
		})
		ctxCounts := []int{0, 10, 20, 30}
		ctxIdx := 0
		switch {
		case d.GroupContextCount >= 25:
			ctxIdx = 3
		case d.GroupContextCount >= 15:
			ctxIdx = 2
		case d.GroupContextCount > 0:
			ctxIdx = 1
		}
		settingsRow(c, m.LarkFieldGroupContext(), func() {
			if segmented(c.Key("lark-f-gctx"), &ctxIdx, m.LarkFieldGroupContext(),
				m.LarkGroupContextOff(), m.LarkGroupContextCount(10), m.LarkGroupContextCount(20), m.LarkGroupContextCount(30)) {
				d.GroupContextCount = ctxCounts[ctxIdx]
			}
		})
		t := c.Theme()
		ui.Column(c).Gap(6).Children(func() {
			ui.Row(c).Gap(8).AlignItems(ui.Center).Children(func() {
				ui.Text(c, m.LarkFieldRole()).FontSize(fsBody).Shrink(0)
				ui.Text(c, m.LarkFieldRoleHint()).FontSize(fsSmall).TextColor(t.TextMuted).Shrink(1)
				ui.Spacer(c)
				if strings.TrimSpace(d.Role) != "" && pressable(textButton(c.Key("lark-f-role-clear"), m.LarkClear())).Clicked() {
					d.Role = ""
				}
			})
			fieldArea(c.Key("lark-f-role"), &d.Role, m.LarkFieldRolePlaceholder(), sz(104))
		})
		ui.Row(c).Gap(8).AlignItems(ui.Center).Children(func() {
			ui.Spacer(c)
			if pressable(textButton(c.Key("lark-cancel"), m.LarkCancel())).Clicked() {
				a.lark.editing = false
			}
			valid := strings.TrimSpace(d.Profile) != ""
			if valid && !a.lark.saving && pressable(accentButton(c.Key("lark-save"), m.LarkSave())).Clicked() {
				a.saveLarkBot(*d)
			}
		})
	})
}

func (a *app) larkChatEditor(c *ui.Context) {
	t, m := c.Theme(), L(c)
	l := &a.lark
	d := &l.chatDraft
	title := m.LarkAddChat()
	if !l.chatEditIsNew {
		title = m.LarkEditChat()
	}
	var parentBot lark.BotConfig
	for _, b := range l.bots {
		if b.ID == l.chatBotID {
			parentBot = b
			break
		}
	}

	settingsCard(c, title, "", func() {
		// 1. 已加入飞书群一键选择器（免手抄 Chat ID）
		if l.chatEditIsNew {
			ui.Column(c).Gap(6).Children(func() {
				ui.Row(c).Gap(8).AlignItems(ui.Center).Children(func() {
					ui.Text(c, m.LarkChatPickLabel()).FontSize(fsBody).Shrink(0)
					ui.Spacer(c)
					if pressable(textButton(c.Key("lark-chat-refresh"), m.LarkRefresh())).Clicked() {
						a.loadLarkBotChats(l.chatBotID)
					}
					if pressable(textButton(c.Key("lark-chat-manual"), m.LarkChatManualToggle())).Clicked() {
						l.chatShowManual = !l.chatShowManual
					}
				})
				if len(l.chatList) > 0 {
					fieldInput(c.Key("lark-chat-search"), &l.chatSearch, m.LarkChatSearchPlaceholder())
				}
				switch {
				case l.chatListLoading:
					ui.Text(c, m.LarkChatLoading()).FontSize(fsSmall).TextColor(t.TextMuted)
				case l.chatListErr != "":
					ui.Text(c, l.chatListErr).FontSize(fsSmall).TextColor(t.Danger)
				case len(l.chatList) == 0:
					ui.Text(c, m.LarkChatNoneJoined()).FontSize(fsSmall).TextColor(t.TextMuted)
				default:
					// 统计同名群，遇重名时自动附短 ID 消歧
					nameCount := map[string]int{}
					for _, ch := range l.chatList {
						nameCount[ch.Name]++
					}
					query := strings.ToLower(strings.TrimSpace(l.chatSearch))
					ui.Row(c).Gap(6).Wrap().Children(func() {
						for _, ch := range l.chatList {
							item := ch
							if query != "" && !strings.Contains(strings.ToLower(item.Name), query) && !strings.Contains(strings.ToLower(item.ChatID), query) {
								continue
							}
							label := item.Name
							if nameCount[item.Name] > 1 {
								label = item.Name + " (" + shortChatID(item.ChatID) + ")"
							}
							var btn ui.Element
							if d.ChatID == item.ChatID {
								btn = accentButton(c.Key("lark-pick-chat:"+item.ChatID), label)
							} else {
								btn = textButton(c.Key("lark-pick-chat:"+item.ChatID), label)
							}
							if pressable(btn).Clicked() {
								d.ChatID = item.ChatID
								if l.chatNameAuto || strings.TrimSpace(d.Name) == "" {
									d.Name = item.Name
									l.chatNameAuto = true
								}
							}
						}
					})
					if l.chatListHasMore {
						ui.Text(c, m.LarkChatHasMore()).FontSize(fsSmall).TextColor(t.TextMuted)
					}
				}
			})
		}

		// 手动输入 Chat ID（新建时默认折叠兜底，编辑时只读/可见）
		if l.chatShowManual || !l.chatEditIsNew {
			ui.Row(c).Gap(8).AlignItems(ui.Center).Children(func() {
				ui.Text(c, m.LarkChatIDLabel()).FontSize(fsBody).Shrink(0)
				ui.Spacer(c)
				if l.chatEditIsNew {
					fieldInputSized(c.Key("lark-cf-id"), &d.ChatID, "oc_…", sz(280), sz(120))
				} else {
					ui.Text(c, d.ChatID).FontSize(fsSmall).Font(monoFont).TextColor(t.TextMuted)
				}
			})
		}

		// 2. 群分身显示名
		ui.Row(c).Gap(8).AlignItems(ui.Center).Children(func() {
			ui.Text(c, m.LarkFieldName()).FontSize(fsBody).Shrink(0)
			ui.Spacer(c)
			beforeName := d.Name
			fieldInputSized(c.Key("lark-cf-name"), &d.Name, m.LarkChatNamePlaceholder(), sz(280), sz(120))
			if d.Name != beforeName {
				l.chatNameAuto = strings.TrimSpace(d.Name) == ""
			}
		})

		// 3. 工作区（首项继承默认，按索引映射 ID 避免同名工作区覆盖）
		if len(a.workspaces) > 0 {
			wsLabels := make([]string, 0, len(a.workspaces)+1)
			wsIDs := make([]string, 0, len(a.workspaces)+1)
			wsLabels = append(wsLabels, m.LarkInherit())
			wsIDs = append(wsIDs, "")
			wsSel := wsLabels[0]
			labelSeen := map[string]int{}
			for _, w := range a.workspaces {
				labelSeen[w.Label]++
			}
			for _, w := range a.workspaces {
				lbl := w.Label
				if labelSeen[w.Label] > 1 {
					lbl = w.Label + " (" + shortChatID(w.ID) + ")"
				}
				wsLabels = append(wsLabels, lbl)
				wsIDs = append(wsIDs, w.ID)
				if w.ID == d.WorkspaceID {
					wsSel = lbl
				}
			}
			settingsRow(c, m.LarkFieldWorkspace(), func() {
				if ui.Select(c.Key("lark-cf-ws"), &wsSel, wsLabels).Changed() {
					for i, lbl := range wsLabels {
						if lbl == wsSel {
							d.WorkspaceID = wsIDs[i]
							break
						}
					}
				}
			})
		}

		// 4. Agent（首项继承默认）
		var agents []string
		for _, ag := range a.installedAgents() {
			if !slices.Contains(a.disabled, ag) && agent.CanJoinDiscussion(ag) {
				agents = append(agents, ag)
			}
		}
		if len(agents) > 0 {
			agLabels := make([]string, 0, len(agents)+1)
			agIDs := make([]string, 0, len(agents)+1)
			agLabels = append(agLabels, m.LarkInherit())
			agIDs = append(agIDs, "")
			agSel := agLabels[0]
			for _, ag := range agents {
				lbl := agentTitle(ag)
				agLabels = append(agLabels, lbl)
				agIDs = append(agIDs, ag)
				if ag == d.Agent {
					agSel = lbl
				}
			}
			settingsRow(c, m.LarkFieldAgent(), func() {
				if ui.Select(c.Key("lark-cf-agent"), &agSel, agLabels).Changed() {
					for i, lbl := range agLabels {
						if lbl == agSel {
							d.Agent = agIDs[i]
							break
						}
					}
				}
			})
		}

		// 5. 免 @ 直接触发开关
		ui.Column(c).Gap(2).Children(func() {
			noMention := d.NoMention
			settingsRow(c, m.LarkChatNoMention(), func() {
				if ui.Switch(c.Key("lark-cf-nomention"), &noMention).Label(m.LarkChatNoMention()).Changed() {
					d.NoMention = noMention
				}
			})
			ui.Text(c, m.LarkChatNoMentionHint()).FontSize(fsSmall).TextColor(t.TextMuted)
		})

		// 6. 工具权限审批（继承 / 询问 / 跳过）
		permIdx := 0
		switch d.PermissionMode {
		case lark.PermAsk:
			permIdx = 1
		case lark.PermSkip:
			permIdx = 2
		}
		settingsRow(c, m.LarkFieldPermission(), func() {
			if segmented(c.Key("lark-cf-perm"), &permIdx, m.LarkFieldPermission(),
				m.LarkInherit(), m.SettingsPermissionAsk(), m.SettingsPermissionSkip()) {
				switch permIdx {
				case 1:
					d.PermissionMode = lark.PermAsk
				case 2:
					d.PermissionMode = lark.PermSkip
				default:
					d.PermissionMode = ""
				}
			}
		})

		// 7. 群聊会话粒度（继承 / 按群共享 / 按话题隔离）
		sessIdx := 0
		switch d.SessionMode {
		case lark.SessionChat:
			sessIdx = 1
		case lark.SessionThread:
			sessIdx = 2
		}
		settingsRow(c, m.LarkFieldSession(), func() {
			if segmented(c.Key("lark-cf-sess"), &sessIdx, m.LarkFieldSession(),
				m.LarkInherit(), m.LarkSessionChat(), m.LarkSessionThread()) {
				switch sessIdx {
				case 1:
					d.SessionMode = lark.SessionChat
				case 2:
					d.SessionMode = lark.SessionThread
				default:
					d.SessionMode = ""
				}
			}
		})

		// 8. 触发权限（继承 / 仅自己 / 白名单 / 全群可用）
		trigIdx := 0
		switch d.TriggerPolicy {
		case lark.TriggerSelf:
			trigIdx = 1
		case lark.TriggerAllowlist:
			trigIdx = 2
		case lark.TriggerGroup:
			trigIdx = 3
		}
		settingsRow(c, m.LarkFieldTrigger(), func() {
			if segmented(c.Key("lark-cf-trig"), &trigIdx, m.LarkFieldTrigger(),
				m.LarkInherit(), m.LarkTriggerSelf(), m.LarkTriggerAllowlist(), m.LarkTriggerGroup()) {
				switch trigIdx {
				case 1:
					d.TriggerPolicy = lark.TriggerSelf
				case 2:
					d.TriggerPolicy = lark.TriggerAllowlist
				case 3:
					d.TriggerPolicy = lark.TriggerGroup
				default:
					d.TriggerPolicy = ""
				}
			}
		})

		// 9. 角色说明（继承 / 自定义 / 无角色）
		ui.Column(c).Gap(6).Children(func() {
			settingsRow(c, m.LarkFieldRole(), func() {
				if segmented(c.Key("lark-cf-rolemode"), &l.chatRoleMode, m.LarkFieldRole(),
					m.LarkInherit(), m.LarkRoleCustom(), m.LarkRoleNone()) {
					if l.chatRoleMode == 1 && strings.TrimSpace(l.chatRoleText) == "" && strings.TrimSpace(parentBot.Role) != "" {
						l.chatRoleText = ""
					}
				}
			})
			if l.chatRoleMode == 1 {
				fieldArea(c.Key("lark-cf-role"), &l.chatRoleText, m.LarkFieldRolePlaceholder(), sz(96))
			}
		})

		ui.Text(c, m.LarkChatEffectHint()).FontSize(fsSmall).TextColor(t.TextMuted)

		ui.Row(c).Gap(8).AlignItems(ui.Center).Children(func() {
			ui.Spacer(c)
			if pressable(textButton(c.Key("lark-chat-cancel"), m.LarkCancel())).Clicked() {
				l.chatEditing = false
			}
			valid := strings.TrimSpace(d.ChatID) != ""
			if valid && !l.saving && pressable(accentButton(c.Key("lark-chat-save"), m.LarkSave())).Clicked() {
				out := *d
				switch l.chatRoleMode {
				case 0:
					out.Role = nil
				case 1:
					v := strings.TrimSpace(l.chatRoleText)
					out.Role = &v
				case 2:
					empty := ""
					out.Role = &empty
				}
				a.saveLarkChat(l.chatBotID, out, l.chatEditIsNew)
			}
		})
	})
}
