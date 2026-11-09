package app

// 讨论的局域网协作在窗口里的部分（docs/features/discussions.md「局域网多人」）：房主的分享、邀请、批准和成员，
// 加入者的加入、连接状态和退出。别人电脑上的 Agent 和执行只显示，不给操作。

import (
	"fmt"
	"strings"
	"time"

	"github.com/egoist/mygo"
	"github.com/egoist/mygo/ui"

	"posse/internal/collaboration"
	"posse/internal/errtext"
)

// 常驻进程暂停队列的原因（collab_join.go 的 collabLostReason、store.NeedsWorkspace）
const (
	collabLostReason = "connection to the host was lost; confirm to continue"
	needsWorkspace   = "choose a workspace on this computer"
	configConflict   = "the host's copy of this agent's settings differs from what you set on this computer; review and save its settings"
	needsConfirm     = "confirm this agent's settings on this computer before it runs requests"
)

type discCollab struct {
	Self, SelfName, HostID, HostName, State, Error, ListenError string
	Host, Shared, Listening, Connected                          bool
	People                                                      []discPerson
	Requests                                                    []discJoinRequest
	Accepts                                                     map[string]string
	Outbox, Rejected                                            int
}

type discPerson struct {
	FP, Name, State string
	Avatar          string
	Host, Online    bool
	Generation      int64
}

type discJoinRequest struct {
	ID, FP, Name, Addr, State string
	CreatedAt                 time.Time
}

type joinForm struct {
	open   bool
	text   string
	addrs  string
	inv    *collaboration.Invite
	err    string
	busy   bool
	parsed string // 上次解析的文本
}

type inviteView struct {
	open    bool
	text    string
	expires time.Time
	addrs   []string
	fp      string
}

// mine：这个 Agent 或执行在不在这台电脑上（拥有者为空是房主自己的旧记录）。
func (d *discDetail) mine(owner string) bool {
	if d.collab == nil {
		return !d.info.Mirror || owner == ""
	}
	if owner == "" {
		return d.collab.Host
	}
	return owner == d.collab.Self
}

func (d *discDetail) person(fp string) *discPerson {
	if d.collab == nil {
		return nil
	}
	for i := range d.collab.People {
		if d.collab.People[i].FP == fp {
			return &d.collab.People[i]
		}
	}
	return nil
}

// ownerName 是 Agent 拥有者的名字（别人的才用得上）。
func (d *discDetail) ownerName(fp string) string {
	if p := d.person(fp); p != nil {
		return p.Name
	}
	if len(fp) > 8 {
		return fp[:8]
	}
	return fp
}

func (d *discDetail) online(fp string) bool {
	if p := d.person(fp); p != nil {
		return p.Online
	}
	return false
}

// writable：没归档；加入的讨论还要仍是成员。
func (d *discDetail) writable() bool {
	if d.info.Status == "archived" {
		return false
	}
	return !d.info.Mirror || d.collab != nil && d.collab.State == "active"
}

// hostRole：本机是这个讨论的房主（本机建的）。
func (d *discDetail) hostRole() bool { return !d.info.Mirror }

// fpText 把指纹的前 16 位分成四组，核对时好读。
func fpText(fp string) string {
	if len(fp) > 16 {
		fp = fp[:16]
	}
	var parts []string
	for i := 0; i < len(fp); i += 4 {
		parts = append(parts, fp[i:min(i+4, len(fp))])
	}
	return strings.Join(parts, " ")
}

// personName 是一个人现在的名字（改过名后按身份取最新的）；不在讨论的人员表里就用消息里存的 snapshot。
func (d *discDetail) personName(fp, snapshot string) string {
	if p := d.person(fp); p != nil && p.Name != "" {
		return p.Name
	}
	return snapshot
}

func initial(name string) string {
	for _, r := range name {
		return strings.ToUpper(string(r))
	}
	return "?"
}

// --- 顶栏、横幅

// collabMeta 是顶栏元信息里协作的一项：房主写已分享，加入者写房主和连接状态。
func (a *app) collabMeta(c *ui.Context, d *discDetail) func() {
	t, m := c.Theme(), L(c)
	cb := d.collab
	if cb == nil {
		return nil
	}
	return func() {
		ui.Row(c).Gap(4).AlignItems(ui.Center).Children(func() {
			if d.info.Mirror {
				color := t.TextMuted
				if cb.Connected {
					color = colors(t).sage
				}
				dot(c, color)
				ui.Text(c, m.CollabHostedBy(cb.HostName)).FontSize(fsSmall).TextColor(t.TextMuted).MaxLines(1)
				return
			}
			ui.Icon(c, iconShare).FontSize(fsSmall).TextColor(t.Accent)
			ui.Text(c, m.CollabSharedBadge()).FontSize(fsSmall).TextColor(t.Accent)
		})
	}
}

// collabBanner 在输入区上面说清楚加入的讨论现在能不能发言：等批准、被移除、离线。返回输入区要不要隐藏。
func (a *app) collabBanner(c *ui.Context, d *discDetail) (hide bool) {
	t, m := c.Theme(), L(c)
	cb := d.collab
	if cb == nil {
		return false
	}
	banner := func(key string, ic *ui.SVG, color ui.Color, title, body string, act func()) {
		ui.Row(c.Key(key)).Gap(8).Padding(8, 12).Radius(rControl).Background(soft(t, color)).AlignItems(ui.Center).Children(func() {
			ui.Icon(c, ic).FontSize(fsBody).TextColor(color)
			ui.Column(c).Gap(2).Grow(1).Shrink(1).MinWidth(0).Children(func() {
				ui.Text(c, title).FontSize(fsSmall).FontWeight(fwMedium).LineHeight(1.45)
				if body != "" {
					ui.Text(c, body).FontSize(fsSmall).TextColor(t.TextMuted).LineHeight(1.45).Selectable()
				}
			})
			if act != nil {
				act()
			}
		})
	}
	if !d.info.Mirror {
		if cb.Shared && !cb.Listening && cb.ListenError != "" {
			banner("collab-listen", iconHexAlert, t.Danger, m.CollabNotListening(), errtext.Text(fmt.Errorf("%s", cb.ListenError)), nil)
		}
		return false
	}
	switch cb.State {
	case "pending":
		body := m.CollabPendingBody(fpText(cb.HostID))
		if cb.Error != "" {
			body += "\n" + errtext.Text(fmt.Errorf("%s", cb.Error))
		}
		banner("collab-pending", iconHexOutline, colors(t).honey, m.CollabPendingTitle(cb.HostName), body, nil)
		return true
	case "rejected":
		banner("collab-rejected", iconHexAlert, t.Danger, m.CollabRejected(), errtext.Text(fmt.Errorf("%s", cb.Error)), nil)
		return true
	case "removed":
		banner("collab-removed", iconHexAlert, t.Danger, m.CollabRemovedNote(), "", nil)
		return true
	case "leaving":
		banner("collab-leaving", iconLeave, colors(t).honey, m.CollabLeavingNote(), "", nil)
		return true
	case "left":
		banner("collab-left", iconLeave, t.TextMuted, m.CollabLeftNote(), "", nil)
		return true
	}
	if !cb.Connected {
		body := m.CollabOfflineBody()
		if cb.Error != "" {
			body = errtext.Text(fmt.Errorf("%s", cb.Error)) + "\n" + body
		}
		banner("collab-offline", iconHexAlert, colors(t).honey, m.CollabOfflineTitle(), body, nil)
	}
	if cb.Outbox > 0 || cb.Rejected > 0 {
		var parts []string
		if cb.Outbox > 0 {
			parts = append(parts, m.CollabOutboxPending(cb.Outbox))
		}
		if cb.Rejected > 0 {
			parts = append(parts, m.CollabOutboxRejected(cb.Rejected))
		}
		ui.Text(c.Key("collab-outbox"), strings.Join(parts, m.DiscColon())).FontSize(fsSmall).TextColor(t.TextMuted).Padding(0, 4)
	}
	return false
}

// --- 右栏：参与的人、加入申请、分享

func (a *app) collabSection(c *ui.Context, d *discDetail) {
	t, m := c.Theme(), L(c)
	cb := d.collab
	archived := d.info.Status == "archived"
	if !d.hostRole() && cb == nil {
		return
	}
	ui.Column(c).Gap(6).Children(func() {
		ui.Row(c).Gap(8).AlignItems(ui.Center).Children(func() {
			cardTitle(c, m.CollabPeople()).Grow(1)
			if d.hostRole() && cb != nil && cb.Shared && !archived {
				if iconButtonSized(c.Key("collab-invite"), iconLink, m.CollabInviteEllipsis(), compactBox(), smallIcon()).Clicked() {
					go a.createInvite(d.key)
				}
			}
		})
		if d.hostRole() && (cb == nil || !cb.Shared) {
			if !archived && pressable(textButton(c.Key("collab-share"), m.CollabShareEllipsis())).Clicked() {
				a.askShare(m, d)
			}
			return
		}
		for _, r := range cb.Requests {
			ui.Column(c.Key("req-" + r.ID)).Gap(6).Padding(8).Radius(rControl).Background(soft(t, colors(t).honey)).Children(func() {
				ui.Text(c, m.CollabNoticeRequest(r.Name)).FontSize(fsSmall).FontWeight(fwMedium)
				ui.Text(c, m.CollabPersonFP(fpText(r.FP))).FontSize(fsSmall).Font(monoFont).TextColor(t.TextMuted)
				ui.Row(c).Gap(8).Children(func() {
					if pressable(accentButton(c.Key("approve-"+r.ID), m.CollabApprove())).Clicked() {
						go a.discCall("discussion.decide", map[string]any{"request": r.ID, "approve": true})
					}
					if pressable(textButton(c.Key("reject-"+r.ID), m.CollabReject())).Clicked() {
						go a.discCall("discussion.decide", map[string]any{"request": r.ID, "approve": false})
					}
				})
			})
		}
		for _, p := range cb.People {
			a.personRow(c, d, p)
		}
		if d.hostRole() && !archived && pressable(textButton(c.Key("collab-unshare"), m.CollabStopSharing())).Clicked() {
			a.confirm(m.CollabStopSharing(), m.CollabStopSharingBody(), m.CollabStopSharing(), func() {
				a.discCall("discussion.share", map[string]any{"discussion": d.key, "on": false})
			})
		}
	})
}

func (a *app) personRow(c *ui.Context, d *discDetail, p discPerson) {
	t, m := c.Theme(), L(c)
	row := ui.Row(c.Key("person-"+p.FP)).Gap(8).Padding(6, 8).Radius(rRow).AlignItems(ui.Center)
	if row.Hovered() {
		row.Background(colors(t).wash)
	}
	row.Children(func() {
		a.personAvatar(c, p.Avatar, p.Name, tileMedium())
		ui.Column(c).Gap(2).Grow(1).Shrink(1).MinWidth(0).Children(func() {
			ui.Row(c).Gap(6).AlignItems(ui.Center).Children(func() {
				ui.Text(c, p.Name).FontSize(fsBody).FontWeight(fwMedium).MaxLines(1).Shrink(1)
				var tags []string
				if p.Host {
					tags = append(tags, m.CollabHostLabel())
				}
				if d.collab != nil && p.FP == d.collab.Self {
					tags = append(tags, m.CollabYouLabel())
				}
				if len(tags) > 0 {
					ui.Text(c, strings.Join(tags, " · ")).FontSize(fsSmall).TextColor(t.TextMuted).Shrink(0)
				}
			})
			status, color := m.CollabOffline(), t.TextMuted
			switch {
			case p.State == "left":
				status = m.CollabLeftLabel()
			case p.State == "removed":
				status = m.CollabRemovedLabel()
			case p.Online:
				status, color = m.CollabOnline(), colors(t).sage
			}
			ui.Text(c, status).FontSize(fsSmall).TextColor(color)
		})
	})
	if d.collab != nil && p.FP == d.collab.Self {
		contextMenu(c, row, func(pm *popMenu) {
			if pm.Item(m.DiscEditProfile()).Icon(iconSettings).Chosen() {
				a.openSettings()
				a.settingsTab = "profile"
			}
		})
		return
	}
	if !d.writable() || p.State != "active" {
		return
	}
	contextMenu(c, row, func(pm *popMenu) {
		if pm.Item(m.CollabMentionPerson(p.Name)).Icon(iconAt).Chosen() {
			a.disc.mention, a.disc.target, a.disc.answerQ = p.FP, "", ""
		}
		if d.hostRole() && !p.Host {
			pm.Separator()
			if pm.Item(m.CollabRemovePersonEllipsis()).Icon(iconTrash).Danger().Chosen() {
				a.confirm(m.CollabRemovePersonTitle(p.Name), m.CollabRemovePersonBody(), m.CommonRemove(), func() {
					a.discCall("discussion.person.remove", map[string]any{"discussion": d.key, "person": p.FP})
				})
			}
		}
	})
}

func (a *app) askShare(m interface {
	CollabShareTitle() string
	CollabShareBody() string
	CollabShareVerb() string
}, d *discDetail) {
	key := d.key
	a.confirm(m.CollabShareTitle(), m.CollabShareBody(), m.CollabShareVerb(), func() {
		if a.call("discussion.share", map[string]any{"discussion": key, "on": true}, nil, 10*time.Second) == nil {
			a.createInvite(key)
		}
		a.refresh()
	})
}

func (a *app) askLeave(m interface {
	CollabLeaveTitle() string
	CollabLeaveBody() string
	CollabLeave() string
}, d *discDetail) {
	key := d.key
	a.confirm(m.CollabLeaveTitle(), m.CollabLeaveBody(), m.CollabLeave(), func() {
		a.discCall("discussion.leave", map[string]any{"discussion": key})
	})
}

// createInvite 生成一份邀请并打开邀请框（复制后由用户自己发出去）。
func (a *app) createInvite(key string) {
	var r struct {
		Invite      string
		Expires     time.Time
		Addrs       []string
		Fingerprint string
	}
	if a.call("discussion.invite", map[string]any{"discussion": key}, &r, 10*time.Second) != nil {
		return
	}
	a.update(func() {
		a.disc.invite = inviteView{open: true, text: r.Invite, expires: r.Expires, addrs: r.Addrs, fp: r.Fingerprint}
	})
}

func (a *app) inviteDialog(c *ui.Context) {
	t, m := c.Theme(), L(c)
	v := &a.disc.invite
	if !v.open {
		return
	}
	ui.Modal(c, &v.open, func() {
		ui.Column(c).Width(480).Gap(12).Children(func() {
			titleText(c, m.CollabInviteTitle(), fsTitle)
			ui.Text(c, m.CollabInviteNote(v.expires.Local())).FontSize(fsSmall).TextColor(t.TextMuted).LineHeight(1.5)
			ui.Text(c.Key("invite-text"), v.text).FontSize(fsSmall).Font(monoFont).LineHeight(1.4).Selectable().
				Padding(8, 10).Radius(rControl).Background(colors(t).fill).MaxLines(6)
			ui.Column(c).Gap(4).Children(func() {
				ui.Text(c, m.CollabFingerprint()).FontSize(fsSmall).TextColor(t.TextMuted)
				ui.Text(c, fpText(v.fp)).FontSize(fsBody).Font(monoFont).Selectable()
				ui.Text(c, m.CollabAddresses()).FontSize(fsSmall).TextColor(t.TextMuted)
				ui.Text(c, strings.Join(v.addrs, ", ")).FontSize(fsSmall).Font(monoFont).Selectable()
			})
			ui.Row(c).Gap(8).Justify(ui.End).Padding(8, 0, 0, 0).Children(func() {
				if pressable(textButton(c, m.CommonClose())).Clicked() {
					v.open = false
				}
				if pressable(accentButton(c, m.CollabCopyInvite())).Clicked() {
					mygo.Clipboard.WriteText(v.text)
				}
			})
		})
	})
}

func (a *app) openJoin() {
	a.disc.join = joinForm{open: true}
	if text := mygo.Clipboard.ReadText(); strings.Contains(text, collaboration.InvitePrefix) {
		a.disc.join.text = text
	}
}

func (a *app) joinDialog(c *ui.Context) {
	t, m := c.Theme(), L(c)
	f := &a.disc.join
	if !f.open {
		return
	}
	if f.text != f.parsed {
		f.parsed, f.inv, f.err = f.text, nil, ""
		if strings.TrimSpace(f.text) != "" {
			if inv, err := collaboration.ParseInvite(f.text); err != nil {
				f.err = errtext.Text(err)
			} else {
				f.inv = &inv
				f.addrs = strings.Join(inv.Addrs, ", ")
			}
		}
	}
	ui.Modal(c, &f.open, func() {
		ui.Column(c).Width(480).Gap(12).Children(func() {
			titleText(c, m.CollabJoinTitle(), fsTitle)
			fieldArea(c, &f.text, m.CollabJoinPaste(), 72).AutoFocus()
			if f.err != "" {
				ui.Text(c, f.err).FontSize(fsSmall).TextColor(t.Danger)
			}
			if inv := f.inv; inv != nil {
				ui.Column(c).Gap(6).Padding(12).Radius(rControl).Background(colors(t).wash).Children(func() {
					ui.Text(c, m.CollabJoinFrom(inv.Title, inv.HostName)).FontSize(fsBody).FontWeight(fwMedium).LineHeight(1.45)
					ui.Text(c, m.CollabFingerprint()).FontSize(fsSmall).TextColor(t.TextMuted)
					ui.Text(c, fpText(inv.Host)).FontSize(fsBody).Font(monoFont).Selectable()
					ui.Text(c, m.CollabJoinCheck()).FontSize(fsSmall).TextColor(t.TextMuted).LineHeight(1.45)
				})
				ui.Column(c).Gap(6).Children(func() {
					ui.Text(c, m.CollabAddresses()).FontSize(fsSmall).TextColor(t.TextMuted)
					fieldInput(c, &f.addrs, "192.168.1.2:6791")
					ui.Text(c, m.CollabJoinAddrsNote()).FontSize(fsSmall).TextColor(t.TextMuted).LineHeight(1.45)
				})
			}
			ready := f.inv != nil && !f.busy
			ui.Row(c).Gap(8).Justify(ui.End).Padding(8, 0, 0, 0).Children(func() {
				if pressable(textButton(c, m.CommonCancel())).Clicked() {
					f.open = false
				}
				if pressable(accentButton(c, m.CollabJoinSend()).Disabled(!ready)).Clicked() && ready {
					f.busy = true
					var addrs []string
					for _, s := range strings.Split(f.addrs, ",") {
						if s = strings.TrimSpace(s); s != "" {
							addrs = append(addrs, s)
						}
					}
					text := f.text
					go func() {
						var r struct{ Discussion string }
						err := a.call("discussion.join", map[string]any{"invite": text, "addrs": addrs}, &r, 30*time.Second)
						a.update(func() {
							f.busy = false
							if err != nil {
								f.err = errtext.Text(err)
								return
							}
							f.open = false
						})
						if err == nil {
							a.refresh()
							a.update(func() { a.openDiscussion(r.Discussion) })
						}
					}()
				}
			})
		})
	})
}
