package app

import (
	"os"
	"path/filepath"
	"strings"
	"unicode/utf8"

	"github.com/egoist/mygo"
	"github.com/egoist/mygo/ui"

	"posse/internal/remote"
	"posse/locales"
)

func hostIconDir(home string) string { return filepath.Join(home, "host-icons") }

func (a *app) hostIconImage(name string) ui.ImageSource {
	if name == "" {
		return nil
	}
	owner := a.windowOwner()
	if img, ok := owner.hostIconCache[name]; ok {
		return img
	}
	if owner.hostIconCache == nil {
		owner.hostIconCache = map[string]ui.ImageSource{}
	}
	var img ui.ImageSource
	if data, err := os.ReadFile(filepath.Join(hostIconDir(owner.home), name)); err == nil {
		if bm, err := ui.DecodeBitmap(data); err == nil {
			img = bm
		}
	}
	owner.hostIconCache[name] = img
	return img
}

func (a *app) dropHostIconFile(name, keepSaved string) {
	if name == "" || name == keepSaved {
		return
	}
	owner := a.windowOwner()
	_ = os.Remove(filepath.Join(hostIconDir(owner.home), name))
	delete(owner.hostIconCache, name)
}

func (a *app) removeHostIconFile(name string) {
	if name == "" {
		return
	}
	owner := a.windowOwner()
	_ = os.Remove(filepath.Join(hostIconDir(owner.home), name))
	delete(owner.hostIconCache, name)
}

func (a *app) pickHostIcon(m locales.Messages, gen uint64, label string) {
	owner := a.windowOwner()
	if label == "" {
		label = m.HostDialogAddTitle()
	}
	paths, err := mygo.Dialog.Open(mygo.OpenDialogOptions{
		Parent:      owner.win,
		Title:       m.ProjectIconPickTitle(label),
		ButtonLabel: m.ProjectIconPickButton(),
		Filters:     []mygo.FileFilter{{Name: m.ProjectIconImages(), Extensions: []string{"png", "jpg", "jpeg", "gif", "webp", "svg"}}},
	})
	var name string
	if err == nil && len(paths) > 0 {
		prefix := "host"
		if id := owner.hostDlg.editing.ID; id != "" {
			prefix = id
		}
		name, err = importIconPNG(hostIconDir(owner.home), prefix, paths[0])
	}
	owner.update(func() {
		d := &owner.hostDlg
		if d.gen != gen {
			if name != "" {
				_ = os.Remove(filepath.Join(hostIconDir(owner.home), name))
			}
			return
		}
		d.pickingIcon = false
		switch {
		case err != nil:
			d.err = iconErrorText(m, err)
		case name != "" && d.open:
			owner.dropHostIconFile(d.iconImage, d.editing.IconImage)
			d.iconImage, d.icon = name, ""
			d.err = ""
		case name != "":
			_ = os.Remove(filepath.Join(hostIconDir(owner.home), name))
		}
	})
}

// hostTileGlyph 画左侧导航栏及编辑预览里的远端主机磁贴主体：
// 支持自定义文字/Emoji、自定义图片，未设置时画精致缩放的缩略字母。
func (a *app) hostTileGlyph(c *ui.Context, h remote.Host, monogram string, fg ui.Color) {
	if txt := strings.TrimSpace(h.Icon); txt != "" {
		size := navigationIcon()
		maxW := navigationBox() - 8
		if w, _ := c.MeasureText(0, ui.Span{Text: txt, Size: size, Weight: fwMedium}); w > maxW && w > 0 {
			size *= maxW / w
		}
		ui.Text(c, txt).FontSize(size).FontWeight(fwMedium).TextColor(fg).SingleLine()
		return
	}
	if h.IconImage != "" {
		if img := a.hostIconImage(h.IconImage); img != nil {
			ui.Image(c, img).Size(navigationIcon(), navigationIcon()).Fit(ui.Contain).Radius(rSmall)
			return
		}
	}
	size := fsCode
	if utf8.RuneCountInString(monogram) <= 1 {
		size = fsBody
	}
	maxW := navigationBox() - 12
	if w, _ := c.MeasureText(0, ui.Span{Text: monogram, Size: size, Weight: fwBold}); w > maxW && w > 0 {
		size *= maxW / w
	}
	ui.Text(c, monogram).FontSize(size).FontWeight(fwBold).TextColor(fg).SingleLine()
}

// hostMiniGlyph 画侧栏标题行主机标签里的小图标，与左侧导航栏自定义图标保持一致。
func (a *app) hostMiniGlyph(c *ui.Context, h remote.Host) {
	t := c.Theme()
	if txt := strings.TrimSpace(h.Icon); txt != "" {
		size := fsSmall
		if w, _ := c.MeasureText(0, ui.Span{Text: txt, Size: size}); w > fsSmall && w > 0 {
			size *= fsSmall / w
		}
		ui.Box(c).Size(fsSmall, fsSmall).Center().Shrink(0).Children(func() {
			ui.Text(c, txt).FontSize(size).TextColor(t.Accent).SingleLine()
		})
		return
	}
	if h.IconImage != "" {
		if img := a.hostIconImage(h.IconImage); img != nil {
			ui.Image(c, img).Size(fsSmall, fsSmall).Fit(ui.Contain).Radius(rSmall).Shrink(0)
			return
		}
	}
	ui.Icon(c, iconHost).FontSize(fsSmall).TextColor(t.Accent).Shrink(0)
}
