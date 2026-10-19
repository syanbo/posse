package app

import (
	"bytes"
	"crypto/sha1"
	"fmt"
	"image"
	_ "image/gif"
	_ "image/jpeg"
	_ "image/png"
	"io"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/egoist/mygo"
	"github.com/egoist/mygo/ui"
	"golang.org/x/image/draw"
	_ "golang.org/x/image/webp"

	"hive/internal/history"
)

// imagesView 是右侧"图片"面板的状态：当前会话里 agent 生成的图片和粘贴过的图片。
//   - 粘贴的：Claude 存在会话的图片目录里，终端里显示成 [Image #N]；
//   - agent 生成的：Claude 工具返回的图片（截图、读进来的图片文件），内嵌在会话文件里；
//     Codex 图像生成和跑代码产出的图片，存在 ~/.codex/generated_images/<会话 ID>/。
//
// 截图解码后一张十几 MB，所以只留缩到面板宽度的缩略图，悬停放大时才解码那一张。
type imagesView struct {
	term    string
	dir     string // 会话的图片目录，找到后不再找
	scanner *history.ImageScanner
	pasted  []imageItem
	made    []imageItem // agent 生成的，新的在前，最多 maxMadeImages 张
	madeAll int         // agent 生成的一共几张
	poll    panelPoll
	failed  map[string]bool // 解码失败的图片，不再重试

	hover   string // 悬停的图片的 key
	big     *ui.Bitmap
	bigKey  string
	bigBusy bool
}

type imageItem struct {
	key   string // 粘贴的和 Codex 的是文件路径，Claude 工具返回的是它在会话文件里的位置
	n     int    // 粘贴的图片的序号
	label string
	at    time.Time
	path  string             // 磁盘上的图片
	tool  *history.ToolImage // Claude 工具返回的图片，内容在会话文件 src 里
	src   string
	thumb *ui.Bitmap
}

const (
	imagesRefresh = 3 * time.Second
	thumbWidth    = 512  // 2 倍屏下正好是面板宽度
	bigWidth      = 1120 // 悬停放大的宽度，2 倍屏下 560pt
	thumbMaxH     = 240  // 缩略图最高显示多少
	// maxMadeImages 是面板里最多列几张 agent 生成的图片：用手机、浏览器的会话动辄几百张截图，缩略图都留着太占内存
	maxMadeImages = 40
)

// imagesSupported 报告能不能列这个 agent 的图片。
func imagesSupported(agent string) bool { return agent == "claude" || agent == "codex" }

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
		path := filepath.Join(dir, e.Name())
		out = append(out, imageItem{key: path, n: n, label: fmt.Sprintf("#%d", n), path: path})
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
	img, _, err := decodeScaled(f, width)
	return img, err
}

// scaledImageBytes 是 scaledImage 读内存里的图片（diff 里某个版本的内容），顺带返回原始尺寸。
func scaledImageBytes(b []byte, width int) (*image.RGBA, image.Point, error) {
	return decodeScaled(bytes.NewReader(b), width)
}

func decodeScaled(r io.Reader, width int) (*image.RGBA, image.Point, error) {
	src, _, err := image.Decode(r)
	if err != nil {
		return nil, image.Point{}, err
	}
	b := src.Bounds()
	w, h := b.Dx(), b.Dy()
	if w > width {
		h = max(h*width/w, 1)
		w = width
	}
	dst := image.NewRGBA(image.Rect(0, 0, w, h))
	draw.CatmullRom.Scale(dst, dst.Bounds(), src, b, draw.Src, nil)
	return dst, b.Size(), nil
}

// codexImages 列出 Codex 会话生成的图片：ig_ 开头的是图像生成工具画的，exec- 开头的是跑代码产出的。新的在前。
func codexImages(sid string) []imageItem {
	home, err := os.UserHomeDir()
	if sid == "" || strings.ContainsAny(sid, `/\*?[`) || err != nil {
		return nil
	}
	dir := filepath.Join(home, ".codex", "generated_images", sid)
	entries, _ := os.ReadDir(dir)
	var out []imageItem
	for _, e := range entries {
		if e.IsDir() || !isImageFile(e.Name()) {
			continue
		}
		fi, err := e.Info()
		if err != nil {
			continue
		}
		label := e.Name()
		switch {
		case strings.HasPrefix(label, "ig_"):
			label = "生成的图片"
		case strings.HasPrefix(label, "exec-"):
			label = "代码产出"
		}
		path := filepath.Join(dir, e.Name())
		out = append(out, imageItem{key: path, label: label, at: fi.ModTime(), path: path})
	}
	slices.SortFunc(out, func(a, b imageItem) int { return b.at.Compare(a.at) })
	return out
}

// source 是图片从哪来：磁盘上的文件，或 Claude 读的文件、返回它的工具。
func (it imageItem) source() string {
	switch {
	case it.tool == nil:
		return it.path
	case it.tool.Path != "":
		return it.tool.Path
	}
	return it.tool.Tool
}

func isImageFile(name string) bool {
	switch strings.ToLower(filepath.Ext(name)) {
	case ".png", ".jpg", ".jpeg", ".gif", ".webp":
		return true
	}
	return false
}

// toolImageItems 把 Claude 工具返回的图片排成新的在前，标上读的文件名或工具名。
func toolImageItems(src string, imgs []history.ToolImage) []imageItem {
	out := make([]imageItem, 0, len(imgs))
	for i := len(imgs) - 1; i >= 0; i-- {
		im := imgs[i]
		label := im.Tool
		if im.Path != "" {
			label = filepath.Base(im.Path)
		} else if j := strings.LastIndex(label, "__"); j >= 0 {
			// mcp__iphone-use__phone_screenshot 只写工具名
			label = label[j+2:]
		}
		out = append(out, imageItem{key: fmt.Sprintf("%s:%d:%d", src, im.Offset, im.Index), label: label, at: im.At, tool: &im, src: src})
	}
	return out
}

// decodeItem 解码一张图片并缩到最宽 width 像素。
func decodeItem(it imageItem, width int) (*image.RGBA, error) {
	if it.tool == nil {
		return scaledImage(it.path, width)
	}
	b, err := history.ReadToolImage(it.src, *it.tool)
	if err != nil {
		return nil, err
	}
	img, _, err := scaledImageBytes(b, width)
	return img, err
}

// pollImages 在主线程调用：换了终端就重来，到点了在后台找图片、给新图片做缩略图。
func (a *app) pollImages(c *ui.Context, cur *terminalInfo) {
	v := &a.images
	if v.term != cur.ID {
		*v = imagesView{term: cur.ID}
	}
	if !imagesSupported(cur.Agent) || !v.poll.due(c, imagesRefresh) {
		return
	}
	id, agent, dir, scanner := cur.ID, cur.Agent, v.dir, v.scanner
	var sid, transcript string
	if cur.AgentState != nil {
		sid, transcript = cur.AgentState.SessionID, cur.AgentState.TranscriptPath
	}
	have := map[string]*ui.Bitmap{}
	for _, it := range slices.Concat(v.pasted, v.made) {
		have[it.key] = it.thumb
	}
	skip := maps.Clone(v.failed)
	go func() {
		var pasted, made []imageItem
		switch agent {
		case "claude":
			if dir == "" {
				dir = claudeImageDir(sid, transcript)
			}
			pasted = listImages(dir)
			// 会话文件要等 agent 写下第一条记录才出现，找不到就下次再找
			if scanner == nil {
				if path := history.TranscriptPath(agent, sid, transcript); path != "" {
					scanner = history.NewImageScanner(path)
				}
			}
			if scanner != nil {
				if imgs, err := scanner.Scan(); err == nil {
					made = toolImageItems(scanner.Path(), imgs)
				}
			}
		case "codex":
			made = codexImages(sid)
		}
		all := len(made)
		made = made[:min(len(made), maxMadeImages)]
		thumbs := map[string]*image.RGBA{}
		var failed []string
		for _, it := range slices.Concat(made, pasted) {
			if have[it.key] == nil && !skip[it.key] {
				if img, err := decodeItem(it, thumbWidth); err == nil {
					thumbs[it.key] = img
				} else {
					failed = append(failed, it.key)
				}
			}
		}
		a.update(func() {
			v := &a.images
			if v.term != id {
				return
			}
			for _, items := range [][]imageItem{pasted, made} {
				for i := range items {
					if bm := have[items[i].key]; bm != nil {
						items[i].thumb = bm
					} else if img := thumbs[items[i].key]; img != nil {
						items[i].thumb = ui.NewBitmap(img)
					}
				}
			}
			for _, k := range failed {
				v.markFailed(k)
			}
			v.dir, v.scanner, v.pasted, v.made, v.madeAll = dir, scanner, pasted, made, all
			v.poll.done()
		})
	}()
}

func (v *imagesView) markFailed(key string) {
	if v.failed == nil {
		v.failed = map[string]bool{}
	}
	v.failed[key] = true
}

// loadBig 在后台解码悬停的那张图的放大版，只留这一张。
func (a *app) loadBig(it imageItem) {
	v := &a.images
	if v.bigKey == it.key || v.bigBusy || v.failed[it.key] {
		return
	}
	v.bigBusy = true
	id := v.term
	go func() {
		img, err := decodeItem(it, bigWidth)
		a.update(func() {
			v := &a.images
			if v.term != id {
				return
			}
			v.bigBusy = false
			if err != nil {
				v.markFailed(it.key)
				return
			}
			v.big, v.bigKey = ui.NewBitmap(img), it.key
		})
	}()
}

// openImage 用默认应用（预览）打开原图。Claude 工具返回的图片只在会话文件里，先写到临时目录：
// 读的那个文件之后可能被改掉，打开的是 agent 当时看到的样子。
func (a *app) openImage(it imageItem) {
	if it.tool == nil {
		if err := mygo.Shell.OpenPath(it.path); err != nil {
			a.err = err.Error()
		}
		return
	}
	go func() {
		path, err := writeToolImage(it)
		if err == nil {
			err = mygo.Shell.OpenPath(path)
		}
		if err != nil {
			a.update(func() { a.err = err.Error() })
		}
	}()
}

func writeToolImage(it imageItem) (string, error) {
	b, err := history.ReadToolImage(it.src, *it.tool)
	if err != nil {
		return "", err
	}
	ext := ".png"
	switch it.tool.Media {
	case "image/jpeg":
		ext = ".jpg"
	case "image/gif":
		ext = ".gif"
	case "image/webp":
		ext = ".webp"
	}
	dir := filepath.Join(os.TempDir(), "hive-images")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", err
	}
	name := it.label
	if ext2 := filepath.Ext(name); ext2 != "" {
		name = strings.TrimSuffix(name, ext2)
	}
	sum := sha1.Sum([]byte(it.key))
	path := filepath.Join(dir, fmt.Sprintf("%s-%x%s", name, sum[:4], ext))
	return path, os.WriteFile(path, b, 0o600)
}

// imagesPanel 是右侧的图片面板：agent 生成的在上，粘贴的（标上 #N）在下；悬停在左边放大，点击用默认应用打开原图。
func (a *app) imagesPanel(c *ui.Context, cur *terminalInfo) ui.Element {
	a.pollImages(c, cur)
	v := &a.images
	v.hover = ""
	a.imagesDrawn = true
	return a.rightPanel(c, "图片", func() {
		ui.Scroll(c).Grow(1).Children(func() {
			ui.Column(c).Padding(0, 12, 16, 12).Gap(12).Children(func() {
				switch {
				case !imagesSupported(cur.Agent):
					usageNote(c, "目前只支持 Claude 和 Codex 会话。")
					return
				case v.poll.at.IsZero():
					usageNote(c, "正在读取…")
					return
				case len(v.made) == 0 && len(v.pasted) == 0:
					usageNote(c, "这个会话还没有图片。")
					return
				}
				if len(v.made) > 0 {
					a.imageGroup(c, "Agent 生成", v.madeAll, v.made)
					if v.madeAll > len(v.made) {
						usageNote(c, fmt.Sprintf("只列出最近 %d 张。", len(v.made)))
					}
				}
				if len(v.pasted) > 0 {
					a.imageGroup(c, "粘贴的", len(v.pasted), v.pasted)
				}
			})
		})
	})
}

// imageGroup 是图片面板的一组：标题和张数，下面是缩略图卡片。
func (a *app) imageGroup(c *ui.Context, title string, n int, items []imageItem) {
	t := c.Theme()
	v := &a.images
	ui.Row(c.Key("group-"+title)).Gap(6).Padding(4, 4, 0, 4).AlignItems(ui.Center).Children(func() {
		ui.Text(c, title).FontSize(12).FontWeight(500)
		ui.Text(c, fmt.Sprint(n)).FontSize(11).FontFeatures("tnum").TextColor(t.TextMuted)
	})
	for _, it := range items {
		card := ui.Column(c.Key(it.key)).Gap(6).Padding(6).Radius(10).Role(ui.RoleButton).Label("图片 " + it.label)
		selectedBg(c, card, false)
		card.Children(func() {
			if it.thumb != nil {
				// 竖的截图按比例缩到限高，居中放，不让一张占满整栏
				w, h := it.thumb.Size()
				dw := float32(244)
				if dh := dw * float32(h) / float32(max(w, 1)); dh > thumbMaxH {
					dw = dw * thumbMaxH / dh
				}
				ui.Row(c).Justify(ui.Center).Children(func() {
					ui.Image(c, it.thumb).Width(dw).Radius(6)
				})
			} else {
				ui.Box(c).Height(120).Radius(6).Background(t.Surface)
			}
			ui.Row(c).Gap(8).Padding(0, 2).AlignItems(ui.Center).Children(func() {
				ui.Text(c, it.label).FontSize(11).FontFeatures("tnum").TextColor(t.TextMuted).SingleLine().Shrink(1).Tooltip(it.source())
				if !it.at.IsZero() {
					ui.Spacer(c)
					ui.Text(c, commitTime(it.at)).FontSize(11).FontFeatures("tnum").TextColor(t.TextMuted).Shrink(0)
				}
			})
		})
		if card.Hovered() {
			v.hover = it.key
			a.loadBig(it)
		}
		if card.Clicked() {
			a.openImage(it)
		}
	}
}

// imagePreview 是悬停时浮在图片面板左边的大图。
func (a *app) imagePreview(c *ui.Context) {
	v := &a.images
	if v.hover == "" || v.big == nil || v.bigKey != v.hover {
		return
	}
	floating(c, ui.Box(c).Absolute().Top(8).Right(8+280+8).Width(560).Padding(8)).Children(func() {
		ui.Image(c, v.big).Width(544).Radius(6)
	})
}
