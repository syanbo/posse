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
	owner    string
	bots     []lark.BotConfig
	statuses map[string]lark.BotStatus
	profiles []lark.ProfileInfo
	err      string

	editing bool
	draft   lark.BotConfig
}

func (a *app) loadLark() {
	if a.lark.loading || a.c == nil {
		return
	}
	a.lark.loading = true
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		var listOut struct {
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
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		var saved lark.BotConfig
		err := a.c.Call(ctx, "lark.bots.save", map[string]any{"ownerOpenId": owner, "bot": bot}, &saved)
		cancel()
		a.update(func() {
			a.lark.saving = false
			if err != nil {
				a.lark.err = errtext.Text(err)
				return
			}
			a.lark.err = ""
			a.lark.editing = false
			a.loadLark()
		})
	}()
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
						l.draft = bot
						l.editing = true
					}
					if pressable(textButton(c.Key("lark-rm:"+bot.ID), m.LarkDeleteBot())).Clicked() {
						a.deleteLarkBot(bot.ID)
					}
				})
				wsLabel := bot.WorkspaceID
				for _, w := range a.workspaces {
					if w.ID == bot.WorkspaceID {
						wsLabel = w.Label
						break
					}
				}
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
			})
		}
	})

	if l.editing {
		a.larkBotEditor(c)
	}
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
			wsByLabel := map[string]string{}
			wsSel := ""
			for _, w := range a.workspaces {
				wsLabels = append(wsLabels, w.Label)
				wsByLabel[w.Label] = w.ID
				if w.ID == d.WorkspaceID {
					wsSel = w.Label
				}
			}
			if wsSel == "" {
				wsSel = wsLabels[0]
			}
			settingsRow(c, m.LarkFieldWorkspace(), func() {
				if ui.Select(c.Key("lark-f-ws"), &wsSel, wsLabels).Changed() {
					if id := wsByLabel[wsSel]; id != "" {
						d.WorkspaceID = id
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
