package app

import (
	"crypto/rand"
	"encoding/hex"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/egoist/mygo"
	"github.com/egoist/mygo/ui"

	"posse/internal/remote"
	"posse/locales"
)

const maxHostIconBytes = 4 << 20

type hostIconEntry struct {
	img     ui.ImageSource
	modTime time.Time
	size    int64
}

func hostIconDir(home string) string { return filepath.Join(home, "host-icons") }

func isSafeHostIconName(name string) bool {
	return name != "" && filepath.Base(name) == name && name != "." && name != ".." && !strings.ContainsAny(name, "/\\\x00\r\n")
}

// importHostIcon 使用与主机 ID 无关的随机文件名把选中的图片缩放存到 $POSSE_HOME/host-icons/，避免配置 ID 拼出越界路径。
func importHostIcon(home, src string) (string, error) {
	var rnd [8]byte
	if _, err := rand.Read(rnd[:]); err != nil {
		return "", err
	}
	return importIconPNG(hostIconDir(home), "host-"+hex.EncodeToString(rnd[:]), src)
}

func (a *app) hostIconImage(name string) ui.ImageSource {
	if !isSafeHostIconName(name) {
		return nil
	}
	owner := a.windowOwner()
	if owner.home == "" {
		return nil
	}
	root, err := os.OpenRoot(hostIconDir(owner.home))
	if err != nil {
		return nil
	}
	defer root.Close()
	info, err := root.Lstat(name)
	if err != nil || !info.Mode().IsRegular() || info.Size() <= 0 || info.Size() > maxHostIconBytes {
		delete(owner.hostIconCache, name)
		return nil
	}
	if cached, ok := owner.hostIconCache[name]; ok && cached.modTime.Equal(info.ModTime()) && cached.size == info.Size() {
		return cached.img
	}
	f, err := root.Open(name)
	if err != nil {
		return nil
	}
	data, err := io.ReadAll(io.LimitReader(f, maxHostIconBytes+1))
	f.Close()
	if err != nil || len(data) == 0 || len(data) > maxHostIconBytes {
		return nil
	}
	bm, err := ui.DecodeBitmap(data)
	if err != nil {
		return nil
	}
	if owner.hostIconCache == nil {
		owner.hostIconCache = map[string]hostIconEntry{}
	}
	owner.hostIconCache[name] = hostIconEntry{img: bm, modTime: info.ModTime(), size: info.Size()}
	return bm
}

func (a *app) hostIconReferenced(name string) bool {
	owner := a.windowOwner()
	for _, h := range owner.hostMenu.hosts {
		if h.IconImage == name {
			return true
		}
	}
	return false
}

func removeHostIconInDir(home, name string) {
	if home == "" || !isSafeHostIconName(name) {
		return
	}
	root, err := os.OpenRoot(hostIconDir(home))
	if err != nil {
		return
	}
	defer root.Close()
	_ = root.Remove(name)
}

func (a *app) dropHostIconFile(name, keepSaved string) {
	if !isSafeHostIconName(name) || name == keepSaved || a.hostIconReferenced(name) {
		return
	}
	owner := a.windowOwner()
	removeHostIconInDir(owner.home, name)
	delete(owner.hostIconCache, name)
}

func (a *app) removeHostIconFile(name string) {
	if !isSafeHostIconName(name) || a.hostIconReferenced(name) {
		return
	}
	owner := a.windowOwner()
	removeHostIconInDir(owner.home, name)
	delete(owner.hostIconCache, name)
}

// pickHostIcon 只使用调用前捕获的不可变 home/gen/label，不在后台协程中读取 owner.hostDlg。
func (a *app) pickHostIcon(m locales.Messages, home string, gen uint64, label string) {
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
		name, err = importHostIcon(home, paths[0])
	}
	owner.update(func() {
		d := &owner.hostDlg
		if d.gen != gen {
			if name != "" {
				removeHostIconInDir(home, name)
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
			removeHostIconInDir(home, name)
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
