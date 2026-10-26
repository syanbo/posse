package app

import (
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/egoist/mygo"
	"github.com/egoist/mygo/ui"
	"posse/internal/media"
)

// openLink 处理终端里 Cmd+点击的链接。网址（http、mailto 等）返回 false，交给系统用默认浏览器打开；
// 文件链接（如 Claude 的 [Image #N] 指向的 file://）和路径弹出链接卡片：图片带预览，可以打开、在文件栏或访达中显示、复制路径。
func (a *app) openLink(v *termView, link string) bool {
	path, local := a.linkPath(v, link)
	if !local {
		return false
	}
	// 悬停时已经挡掉了不存在的文件；这里是点下去前刚好被删的情况
	if path == "" {
		return true
	}
	a.showLinkCard(path)
	return true
}

// linkPath 是链接指向的本地文件：file:// 链接取路径，网址返回 ok=false，路径按终端目录解析，不存在返回空。
func (a *app) linkPath(v *termView, link string) (path string, local bool) {
	if len(link) >= len("file://") && strings.EqualFold(link[:len("file://")], "file://") {
		return fileURLPath(link), true
	}
	if strings.Contains(link, "://") || strings.HasPrefix(link, "mailto:") {
		return "", false
	}
	return resolveLinkPath(link, a.linkBase(v)), true
}

// fileURLPath 是 file:// 链接指向的、存在的文件，不存在返回空。程序在 OSC 8 里常直接拼绝对路径、不做百分号编码：
// 文件名里的 # 和 ? 会被 url.Parse 当成 fragment、query 截掉，单独的 % 让它直接报错。
// 先按解码后的路径找，找不到（或解析失败）再按原文取一次。
func fileURLPath(link string) string {
	if u, err := url.Parse(link); err == nil {
		if _, err := os.Stat(u.Path); err == nil {
			return u.Path
		}
	}
	raw := link[len("file://"):]
	if i := strings.IndexByte(raw, '/'); i >= 0 {
		raw = raw[i:] // 去掉主机名（file://localhost/…）
		if _, err := os.Stat(raw); err == nil {
			return raw
		}
	}
	return ""
}

// linkUsable 告诉终端一个链接能不能打开：网址都能，文件链接和路径要文件还在。
// 不能打开的不显示下划线，点了也没反应。悬停时每帧都会问，结果缓存一会儿。
func (a *app) linkUsable(v *termView, link string) bool {
	if c, ok := a.linkChecks[link]; ok && time.Since(c.at) < linkCheckTTL {
		return c.ok
	}
	path, local := a.linkPath(v, link)
	ok := !local || path != ""
	if a.linkChecks == nil || len(a.linkChecks) > 256 {
		a.linkChecks = map[string]linkCheck{}
	}
	a.linkChecks[link] = linkCheck{ok: ok, at: time.Now()}
	return ok
}

type linkCheck struct {
	ok bool
	at time.Time
}

const linkCheckTTL = 2 * time.Second

// linkBase 是相对路径的起点：shell 报告的当前目录（OSC 7），没有就用终端启动时的目录，再没有用工作区目录。
// 终端的 LinkUsable 回调持着终端的锁调用它，所以只用每帧取好的 v.dir，不碰 v.term。
func (a *app) linkBase(v *termView) string {
	if v.dir != "" {
		return v.dir
	}
	w, t := a.findTerminal(v.id)
	switch {
	case t == nil:
		return ""
	case t.Dir != "":
		return t.Dir
	}
	return w.Path
}

// linePos 是路径末尾的 :行 或 :行:列。
var linePos = regexp.MustCompile(`(:\d+){1,2}$`)

// resolveLinkPath 把终端里打印的路径变成存在的绝对路径：去掉 :行:列，展开 ~，相对路径接在 base 后面。
// 不存在返回空。
func resolveLinkPath(link, base string) string {
	p := linePos.ReplaceAllString(strings.TrimSpace(link), "")
	if p == "" {
		return ""
	}
	if p == "~" || strings.HasPrefix(p, "~/") {
		if homeDir == "" {
			return ""
		}
		p = filepath.Join(homeDir, p[1:])
	} else if !filepath.IsAbs(p) {
		if base == "" {
			return ""
		}
		p = filepath.Join(base, p)
	}
	p = filepath.Clean(p)
	if _, err := os.Stat(p); err != nil {
		return ""
	}
	return p
}

// linkCard 是 Cmd+点击文件链接或路径后弹出的卡片，出现在点击的位置。
type linkCard struct {
	path    string
	isDir   bool
	x, y    float32 // 相对终端区域；placed 前还没取
	placed  bool
	preview *ui.Bitmap // 图片的预览
}

const linkPreviewWidth = 640 // 2 倍屏下 320pt

func isImagePath(p string) bool {
	switch strings.ToLower(filepath.Ext(p)) {
	case ".png", ".jpg", ".jpeg", ".gif", ".webp":
		return true
	}
	return false
}

func (a *app) showLinkCard(path string) {
	fi, _ := os.Stat(path)
	card := &linkCard{path: path, isDir: fi != nil && fi.IsDir()}
	a.linkCard = card
	if !card.isDir && isImagePath(path) {
		go func() {
			img, err := media.ScaledImage(path, linkPreviewWidth)
			if err != nil {
				return
			}
			a.win.Update(func() {
				if a.linkCard == card {
					card.preview = ui.NewBitmap(img)
				}
			})
		}()
	}
}

// linkCardView 画链接卡片。area 是终端区域，卡片按它定位；点卡片外面或按 Esc 关闭。
func (a *app) linkCardView(c *ui.Context, area ui.Element) {
	card := a.linkCard
	if card == nil {
		return
	}
	t := c.Theme()
	p := colors(t)
	const width = 340
	// 弹出卡片的那一下点击也算"在卡片外面按下"，弹出的这一帧不能据此关掉它
	fresh := !card.placed
	if !card.placed {
		// 只在弹出时取一次指针位置：一直取的话指针每动一下都要重画
		if x, y, ok := area.PointerPosition(); ok {
			card.x, card.y = x, y
		}
		card.placed = true
	}
	b := area.Bounds()
	x := card.x + 4
	if b.W > 0 {
		x = max(8, min(x, b.W-width-8))
	}
	el := floating(c, ui.Column(c).Absolute().Left(x).Width(width).Padding(6).Gap(2))
	// 点在下半部分时往上弹：卡片的下边贴着点击处上方，卡片多高都对得上；否则顶边贴着点击处下方
	if b.H > 0 && card.y > b.H*0.55 {
		el.Bottom(max(8, b.H-card.y+10))
	} else {
		el.Top(card.y + 14)
	}
	el.Children(func() {
		ui.Text(c, shortPath(card.path)).FontSize(fsSmall).Font(monoFont).TextColor(t.TextMuted).MaxLines(2).Padding(6, 8, 4, 8).Selectable()
		if card.preview != nil {
			ui.Image(c, card.preview).Width(width-12).Radius(8).Margin(2, 0, 4, 0)
		}
		ui.Box(c).Height(1).Margin(2, 6).Background(p.line)
		// 最常用的在最上面：在 Posse 里打开
		if !card.isDir && linkCardItem(c, iconFile, "在文件标签中打开") {
			w := a.workspaceOf(card.path)
			if w == nil {
				w = a.workspace(a.selected)
			}
			if w != nil {
				a.openFile(w, card.path, 0, [2]int{})
			}
			a.linkCard = nil
		}
		// 文件栏显示的是当前工作区的树，只有它里面的路径能选中
		if w := a.workspace(a.selected); w != nil && inDir(card.path, w.Path) && linkCardItem(c, iconPanelRight, "在文件栏中显示") {
			a.revealInFiles(w, card.path)
			a.linkCard = nil
		}
		openIcon, openLabel := iconOpen, "打开"
		if card.isDir {
			// 在访达里的动作都用文件夹图标
			openIcon, openLabel = iconFolder, "在访达中打开"
		} else if isImagePath(card.path) {
			openLabel = "用预览打开"
		}
		if linkCardItem(c, openIcon, openLabel) {
			if err := mygo.Shell.OpenPath(card.path); err != nil {
				a.err = err.Error()
			}
			a.linkCard = nil
		}
		if !card.isDir && linkCardItem(c, iconFolder, "在访达中显示") {
			mygo.Shell.ShowItemInFolder(card.path)
			a.linkCard = nil
		}
		if linkCardItem(c, iconCopy, "复制路径") {
			c.WriteClipboard(card.path)
			a.linkCard = nil
		}
	})
	if !fresh && (el.PressedOutside() || el.Shortcut(0, ui.KeyEscape)) {
		a.linkCard = nil
	}
}

// linkCardItem 是链接卡片里的一行操作，返回这一帧是否点了它。
func linkCardItem(c *ui.Context, icon *ui.SVG, label string) bool {
	t := c.Theme()
	row := ui.Row(c.Key(label)).Height(sz(30)).Padding(0, 8).Gap(10).Radius(7).AlignItems(ui.Center).Role(ui.RoleButton).Label(label)
	bg := ui.Transparent
	if row.Hovered() {
		bg = colors(t).wash
	}
	row.Background(row.AnimateColor("bg", bg, hover))
	row.Children(func() {
		ui.Icon(c, icon).FontSize(sz(14)).TextColor(t.TextMuted)
		ui.Text(c, label).FontSize(fsBody)
	})
	return row.Clicked()
}

// 鼠标停在终端里的图片链接上（Claude 的 [Image #N]、打印出来的图片路径）时，在指针旁边浮出预览。
const (
	hoverPreviewWidth = 360
	hoverPreviewPx    = 720 // 2 倍屏下正好是预览宽度
	hoverPreviewKeep  = 2   // 最多留几张解码好的预览，换终端时全部放掉
)

// onHoverLink 是终端的悬停回调：持着终端的锁调用，只记下状态、在后台解码，不碰终端。
func (a *app) onHoverLink(v *termView, link string) {
	a.hoverImage = ""
	if link == "" {
		return
	}
	path, local := a.linkPath(v, link)
	if !local || path == "" || !isImagePath(path) {
		return
	}
	a.hoverImage = path
	if a.previews[path] != nil || a.previewLoading[path] {
		return
	}
	if a.previewLoading == nil {
		a.previewLoading = map[string]bool{}
	}
	a.previewLoading[path] = true
	go func() {
		img, err := media.ScaledImage(path, hoverPreviewPx)
		a.win.Update(func() {
			delete(a.previewLoading, path)
			if err != nil {
				return
			}
			if a.previews == nil || len(a.previews) >= hoverPreviewKeep {
				a.previews = map[string]*ui.Bitmap{}
			}
			a.previews[path] = ui.NewBitmap(img)
		})
	}()
}

// hoverPreviewView 画悬停预览：指针在终端上半部分时出现在下方，下半部分时出现在上方。弹出链接卡片时让位给卡片。
func (a *app) hoverPreviewView(c *ui.Context, area ui.Element) {
	bm := a.previews[a.hoverImage]
	if a.hoverImage == "" || bm == nil || a.linkCard != nil {
		return
	}
	// 只在悬停图片时取指针位置：一直取的话指针每动一下都要重画
	x, y, ok := area.PointerPosition()
	if !ok {
		return
	}
	b := area.Bounds()
	left := x + 12
	if b.W > 0 {
		left = max(8, min(left, b.W-hoverPreviewWidth-8))
	}
	el := floating(c, ui.Box(c).Absolute().Left(left).Width(hoverPreviewWidth).Padding(6))
	if b.H > 0 && y > b.H*0.5 {
		el.Bottom(max(8, b.H-y+14))
	} else {
		el.Top(y + 20)
	}
	el.Children(func() {
		ui.Image(c, bm).Width(hoverPreviewWidth - 12).Radius(8)
	})
}
