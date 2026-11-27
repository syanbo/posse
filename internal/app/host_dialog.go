package app

import (
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/egoist/mygo/ui"

	"posse/internal/errtext"
	"posse/internal/remote"
	"posse/locales"
)

type hostDialogState struct {
	open        bool
	gen         uint64
	editing     remote.Host // ID 为空表示添加
	target      string
	name        string
	icon        string
	iconImage   string
	pickingIcon bool
	user        string
	port        string
	home        string
	exe         string
	nameTouched bool
	portAuto    bool // 端口由粘贴的 ssh 命令自动填入，替换目标时自动清理
	advanced    bool
	submitted   bool
	saving      bool
	hints       []string
	fieldErr    map[string]string
	err         string
	aliases     []remote.Alias
	aliasesDone bool
}

func (a *app) openHostDialog(editing remote.Host) {
	owner := a.windowOwner()
	if owner.hostDlg.open {
		owner.dropHostIconFile(owner.hostDlg.iconImage, owner.hostDlg.editing.IconImage)
	}
	owner.loadHostMenu()
	d := &owner.hostDlg
	nextGen := d.gen + 1
	*d = hostDialogState{
		open:        true,
		gen:         nextGen,
		editing:     editing,
		target:      editing.Target,
		name:        editing.Name,
		icon:        editing.Icon,
		iconImage:   editing.IconImage,
		user:        editing.User,
		home:        editing.Home,
		exe:         editing.Executable,
		nameTouched: editing.ID != "",
		advanced:    editing.User != "" || editing.Port != 0 || editing.Home != "" || editing.Executable != "",
		fieldErr:    map[string]string{},
	}
	if editing.Port != 0 {
		d.port = strconv.Itoa(editing.Port)
	}
	go func(gen uint64) {
		homeDir, _ := os.UserHomeDir()
		aliases := remote.ConfigAliases(homeDir)
		owner.update(func() {
			if owner.hostDlg.open && owner.hostDlg.gen == gen {
				owner.hostDlg.aliases = aliases
				owner.hostDlg.aliasesDone = true
			}
		})
	}(nextGen)
}

func (a *app) hostDialog(c *ui.Context) {
	owner := a.windowOwner()
	d := &owner.hostDlg
	if !d.open {
		return
	}
	t, p, m := c.Theme(), colors(c.Theme()), L(c)
	isEdit := d.editing.ID != ""
	title := m.HostDialogAddTitle()
	if isEdit {
		title = m.HostDialogEditTitle(d.editing.Name)
	}

	// 直接传 &d.open 让 mygo 的 Escape 生效；点遮罩或保存中恢复打开，避免误点丢掉表单内容
	modal := ui.Modal(c.Key("host-dialog"), &d.open, func() {
		ui.Column(c).Width(hostDialogWidth()).Gap(12).Children(func() {
			titleText(c, title, fsTitle)

			var submit bool

			// 表单正文放入可滚动容器，确保 900×560 最小窗口下底部按钮始终可达
			ui.Scroll(c).MaxHeight(sz(360)).Children(func() {
				ui.Column(c).Gap(12).Children(func() {
					// SSH 目标
					ui.Column(c).Gap(4).Children(func() {
						ui.Text(c, m.HostFieldTarget()).FontSize(fsSmall).FontWeight(fwMedium).TextColor(t.TextMuted)
						inp := fieldInput(c.Key("host-target"), &d.target, m.HostFieldTargetPlaceholder()).Label(m.HostFieldTarget()).AutoFocus()
						if inp.Changed() {
							a.onHostTargetChanged(m, d)
						}
						if inp.Submitted() {
							submit = true
						}
						if msg := d.fieldErr["target"]; msg != "" {
							ui.Text(c, msg).FontSize(fsSmall).TextColor(t.Danger).Wrap()
						} else {
							for _, hint := range d.hints {
								ui.Text(c, hint).FontSize(fsSmall).TextColor(t.TextMuted).Wrap()
							}
							if dup := a.duplicateEndpointName(d); dup != "" {
								ui.Text(c, m.HostDuplicateEndpoint(dup)).FontSize(fsSmall).TextColor(t.TextMuted).Wrap()
							}
						}
					})

					// ~/.ssh/config 候选（仅添加时显示，最多 6 条，按目标前缀筛选）
					if !isEdit {
						filtered := filterAliases(d.aliases, d.target)
						if len(filtered) > 0 {
							ui.Column(c).Gap(4).Children(func() {
								ui.Text(c, m.HostSshConfig()).FontSize(fsSmall).TextColor(t.TextMuted)
								ui.Column(c).Gap(2).Children(func() {
									for _, al := range filtered {
										row := ui.Row(c.Key("ssh-alias-"+al.Name)).Height(sz(28)).Padding(0, 8).Gap(8).Radius(rRow).
											AlignItems(ui.Center).Role(ui.RoleButton).Label(al.Name)
										bg := ui.Transparent
										if row.Hovered() {
											bg = p.wash
										}
										row.Background(row.AnimateColor("bg", bg, hover)).Children(func() {
											ui.Icon(c, iconHost).FontSize(smallIcon()).TextColor(t.TextMuted).Shrink(0)
											ui.Text(c, al.Name).FontSize(fsBody).TextColor(t.Text).SingleLine().Shrink(0)
											if hint := al.Hint(); hint != "" {
												ui.Text(c, hint).FontSize(fsSmall).Font(monoFont).TextColor(t.TextMuted).SingleLine().Ellipsis("…").Shrink(1).MinWidth(0)
											}
										})
										if row.Clicked() {
											d.target = al.Name
											d.hints = nil
											if d.portAuto {
												d.port, d.portAuto = "", false
											}
											if !d.nameTouched {
												d.name = remote.DeriveName(al.Name)
											}
											if d.submitted {
												a.validateHostDialog(m, d)
											}
										}
									}
								})
							})
						}
					}

					// 名称
					ui.Column(c).Gap(4).Children(func() {
						ui.Text(c, m.HostFieldName()).FontSize(fsSmall).FontWeight(fwMedium).TextColor(t.TextMuted)
						placeholder := remote.DeriveName(d.target)
						inp := fieldInput(c.Key("host-name"), &d.name, placeholder).Label(m.HostFieldName())
						if inp.Changed() {
							d.nameTouched = strings.TrimSpace(d.name) != ""
							if d.submitted {
								a.validateHostDialog(m, d)
							}
						}
						if inp.Submitted() {
							submit = true
						}
						if msg := d.fieldErr["name"]; msg != "" {
							ui.Text(c, msg).FontSize(fsSmall).TextColor(t.Danger).Wrap()
						}
					})

					// 图标（自定义 Emoji/文字或图片，留空则显示名称缩略）
					ui.Column(c).Gap(4).Children(func() {
						ui.Text(c, m.HostFieldIcon()).FontSize(fsSmall).FontWeight(fwMedium).TextColor(t.TextMuted)
						draftHost, _, _ := a.buildHostFromDialog(d)
						previewMono := "SH"
						if cands := monogramCandidates(draftHost.Name); len(cands) > 0 {
							previewMono = cands[0]
						}
						ui.Row(c).Gap(8).AlignItems(ui.Center).Children(func() {
							ui.Box(c.Key("host-icon-preview")).Size(navigationBox(), navigationBox()).Shrink(0).Radius(rControl).Center().
								Background(p.tint).BorderWidth(hairline).BorderColor(t.Accent).Children(func() {
								a.hostTileGlyph(c, draftHost, previewMono, t.Accent)
							})
							ui.Column(c).Grow(1).MinWidth(0).Children(func() {
								inp := fieldInput(c.Key("host-icon-text"), &d.icon, m.ProjectIconTextPlaceholder()).Label(m.HostFieldIcon())
								if inp.Submitted() {
									submit = true
								}
							})
							pickLabel := m.ProjectIconChooseImage()
							if d.pickingIcon {
								pickLabel = m.ProjectIconChoosing()
							}
							if pressable(textButton(c.Key("host-icon-pick"), pickLabel).Disabled(d.pickingIcon || d.saving)).Clicked() && !d.pickingIcon && !d.saving {
								d.pickingIcon = true
								go a.pickHostIcon(m, d.gen, draftHost.Name)
							}
							if strings.TrimSpace(d.icon) != "" || d.iconImage != "" {
								if pressable(textButton(c.Key("host-icon-reset"), m.CommonResetToDefault()).Disabled(d.saving)).Clicked() && !d.saving {
									owner.dropHostIconFile(d.iconImage, d.editing.IconImage)
									d.icon, d.iconImage = "", ""
								}
							}
						})
					})

					// 折叠高级设置
					ui.Column(c).Gap(8).Children(func() {
						chev := iconChevron
						if d.advanced {
							chev = iconDown
						}
						toggle := ui.Row(c.Key("host-advanced-toggle")).Height(sz(24)).Padding(0, 4).Gap(6).Radius(rSmall).
							AlignItems(ui.Center).AlignSelf(ui.Start).Role(ui.RoleButton).Label(m.HostAdvanced())
						if toggle.Hovered() {
							toggle.Background(p.wash)
						}
						toggle.Children(func() {
							ui.Icon(c, chev).FontSize(fsSmall).TextColor(t.TextMuted).Shrink(0)
							ui.Text(c, m.HostAdvanced()).FontSize(fsSmall).TextColor(t.TextMuted).SingleLine()
						})
						if toggle.Clicked() {
							d.advanced = !d.advanced
						}
						if d.advanced {
							ui.Column(c).Gap(8).Padding(0, 0, 0, 12).Children(func() {
								ui.Row(c).Gap(8).Children(func() {
									ui.Column(c).Grow(1).MinWidth(0).Gap(4).Children(func() {
										ui.Text(c, m.HostFieldUser()).FontSize(fsSmall).TextColor(t.TextMuted)
										if fieldInput(c.Key("host-user"), &d.user, "").Label(m.HostFieldUser()).Changed() && d.submitted {
											a.validateHostDialog(m, d)
										}
									})
									ui.Column(c).Width(sz(100)).Shrink(0).Gap(4).Children(func() {
										ui.Text(c, m.HostFieldPort()).FontSize(fsSmall).TextColor(t.TextMuted)
										if fieldInput(c.Key("host-port"), &d.port, "22").Label(m.HostFieldPort()).Changed() {
											d.portAuto = false
											if d.submitted {
												a.validateHostDialog(m, d)
											}
										}
									})
								})
								if msg := d.fieldErr["user"]; msg != "" {
									ui.Text(c, msg).FontSize(fsSmall).TextColor(t.Danger).Wrap()
								}
								if msg := d.fieldErr["port"]; msg != "" {
									ui.Text(c, msg).FontSize(fsSmall).TextColor(t.Danger).Wrap()
								}
								ui.Column(c).Gap(4).Children(func() {
									ui.Text(c, m.HostFieldHome()).FontSize(fsSmall).TextColor(t.TextMuted)
									if fieldInput(c.Key("host-home"), &d.home, "~/.posse").Label(m.HostFieldHome()).Changed() && d.submitted {
										a.validateHostDialog(m, d)
									}
									if msg := d.fieldErr["home"]; msg != "" {
										ui.Text(c, msg).FontSize(fsSmall).TextColor(t.Danger).Wrap()
									}
								})
								ui.Column(c).Gap(4).Children(func() {
									ui.Text(c, m.HostFieldExecutable()).FontSize(fsSmall).TextColor(t.TextMuted)
									if fieldInput(c.Key("host-exe"), &d.exe, m.HostFieldExecutablePlaceholder()).Label(m.HostFieldExecutable()).Changed() && d.submitted {
										a.validateHostDialog(m, d)
									}
									if msg := d.fieldErr["exe"]; msg != "" {
										ui.Text(c, msg).FontSize(fsSmall).TextColor(t.Danger).Wrap()
									}
								})
							})
						}
					})
				})
			})

			reconnectLive := isEdit && a.editingLiveConnectionChanged(d)
			if reconnectLive {
				ui.Text(c, m.HostEditLiveNote()).FontSize(fsSmall).TextColor(t.TextMuted).Wrap()
			}
			if d.err != "" {
				ui.Text(c, d.err).FontSize(fsSmall).TextColor(t.Danger).Wrap()
			}

			// 底部按钮行
			ui.Row(c).Gap(8).AlignItems(ui.Center).Padding(8, 0, 0, 0).Children(func() {
				if isEdit {
					rm := confirmButton(c, pressable(textButton(c.Key("host-remove"), m.HostRemove()).TextColor(t.Danger).Disabled(d.saving)))
					if rm.Clicked() && !d.saving {
						editing := d.editing
						owner.dropHostIconFile(d.iconImage, d.editing.IconImage)
						d.open = false
						a.confirmRemoveHost(m, editing)
					}
				}
				ui.Spacer(c)
				if confirmButton(c, pressable(textButton(c.Key("host-cancel"), m.CommonCancel()).Disabled(d.saving))).Clicked() && !d.saving {
					owner.dropHostIconFile(d.iconImage, d.editing.IconImage)
					d.open = false
				}
				primary := m.HostAddConnect()
				if isEdit {
					primary = m.CommonSave()
					if reconnectLive {
						primary = m.HostSaveReconnect()
					}
				}
				if confirmButton(c, pressable(accentButton(c.Key("host-submit"), primary).Disabled(d.saving))).Clicked() && !d.saving {
					submit = true
				}
			})

			if submit && !d.saving {
				a.submitHostDialog(m, d, reconnectLive)
			}
		})
	})
	if modal.ClickedOutside() || d.saving {
		d.open = true
	} else if !d.open {
		owner.dropHostIconFile(d.iconImage, d.editing.IconImage)
	}
}

func (a *app) onHostTargetChanged(m locales.Messages, d *hostDialogState) {
	d.hints = nil
	if parsed, ignored, ok := remote.ParseSSHCommand(d.target); ok {
		d.target = parsed.Target
		if parsed.Port != 0 {
			d.port = strconv.Itoa(parsed.Port)
			d.portAuto = true
			d.advanced = true
			d.hints = append(d.hints, m.HostParsedPort(parsed.Port))
		} else if d.portAuto {
			d.port, d.portAuto = "", false
		}
		if len(ignored) > 0 {
			d.hints = append(d.hints, m.HostParsedIgnored(strings.Join(ignored, ", ")))
		}
	} else if d.portAuto {
		d.port, d.portAuto = "", false
	}
	if !d.nameTouched {
		d.name = remote.DeriveName(d.target)
	}
	if d.submitted {
		a.validateHostDialog(m, d)
	}
}

func (a *app) buildHostFromDialog(d *hostDialogState) (remote.Host, int, bool) {
	target := strings.TrimSpace(d.target)
	name := strings.TrimSpace(d.name)
	if name == "" {
		name = remote.DeriveName(target)
	}
	portVal := 0
	portOK := true
	if p := strings.TrimSpace(d.port); p != "" {
		n, err := strconv.Atoi(p)
		if err != nil || n < 1 || n > 65535 {
			portOK = false
		} else {
			portVal = n
		}
	}
	iconText := strings.TrimSpace(d.icon)
	iconImg := ""
	if iconText == "" {
		iconImg = d.iconImage
	}
	h := remote.Host{
		ID:         d.editing.ID,
		Name:       name,
		Target:     target,
		User:       strings.TrimSpace(d.user),
		Port:       portVal,
		Home:       strings.TrimSpace(d.home),
		Executable: strings.TrimSpace(d.exe),
		Icon:       iconText,
		IconImage:  iconImg,
	}
	return h, portVal, portOK
}

func (a *app) validateHostDialog(m locales.Messages, d *hostDialogState) (remote.Host, bool) {
	d.fieldErr = map[string]string{}
	d.err = ""
	h, _, portOK := a.buildHostFromDialog(d)
	if h.Target == "" {
		d.fieldErr["target"] = m.HostErrTargetRequired()
	} else if err := (remote.Host{Name: "x", Target: h.Target}).Validate(); err != nil {
		d.fieldErr["target"] = m.HostErrTargetInvalid()
	}
	if h.Name != "" {
		for _, existing := range a.windowOwner().hostMenu.hosts {
			if existing.ID != h.ID && existing.Name == h.Name {
				d.fieldErr["name"] = m.HostErrNameTaken()
				break
			}
		}
	}
	if !portOK {
		d.fieldErr["port"] = m.HostErrPort()
		d.advanced = true
	}
	if h.User != "" && strings.Contains(h.Target, "@") {
		d.fieldErr["user"] = m.HostErrUserConflict()
		d.advanced = true
	}
	for field, p := range map[string]string{"home": h.Home, "exe": h.Executable} {
		if p != "" && ((!filepath.IsAbs(p) && !strings.HasPrefix(p, "~/")) || strings.ContainsAny(p, "\x00\r\n")) {
			d.fieldErr[field] = m.HostErrPath()
			d.advanced = true
		}
	}
	return h, len(d.fieldErr) == 0
}

func (a *app) duplicateEndpointName(d *hostDialogState) string {
	h, _, portOK := a.buildHostFromDialog(d)
	if !portOK || h.Target == "" {
		return ""
	}
	for _, existing := range a.windowOwner().hostMenu.hosts {
		if existing.ID == h.ID {
			continue
		}
		if existing.Target == h.Target && existing.User == h.User && existing.Port == h.Port {
			return existing.Name
		}
	}
	return ""
}

func (a *app) editingLiveConnectionChanged(d *hostDialogState) bool {
	if d.editing.ID == "" || a.views == nil {
		return false
	}
	view := a.views.all[d.editing.ID]
	if view == nil || (!view.connecting && (view.host == nil || !view.host.snapshot().Connected)) {
		return false
	}
	h, _, portOK := a.buildHostFromDialog(d)
	if !portOK {
		return true
	}
	return h.Target != d.editing.Target || h.User != d.editing.User || h.Port != d.editing.Port || h.Home != d.editing.Home || h.Executable != d.editing.Executable
}

func (a *app) submitHostDialog(m locales.Messages, d *hostDialogState, reconnectLive bool) {
	d.submitted = true
	h, ok := a.validateHostDialog(m, d)
	if !ok {
		return
	}
	isEdit := h.ID != ""
	var expected *remote.Host
	if isEdit {
		cp := d.editing
		expected = &cp
	}
	owner := a.windowOwner()
	gen := d.gen
	d.saving = true
	go func() {
		saved, err := remote.PutUnique(owner.home, h, expected)
		owner.update(func() {
			if owner.hostDlg.gen != gen {
				return
			}
			owner.hostDlg.saving = false
			if err != nil {
				owner.hostDlg.err = errtext.Text(err)
				return
			}
			owner.loadHostMenu()
			oldImg := owner.hostDlg.editing.IconImage
			draftImg := owner.hostDlg.iconImage
			if draftImg != saved.IconImage {
				owner.dropHostIconFile(draftImg, saved.IconImage)
			}
			if oldImg != "" && oldImg != saved.IconImage {
				owner.removeHostIconFile(oldImg)
			}
			owner.hostDlg.open = false
			if !isEdit {
				owner.selectRemoteHost(saved)
			} else if reconnectLive {
				owner.hosts.disconnect(saved.ID)
				owner.selectRemoteHost(saved)
			}
		})
	}()
}

func (a *app) confirmRemoveHost(m locales.Messages, h remote.Host) {
	owner := a.windowOwner()
	a.confirm(m.HostRemoveTitle(h.Name), m.HostRemoveBody(), m.CommonRemove(), func() {
		// 先在锁内核对并落盘移除，确认写盘成功后再切换视图与回收连接
		err := remote.RemoveIf(owner.home, h)
		owner.update(func() {
			owner.loadHostMenu()
			if err != nil {
				owner.displayed().err = errtext.Text(err)
				return
			}
			if h.IconImage != "" {
				owner.removeHostIconFile(h.IconImage)
			}
			if owner.views != nil && (owner.views.active.Load().viewID == h.ID || owner.views.target.viewID == h.ID) {
				if owner.hosts != nil {
					owner.hosts.disconnect(h.ID)
				}
				owner.selectLocalHost()
			} else if owner.hosts != nil {
				owner.hosts.disconnect(h.ID)
			}
		})
	})
}

func filterAliases(aliases []remote.Alias, query string) []remote.Alias {
	q := strings.ToLower(strings.TrimSpace(query))
	var out []remote.Alias
	for _, al := range aliases {
		if q != "" && !strings.HasPrefix(strings.ToLower(al.Name), q) && !strings.Contains(strings.ToLower(al.HostName), q) {
			continue
		}
		out = append(out, al)
		if len(out) >= 6 {
			break
		}
	}
	return out
}
