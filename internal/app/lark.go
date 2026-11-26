package app

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"
	"unicode"

	"github.com/egoist/mygo"
	"github.com/egoist/mygo/ui"

	"posse/internal/agent"
	"posse/internal/errtext"
	"posse/internal/lark"
	"posse/internal/rpc"
	"posse/locales"
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

	editing        bool
	botEditGen     uint64
	botWSPickSeq   uint64
	botWSPicking   bool
	botWSErr       string
	draft          lark.BotConfig
	botModelMode   int // 1=使用 Agent 默认 | 2=列表指定 | 3=自定义
	botModelText   string
	botEffortMode  int // 1=默认 | 2=指定档位
	botEffortText  string
	botModelDirty  bool
	botEffortDirty bool
	botOrigModel   string
	botOrigEffort  string

	// 群分身编辑状态
	chatEditing     bool
	chatEditGen     uint64
	chatWSPickSeq   uint64
	chatWSPicking   bool
	chatWSErr       string
	chatEditIsNew   bool
	chatBotID       string
	chatDraft       lark.ChatOverride
	chatModelMode   int // 0=继承默认 | 1=使用 Agent 默认 | 2=列表指定 | 3=自定义
	chatModelText   string
	chatEffortMode  int // 0=继承默认 | 1=使用 Agent 默认 | 2=指定档位
	chatEffortText  string
	chatModelDirty  bool
	chatEffortDirty bool
	chatSplitSuffix bool // 仅展示拆分了旧 agy 带档位后缀的模型 ID；修改档位时同步落盘基础 ID
	chatOrigModel   *string
	chatOrigEffort  *string
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

	// 按 Agent 缓存的本机可用模型列表
	models        map[string]agent.ModelList
	modelsLoading map[string]bool
	modelsReqID   map[string]uint64
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
			a.closeLarkBotEditor()
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
			a.closeLarkChatEditor()
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
				a.closeLarkChatEditor()
			}
			a.loadLark()
		})
	}()
}

func (a *app) loadLarkAgentModels(agentName string, refresh bool) {
	name := agent.NormalizeName(agentName)
	if a.c == nil || name == "" {
		return
	}
	l := &a.lark
	if l.models == nil {
		l.models = map[string]agent.ModelList{}
	}
	if l.modelsLoading == nil {
		l.modelsLoading = map[string]bool{}
	}
	if l.modelsReqID == nil {
		l.modelsReqID = map[string]uint64{}
	}
	if l.modelsLoading[name] {
		return
	}
	if !refresh {
		if _, ok := l.models[name]; ok {
			return
		}
	}
	l.modelsReqID[name]++
	reqID := l.modelsReqID[name]
	l.modelsLoading[name] = true
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 16*time.Second)
		var out agent.ModelList
		err := a.c.Call(ctx, "agent.models", map[string]any{
			"agent":   name,
			"refresh": refresh,
		}, &out)
		cancel()
		a.update(func() {
			if a.lark.modelsReqID[name] != reqID {
				return
			}
			a.lark.modelsLoading[name] = false
			if err != nil {
				out = agent.ModelList{Agent: name, Error: err.Error()}
			}
			if out.Agent == "" {
				out.Agent = name
			}
			a.lark.models[name] = out
		})
	}()
}

func (a *app) openLarkBotEditor(bot lark.BotConfig) {
	l := &a.lark
	a.closeLarkChatEditor()
	l.botEditGen++
	l.botWSPickSeq++
	l.botWSPicking = false
	l.botWSErr = ""
	l.draft = lark.CloneBot(bot)
	l.botOrigModel = l.draft.Model
	l.botOrigEffort = l.draft.Effort
	l.botModelDirty = false
	l.botEffortDirty = false
	dispModel, dispEffort := l.draft.Model, l.draft.Effort
	// 兼容旧配置：agy 带档位后缀的完整 ID 仅用于展示拆分为基础模型与档位，未改模型时不改写原值
	if l.draft.Agent == "antigravity" && dispModel != "" && dispEffort == "" {
		if base, ef, ok := agent.SplitAntigravityModelEffort(dispModel); ok {
			dispModel = base
			dispEffort = ef
		}
	}
	if dispModel == "" {
		l.botModelMode = 1
		l.botModelText = ""
	} else {
		l.botModelMode = 2
		l.botModelText = dispModel
	}
	if dispEffort == "" {
		l.botEffortMode = 1
		l.botEffortText = ""
	} else {
		l.botEffortMode = 2
		l.botEffortText = dispEffort
	}
	l.editing = true
	a.loadLarkAgentModels(l.draft.Agent, false)
}

func (a *app) closeLarkBotEditor() {
	l := &a.lark
	l.editing = false
	l.botEditGen++
	l.botWSPickSeq++
	l.botWSPicking = false
	l.botWSErr = ""
}

func (a *app) closeLarkChatEditor() {
	l := &a.lark
	l.chatEditing = false
	l.chatEditGen++
	l.chatWSPickSeq++
	l.chatWSPicking = false
	l.chatWSErr = ""
}

func (a *app) openLarkChatEditor(botID string, ov lark.ChatOverride, isNew bool) {
	l := &a.lark
	a.closeLarkBotEditor()
	l.chatEditGen++
	l.chatWSPickSeq++
	l.chatWSPicking = false
	l.chatWSErr = ""
	l.chatEditing = true
	l.chatEditIsNew = isNew
	l.chatBotID = botID
	l.chatDraft = lark.NormalizeChatOverride(ov)
	l.chatOrigModel = cloneOptStr(l.chatDraft.Model)
	l.chatOrigEffort = cloneOptStr(l.chatDraft.Effort)
	l.chatModelDirty = false
	l.chatEffortDirty = false
	l.chatSplitSuffix = false
	var parentAgent string
	for _, b := range l.bots {
		if b.ID == botID {
			parentAgent = b.Agent
			break
		}
	}
	effAgent := l.chatDraft.Agent
	if effAgent == "" {
		effAgent = parentAgent
	}
	dispModelPtr := cloneOptStr(l.chatDraft.Model)
	dispEffortPtr := cloneOptStr(l.chatDraft.Effort)
	// 兼容旧配置：agy 带档位后缀的完整 ID 仅用于展示拆分，未改模型时不改写原三态
	if l.chatDraft.Agent == "antigravity" && dispModelPtr != nil && *dispModelPtr != "" &&
		(dispEffortPtr == nil || *dispEffortPtr == "") {
		if base, ef, ok := agent.SplitAntigravityModelEffort(*dispModelPtr); ok {
			dispModelPtr = &base
			dispEffortPtr = &ef
			l.chatSplitSuffix = true
		}
	}
	if dispModelPtr == nil {
		if l.chatDraft.Agent != "" && l.chatDraft.Agent != parentAgent {
			l.chatModelMode = 1
		} else {
			l.chatModelMode = 0
		}
		l.chatModelText = ""
	} else if *dispModelPtr == "" {
		l.chatModelMode = 1
		l.chatModelText = ""
	} else {
		l.chatModelMode = 2
		l.chatModelText = *dispModelPtr
	}
	if dispEffortPtr == nil {
		if l.chatDraft.Agent != "" && l.chatDraft.Agent != parentAgent {
			l.chatEffortMode = 1
		} else {
			l.chatEffortMode = 0
		}
		l.chatEffortText = ""
	} else if *dispEffortPtr == "" {
		l.chatEffortMode = 1
		l.chatEffortText = ""
	} else {
		l.chatEffortMode = 2
		l.chatEffortText = *dispEffortPtr
	}
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
	a.loadLarkAgentModels(effAgent, false)
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
				a.openLarkBotEditor(lark.BotConfig{
					Profile:        defProfile,
					Name:           defProfile,
					WorkspaceID:    defWS,
					Agent:          "antigravity",
					PermissionMode: lark.PermAsk,
					SessionMode:    lark.SessionChat,
					TriggerPolicy:  lark.TriggerSelf,
					Enabled:        true,
				})
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
						a.openLarkBotEditor(bot)
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
					if ml := larkModelSummary(m, bot.Model, bot.Effort); ml != "" {
						ui.Text(c, ml).FontSize(fsSmall).Font(monoFont).TextColor(t.TextMuted).Shrink(1)
					}
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
									if ml := larkModelSummary(m, eff.Model, eff.Effort); ml != "" {
										ui.Text(c, ml).FontSize(fsSmall).Font(monoFont).TextColor(t.TextMuted).Shrink(1)
									}
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
		a.larkWorkspaceRow(c, "lark-f-ws", false, &d.WorkspaceID, a.lark.botWSPicking, a.lark.botWSErr, func() {
			a.pickLarkWorkspaceFolder(m, larkPickBot)
		})
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
					if ag := agByLabel[agSel]; ag != "" && ag != d.Agent {
						d.Agent = ag
						d.Model = ""
						d.Effort = ""
						a.lark.botModelMode = 1
						a.lark.botModelText = ""
						a.lark.botEffortMode = 1
						a.lark.botEffortText = ""
						a.lark.botModelDirty = true
						a.lark.botEffortDirty = true
						a.loadLarkAgentModels(ag, false)
					}
				}
			})
		}
		a.larkModelEffortFields(c, "lark-f", d.Agent, "", "", false, false, false, &a.lark.botModelMode, &a.lark.botModelText, &a.lark.botModelDirty, &a.lark.botEffortMode, &a.lark.botEffortText, &a.lark.botEffortDirty)
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
				a.closeLarkBotEditor()
			}
			valid := strings.TrimSpace(d.Profile) != ""
			if valid && !a.lark.saving && !a.lark.botWSPicking && pressable(accentButton(c.Key("lark-save"), m.LarkSave())).Clicked() {
				out := *d
				if !a.lark.botModelDirty && !a.lark.botEffortDirty {
					out.Model = a.lark.botOrigModel
					out.Effort = a.lark.botOrigEffort
				} else {
					if a.lark.botModelMode == 1 {
						out.Model = ""
					} else {
						out.Model = strings.TrimSpace(a.lark.botModelText)
					}
					if a.lark.botEffortMode == 1 || d.Agent == "cursor" {
						out.Effort = ""
					} else {
						out.Effort = strings.ToLower(strings.TrimSpace(a.lark.botEffortText))
					}
				}
				a.saveLarkBot(out)
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
					if pressable(textButton(c.Key("lark-chat-manual"), m.LarkChatManualToggle())).Clicked() {
						l.chatShowManual = !l.chatShowManual
					}
				})
				ui.Row(c).Gap(8).AlignItems(ui.Center).Children(func() {
					ui.Box(c).Grow(1).Shrink(1).Children(func() {
						if fieldInput(c.Key("lark-chat-search"), &l.chatSearch, m.LarkChatSearchPlaceholder()).Submitted() {
							a.loadLarkBotChats(l.chatBotID)
						}
					})
					if pressable(textButton(c.Key("lark-chat-refresh"), m.LarkChatSearchButton())).Clicked() {
						a.loadLarkBotChats(l.chatBotID)
					}
				})
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
		a.larkWorkspaceRow(c, "lark-cf-ws", true, &d.WorkspaceID, l.chatWSPicking, l.chatWSErr, func() {
			a.pickLarkWorkspaceFolder(m, larkPickChat)
		})

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
							if agIDs[i] != d.Agent {
								d.Agent = agIDs[i]
								if d.Agent != "" && d.Agent != parentBot.Agent {
									l.chatModelMode = 1
									l.chatEffortMode = 1
								} else {
									l.chatModelMode = 0
									l.chatEffortMode = 0
								}
								l.chatModelText = ""
								l.chatEffortText = ""
								l.chatModelDirty = true
								l.chatEffortDirty = true
								effAg := d.Agent
								if effAg == "" {
									effAg = parentBot.Agent
								}
								a.loadLarkAgentModels(effAg, false)
							}
							break
						}
					}
				}
			})
		}

		// 4b. 模型与思考深度
		effAgent := d.Agent
		if effAgent == "" {
			effAgent = parentBot.Agent
		}
		canInheritModel := d.Agent == "" || d.Agent == parentBot.Agent
		parentModel, parentEffort := "", ""
		if canInheritModel {
			parentModel, parentEffort = parentBot.Model, parentBot.Effort
		}
		a.larkModelEffortFields(c, "lark-cf", effAgent, parentModel, parentEffort, true, d.Agent == "", canInheritModel, &l.chatModelMode, &l.chatModelText, &l.chatModelDirty, &l.chatEffortMode, &l.chatEffortText, &l.chatEffortDirty)

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
				a.closeLarkChatEditor()
			}
			valid := strings.TrimSpace(d.ChatID) != ""
			if valid && !l.saving && !l.chatWSPicking && pressable(accentButton(c.Key("lark-chat-save"), m.LarkSave())).Clicked() {
				out := *d
				if !l.chatModelDirty && !l.chatEffortDirty {
					out.Model = cloneOptStr(l.chatOrigModel)
					out.Effort = cloneOptStr(l.chatOrigEffort)
				} else {
					if !l.chatModelDirty && !(l.chatEffortDirty && l.chatSplitSuffix) {
						out.Model = cloneOptStr(l.chatOrigModel)
					} else {
						switch l.chatModelMode {
						case 0:
							if d.Agent == "" || d.Agent == parentBot.Agent {
								out.Model = nil
							} else {
								empty := ""
								out.Model = &empty
							}
						case 1:
							empty := ""
							out.Model = &empty
						default:
							v := strings.TrimSpace(l.chatModelText)
							out.Model = &v
						}
					}
					if !l.chatEffortDirty {
						out.Effort = cloneOptStr(l.chatOrigEffort)
					} else {
						switch l.chatEffortMode {
						case 0:
							if d.Agent == "" || d.Agent == parentBot.Agent {
								out.Effort = nil
							} else {
								empty := ""
								out.Effort = &empty
							}
						case 1:
							empty := ""
							out.Effort = &empty
						default:
							if effAgent == "cursor" {
								empty := ""
								out.Effort = &empty
							} else {
								v := strings.ToLower(strings.TrimSpace(l.chatEffortText))
								out.Effort = &v
							}
						}
					}
				}
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

func cloneOptStr(p *string) *string {
	if p == nil {
		return nil
	}
	v := *p
	return &v
}

func larkModelSummary(m locales.Messages, model, effort string) string {
	model = strings.TrimSpace(model)
	effort = strings.TrimSpace(effort)
	if model == "" && effort == "" {
		return ""
	}
	if model == "" {
		return m.LarkModelAgentDefault() + " (" + effort + ")"
	}
	return lark.FormatModelLabel("", model, effort)
}

// larkModelEffortFields 渲染机器人或群分身编辑器里的「模型」与「思考深度」两行控件。
// 渲染期不主动改写用户草稿；仅在用户显式切换模型/档位时更新草稿与脏标记。
func (a *app) larkModelEffortFields(
	c *ui.Context,
	keyPrefix, agentName, parentModel, parentEffort string,
	isChat, agentInherited, canInherit bool,
	modelMode *int, modelText *string, modelDirty *bool,
	effortMode *int, effortText *string, effortDirty *bool,
) {
	t, m := c.Theme(), L(c)
	normAgent := agent.NormalizeName(agentName)
	list := a.lark.models[normAgent]
	loading := a.lark.modelsLoading[normAgent]

	// 群分身且 Agent 选「继承」：只能选「继承默认」或「使用 Agent 默认」，防止父级换 Agent 后模型串扰
	if isChat && agentInherited {
		mLabels := []string{m.LarkInherit(), m.LarkModelAgentDefault()}
		mSel := mLabels[0]
		if *modelMode == 1 {
			mSel = mLabels[1]
		}
		settingsRow(c, m.LarkFieldModel(), func() {
			if ui.Select(c.Key(keyPrefix+"-model"), &mSel, mLabels).Changed() {
				if mSel == mLabels[1] {
					*modelMode = 1
				} else {
					*modelMode = 0
				}
				*modelText = ""
				*modelDirty = true
			}
		})
		eLabels := []string{m.LarkInherit(), m.LarkModelAgentDefault()}
		eSel := eLabels[0]
		if *effortMode == 1 {
			eSel = eLabels[1]
		}
		ui.Column(c).Gap(2).Children(func() {
			settingsRow(c, m.LarkFieldEffort(), func() {
				if ui.Select(c.Key(keyPrefix+"-effort"), &eSel, eLabels).Changed() {
					if eSel == eLabels[1] {
						*effortMode = 1
					} else {
						*effortMode = 0
					}
					*effortText = ""
					*effortDirty = true
				}
			})
			ui.Text(c, m.LarkModelPinAgentHint()).FontSize(fsSmall).TextColor(t.TextMuted)
		})
		return
	}

	// 构造模型下拉项（按索引映射，避免重名标签串扰）
	var mLabels []string
	var mModes []int
	var mIDs []string
	if isChat && canInherit {
		mLabels = append(mLabels, m.LarkInherit())
		mModes = append(mModes, 0)
		mIDs = append(mIDs, "")
	}
	mLabels = append(mLabels, m.LarkModelAgentDefault())
	mModes = append(mModes, 1)
	mIDs = append(mIDs, "")

	labelCount := map[string]int{}
	for _, opt := range list.Models {
		lbl := opt.Label
		if lbl == "" {
			lbl = opt.ID
		}
		labelCount[lbl]++
	}
	var matchedOpt *agent.ModelOption
	selectedIdx := 0
	for i := range list.Models {
		opt := list.Models[i]
		lbl := opt.Label
		if lbl == "" {
			lbl = opt.ID
		}
		if labelCount[lbl] > 1 {
			lbl = lbl + " (" + opt.ID + ")"
		}
		idx := len(mLabels)
		mLabels = append(mLabels, lbl)
		mModes = append(mModes, 2)
		mIDs = append(mIDs, opt.ID)
		if *modelMode == 2 && *modelText == opt.ID {
			selectedIdx = idx
			matchedOpt = &list.Models[i]
		}
	}
	customIdx := len(mLabels)
	mLabels = append(mLabels, m.LarkModelCustom())
	mModes = append(mModes, 3)
	mIDs = append(mIDs, "")

	curModelMode := *modelMode
	if curModelMode == 0 && !(isChat && canInherit) {
		curModelMode = 1
	}
	switch curModelMode {
	case 0:
		selectedIdx = 0
	case 1:
		for i, md := range mModes {
			if md == 1 {
				selectedIdx = i
				break
			}
		}
	case 2:
		if matchedOpt == nil && *modelText != "" {
			selectedIdx = customIdx
		}
	case 3:
		selectedIdx = customIdx
	}

	mSel := mLabels[selectedIdx]
	ui.Column(c).Gap(6).Children(func() {
		settingsRow(c, m.LarkFieldModel(), func() {
			ui.Row(c).Gap(8).AlignItems(ui.Center).Children(func() {
				if ui.Select(c.Key(keyPrefix+"-model"), &mSel, mLabels).Changed() {
					for i, lbl := range mLabels {
						if lbl == mSel {
							*modelMode = mModes[i]
							*modelDirty = true
							if mModes[i] == 2 {
								*modelText = mIDs[i]
								// 切换到具体模型时，若当前有效档位（显式或继承自父级）不被新模型支持，切回默认
								var newOpt *agent.ModelOption
								for j := range list.Models {
									if list.Models[j].ID == mIDs[i] {
										newOpt = &list.Models[j]
										break
									}
								}
								if newOpt != nil {
									effVal := ""
									if *effortMode == 2 {
										effVal = *effortText
									} else if isChat && canInherit && *effortMode == 0 {
										effVal = parentEffort
									}
									if effVal != "" && !slices.Contains(newOpt.Efforts, effVal) {
										*effortMode = 1
										*effortText = ""
										*effortDirty = true
									}
								}
							} else if mModes[i] <= 1 {
								*modelText = ""
							}
							break
						}
					}
				}
				if !loading && pressable(textButton(c.Key(keyPrefix+"-model-refresh"), m.LarkRefresh())).Clicked() {
					a.loadLarkAgentModels(normAgent, true)
				}
			})
		})
		if selectedIdx == customIdx {
			ui.Row(c).Gap(8).AlignItems(ui.Center).Children(func() {
				ui.Spacer(c)
				before := *modelText
				fieldInputSized(c.Key(keyPrefix+"-model-custom"), modelText, m.LarkModelCustomPlaceholder(), sz(280), sz(120))
				if *modelText != before {
					*modelDirty = true
				}
			})
		}
		if loading {
			ui.Text(c, m.LarkModelLoading()).FontSize(fsSmall).TextColor(t.TextMuted)
		} else if list.Error != "" {
			ui.Text(c, errtext.Text(errors.New(list.Error))).FontSize(fsSmall).TextColor(t.Danger)
		}
	})

	// 思考深度下拉（按当前 Agent 与已选模型过滤可用档位；不在渲染期清空已有草稿）
	if normAgent == "cursor" {
		ui.Column(c).Gap(2).Children(func() {
			settingsRow(c, m.LarkFieldEffort(), func() {
				ui.Text(c, m.LarkEffortIncludedInModel()).FontSize(fsSmall).TextColor(t.TextMuted)
			})
		})
		return
	}

	// 群分身模型选「继承默认」时，按父级模型查找对应的档位能力（避免 Carbon 继承时误展示 xhigh）
	effMatchedOpt := matchedOpt
	if effMatchedOpt == nil && isChat && canInherit && curModelMode == 0 && parentModel != "" {
		lookup := parentModel
		if normAgent == "antigravity" {
			if base, _, ok := agent.SplitAntigravityModelEffort(parentModel); ok {
				lookup = base
			}
		}
		for i := range list.Models {
			if strings.EqualFold(list.Models[i].ID, lookup) {
				effMatchedOpt = &list.Models[i]
				break
			}
		}
	}

	var availEfforts []string
	if effMatchedOpt != nil {
		availEfforts = effMatchedOpt.Efforts
	} else if len(list.Efforts) > 0 {
		availEfforts = list.Efforts
	} else {
		availEfforts = agent.DefaultEfforts(normAgent)
	}
	inheritedConflict := isChat && canInherit && *effortMode == 0 && parentEffort != "" &&
		effMatchedOpt != nil && !slices.Contains(availEfforts, parentEffort)
	staleExplicit := *effortMode == 2 && *effortText != "" && !slices.Contains(availEfforts, *effortText)
	if len(availEfforts) == 0 && !inheritedConflict && !staleExplicit {
		settingsRow(c, m.LarkFieldEffort(), func() {
			ui.Text(c, m.LarkEffortNotSupported()).FontSize(fsSmall).TextColor(t.TextMuted)
		})
		return
	}

	var eLabels []string
	var eModes []int
	var eVals []string
	if isChat && canInherit {
		eLabels = append(eLabels, m.LarkInherit())
		eModes = append(eModes, 0)
		eVals = append(eVals, "")
	}
	eLabels = append(eLabels, m.LarkEffortDefault())
	eModes = append(eModes, 1)
	eVals = append(eVals, "")
	eSelectedIdx := 0
	for _, ef := range availEfforts {
		idx := len(eLabels)
		eLabels = append(eLabels, ef)
		eModes = append(eModes, 2)
		eVals = append(eVals, ef)
		if *effortMode == 2 && *effortText == ef {
			eSelectedIdx = idx
		}
	}
	// 保留尚未出现在候选中的已有档位草稿，避免加载中或刷新时静默丢失
	if *effortMode == 2 && *effortText != "" && !slices.Contains(availEfforts, *effortText) {
		eSelectedIdx = len(eLabels)
		eLabels = append(eLabels, *effortText)
		eModes = append(eModes, 2)
		eVals = append(eVals, *effortText)
	}
	curEffortMode := *effortMode
	if curEffortMode == 0 && !(isChat && canInherit) {
		curEffortMode = 1
	}
	if curEffortMode == 1 {
		for i, md := range eModes {
			if md == 1 {
				eSelectedIdx = i
				break
			}
		}
	}
	eSel := eLabels[eSelectedIdx]
	ui.Column(c).Gap(2).Children(func() {
		settingsRow(c, m.LarkFieldEffort(), func() {
			if ui.Select(c.Key(keyPrefix+"-effort"), &eSel, eLabels).Changed() {
				for i, lbl := range eLabels {
					if lbl == eSel {
						*effortMode = eModes[i]
						*effortText = eVals[i]
						*effortDirty = true
						break
					}
				}
			}
		})
		if inheritedConflict || (len(availEfforts) == 0 && staleExplicit) {
			ui.Text(c, m.LarkEffortNotSupported()).FontSize(fsSmall).TextColor(t.Danger)
		}
	})
}

type larkPickTarget uint8

const (
	larkPickBot larkPickTarget = iota
	larkPickChat
)

type larkPickToken struct {
	target  larkPickTarget
	editGen uint64
	pickSeq uint64
}

func (a *app) larkWorkspaceRow(c *ui.Context, key string, allowInherit bool, wsID *string, picking bool, wsErr string, onPick func()) {
	t, m := c.Theme(), L(c)
	capN := len(a.workspaces)
	if allowInherit {
		capN++
	}
	wsLabels := make([]string, 0, capN)
	wsIDs := make([]string, 0, capN)
	wsSel := ""
	if allowInherit {
		wsLabels = append(wsLabels, m.LarkInherit())
		wsIDs = append(wsIDs, "")
		wsSel = wsLabels[0]
	}
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
		if w.ID == *wsID {
			wsSel = lbl
		}
	}
	if wsSel == "" && len(wsLabels) > 0 {
		wsSel = wsLabels[0]
	}
	r := a.request()
	canPick := r.canWrite() && !a.remoteHost()
	ui.Column(c).Gap(2).Children(func() {
		settingsRow(c, m.LarkFieldWorkspace(), func() {
			ui.Row(c).Gap(8).AlignItems(ui.Center).Children(func() {
				if len(wsLabels) > 0 {
					if ui.Select(c.Key(key), &wsSel, wsLabels).Changed() {
						for i, lbl := range wsLabels {
							if lbl == wsSel {
								*wsID = wsIDs[i]
								break
							}
						}
					}
				}
				if canPick {
					if pressable(textButton(c.Key(key+"-pick"), m.LarkChooseFolder())).Clicked() && !picking {
						onPick()
					}
				}
			})
		})
		if wsErr != "" {
			ui.Text(c, wsErr).FontSize(fsSmall).TextColor(t.Danger)
		}
	})
}

func (a *app) larkPickActive(tok larkPickToken) bool {
	l := &a.lark
	switch tok.target {
	case larkPickBot:
		return l.editing && l.botEditGen == tok.editGen && l.botWSPickSeq == tok.pickSeq
	case larkPickChat:
		return l.chatEditing && l.chatEditGen == tok.editGen && l.chatWSPickSeq == tok.pickSeq
	default:
		return false
	}
}

func (a *app) beginLarkPick(target larkPickTarget) (larkPickToken, bool) {
	l := &a.lark
	switch target {
	case larkPickBot:
		if !l.editing || l.botWSPicking {
			return larkPickToken{}, false
		}
		l.botWSPickSeq++
		l.botWSPicking = true
		l.botWSErr = ""
		return larkPickToken{target: target, editGen: l.botEditGen, pickSeq: l.botWSPickSeq}, true
	case larkPickChat:
		if !l.chatEditing || l.chatWSPicking {
			return larkPickToken{}, false
		}
		l.chatWSPickSeq++
		l.chatWSPicking = true
		l.chatWSErr = ""
		return larkPickToken{target: target, editGen: l.chatEditGen, pickSeq: l.chatWSPickSeq}, true
	default:
		return larkPickToken{}, false
	}
}

func (a *app) finishLarkPick(tok larkPickToken, wsID string, errMsg string) {
	if !a.larkPickActive(tok) {
		return
	}
	l := &a.lark
	switch tok.target {
	case larkPickBot:
		l.botWSPicking = false
		l.botWSErr = errMsg
		if wsID != "" {
			l.draft.WorkspaceID = wsID
		}
	case larkPickChat:
		l.chatWSPicking = false
		l.chatWSErr = errMsg
		if wsID != "" {
			l.chatDraft.WorkspaceID = wsID
		}
	}
}

func (a *app) pickLarkWorkspaceFolder(m locales.Messages, target larkPickTarget) {
	r := a.request()
	if !r.canWrite() || !r.canChooseLocalDirectory() || a.dialogOpen {
		return
	}
	tok, ok := a.beginLarkPick(target)
	if !ok {
		return
	}
	var dialogErr error
	a.inDialog(func() []string {
		paths, err := openDialog(mygo.OpenDialogOptions{
			Parent:      a.win,
			Title:       m.LarkChooseFolderTitle(),
			ButtonLabel: m.ChatChooseButton(),
			Directory:   true,
		})
		dialogErr = err
		return paths
	}, func(paths []string) {
		if !r.current() || !a.larkPickActive(tok) {
			return
		}
		if dialogErr != nil {
			a.finishLarkPick(tok, "", errtext.Text(dialogErr))
			return
		}
		if len(paths) == 0 {
			a.finishLarkPick(tok, "", "")
			return
		}
		if !r.canWrite() {
			a.finishLarkPick(tok, "", "")
			return
		}
		a.startLarkWorkspaceForDir(r, tok, paths[0])
	})
}

// startLarkWorkspaceForDir 在界面线程校验路径与已有工作区；原样保留路径传给 daemon，不裁剪尾随空格或 link/..。
func (a *app) startLarkWorkspaceForDir(r appRequest, tok larkPickToken, dir string) {
	if !r.current() || r.client == nil || !a.larkPickActive(tok) {
		return
	}
	if dir == "" || !filepath.IsAbs(dir) {
		a.finishLarkPick(tok, "", "")
		return
	}
	if w, ok := findWorkspaceByDir(a.workspaces, dir); ok {
		a.finishLarkPick(tok, w.ID, "")
		return
	}
	projectNames := make([]string, 0, len(a.projects))
	for _, p := range a.projects {
		projectNames = append(projectNames, p.Name)
	}
	go r.ensureLarkWorkspaceForDir(tok, dir, projectNames)
}

func (r appRequest) ensureLarkWorkspaceForDir(tok larkPickToken, dir string, projectNames []string) {
	a := r.app
	if !r.current() || r.client == nil {
		return
	}
	var added struct {
		ID string `json:"id"`
	}
	tried := make(map[string]bool, len(projectNames)+4)
	for _, n := range projectNames {
		tried[strings.ToLower(n)] = true
	}
	// 首次不传 name，由 daemon 按原始目录名校验与登记；遇重名或非法字符再换候选名做有限次重试。
	err := r.callProjectAddForLark(dir, "", &added)
	if err != nil && isProjectNameErr(err) {
		tried[strings.ToLower(filepath.Base(dir))] = true
		for range 3 {
			cand := nextLarkProjectName(dir, tried)
			tried[strings.ToLower(cand)] = true
			err = r.callProjectAddForLark(dir, cand, &added)
			if err == nil || !isProjectNameErr(err) {
				break
			}
		}
	}
	if err != nil && !isProjectAlreadyAddedErr(err) {
		r.update(func() {
			a.finishLarkPick(tok, "", errtext.Text(err))
		})
		return
	}

	// 单独查询最新 workspace.list，不依赖整轮 refresh（避免 task.list 等无关 RPC 失败丢失自动选中）。
	ctx, cancel := context.WithTimeout(a.windowContext(), 10*time.Second)
	var fresh []workspace
	listErr := r.invoke(ctx, "workspace.list", nil, &fresh)
	cancel()
	if listErr != nil {
		r.update(func() {
			a.finishLarkPick(tok, "", errtext.Text(listErr))
		})
		return
	}
	ws, ok := findWorkspaceForAddedProject(fresh, added.ID, dir)
	if !ok {
		r.update(func() {
			a.finishLarkPick(tok, "", errtext.Text(errors.New("workspace not found after adding project")))
		})
		return
	}
	r.update(func() {
		if !slices.ContainsFunc(a.workspaces, func(cur workspace) bool { return cur.ID == ws.ID }) {
			a.workspaces = append(a.workspaces, ws)
		}
		a.finishLarkPick(tok, ws.ID, "")
	})
	go r.refresh()
}

func (r appRequest) callProjectAddForLark(path, name string, out any) error {
	params := map[string]any{"path": path}
	if name != "" {
		params["name"] = name
	}
	ctx, cancel := context.WithTimeout(r.app.windowContext(), 10*time.Second)
	defer cancel()
	return r.invoke(ctx, "project.add", params, out)
}

func findWorkspaceForAddedProject(list []workspace, projectID, dir string) (workspace, bool) {
	if projectID != "" {
		for _, w := range list {
			if w.ProjectID == projectID && w.Kind == "main" {
				return w, true
			}
		}
		for _, w := range list {
			if w.ProjectID == projectID {
				return w, true
			}
		}
	}
	return findWorkspaceByDir(list, dir)
}

func findWorkspaceByDir(list []workspace, dir string) (workspace, bool) {
	var fallback workspace
	var foundFallback bool
	for _, w := range list {
		if !sameLocalDir(w.Path, dir) {
			continue
		}
		if w.Kind == "main" {
			return w, true
		}
		if !foundFallback {
			fallback = w
			foundFallback = true
		}
	}
	return fallback, foundFallback
}

// sameLocalDir 通过文件系统目录身份比对本机路径，涵盖软链接、大小写不敏感卷与带尾随空格的目录名。
func sameLocalDir(a, b string) bool {
	if a == "" || b == "" {
		return false
	}
	fa, errA := os.Stat(a)
	fb, errB := os.Stat(b)
	if errA == nil && errB == nil {
		return os.SameFile(fa, fb)
	}
	return a == b
}

func isProjectAlreadyAddedErr(err error) bool {
	var re *rpc.Error
	if !errors.As(err, &re) || re.Code != rpc.CodeConflict {
		return false
	}
	return strings.HasPrefix(re.Message, "project ") && strings.Contains(re.Message, " already added as ")
}

func isProjectNameErr(err error) bool {
	var re *rpc.Error
	if !errors.As(err, &re) {
		return false
	}
	if re.Code == rpc.CodeConflict {
		return strings.HasPrefix(re.Message, "project name ") && strings.HasSuffix(re.Message, " already used")
	}
	if re.Code == rpc.CodeBadRequest {
		return re.Message == "name required" ||
			strings.HasPrefix(re.Message, "invalid name ") ||
			strings.HasSuffix(re.Message, " must not contain spaces")
	}
	return false
}

func sanitizeProjectName(raw string) string {
	var b strings.Builder
	lastDash := false
	for _, r := range strings.TrimSpace(raw) {
		bad := unicode.IsSpace(r) || unicode.IsControl(r) || strings.ContainsRune(`/\:*?"<>|~^`, r)
		if bad {
			if !lastDash && b.Len() > 0 {
				b.WriteByte('-')
				lastDash = true
			}
			continue
		}
		b.WriteRune(r)
		lastDash = false
	}
	s := strings.Trim(b.String(), "-")
	if s == "" || s == "." || s == ".." {
		return "folder"
	}
	return s
}

func nextLarkProjectName(dir string, tried map[string]bool) string {
	base := sanitizeProjectName(filepath.Base(dir))
	if !tried[strings.ToLower(base)] {
		return base
	}
	if parent := sanitizeProjectName(filepath.Base(filepath.Dir(dir))); parent != "" && parent != "folder" && parent != base {
		cand := parent + "-" + base
		if !tried[strings.ToLower(cand)] {
			return cand
		}
	}
	for i := 2; ; i++ {
		cand := fmt.Sprintf("%s-%d", base, i)
		if !tried[strings.ToLower(cand)] {
			return cand
		}
	}
}
