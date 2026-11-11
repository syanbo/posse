package app

import (
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/egoist/mygo"
	"github.com/egoist/mygo/ui"
	"posse/internal/errtext"
	"posse/internal/media"
	"posse/locales"
)

// openLink 处理终端里点击的链接。单击弹出链接卡片，Cmd+点击直接打开，Shift+Cmd+点击交给系统；设置里可以
// 让单击和 Cmd+点击反过来（LinkClickOpens），也可以让直接打开和交给系统的按法互换（LinkSwapSystem），见 posseKeys。直接打开：网址在网页标签里打开（不是 http、https 的交给系统），文件在文件标签里
// 打开、跳到 :行（本地网页在网页标签里打开），目录在文件栏中显示（不在当前工作区里的弹卡片）；交给系统：网址用默认浏览器，文件用默认程序，
// 目录在访达中打开。卡片里是全部操作：图片带预览，可以打开、在文件栏或访达中显示、复制路径。
func (a *app) openLink(v *termView, link string, mods ui.Modifiers) bool {
	system := mods&ui.Cmd != 0 && mods&ui.Shift != 0
	direct := !system && (mods&ui.Cmd != 0) != a.state.LinkClickOpens
	if a.state.LinkSwapSystem {
		direct, system = system, direct
	}
	path, local := a.linkPath(v, link)
	if !local {
		switch {
		case system:
			return false
		case !direct:
			a.linkCard = &linkCard{url: link, ws: a.termWorkspace(v), term: v.id}
			return true
		case isWebURL(link):
			if w := a.termWorkspace(v); w != nil {
				a.openWeb(w, link)
				return true
			}
		}
		return false
	}
	// 悬停时已经挡掉了不存在的文件；这里是点下去前刚好被删的情况
	if path == "" {
		return true
	}
	card := a.newLinkCard(path, linkLine(link))
	card.ws, card.term = a.termWorkspace(v), v.id
	switch {
	case system:
		a.openPathExternal(card)
	case !direct:
		a.showLinkCard(card)
	case !card.isDir && isHTML(card.path) && a.openLinkWeb(card):
	case !card.isDir && a.openLinkFile(card):
	case card.isDir && a.revealLinkDir(card):
	default:
		// 没有工作区能放这个文件、目录不在文件栏的树上：弹卡片，不让这一下没反应
		a.showLinkCard(card)
	}
	return true
}

// posseKeys 是在 Posse 里直接打开链接的按法，systemKeys 是交给系统的，卡片里写在对应的操作右边。
func (a *app) posseKeys(m locales.Messages) string {
	direct, system := a.linkKeys(m)
	if a.state.LinkSwapSystem {
		return system
	}
	return direct
}

func (a *app) systemKeys(m locales.Messages) string {
	direct, system := a.linkKeys(m)
	if a.state.LinkSwapSystem {
		return direct
	}
	return system
}

// linkKeys 是不互换时直接打开和交给系统的按法。
func (a *app) linkKeys(m locales.Messages) (direct, system string) {
	if a.state.LinkClickOpens {
		return m.LinksClick(), m.LinksShiftCmdClick()
	}
	return m.LinksCmdClick(), m.LinksShiftCmdClick()
}

// linkLine 是打印出来的路径末尾 :行 或 :行:列 里的行号，没有是 0。file:// 链接不带行号，文件名里的 :数字 是名字的一部分。
func linkLine(link string) int {
	if len(link) >= len("file://") && strings.EqualFold(link[:len("file://")], "file://") {
		return 0
	}
	m := linePos.FindString(strings.TrimSpace(link))
	if m == "" {
		return 0
	}
	n, _ := strconv.Atoi(strings.Split(m, ":")[1])
	return n
}

// openLinkFile 在链接所在终端的工作区的文件标签里打开链接指向的文件，文件属于别的工作区也一样：
// 就在正看着的会话旁边预览，不切走。代价是文件栏的树、⌘P 和跳到定义按这个工作区算，
// 别的工作区（比如 worktree）里的文件跳到定义会跳到这里的同名文件。工作区不在了返回 false。
func (a *app) openLinkFile(card *linkCard) bool {
	w := a.cardWorkspace(card)
	if w == nil {
		return false
	}
	a.openFile(w, card.path, card.line, [2]int{})
	return true
}

// openLinkWeb 在链接所在终端的工作区的网页标签里打开本地网页（agent 生成的报告这类），工作区不在了返回 false。
func (a *app) openLinkWeb(card *linkCard) bool {
	w := a.cardWorkspace(card)
	if w == nil {
		return false
	}
	a.openWeb(w, fileURL(card.path))
	return true
}

// cardWorkspace 是卡片所在终端的工作区，按 ID 现查：卡片开着时轮询可能已经换掉了工作区列表，
// 卡片里记的那份可能过时（删掉了、换了目录）。
func (a *app) cardWorkspace(card *linkCard) *workspace {
	if card.ws == nil {
		return nil
	}
	return a.workspace(card.ws.ID)
}

// revealLinkDir 在文件栏中显示链接指向的目录；文件栏是终端所在工作区的树，不在里面的返回 false。
func (a *app) revealLinkDir(card *linkCard) bool {
	w := a.cardWorkspace(card)
	if w == nil || !inDir(card.path, w.Path) {
		return false
	}
	a.revealInFiles(w, card.path)
	return true
}

// openPathExternal 交给系统：目录在访达中打开，文件用默认程序打开。
func (a *app) openPathExternal(card *linkCard) {
	if err := mygo.Shell.OpenPath(card.path); err != nil {
		a.err = errtext.Text(err)
	}
}

// termWorkspace 是终端所在的工作区，找不到时是选中的工作区。
func (a *app) termWorkspace(v *termView) *workspace {
	if w, t := a.findTerminal(v.id); t != nil && w != nil {
		return w
	}
	return a.workspace(a.selected)
}

// linkPath 是链接指向的本地文件：file:// 链接取路径，网址返回 ok=false，路径按终端目录解析，不存在返回空。
func (a *app) linkPath(v *termView, link string) (path string, local bool) {
	if m := imageTagRe.FindStringSubmatch(link); m != nil {
		return a.pastedImage(v, m[1]), true
	}
	if len(link) >= len("file://") && strings.EqualFold(link[:len("file://")], "file://") {
		return fileURLPath(link), true
	}
	if strings.Contains(link, "://") || strings.HasPrefix(link, "mailto:") {
		return "", false
	}
	if p := resolveLinkPath(link, a.linkBase(v)); p != "" {
		return p, true
	}
	return a.viewedImage(v, link), true
}

// imageTagRe 是 agent 输入框里粘贴的图片占位（终端插件认出的 [Image #N]）。发出去以后 Claude 把它输出成 OSC 8 链接，那时走 file://。
var imageTagRe = regexp.MustCompile(`^\[Image #(\d+)\]$`)

// pastedImage 是终端里 Claude 会话粘贴的第 n 张图：Claude 粘贴时就存进会话的图片目录（N.png 这样），还没发送也在。
// 别家的输入框里的图不知道存在哪，返回空，占位就不当链接。
func (a *app) pastedImage(v *termView, n string) string {
	_, ti := a.findTerminal(v.id)
	if ti == nil || ti.Agent != "claude" || ti.AgentState == nil {
		return ""
	}
	dir := media.ClaudeImageDir(ti.AgentState.SessionID, ti.AgentState.TranscriptPath)
	if dir == "" {
		return ""
	}
	for _, ext := range []string{".png", ".jpg", ".jpeg"} {
		p := filepath.Join(dir, n+ext)
		if fi, err := os.Stat(p); err == nil && !fi.IsDir() {
			return p
		}
	}
	return ""
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
	// 按终端分开记：相对路径按各自的目录找，[Image #N] 是各自会话的图
	key := v.id + "\x00" + link
	if c, ok := a.linkChecks[key]; ok && time.Since(c.at) < linkCheckTTL {
		return c.ok
	}
	path, local := a.linkPath(v, link)
	ok := !local || path != ""
	if a.linkChecks == nil || len(a.linkChecks) > 256 {
		a.linkChecks = map[string]linkCheck{}
	}
	a.linkChecks[key] = linkCheck{ok: ok, at: time.Now()}
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

// linkCard 是点击网址、文件链接或路径后弹出的卡片，出现在点击的位置。
type linkCard struct {
	url     string     // 网址的卡片只有它，path 为空
	ws      *workspace // 链接所在终端的工作区：网址在它的网页标签里打开，不在任何工作区里的文件在它的文件标签里打开
	path    string
	line    int // 路径带的 :行，在文件标签中打开时跳过去
	isDir   bool
	x, y    float32 // 相对终端区域；placed 前还没取
	placed  bool
	preview *ui.Bitmap // 图片的预览
	// 图片预览的高度上限（按卡片弹出那一边剩下的空间算）、这一帧画出的高度，
	// 和卡片里预览以外部分的高（上一帧量出来的）
	previewMaxH, previewH, chromeH float32
	term                           string // 点的是哪个终端里的链接：分栏时卡片只画在那一栏
}

const (
	linkPreviewWidth = 640  // 2 倍屏下 320pt
	linkPreviewMaxPx = 1400 // 高的上限，显示时还会按终端高度再压
)

func isImagePath(p string) bool {
	switch strings.ToLower(filepath.Ext(p)) {
	case ".png", ".jpg", ".jpeg", ".gif", ".webp":
		return true
	}
	return false
}

func (a *app) newLinkCard(path string, line int) *linkCard {
	fi, _ := os.Stat(path)
	return &linkCard{path: path, line: line, isDir: fi != nil && fi.IsDir()}
}

func (a *app) showLinkCard(card *linkCard) {
	path := card.path
	a.linkCard = card
	if !card.isDir && isImagePath(path) {
		go func() {
			img, err := media.FittedImage(path, linkPreviewWidth, linkPreviewMaxPx)
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
func (a *app) linkCardView(c *ui.Context, area ui.Element, termID string) {
	card := a.linkCard
	if card == nil || card.term != "" && card.term != termID {
		return
	}
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
	// 往空间大的一边弹：往上时卡片的下边贴着点击处上方，卡片多高都对得上；往下时顶边贴着点击处下方。
	// 图片预览压到那一边放得下整张卡片，下面的按钮不被挤出窗口
	const upGap, downGap, edge = 10, 14, 8
	up, down := card.y-upGap-edge, b.H-card.y-downGap-edge
	if b.H > 0 && up > down {
		el.Bottom(max(edge, b.H-card.y+upGap))
	} else {
		el.Top(card.y + downGap)
	}
	if h := el.Bounds().H; h > 0 {
		card.chromeH = h - card.previewH
	}
	card.previewMaxH = 0
	if b.H > 0 {
		chrome := card.chromeH
		if chrome <= 0 {
			chrome = 220 // 还没量过：路径两行加四项操作的高
		}
		card.previewMaxH = max(80, max(up, down)-chrome)
	}
	card.previewH = 0
	if card.url != "" {
		el.Children(func() { a.urlCardItems(c, card) })
	} else {
		el.Children(func() { a.pathCardItems(c, card, width) })
	}
	if !fresh && (el.PressedOutside() || el.Shortcut(0, ui.KeyEscape)) {
		a.linkCard = nil
	}
}

// urlCardItems 是网址卡片的内容：网址和复制按钮，在网页标签里打开（直接打开的按法做这一步）、
// 用浏览器打开（交给系统的按法）。
func (a *app) urlCardItems(c *ui.Context, card *linkCard) {
	a.linkCardHead(c, card.url, card.url, L(c).LinksCopyLink())
	hline(c).Margin(4, 6)
	// 按 ID 现查：工作区删了就没有「在 Posse 里打开」
	w := a.cardWorkspace(card)
	web := isWebURL(card.url) && w != nil
	if web && linkCardItem(c, iconGlobe, L(c).LinksOpenInPosse(), a.posseKeys(L(c))) {
		a.openWeb(w, card.url)
		a.linkCard = nil
	}
	// 不是 http、https 的网址两种按法都交给系统，写在 Posse 里打开的那个
	label, keys := L(c).CommonOpen(), a.posseKeys(L(c))
	if web {
		label, keys = L(c).LinksOpenInBrowser(), a.systemKeys(L(c))
	}
	if linkCardItem(c, iconOpen, label, keys) {
		c.OpenURL(card.url)
		a.linkCard = nil
	}
}

// linkCardHead 是链接卡片顶上的一行：显示 text（最多两行），后面的复制按钮复制 value。
func (a *app) linkCardHead(c *ui.Context, text, value, label string) {
	t := c.Theme()
	ui.Row(c).Padding(2, 2, 0, 8).Gap(4).AlignItems(ui.Center).Children(func() {
		ui.Text(c, text).FontSize(fsSmall).Font(monoFont).TextColor(t.TextMuted).MaxLines(2).Grow(1).Shrink(1).Selectable()
		if iconButton(c.Key("copy"), iconCopy, label).Clicked() {
			c.WriteClipboard(value)
			a.linkCard = nil
		}
	})
}

// pathCardItems 是文件链接和路径卡片的内容。
func (a *app) pathCardItems(c *ui.Context, card *linkCard, width float32) {
	a.linkCardHead(c, shortPath(card.path), card.path, L(c).CommonCopyPath())
	if card.preview != nil {
		// 高图按 linkCardView 算的上限压低，竖图居中
		iw, ih := card.preview.Size()
		maxH := card.previewMaxH
		if maxH <= 0 {
			maxH = 240
		}
		w, h := fitSize(float32(iw), float32(ih), width-12, maxH)
		card.previewH = h + 2 + 4 + 2 // 上下外边距和列的间隔
		ui.Row(c).Justify(ui.Center).Margin(2, 0, 4, 0).Children(func() {
			ui.Image(c, card.preview).Width(w).Radius(rRow)
		})
	}
	hline(c).Margin(4, 6)
	// 最常用的在最上面：在 Posse 里打开。网页直接打开的是渲染出来的页面，源码在文件标签里看
	fileKeys := a.posseKeys(L(c))
	if !card.isDir && isHTML(card.path) && a.cardWorkspace(card) != nil {
		if linkCardItem(c, iconGlobe, L(c).LinksOpenInPosse(), fileKeys) {
			a.openLinkWeb(card)
			a.linkCard = nil
		}
		fileKeys = ""
	}
	if !card.isDir && linkCardItem(c, iconFile, L(c).LinksOpenInFileTab(), fileKeys) {
		a.openLinkFile(card)
		a.linkCard = nil
	}
	// 文件栏显示的是当前工作区的树，只有它里面的路径能选中
	revealKeys := ""
	if card.isDir {
		revealKeys = a.posseKeys(L(c))
	}
	if w := a.workspace(a.selected); w != nil && inDir(card.path, w.Path) && linkCardItem(c, iconPanelRight, L(c).CommonRevealInFilesPanel(), revealKeys) {
		a.revealInFiles(w, card.path)
		a.linkCard = nil
	}
	openIcon, openLabel := iconOpen, L(c).CommonOpen()
	if card.isDir {
		// 在访达里的动作都用文件夹图标
		openIcon, openLabel = iconFolder, L(c).CommonOpenInFinder()
	} else if isImagePath(card.path) {
		openLabel = L(c).LinksOpenInPreview()
	}
	if linkCardItem(c, openIcon, openLabel, a.systemKeys(L(c))) {
		a.openPathExternal(card)
		a.linkCard = nil
	}
	if !card.isDir && linkCardItem(c, iconFolder, L(c).CommonRevealInFinder(), "") {
		mygo.Shell.ShowItemInFolder(card.path)
		a.linkCard = nil
	}
}

// linkCardItem 是链接卡片里的一行操作，keys 写在右边，返回这一帧是否点了它。
func linkCardItem(c *ui.Context, icon *ui.SVG, label, keys string) bool {
	t := c.Theme()
	row := ui.Row(c.Key(label)).Height(sz(30)).Padding(0, 8).Gap(8).Radius(rRow).AlignItems(ui.Center).Role(ui.RoleButton).Label(label)
	bg := ui.Transparent
	if row.Hovered() {
		bg = colors(t).wash
	}
	row.Background(row.AnimateColor("bg", bg, hover))
	row.Children(func() {
		ui.Icon(c, icon).FontSize(fsBody).TextColor(t.TextMuted)
		ui.Text(c, label).FontSize(fsBody).Grow(1)
		if keys != "" {
			ui.Text(c, keys).FontSize(fsSmall).TextColor(t.TextMuted).Shrink(0)
		}
	})
	return row.Clicked()
}

// 鼠标停在终端里的图片链接上（Claude 的 [Image #N]、打印出来的图片路径）时，在指针旁边浮出预览。
const (
	hoverPreviewMinWidth = 360 // 预览宽度随终端变：终端宽的一半，夹在这两个数之间
	hoverPreviewMaxWidth = 800
	hoverPreviewPx       = 2 * hoverPreviewMaxWidth // 2 倍屏下最宽时的像素
	hoverPreviewMaxPx    = 2000                     // 高的上限：2 倍屏下 1000pt，长截图不至于解出几十 MB
	hoverPreviewKeep     = 2                        // 最多留几张解码好的预览，换终端时全部放掉
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
	a.hoverImage, a.hoverTerm = path, v.id
	if a.previews[path] != nil || a.previewLoading[path] {
		return
	}
	if a.previewLoading == nil {
		a.previewLoading = map[string]bool{}
	}
	a.previewLoading[path] = true
	go func() {
		img, err := media.FittedImage(path, hoverPreviewPx, hoverPreviewMaxPx)
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

// hoverPreviewView 画悬停预览，位置和大小见 placeHoverPreview。弹出链接卡片时让位给卡片。
func (a *app) hoverPreviewView(c *ui.Context, area ui.Element, termID string) {
	bm := a.previews[a.hoverImage]
	if a.hoverImage == "" || bm == nil || a.linkCard != nil || a.hoverTerm != termID {
		return
	}
	// 只在悬停图片时取指针位置：一直取的话指针每动一下都要重画
	x, y, ok := area.PointerPosition()
	if !ok {
		return
	}
	b := area.Bounds()
	iw, ih := bm.Size()
	r := placeHoverPreview(b.W, b.H, x, y, float32(iw), float32(ih))
	floating(c, ui.Box(c).Absolute().Left(r.X).Top(r.Y).Padding(hoverPreviewPad)).Children(func() {
		ui.Image(c, bm).Width(r.W - 2*hoverPreviewPad).Radius(rRow)
	})
}

const hoverPreviewPad = 6

// placeHoverPreview 算悬停预览外框的位置和大小（相对终端区域 bw×bh，指针在 x,y，图片 iw×ih）。
// 指针下方、上方、右边、左边四处各按剩下的空间等比缩放，取图最大的一处；一样大时按这个顺序，横图照旧在上下。
// 竖图放在左右能用满终端的高度。预览不能盖住指针：盖住了指针就不在链接上，预览会来回闪。
// 最宽是终端宽的一半（360 到 800）；小图最多放大到原来的 360 宽，不再往上放大（按 2 倍屏算原图有多大）。
func placeHoverPreview(bw, bh, x, y, iw, ih float32) ui.Rect {
	const edge, below, above, side = 8, 20, 14, 12
	pad := float32(2 * hoverPreviewPad)
	maxW := min(max(bw/2, hoverPreviewMinWidth), hoverPreviewMaxWidth) - pad
	maxW = min(maxW, max(hoverPreviewMinWidth-pad, iw/2))
	if bw <= 0 || bh <= 0 {
		// 还没排版出尺寸：照旧放在指针右下
		w, h := fitSize(iw, ih, maxW, maxW*4)
		return ui.Rect{X: x + side, Y: y + below, W: w + pad, H: h + pad}
	}
	clamp := func(v, size, limit float32) float32 { return max(edge, min(v, limit-size-edge)) }
	fullW, fullH := bw-2*edge-pad, bh-2*edge-pad
	var best ui.Rect
	var bestArea float32
	try := func(availW, availH float32, at func(w, h float32) (float32, float32)) {
		w, h := fitSize(iw, ih, availW, availH)
		if w <= 0 || h <= 0 || w*h <= bestArea {
			return
		}
		bestArea = w * h
		w, h = w+pad, h+pad
		px, py := at(w, h)
		best = ui.Rect{X: px, Y: py, W: w, H: h}
	}
	try(min(maxW, fullW), bh-(y+below)-edge-pad, func(w, h float32) (float32, float32) { return clamp(x+side, w, bw), y + below })
	try(min(maxW, fullW), y-above-edge-pad, func(w, h float32) (float32, float32) { return clamp(x+side, w, bw), y - above - h })
	try(min(maxW, bw-(x+side)-edge-pad), fullH, func(w, h float32) (float32, float32) { return x + side, clamp(y-h/2, h, bh) })
	try(min(maxW, x-side-edge-pad), fullH, func(w, h float32) (float32, float32) { return x - side - w, clamp(y-h/2, h, bh) })
	if bestArea == 0 {
		// 终端小到哪边都放不下：贴着左上角，至少看得见
		w, h := fitSize(iw, ih, max(fullW, 40), max(fullH, 40))
		return ui.Rect{X: edge, Y: edge, W: w + pad, H: h + pad}
	}
	return best
}

// fitSize 把 w×h 等比缩放到正好放进 maxW×maxH（小图也放大到放满）。
func fitSize(w, h, maxW, maxH float32) (float32, float32) {
	if w <= 0 || h <= 0 || maxW <= 0 || maxH <= 0 {
		return 0, 0
	}
	s := min(maxW/w, maxH/h)
	return w * s, h * s
}
