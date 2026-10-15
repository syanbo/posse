package app

import (
	"fmt"
	"image"
	_ "image/jpeg"
	_ "image/png"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/egoist/mygo"
	"github.com/egoist/mygo/ui"
	"golang.org/x/image/draw"
)

const panelImages = "images"

// imagesView 是右侧"图片"面板的状态：当前 Claude 会话里粘贴过的图片（终端里显示成 [Image #N]）。
// 截图解码后一张十几 MB，所以只留缩到面板宽度的缩略图，悬停放大时才解码那一张。
type imagesView struct {
	term    string
	dir     string // 会话的图片目录，找到后不再找
	items   []imageItem
	busy    bool
	scanned time.Time

	hover   int // 悬停的图片序号，0 表示没有
	big     *ui.Bitmap
	bigN    int
	bigBusy bool
}

type imageItem struct {
	n     int
	path  string
	thumb *ui.Bitmap
}

const (
	imagesRefresh = 3 * time.Second
	thumbWidth    = 512  // 2 倍屏下正好是面板宽度
	bigWidth      = 1120 // 悬停放大的宽度，2 倍屏下 560pt
)

// claudeImageDir 找 Claude 会话保存粘贴图片的目录：/private/tmp/claude-<uid>/<项目目录编码>/<会话 ID>/images。
// 项目目录编码和会话文件所在目录同名；拿不到会话文件时按会话 ID 找。
func claudeImageDir(sid, transcript string) string {
	if sid == "" || strings.ContainsAny(sid, `/\*?[`) {
		return ""
	}
	isDir := func(p string) bool {
		fi, err := os.Stat(p)
		return err == nil && fi.IsDir()
	}
	roots := []string{fmt.Sprintf("/private/tmp/claude-%d", os.Getuid()), filepath.Join(os.TempDir(), fmt.Sprintf("claude-%d", os.Getuid()))}
	if transcript != "" {
		for _, root := range roots {
			if p := filepath.Join(root, filepath.Base(filepath.Dir(transcript)), sid, "images"); isDir(p) {
				return p
			}
		}
	}
	for _, root := range roots {
		if m, _ := filepath.Glob(filepath.Join(root, "*", sid, "images")); len(m) > 0 {
			return m[0]
		}
	}
	return ""
}

// listImages 列出目录里以序号命名的图片（1.png、2.jpg…），序号大的（新的）在前。
func listImages(dir string) []imageItem {
	entries, _ := os.ReadDir(dir)
	var out []imageItem
	for _, e := range entries {
		ext := strings.ToLower(filepath.Ext(e.Name()))
		if e.IsDir() || (ext != ".png" && ext != ".jpg" && ext != ".jpeg") {
			continue
		}
		n, err := strconv.Atoi(strings.TrimSuffix(e.Name(), filepath.Ext(e.Name())))
		if err != nil {
			continue
		}
		out = append(out, imageItem{n: n, path: filepath.Join(dir, e.Name())})
	}
	slices.SortFunc(out, func(a, b imageItem) int { return b.n - a.n })
	return out
}

// scaledImage 解码图片并缩到最宽 width 像素；本来就更小的不放大。
func scaledImage(path string, width int) (*image.RGBA, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	src, _, err := image.Decode(f)
	if err != nil {
		return nil, err
	}
	b := src.Bounds()
	w, h := b.Dx(), b.Dy()
	if w > width {
		h = max(h*width/w, 1)
		w = width
	}
	dst := image.NewRGBA(image.Rect(0, 0, w, h))
	draw.CatmullRom.Scale(dst, dst.Bounds(), src, b, draw.Src, nil)
	return dst, nil
}

// pollImages 在主线程调用：换了终端就重来，到点了在后台找目录、列图片、给新图片做缩略图。
func (a *app) pollImages(cur *terminalInfo) {
	v := &a.images
	if v.term != cur.ID {
		*v = imagesView{term: cur.ID}
	}
	if cur.Agent != "claude" || v.busy || time.Since(v.scanned) < imagesRefresh {
		return
	}
	v.busy = true
	id, dir := cur.ID, v.dir
	var sid, transcript string
	if cur.AgentState != nil {
		sid, transcript = cur.AgentState.SessionID, cur.AgentState.TranscriptPath
	}
	have := map[string]*ui.Bitmap{}
	for _, it := range v.items {
		have[it.path] = it.thumb
	}
	go func() {
		if dir == "" {
			dir = claudeImageDir(sid, transcript)
		}
		items := listImages(dir)
		thumbs := map[string]*image.RGBA{}
		for _, it := range items {
			if have[it.path] == nil {
				if img, err := scaledImage(it.path, thumbWidth); err == nil {
					thumbs[it.path] = img
				}
			}
		}
		a.win.Update(func() {
			v := &a.images
			if v.term != id {
				return
			}
			for i := range items {
				if bm := have[items[i].path]; bm != nil {
					items[i].thumb = bm
				} else if img := thumbs[items[i].path]; img != nil {
					items[i].thumb = ui.NewBitmap(img)
				}
			}
			v.dir, v.items, v.busy, v.scanned = dir, items, false, time.Now()
		})
	}()
}

// loadBig 在后台解码悬停的那张图的放大版，只留这一张。
func (a *app) loadBig(it imageItem) {
	v := &a.images
	if v.bigN == it.n || v.bigBusy {
		return
	}
	v.bigBusy = true
	id := v.term
	go func() {
		img, err := scaledImage(it.path, bigWidth)
		a.win.Update(func() {
			v := &a.images
			v.bigBusy = false
			if v.term != id || err != nil {
				return
			}
			v.big, v.bigN = ui.NewBitmap(img), it.n
		})
	}()
}

// imagesPanel 是右侧的图片面板：缩略图标上 #N，悬停在左边放大，点击用默认应用（预览）打开原图。
func (a *app) imagesPanel(c *ui.Context, cur *terminalInfo) ui.Element {
	t := c.Theme()
	a.pollImages(cur)
	v := &a.images
	v.hover = 0
	return a.rightPanel(c, "图片", func() {
		ui.Scroll(c).Grow(1).Children(func() {
			ui.Column(c).Padding(0, 12, 16, 12).Gap(12).Children(func() {
				switch {
				case cur.Agent != "claude":
					usageNote(c, "目前只支持 Claude 会话：终端里的 [Image #N] 对应这里的 #N。")
					return
				case v.scanned.IsZero():
					usageNote(c, "正在读取…")
					return
				case len(v.items) == 0:
					usageNote(c, "这个会话还没有粘贴过图片。")
					return
				}
				for _, it := range v.items {
					card := ui.Column(c.Key(it.path)).Gap(6).Padding(6).Radius(10).Role(ui.RoleButton).Label(fmt.Sprintf("图片 #%d", it.n))
					selectable(c, card, false)
					card.Children(func() {
						if it.thumb != nil {
							ui.Image(c, it.thumb).Width(244).Radius(6)
						} else {
							ui.Box(c).Height(120).Radius(6).Background(t.Surface)
						}
						ui.Text(c, fmt.Sprintf("#%d", it.n)).FontSize(11).FontFeatures("tnum").TextColor(t.TextMuted).Padding(0, 2)
					})
					if card.Hovered() {
						v.hover = it.n
						a.loadBig(it)
					}
					if card.Clicked() {
						if err := mygo.Shell.OpenPath(it.path); err != nil {
							a.err = err.Error()
						}
					}
				}
			})
		})
	})
}

// imagePreview 是悬停时浮在图片面板左边的大图。
func (a *app) imagePreview(c *ui.Context) {
	v := &a.images
	if v.hover == 0 || v.big == nil || v.bigN != v.hover {
		return
	}
	t := c.Theme()
	shadow := ui.RGBA(0, 0, 0, 0.18)
	if t.Dark {
		shadow = ui.RGBA(0, 0, 0, 0.45)
	}
	ui.Box(c).Absolute().Top(8).Right(8+280+8).Width(560).Padding(8).Radius(12).Background(colors(t).band).
		Border(1, colors(t).line).Shadow(0, 8, 24, 0, shadow).Children(func() {
		ui.Image(c, v.big).Width(544).Radius(6)
	})
}
