package app

import (
	"context"
	"os"
	"strings"
	"time"

	"github.com/egoist/mygo"
	"github.com/egoist/mygo/ui"

	"posse/internal/collaboration"
	"posse/internal/errtext"
	"posse/locales"
)

// 个人资料：名字和头像跟着本机的安装身份走，所有讨论和配对的手机共用（常驻进程的 profile.get/set）。
// 头像文件在 $POSSE_HOME/avatars，按摘要命名，窗口直接读。

type profileData struct {
	ID, Name, Avatar string
	Rev              int64
	Pending          []struct{ Room, Title, Host, Reason string }
}

type profileState struct {
	info    profileData
	loaded  bool
	loading bool
	at      time.Time

	// 设置页里的草稿：改名、选了图（预览）或要去掉头像，点保存才交给常驻进程
	name    string
	edited  bool
	pick    string
	preview ui.ImageSource
	remove  bool
	picking bool
	saving  bool
	err     string // 选图、保存失败：写在卡片里，侧栏底部的错误条离得太远，收起侧栏时还看不到
}

type avatarEntry struct {
	img     ui.ImageSource
	checked time.Time
}

// due 是该不该再读资料：读到过的隔 maxAge 再读（手机、CLI 可能改过）；没读到的（旧常驻进程不认）
// 至多 5 秒试一次，失败后界面重建不会每帧都发请求。
func (p *profileState) due(maxAge time.Duration) bool {
	if p.loading {
		return false
	}
	if !p.loaded {
		maxAge = min(maxAge, 5*time.Second)
	}
	return time.Since(p.at) >= maxAge
}

// loadProfile 在后台读一次资料；pending（还没同步到的讨论）靠设置页定时再读。
func (a *app) loadProfile() {
	if a.profile.loading || a.c == nil {
		return
	}
	a.profile.loading = true
	go func() {
		var r profileData
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		err := a.c.Call(ctx, "profile.get", nil, &r)
		cancel()
		a.update(func() {
			p := &a.profile
			p.loading, p.at = false, time.Now()
			if err != nil {
				return
			}
			p.info, p.loaded = r, true
			if !p.edited {
				p.name = r.Name
			}
		})
	}()
}

// selfAvatar 是自己的头像摘要，没读过或读得久了先去读。
func (a *app) selfAvatar() string {
	if a.profile.due(30 * time.Second) {
		a.loadProfile()
	}
	return a.profile.info.Avatar
}

// selfName 是自己的名字（头像没有时画它的首字）。
func (a *app) selfName(d *discDetail) string {
	if a.profile.loaded {
		return a.profile.info.Name
	}
	if d != nil && d.collab != nil {
		return d.collab.SelfName
	}
	return ""
}

// avatarImage 读出头像：读到的一直缓存；还没有的（成员刚同步过来、头像还在取）隔几秒再看一次。
func (a *app) avatarImage(sha string) ui.ImageSource {
	if sha == "" {
		return nil
	}
	if a.avatars == nil {
		a.avatars = map[string]avatarEntry{}
	}
	e, ok := a.avatars[sha]
	if ok && (e.img != nil || time.Since(e.checked) < 3*time.Second) || a.home == "" {
		return e.img
	}
	e = avatarEntry{checked: time.Now()}
	if data, err := collaboration.ReadAvatar(a.home, sha); err == nil {
		if bm, err := ui.DecodeBitmap(data); err == nil {
			e.img = bm
		}
	}
	a.avatars[sha] = e
	return e.img
}

// personAvatar 画一个人的圆形头像；没有头像（或还没取到）画名字的首字。
func (a *app) personAvatar(c *ui.Context, sha, name string, size float32) {
	if img := a.avatarImage(sha); img != nil {
		ui.Image(c, img).Size(size, size).Fit(ui.Cover).Radius(full(size)).Shrink(0)
		return
	}
	if sha != "" {
		c.After(3 * time.Second)
	}
	avatarInitial(c, name, size)
}

func avatarInitial(c *ui.Context, name string, size float32) {
	t := c.Theme()
	ui.Box(c).Size(size, size).Radius(full(size)).Background(colors(t).tint).Center().Shrink(0).Children(func() {
		ui.Text(c, initial(name)).FontSize(fsSmall).FontWeight(fwMedium).TextColor(t.Accent)
	})
}

// profileSettings 是设置页「个人资料」：头像（预览、换、去掉）和名字，保存后各讨论一起更新。
func (a *app) profileSettings(c *ui.Context) {
	t, m := c.Theme(), L(c)
	p := &a.profile
	if p.due(5 * time.Second) {
		a.loadProfile()
	}
	if len(p.info.Pending) > 0 {
		c.After(5 * time.Second)
	}
	settingsCard(c, m.SettingsProfileTitle(), m.SettingsProfileNote(), func() {
		ui.Row(c).Gap(16).AlignItems(ui.Center).Children(func() {
			size := tileLarge()
			name := strings.TrimSpace(p.name)
			switch {
			case p.preview != nil:
				ui.Image(c, p.preview).Size(size, size).Fit(ui.Cover).Radius(full(size)).Shrink(0)
			case p.remove:
				avatarInitial(c, name, size)
			default:
				a.personAvatar(c, p.info.Avatar, name, size)
			}
			ui.Column(c).Gap(8).Grow(1).Shrink(1).MinWidth(0).Children(func() {
				ui.Row(c).Gap(8).AlignItems(ui.Center).Children(func() {
					if pressable(textButton(c.Key("profile-pick"), m.SettingsProfileChoose())).Clicked() && !p.picking {
						p.picking = true
						go a.pickAvatar(m)
					}
					if (p.preview != nil || p.info.Avatar != "" && !p.remove) && pressable(textButton(c.Key("profile-remove"), m.SettingsProfileRemove())).Clicked() {
						p.pick, p.preview, p.remove = "", nil, true
					}
				})
				if p.preview != nil {
					ui.Text(c, m.SettingsProfileCrop()).FontSize(fsSmall).TextColor(t.TextMuted)
				}
			})
		})
		// 不用 settingsRow：窄的时候让输入框缩，标签不折成一字一行
		ui.Row(c).Gap(8).AlignItems(ui.Center).Children(func() {
			ui.Text(c, m.SettingsProfileName()).FontSize(fsBody).Shrink(0)
			ui.Spacer(c)
			before := p.name
			fieldInputSized(c.Key("profile-name"), &p.name, m.SettingsProfileName(), sz(280), sz(120))
			if p.name != before {
				p.edited = true
			}
		})
		name := strings.TrimSpace(p.name)
		dirty := p.loaded && (name != p.info.Name || p.pick != "" || p.remove && p.info.Avatar != "")
		valid := name != "" && len([]rune(name)) <= 60
		ui.Row(c).Gap(8).AlignItems(ui.Center).Children(func() {
			ui.Spacer(c)
			if dirty && valid && !p.saving && pressable(accentButton(c.Key("profile-save"), m.CommonSave())).Clicked() {
				p.saving = true
				go a.saveProfile(name, p.pick, p.remove)
			}
		})
		if p.err != "" {
			ui.Text(c, p.err).FontSize(fsSmall).TextColor(t.Danger)
		}
		for _, r := range p.info.Pending {
			reason := r.Reason
			switch reason {
			case "offline":
				reason = m.SettingsProfileOffline()
			case "unsupported":
				reason = m.SettingsProfileUnsupported()
			default:
				reason = errtext.Message(reason)
			}
			title := r.Title
			if title == "" {
				title = r.Host
			}
			ui.Text(c, m.SettingsProfilePending(title, reason)).FontSize(fsSmall).TextColor(t.TextMuted)
		}
	})
}

// pickAvatar 选一张图做草稿：按常驻进程同样的裁法裁好给预览，保存时才真正换上。
func (a *app) pickAvatar(m locales.Messages) {
	paths, err := mygo.Dialog.Open(mygo.OpenDialogOptions{Parent: a.win, Title: m.SettingsProfilePickTitle(), ButtonLabel: m.SettingsProfileChoose(),
		Filters: []mygo.FileFilter{{Name: m.SettingsProfileImages(), Extensions: []string{"png", "jpg", "jpeg"}}}})
	var preview ui.ImageSource
	var path string
	if err == nil && len(paths) > 0 {
		path = paths[0]
		var data []byte
		if data, err = os.ReadFile(path); err == nil {
			if data, err = collaboration.NormalizeAvatar(data); err == nil {
				preview, err = ui.DecodeBitmap(data)
			}
		}
	}
	a.update(func() {
		p := &a.profile
		p.picking = false
		switch {
		case err != nil:
			p.err = m.SettingsProfileImageError()
		case preview != nil:
			p.pick, p.preview, p.remove, p.err = path, preview, false, ""
		}
	})
}

func (a *app) saveProfile(name, path string, remove bool) {
	params := map[string]any{"name": name, "path": path, "remove": remove}
	var r profileData
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	err := a.c.Call(ctx, "profile.set", params, &r)
	cancel()
	a.update(func() {
		p := &a.profile
		p.saving = false
		if err != nil {
			p.err = errtext.Text(err)
			return
		}
		p.err = ""
		p.info, p.loaded, p.at = r, true, time.Now()
		p.name, p.edited, p.pick, p.preview, p.remove = r.Name, false, "", nil, false
	})
	a.refresh()
}
