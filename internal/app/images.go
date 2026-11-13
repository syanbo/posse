package app

import (
	"fmt"
	"image"
	"maps"
	"os/exec"
	"slices"
	"time"

	"github.com/egoist/mygo"
	"github.com/egoist/mygo/transfer"
	"github.com/egoist/mygo/ui"

	"posse/internal/errtext"
	"posse/internal/media"
	"posse/locales"
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
	found   bool        // 找到了会话文件；没找到时内嵌的图片列不出来，不能说没有图片
	// round 标记正在进行的这一轮扫描：面板关掉再打开后终端 ID 一样，靠它认出上一轮的结果
	round  *byte
	poll   panelPoll
	failed map[string]bool // 解码失败的图片，不再重试
	tab    int             // 顶部的分段：0 是 agent 生成的，1 是粘贴的
	tabSet bool            // 用户点过分段；没点过时按有没有图片自动选

	hover   string // 悬停的图片的 key
	big     *ui.Bitmap
	bigAnim *animPlayer // 悬停的是动图：big 是它的位图
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
	// thumbFlushEvery 是做缩略图时多久交给界面一批：一张约 20～50 ms，每张都重建界面太频繁
	thumbFlushEvery = 150 * time.Millisecond
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
	// 标记必须占实际内存，零大小对象可能共用地址，无法隔离扫描轮次。
	round := new(byte)
	v.round = round
	go func() {
		p, m := scanner.Scan()
		all, found := len(m), scanner.Found()
		pasted, made := wrapItems(p), wrapItems(m[:min(len(m), maxMadeImages)])
		for _, items := range [][]imageItem{pasted, made} {
			for i := range items {
				items[i].thumb = have[items[i].Key]
			}
		}
		// 先交出列表，缩略图在后面分批补上：几十张截图全解完要一两秒，不能一直显示"正在读取"
		a.update(func() {
			v := &a.images
			if v.term != id || v.round != round {
				return
			}
			v.scanner, v.pasted, v.made, v.madeAll, v.found = scanner, pasted, made, all, found
			// 没点过分段时，生成的为空、粘贴的有就看粘贴的
			if !v.tabSet {
				v.tab = 0
				if len(made) == 0 && len(pasted) > 0 {
					v.tab = 1
				}
			}
			v.poll.at = time.Now()
		})
		// 按面板上的顺序做：当前分段的在前，最新的在最上面
		order := slices.Concat(made, pasted)
		if len(made) == 0 {
			order = slices.Concat(pasted, made)
		}
		thumbs := map[string]*image.RGBA{}
		var failed []string
		flush := func(last bool) {
			got, bad := thumbs, failed
			thumbs, failed = map[string]*image.RGBA{}, nil
			a.update(func() {
				v := &a.images
				if v.term != id || v.round != round {
					return
				}
				for _, items := range [][]imageItem{v.pasted, v.made} {
					for i := range items {
						if img := got[items[i].Key]; img != nil && items[i].thumb == nil {
							items[i].thumb = ui.NewBitmap(img)
						}
					}
				}
				for _, k := range bad {
					v.markFailed(k)
				}
				if last {
					v.poll.done()
				}
			})
		}
		started, sentFirst := time.Now(), false
		for _, it := range order {
			if it.thumb != nil || skip[it.Key] {
				continue
			}
			if img, err := media.Decode(it.Item, thumbWidth); err == nil {
				thumbs[it.Key] = img
			} else {
				failed = append(failed, it.Key)
			}
			// 最上面那张做好就先交，其余的攒一批再交
			if len(thumbs)+len(failed) == 1 && !sentFirst || time.Since(started) >= thumbFlushEvery {
				sentFirst = true
				flush(false)
				started = time.Now()
			}
		}
		flush(true)
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
		anim, img, err := decodeBig(it.Item)
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
			v.big, v.bigAnim, v.bigKey = ui.NewBitmap(img), anim, it.Key
			if anim != nil {
				v.big = anim.bm
			}
		})
	}()
}

// decodeBig 解码悬停放大的那张图；动图给出播放器，img 是它的第一帧。
func decodeBig(it media.Item) (*animPlayer, *image.RGBA, error) {
	if !it.Video {
		if b, err := media.ReadImage(it); err == nil {
			if anim, _, err := newAnimPlayer(b, bigWidth); anim != nil && err == nil {
				return anim, anim.cur, nil
			}
		}
	}
	img, err := media.Decode(it, bigWidth)
	return nil, img, err
}

// withImagePath 拿到原图或视频的路径后在主线程调用 f。工具返回的图片只在会话文件里，先写到临时目录：
// 读的那个文件之后可能被改掉，打开的是 agent 当时看到的样子。
func (a *app) withImagePath(it imageItem, f func(path string) error) {
	if it.Tool == nil {
		if err := f(it.Path); err != nil {
			a.err = errtext.Text(err)
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
				a.err = errtext.Text(err)
			}
		})
	}()
}

// openPreview 用系统的预览打开 path，测试里换掉。
var openPreview = func(path string) error { return exec.Command("open", "-a", "Preview", path).Run() }

// openInPreview 用系统的预览打开图片。open 要等预览接手才退出（预览没开着时要先启动它），在后台等，不卡界面。
func (a *app) openInPreview(it imageItem) {
	a.withImagePath(it, func(path string) error {
		go func() {
			if err := openPreview(path); err != nil {
				a.update(func() { a.err = errtext.Text(err) })
			}
		}()
		return nil
	})
}

// copyImage 把图片放进剪贴板：同一项里既有 PNG 数据（贴进聊天、文档这类认图片的地方）又有文件
// （访达里粘贴得到文件，Posse 的终端里 ⌘V 贴的是路径，agent 当附件）。转 PNG 在后台做，大图要一会儿。
func (a *app) copyImage(m locales.Messages, it imageItem) {
	a.withImagePath(it, func(path string) error {
		go func() {
			b, err := media.PNGBytes(path)
			a.update(func() {
				if err != nil {
					a.err = m.ImagesCopyFailed(errtext.Text(err))
					return
				}
				files, err := transfer.FileData(path)
				if err != nil {
					a.err = m.ImagesCopyFailed(errtext.Text(err))
					return
				}
				reps := []transfer.Representation{transfer.Bytes(transfer.PNG, b)}
				for _, f := range []transfer.Format{transfer.FileList, transfer.URIList} {
					if fb, err := files.Read(f); err == nil {
						reps = append(reps, transfer.Bytes(f, fb))
					}
				}
				mygo.Clipboard.Write(transfer.New(transfer.NewItem(reps...)))
				a.notify(m.ImagesCopied(), "", nil)
			})
		}()
		return nil
	})
}

// openImage 是点卡片：图片在文件标签里预览（可缩放），视频和找不到工作区的用默认应用打开。
func (a *app) openImage(it imageItem) {
	a.withImagePath(it, func(path string) error {
		// 和终端里的链接一样，开在正看着的工作区里：放进图片所属的工作区（比如 worktree）就看不见了
		w := a.workspace(a.selected)
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
		if !it.Video && m.Item(L(c).ImagesCopyImage()).Icon(iconCopy).Chosen() {
			a.copyImage(L(c), it)
		}
		if m.Item(L(c).CommonRevealInFinder()).Icon(iconFolder).Chosen() {
			a.withImagePath(it, func(path string) error {
				mygo.Shell.ShowItemInFolder(path)
				return nil
			})
		}
		if it.Video {
			if m.Item(L(c).FilesOpenWithDefaultApp()).Chosen() {
				a.withImagePath(it, mygo.Shell.OpenPath)
			}
		} else if m.Item(L(c).ImagesOpenInPreview()).Chosen() {
			a.openInPreview(it)
		}
	})
}

// imagesPanel 是右侧的图片面板：顶部分段切换 agent 生成的和粘贴的（标上 #N）；悬停在左边放大，点击在文件标签里预览，右键在访达中显示或用系统预览打开。
func (a *app) imagesPanel(c *ui.Context, cur *terminalInfo) ui.Element {
	a.pollImages(c, cur)
	v := &a.images
	v.hover = ""
	a.imagesDrawn = true
	return a.rightPanel(c, L(c).ImagesTitle(), func() {
		if media.Supported(cur.Agent) && !v.poll.at.IsZero() && (len(v.made) > 0 || len(v.pasted) > 0) {
			ui.Row(c).Padding(0, 12, 12, 12).Shrink(0).Children(func() {
				// 和文件栏的分段一样撑满栏宽
				ui.Column(c).Grow(1).Children(func() {
					if segmentedBadges(c, &v.tab, L(c).ImagesSource(), []int{v.madeAll, len(v.pasted)}, L(c).ImagesGeneratedTab(), L(c).ImagesPastedTab()) {
						v.tabSet = true
					}
				})
			})
		}
		ui.Scroll(c.Key(fmt.Sprint("images-", v.tab))).Grow(1).Children(func() {
			ui.Column(c).Padding(0, 12, 16, 12).Gap(12).Children(func() {
				switch {
				case !media.Supported(cur.Agent):
					usageNote(c, L(c).ImagesUnsupported())
					return
				case v.poll.at.IsZero():
					usageNote(c, L(c).CommonLoading())
					return
				case len(v.made) == 0 && len(v.pasted) == 0 && !v.found:
					usageNote(c, L(c).ImagesWaiting())
					return
				case len(v.made) == 0 && len(v.pasted) == 0:
					usageNote(c, L(c).ImagesNone())
					return
				}
				if v.tab == 0 {
					if len(v.made) == 0 {
						usageNote(c, L(c).ImagesNoneGenerated())
						return
					}
					a.imageGroup(c, v.made)
					if v.madeAll > len(v.made) {
						usageNote(c, L(c).ImagesRecentOnly(len(v.made)))
					}
					return
				}
				if len(v.pasted) == 0 {
					usageNote(c, L(c).ImagesNonePasted())
					return
				}
				a.imageGroup(c, v.pasted)
			})
		})
	})
}

// mediaLabel 是缩略图下的名字：没有名字的一类按界面语言写出叫法。
func mediaLabel(m locales.Messages, it media.Item) string {
	switch it.Tag {
	case media.TagGenerated:
		return m.ImagesGenerated()
	case media.TagCodeOutput:
		return m.ImagesCodeOutput()
	case media.TagPasted:
		return m.ImagesPasted()
	}
	return it.Label
}

// imageGroup 是图片面板当前分段的缩略图卡片，数目在分段上。
func (a *app) imageGroup(c *ui.Context, items []imageItem) {
	t := c.Theme()
	v := &a.images
	for _, it := range items {
		label := L(c).ImagesImageLabel(mediaLabel(L(c), it.Item))
		if it.Video {
			label = L(c).ImagesVideoLabel(mediaLabel(L(c), it.Item))
		}
		card := ui.Column(c.Key(it.Key)).Gap(6).Padding(6).Radius(rCard).Role(ui.RoleButton).Label(label)
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
				ui.Text(c, mediaLabel(L(c), it.Item)).FontSize(fsSmall).FontFeatures("tnum").TextColor(t.TextMuted).SingleLine().Shrink(1).Tooltip(it.Source())
				if !it.At.IsZero() {
					ui.Spacer(c)
					ui.Text(c, commitTime(L(c), it.At)).FontSize(fsSmall).FontFeatures("tnum").TextColor(t.TextMuted).Shrink(0)
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
		// 滑入途中不能拖：卡片在不动的指针下面移动，mygo 会把这一下点击当成拖动（同 dragPath）
		if a.panelShown >= 1 {
			card.Drag(pathDrag{image: &it})
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
		dw, _ = fitSize(float32(max(w, 1)), float32(max(h, 1)), dw, max(bh-32, 80))
	}
	floating(c, ui.Box(c).Absolute().Top(8).Right(8+280+8).Padding(8)).Children(func() {
		anim := v.bigAnim
		// 动图在外层的 Draw 里换帧，里面的图片画的就是这一帧
		ui.Box(c).Draw(func(p *ui.Painter, r ui.Rect) { animate(p, anim) }).Children(func() {
			ui.Image(c, v.big).Width(dw).Radius(rSmall) // 外框内边距 8：12 - 8
		})
	})
}
