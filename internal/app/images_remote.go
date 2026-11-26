package app

import (
	"context"
	"image"
	"slices"
	"strings"
	"time"

	"github.com/egoist/mygo"
	"github.com/egoist/mygo/transfer"
	"github.com/egoist/mygo/ui"

	"posse/internal/errtext"
	"posse/internal/files"
	"posse/internal/media"
	"posse/internal/rpc"
	"posse/locales"
)

// targetImages 的寿命覆盖一次面板会话；轮询换轮次不使用户刚点的动作失效。
type targetImages struct {
	request appRequest
	source  rpc.MediaSource
	ctx     context.Context
	cancel  context.CancelFunc
	err     error
}

func (a *app) closeImages() {
	if s := a.images.target; s != nil {
		s.cancel()
	}
	a.images = imagesView{}
}

func (s *targetImages) current() bool {
	return s != nil && s.ctx.Err() == nil && s.request.current() && s.request.app.images.target == s
}

func (a *app) pollTargetImages(c *ui.Context, cur *terminalInfo) {
	source := rpc.MediaSource{Agent: cur.Agent}
	if cur.AgentState != nil {
		source.SessionID, source.TranscriptPath = cur.AgentState.SessionID, cur.AgentState.TranscriptPath
	}
	v := &a.images
	s := v.target
	if v.term != cur.ID || s == nil || s.source != source || !s.current() {
		a.closeImages()
		ctx, cancel := context.WithCancel(a.windowContext())
		s = &targetImages{request: a.request(), source: source, ctx: ctx, cancel: cancel}
		v.term, v.target = cur.ID, s
	}
	if !media.Supported(cur.Agent) || !v.poll.due(c, imagesRefresh) {
		return
	}
	have := map[string]*ui.Bitmap{}
	for _, it := range slices.Concat(v.made, v.pasted) {
		have[it.Key] = it.thumb
	}
	round := new(byte)
	v.round = round
	go func() {
		ctx, cancel := context.WithTimeout(s.ctx, 2*time.Minute)
		defer cancel()
		var list rpc.MediaListResult
		err := context.Canceled
		if s.request.current() && s.request.client != nil {
			list, err = s.request.client.MediaList(ctx, s.source)
		}
		if err != nil {
			a.update(func() {
				if s.current() && a.images.round == round {
					s.err = err
					a.images.poll.done()
					a.err = errtext.Text(err)
				}
			})
			return
		}
		pasted, made := wrapItems(list.Pasted), wrapItems(list.Made)
		for _, items := range [][]imageItem{pasted, made} {
			for i := range items {
				items[i].thumb = have[items[i].Key]
			}
		}
		a.update(func() {
			if !s.current() || a.images.round != round {
				return
			}
			v := &a.images
			v.pasted, v.made, v.madeAll, v.found = pasted, made, list.MadeAll, list.Found
			v.failed, s.err = nil, nil
			if !v.tabSet {
				v.tab = 0
				if len(made) == 0 && len(pasted) > 0 {
					v.tab = 1
				}
			}
			v.poll.at = time.Now()
		})
		thumbs := map[string]*image.RGBA{}
		var failed []string
		flush := func(last bool) {
			got, bad := thumbs, failed
			thumbs, failed = map[string]*image.RGBA{}, nil
			a.update(func() {
				if !s.current() || a.images.round != round {
					return
				}
				v := &a.images
				for _, items := range [][]imageItem{v.made, v.pasted} {
					for i := range items {
						if img := got[items[i].Key]; img != nil {
							items[i].thumb = ui.NewBitmap(img)
						}
					}
				}
				for _, key := range bad {
					v.markFailed(key)
				}
				if last {
					v.poll.done()
				}
			})
		}
		started, sentFirst := time.Now(), false
		for _, it := range slices.Concat(made, pasted) {
			if ctx.Err() != nil {
				break
			}
			if it.thumb != nil {
				continue
			}
			data, err := s.request.readMedia(ctx, it.Item)
			if err == nil {
				var img *image.RGBA
				img, _, err = media.ScaledImageBytes(data, thumbWidth)
				if err == nil {
					thumbs[it.Key] = img
				}
			}
			if err != nil {
				failed = append(failed, it.Key)
			}
			if !sentFirst || time.Since(started) >= thumbFlushEvery {
				flush(false)
				started, sentFirst = time.Now(), true
			}
		}
		flush(true)
	}()
}

// readMedia 只读取目标端核验或导出的路径，视频在目标生成缩略图。
func (r appRequest) readMedia(ctx context.Context, it media.Item) ([]byte, error) {
	if !r.current() || r.client == nil {
		return nil, context.Canceled
	}
	path, err := r.client.MediaPath(ctx, it)
	if err != nil {
		return nil, err
	}
	data, _, err := r.client.ReadResource(ctx, "/", strings.TrimPrefix(path, "/"), files.MaxSize)
	if err != nil {
		return nil, err
	}
	if !r.current() {
		return nil, context.Canceled
	}
	return data, ctx.Err()
}

func (a *app) loadTargetBig(it imageItem) {
	v, s := &a.images, a.images.target
	if !s.current() || v.bigKey == it.Key || v.bigBusy || v.failed[it.Key] {
		return
	}
	v.bigBusy = true
	go func() {
		ctx, cancel := context.WithTimeout(s.ctx, 30*time.Second)
		defer cancel()
		data, err := s.request.readMedia(ctx, it.Item)
		var anim *animPlayer
		var img *image.RGBA
		var info imageInfo
		if err == nil {
			if !it.Video {
				info = imageBytesInfo(data)
				anim, _, _ = newAnimPlayer(data, bigWidth)
			}
			if anim != nil {
				img = anim.cur
			} else if !it.Video {
				if pi, perr := media.DecodePreviewBytes(data, bigMaxPixels); perr == nil {
					img = pi.Img
					if info.w == 0 {
						info.w, info.h, info.format = pi.Size.X, pi.Size.Y, pi.Format
					}
					if info.bytes == 0 {
						info.bytes = int64(len(data))
					}
				} else {
					img, _, err = media.ScaledImageBytes(data, bigWidth)
				}
			} else {
				img, _, err = media.ScaledImageBytes(data, bigWidth)
			}
		}
		a.update(func() {
			if !s.current() {
				return
			}
			v := &a.images
			v.bigBusy = false
			if err != nil {
				v.markFailed(it.Key)
				return
			}
			v.big, v.bigAnim, v.bigInfo, v.bigKey = ui.NewBitmap(img), anim, info, it.Key
			if anim != nil {
				v.big = anim.bm
			}
		})
	}()
}

var writeMediaClipboard = func(data []byte) {
	mygo.Clipboard.Write(transfer.New(transfer.NewItem(transfer.Bytes(transfer.PNG, data))))
}

func (a *app) copyTargetImage(m locales.Messages, it imageItem) {
	s := a.images.target
	if !s.current() || it.Video {
		return
	}
	go func() {
		ctx, cancel := context.WithTimeout(s.ctx, 30*time.Second)
		defer cancel()
		data, err := s.request.readMedia(ctx, it.Item)
		if err == nil {
			data, err = media.PNGData(data)
		}
		a.update(func() {
			if !s.current() {
				return
			}
			if err != nil {
				a.err = m.ImagesCopyFailed(errtext.Text(err))
				return
			}
			// 目标路径不是本机文件；只放图片数据，终端粘贴由既有目标上传链路处理。
			writeMediaClipboard(data)
			a.notify(m.ImagesCopied(), "", nil)
		})
	}()
}

func (a *app) openTargetImage(it imageItem, copy bool) {
	s, w := a.images.target, a.workspace(a.selected)
	if !s.current() || w == nil {
		return
	}
	selected := a.selected
	valid := func() bool { return s.current() && a.selected == selected }
	go func() {
		ctx, cancel := context.WithTimeout(s.ctx, 30*time.Second)
		defer cancel()
		path, err := it.Path, error(nil)
		if !it.Video {
			path, err = s.request.client.MediaPath(ctx, it.Item)
		} else if it.InlineMime != "" {
			path, err = s.request.client.MediaOriginal(ctx, it.Item)
		}
		a.update(func() {
			if !valid() {
				return
			}
			if err != nil {
				a.err = errtext.Text(err)
				return
			}
			if copy || it.Video {
				a.chooseDownloadChecked(appL(), a.filesFor(w), path, true, valid)
				return
			}
			a.state.RightPanel = ""
			a.saveState()
			a.previewFile(w, path)
		})
	}()
}
