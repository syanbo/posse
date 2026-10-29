package app

import (
	"fmt"
	"image"
	"maps"
	"os/exec"
	"slices"
	"time"

	"github.com/egoist/mygo"
	"github.com/egoist/mygo/ui"

	"posse/internal/media"
)

// imagesView 是右侧"图片"面板的状态：当前会话里 agent 生成的图片、视频和粘贴过的图片，怎么找见 media 包。
//
// 截图解码后一张十几 MB，所以只留缩到面板宽度的缩略图，悬停放大时才解码那一张。
type imagesView struct {
	term    string
	scanner *media.Scanner
	pasted  []imageItem
	made    []imageItem // agent 生成的，新的在前，最多 maxMadeImages 张
	madeAll int         // agent 生成的一共几张
	poll    panelPoll
	failed  map[string]bool // 解码失败的图片，不再重试
	tab     int             // 顶部的分段：0 是 agent 生成的，1 是粘贴的
	tabSet  bool            // 用户点过分段；没点过时按有没有图片自动选

	hover   string // 悬停的图片的 key
	big     *ui.Bitmap
	bigKey  string
	bigBusy bool
}

type imageItem struct {
	media.Item
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

func wrapItems(items []media.Item) []imageItem {
	out := make([]imageItem, len(items))
	for i, it := range items {
		out[i].Item = it
	}
	return out
}

// pollImages 在主线程调用：换了终端就重来，到点了在后台找图片、给新图片做缩略图。
func (a *app) pollImages(c *ui.Context, cur *terminalInfo) {
	v := &a.images
	if v.term != cur.ID {
		*v = imagesView{term: cur.ID}
	}
	if !media.Supported(cur.Agent) || !v.poll.due(c, imagesRefresh) {
		return
	}
	var sid, transcript string
	if cur.AgentState != nil {
		sid, transcript = cur.AgentState.SessionID, cur.AgentState.TranscriptPath
	}
	id, scanner := cur.ID, v.scanner
	if !scanner.Same(cur.Agent, sid, transcript) {
		scanner = media.NewScanner(cur.Agent, sid, transcript)
	}
	have := map[string]*ui.Bitmap{}
	for _, it := range slices.Concat(v.pasted, v.made) {
		have[it.Key] = it.thumb
	}
	skip := maps.Clone(v.failed)
	go func() {
		p, m := scanner.Scan()
		all := len(m)
		pasted, made := wrapItems(p), wrapItems(m[:min(len(m), maxMadeImages)])
		thumbs := map[string]*image.RGBA{}
		var failed []string
		for _, it := range slices.Concat(made, pasted) {
			if have[it.Key] == nil && !skip[it.Key] {
				if img, err := media.Decode(it.Item, thumbWidth); err == nil {
					thumbs[it.Key] = img
				} else {
					failed = append(failed, it.Key)
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
					if bm := have[items[i].Key]; bm != nil {
						items[i].thumb = bm
					} else if img := thumbs[items[i].Key]; img != nil {
						items[i].thumb = ui.NewBitmap(img)
					}
				}
			}
			for _, k := range failed {
				v.markFailed(k)
			}
			v.scanner, v.pasted, v.made, v.madeAll = scanner, pasted, made, all
			// 没点过分段时，生成的为空、粘贴的有就看粘贴的
			if !v.tabSet {
				v.tab = 0
				if len(made) == 0 && len(pasted) > 0 {
					v.tab = 1
				}
			}
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
	if v.bigKey == it.Key || v.bigBusy || v.failed[it.Key] {
		return
	}
	v.bigBusy = true
	id := v.term
	go func() {
		img, err := media.Decode(it.Item, bigWidth)
		a.update(func() {
			v := &a.images
			if v.term != id {
				return
			}
			v.bigBusy = false
			if err != nil {
				v.markFailed(it.Key)
				return
			}
			v.big, v.bigKey = ui.NewBitmap(img), it.Key
		})
	}()
}

// withImagePath 拿到原图或视频的路径后在主线程调用 f。工具返回的图片只在会话文件里，先写到临时目录：
// 读的那个文件之后可能被改掉，打开的是 agent 当时看到的样子。
func (a *app) withImagePath(it imageItem, f func(path string) error) {
	if it.Tool == nil {
		if err := f(it.Path); err != nil {
			a.err = err.Error()
		}
		return
	}
	go func() {
		path, err := media.WriteToolImage(it.Item)
		a.update(func() {
			if err == nil {
				err = f(path)
			}
			if err != nil {
				a.err = err.Error()
			}
		})
	}()
}

// openImage 是点卡片：图片在文件标签里预览（可缩放），视频和找不到工作区的用默认应用打开。
func (a *app) openImage(it imageItem) {
	a.withImagePath(it, func(path string) error {
		w := a.workspaceOf(path)
		if w == nil {
			w = a.workspace(a.selected)
		}
		if it.Video || w == nil {
			return mygo.Shell.OpenPath(path)
		}
		// 文件标签盖住了终端，面板留着会挡住图
		a.state.RightPanel = ""
		a.saveState()
		a.previewFile(w, path)
		return nil
	})
}

// imageMenu 是卡片的右键菜单。图片指定用系统的"预览"打开：默认应用可能被改成别的编辑器。
func (a *app) imageMenu(c *ui.Context, card ui.Element, it imageItem) {
	contextMenu(c, card, func(m *popMenu) {
		if m.Item("在访达中显示").Icon(iconFolder).Chosen() {
			a.withImagePath(it, func(path string) error {
				mygo.Shell.ShowItemInFolder(path)
				return nil
			})
		}
		if it.Video {
			if m.Item("用默认应用打开").Chosen() {
				a.withImagePath(it, mygo.Shell.OpenPath)
			}
		} else if m.Item("用系统预览打开").Chosen() {
			a.withImagePath(it, func(path string) error {
				return exec.Command("open", "-a", "Preview", path).Run()
			})
		}
	})
}

// imagesPanel 是右侧的图片面板：顶部分段切换 agent 生成的和粘贴的（标上 #N）；悬停在左边放大，点击在文件标签里预览，右键在访达中显示或用系统预览打开。
func (a *app) imagesPanel(c *ui.Context, cur *terminalInfo) ui.Element {
	a.pollImages(c, cur)
	v := &a.images
	v.hover = ""
	a.imagesDrawn = true
	return a.rightPanel(c, "图片", func() {
		if media.Supported(cur.Agent) && !v.poll.at.IsZero() && (len(v.made) > 0 || len(v.pasted) > 0) {
			ui.Row(c).Padding(0, 12, 12, 12).Shrink(0).Children(func() {
				// 和文件栏的分段一样撑满栏宽
				ui.Column(c).Grow(1).Children(func() {
					if segmentedBadges(c, &v.tab, "图片来源", []int{v.madeAll, len(v.pasted)}, "生成", "粘贴") {
						v.tabSet = true
					}
				})
			})
		}
		ui.Scroll(c.Key(fmt.Sprint("images-", v.tab))).Grow(1).Children(func() {
			ui.Column(c).Padding(0, 12, 16, 12).Gap(12).Children(func() {
				switch {
				case !media.Supported(cur.Agent):
					usageNote(c, "目前只支持 Claude、Codex、Grok 和 Cursor 会话。")
					return
				case v.poll.at.IsZero():
					usageNote(c, "正在读取…")
					return
				case len(v.made) == 0 && len(v.pasted) == 0:
					usageNote(c, "这个会话还没有图片或视频。")
					return
				}
				if v.tab == 0 {
					if len(v.made) == 0 {
						usageNote(c, "agent 还没有生成图片或视频。")
						return
					}
					a.imageGroup(c, v.made)
					if v.madeAll > len(v.made) {
						usageNote(c, fmt.Sprintf("只列出最近 %d 项。", len(v.made)))
					}
					return
				}
				if len(v.pasted) == 0 {
					usageNote(c, "这个会话还没有粘贴过图片。")
					return
				}
				a.imageGroup(c, v.pasted)
			})
		})
	})
}

// imageGroup 是图片面板当前分段的缩略图卡片，数目在分段上。
func (a *app) imageGroup(c *ui.Context, items []imageItem) {
	t := c.Theme()
	v := &a.images
	for _, it := range items {
		kind := "图片 "
		if it.Video {
			kind = "视频 "
		}
		card := ui.Column(c.Key(it.Key)).Gap(6).Padding(6).Radius(rCard).Role(ui.RoleButton).Label(kind + it.Label)
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
					ui.Box(c).Children(func() {
						ui.Image(c, it.thumb).Width(dw).Radius(rRow)
						if it.Video {
							playBadge(c)
						}
					})
				})
			} else {
				ui.Box(c).Height(120).Radius(rRow).Background(t.Surface).Children(func() {
					if it.Video {
						playBadge(c)
					}
				})
			}
			ui.Row(c).Gap(8).Padding(0, 2).AlignItems(ui.Center).Children(func() {
				ui.Text(c, it.Label).FontSize(fsSmall).FontFeatures("tnum").TextColor(t.TextMuted).SingleLine().Shrink(1).Tooltip(it.Source())
				if !it.At.IsZero() {
					ui.Spacer(c)
					ui.Text(c, commitTime(it.At)).FontSize(fsSmall).FontFeatures("tnum").TextColor(t.TextMuted).Shrink(0)
				}
			})
		})
		if card.Hovered() {
			v.hover = it.Key
			a.loadBig(it)
		}
		if card.Clicked() {
			a.openImage(it)
		}
		a.imageMenu(c, card, it)
	}
}

// playBadge 是盖在视频缩略图正中的播放标记，深色圆底让它在亮暗画面上都看得见。
func playBadge(c *ui.Context) {
	ui.Box(c).Absolute().Top(0).Right(0).Bottom(0).Left(0).Center().Children(func() {
		ui.Box(c).Size(36, 36).Radius(full(36)).Center().Background(colors(c.Theme()).scrim).Children(func() {
			ui.Icon(c, iconPlay).FontSize(sz(22)).TextColor(white)
		})
	})
}

// imagePreview 是悬停时浮在图片面板左边的大图：宽不超过 544、高不超过 body，竖的长截图按高缩小，整张看得见。
func (a *app) imagePreview(c *ui.Context, body ui.Element) {
	v := &a.images
	if v.hover == "" || v.big == nil || v.bigKey != v.hover {
		return
	}
	w, h := v.big.Size()
	dw := float32(544)
	b := body.Bounds()
	if b.W > 0 {
		// 左边留 8，右边让开面板
		dw = min(dw, max(b.W-8-(8+280+8)-16, 80))
	}
	if bh := b.H; bh > 0 {
		// 上下各留 8，卡片自己的内边距各 8
		if maxH := bh - 32; dw*float32(h)/float32(max(w, 1)) > maxH {
			dw = max(maxH, 80) * float32(w) / float32(max(h, 1))
		}
	}
	floating(c, ui.Box(c).Absolute().Top(8).Right(8+280+8).Padding(8)).Children(func() {
		ui.Image(c, v.big).Width(dw).Radius(rSmall) // 外框内边距 8：12 - 8
	})
}
