package app

import (
	"errors"
	"image"
	"os"
	"os/exec"
	"path/filepath"
	"time"

	"posse/internal/media"

	"github.com/egoist/mygo"
	"github.com/egoist/mygo/ui"
)

const (
	// 消息里的图片等比放进 320×320 的框，不放大小图（Element 324、Rocket.Chat 360 一类的做法）
	discThumbBox = 320
	// 缩略图按 2 倍屏的框算像素上限
	discThumbPixels = 2 * discThumbBox * 2 * discThumbBox
	// 解码要先把整张原图解出来：原图超过这个像素数、文件超过这个大小就不解，显示成文件标签。
	// 一张 24M 像素的图解出来约 96 MB，同时最多解两张
	discThumbMaxSource = 24 << 20
	discThumbMaxBytes  = 50 << 20
	// 最多留几张解好的缩略图：看不见的先被挤掉，滚回来再解（尺寸留着，占位块和原来一样大）
	discThumbKeep = 24
	discThumbBusy = 2 // 同时在后台解几张
	// 看得见的范围上下各多算这么多：快要滚进来的先解
	discThumbMargin = 400
)

type discThumb struct {
	bm     *ui.Bitmap
	w, h   float32 // 显示尺寸（点），解过一次就记下，被挤掉后还在
	failed bool    // 解不开或太大：按文件标签显示，不再重试
	busy   bool
	used   int64
}

// discThumbs 是讨论里图片附件的缩略图，按文件的绝对路径记。
type discThumbs struct {
	m    map[string]*discThumb
	tick int64
	busy int
	// wanted 是这一帧已经要了几张（消息区每帧开头清零）：超过 discThumbKeep 的不再要，
	// 看得见的比缓存多时不会互相挤掉、一直重解
	wanted int
}

func (v *discThumbs) failed(path string) bool {
	th := v.m[path]
	return th != nil && th.failed
}

// discThumb 返回 path 的缩略图和显示尺寸（没解过时为 0）；want 为真（看得见）且还没解时在后台解，解好后重画。
func (a *app) discThumb(path string, want bool) (*ui.Bitmap, float32, float32) {
	v := &a.discThumbs
	if v.m == nil {
		v.m = map[string]*discThumb{}
	}
	if want {
		if v.wanted >= discThumbKeep {
			want = false
		} else {
			v.wanted++
		}
	}
	th := v.m[path]
	if th != nil && want {
		v.tick++
		th.used = v.tick
	}
	if th != nil && (th.bm != nil || th.busy || th.failed) || !want || v.busy >= discThumbBusy {
		if th == nil {
			return nil, 0, 0
		}
		return th.bm, th.w, th.h
	}
	if th == nil {
		v.tick++
		th = &discThumb{used: v.tick}
		v.m[path] = th
	}
	th.busy = true
	v.busy++
	go func() {
		img, err := decodeDiscThumb(path)
		a.update(func() {
			v := &a.discThumbs
			v.busy--
			th.busy = false
			if err != nil {
				th.failed = true
				return
			}
			th.set(img)
			v.evict()
		})
	}()
	return nil, th.w, th.h
}

// set 放进解好的图，记下显示尺寸：解出来的是 2 倍像素，原图比框小时按它一半的点数显示，不放大。
func (th *discThumb) set(img *image.RGBA) {
	th.bm = ui.NewBitmap(img)
	w, h := float32(img.Bounds().Dx())/2, float32(img.Bounds().Dy())/2
	if k := min(float32(discThumbBox)/max(w, 1), float32(discThumbBox)/max(h, 1)); k < 1 {
		w, h = w*k, h*k
	}
	th.w, th.h = max(w, 24), max(h, 24)
}

// decodeDiscThumb 先只读文件头看尺寸，原图太大就不解，再解码并缩到缩略图的像素上限。
func decodeDiscThumb(path string) (*image.RGBA, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	st, err := f.Stat()
	if err == nil && st.Size() > discThumbMaxBytes {
		err = errImageTooLarge
	}
	var cfg image.Config
	if err == nil {
		cfg, _, err = image.DecodeConfig(f)
	}
	f.Close()
	if err != nil {
		return nil, err
	}
	if int64(cfg.Width)*int64(cfg.Height) > discThumbMaxSource {
		return nil, errImageTooLarge
	}
	pi, err := media.DecodePreview(path, discThumbPixels)
	if err != nil {
		return nil, err
	}
	return pi.Img, nil
}

var errImageTooLarge = errors.New("image too large to preview")

// evict 挤掉最久没看见的缩略图，留下 discThumbKeep 张；尺寸留着，滚回来时占位块不跳。
func (v *discThumbs) evict() {
	for {
		n, oldest, key := 0, int64(0), ""
		for k, th := range v.m {
			if th.bm == nil {
				continue
			}
			n++
			if key == "" || th.used < oldest {
				oldest, key = th.used, k
			}
		}
		if n <= discThumbKeep {
			return
		}
		v.m[key].bm = nil
	}
}

// discImage 画一张图片附件：没解好时是同样大小的占位块，只解码看得见（含上下一段预取）的。
// 点了用系统默认程序打开，右键复制图片、在访达中显示、用预览打开、复制路径。
func (a *app) discImage(c *ui.Context, key, abs string) {
	t, m := c.Theme(), L(c)
	box := ui.Box(c.Key(key)).Radius(rSmall).Background(colors(t).fill).Shrink(0).Role(ui.RoleButton).Label(filepath.Base(abs))
	// 位置是上一帧的：刚出现的还没排版，约一帧再看；消息区被挤成零高度时什么都看不见，不解也不约
	r, view := box.Bounds(), a.disc.view
	if r.W == 0 {
		c.After(30 * time.Millisecond)
	}
	want := r.W > 0 && view.H > 0 && r.Y+r.H >= view.Y-discThumbMargin && r.Y <= view.Y+view.H+discThumbMargin
	bm, w, h := a.discThumb(abs, want)
	if w == 0 {
		w, h = float32(discThumbBox)*0.6, float32(discThumbBox)*0.45
	}
	box.Size(w, h)
	box.Children(func() {
		if bm != nil {
			ui.Image(c, bm).Size(w, h).Fit(ui.Contain).Radius(rSmall)
		}
	})
	if box.Clicked() {
		mygo.Shell.OpenPath(abs)
	}
	contextMenu(c, box, func(pm *popMenu) {
		if pm.Item(m.ImagesCopyImage()).Icon(iconCopy).Chosen() {
			a.copyImage(m, imageItem{Item: media.Item{Path: abs}})
		}
		if pm.Item(m.CommonRevealInFinder()).Icon(iconFolder).Chosen() {
			mygo.Shell.ShowItemInFolder(abs)
		}
		if pm.Item(m.ImagesOpenInPreview()).Chosen() {
			go exec.Command("open", "-a", "Preview", abs).Run()
		}
		if pm.Item(m.CommonCopyPath()).Chosen() {
			mygo.Clipboard.WriteText(abs)
		}
	})
}
