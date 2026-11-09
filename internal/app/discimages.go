package app

import (
	"errors"
	"fmt"
	"image"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"time"

	"posse/internal/errtext"
	"posse/internal/media"
	"posse/locales"

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
	if err := checkDiscSource(path); err != nil {
		return nil, err
	}
	pi, err := media.DecodePreview(path, discThumbPixels)
	if err != nil {
		return nil, err
	}
	return pi.Img, nil
}

// checkDiscSource 只读文件头：文件或原图像素超过上限就报 errImageTooLarge。解码要先把整张原图解出来，
// 缩略图和大图都先过这一关。
func checkDiscSource(path string) error {
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer f.Close()
	if st, err := f.Stat(); err != nil {
		return err
	} else if st.Size() > discThumbMaxBytes {
		return errImageTooLarge
	}
	cfg, _, err := image.DecodeConfig(f)
	if err != nil {
		return err
	}
	if int64(cfg.Width)*int64(cfg.Height) > discThumbMaxSource {
		return errImageTooLarge
	}
	return nil
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
// 点了在窗口里看大图，右键见 discImageMenu。
func (a *app) discImage(c *ui.Context, key, abs string) {
	t := c.Theme()
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
		// 截图常是和消息底色一样的白底或黑底，描一圈才看得出图的边界；盖在图上面，不占图的位置
		ui.Box(c).Absolute().Top(0).Right(0).Bottom(0).Left(0).Radius(rSmall).BorderWidth(hairline).BorderColor(colors(t).line)
	})
	if box.Clicked() {
		a.openDiscImage(abs)
	}
	a.discImageMenu(c, box, abs)
}

// discImageMenu 是讨论里图片的右键菜单（缩略图和大图共用）：复制图片、存储为、在访达中显示、用预览打开、复制路径。
func (a *app) discImageMenu(c *ui.Context, el ui.Element, abs string) {
	m := L(c)
	contextMenu(c, el, func(pm *popMenu) {
		if pm.Item(m.ImagesCopyImage()).Icon(iconCopy).Chosen() {
			a.copyImage(m, imageItem{Item: media.Item{Path: abs}})
		}
		if pm.Item(m.ImagesSaveAs()).Icon(iconSave).Chosen() {
			a.saveImageAs(m, abs)
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

// saveImageAs 让用户选位置，把附件复制一份过去；默认放在下载目录、用附件原来的名字。
func (a *app) saveImageAs(m locales.Messages, abs string) {
	name := filepath.Base(abs)
	def := name
	if home, err := os.UserHomeDir(); err == nil {
		def = filepath.Join(home, "Downloads", name)
	}
	dst, err := mygo.Dialog.Save(mygo.SaveDialogOptions{Parent: a.win, Title: m.ImagesSaveAs(), DefaultPath: def, CreateDirectories: true})
	if err != nil || dst == "" || dst == abs {
		return
	}
	go func() {
		err := copyFileTo(abs, dst)
		a.update(func() {
			if err != nil {
				a.err = errtext.Text(err)
			}
		})
	}()
}

// copyFileTo 先写到同目录的临时文件再改名：中途失败不留下半截的文件，也不毁掉被覆盖的那个。
func copyFileTo(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	tmp, err := os.CreateTemp(filepath.Dir(dst), ".posse-save-*")
	if err != nil {
		return err
	}
	_, err = io.Copy(tmp, in)
	if cerr := tmp.Close(); err == nil {
		err = cerr
	}
	if err == nil {
		err = os.Chmod(tmp.Name(), 0o644)
	}
	if err == nil {
		err = os.Rename(tmp.Name(), dst)
	}
	if err != nil {
		os.Remove(tmp.Name())
	}
	return err
}

// discViewer 是讨论里点开的大图：盖在窗口上，Esc 或点外面关掉，关了就放掉位图。
type discViewer struct {
	open bool
	path string
	bm   *ui.Bitmap
	size image.Point // 原图像素
}

// discViewerMaxPixels 是大图解码的像素上限：够铺满 2 倍屏上的大窗口，一张最多约 64 MB
const discViewerMaxPixels = 16 << 20

// openDiscImage 打开大图，在后台按原图解码（超过上限才缩小）；解不开的用系统默认程序打开。
func (a *app) openDiscImage(abs string) {
	a.discView = discViewer{open: true, path: abs}
	go func() {
		pi, err := decodeDiscFull(abs)
		a.update(func() {
			v := &a.discView
			if v.path != abs || !v.open {
				return
			}
			if err != nil {
				v.open = false
				mygo.Shell.OpenPath(abs)
				return
			}
			v.bm, v.size = ui.NewBitmap(pi.Img), pi.Size
		})
	}()
}

// decodeDiscFull 解大图：缩略图还没解完时也能点开，所以这里同样先查原图大小。
func decodeDiscFull(path string) (media.PreviewImage, error) {
	if err := checkDiscSource(path); err != nil {
		return media.PreviewImage{}, err
	}
	return media.DecodePreview(path, discViewerMaxPixels)
}

// discImageDialog 画大图：原图按 2 倍屏的点数显示，放不下等比缩到窗口里，不放大小图；下面一行是名字和操作。
func (a *app) discImageDialog(c *ui.Context) {
	v := &a.discView
	if !v.open {
		if v.bm != nil || v.path != "" {
			a.discView = discViewer{}
		}
		return
	}
	t, m := c.Theme(), L(c)
	abs := v.path
	panel := ui.Modal(c, &v.open, func() {
		ww, wh := c.Size()
		maxW, maxH := max(ww-96, 160), max(wh-96-sz(28)-8-24, 120)
		w, h := float32(discThumbBox), float32(discThumbBox)*0.75
		if v.bm != nil {
			w, h = float32(v.size.X)/2, float32(v.size.Y)/2
		}
		if k := min(maxW/max(w, 1), maxH/max(h, 1)); k < 1 {
			w, h = w*k, h*k
		}
		img := ui.Box(c.Key("disc-view")).Size(w, h).Radius(rSmall).Background(colors(t).fill).Center().Label(filepath.Base(abs))
		img.Children(func() {
			if v.bm != nil {
				ui.Image(c, v.bm).Size(w, h).Fit(ui.Contain).Radius(rSmall)
			}
		})
		a.discImageMenu(c, img, abs)
		ui.Row(c).Gap(4).AlignItems(ui.Center).Width(max(w, 320)).Children(func() {
			ui.Text(c, filepath.Base(abs)).FontSize(fsSmall).TextColor(t.TextMuted).SingleLine().Shrink(1)
			if v.size.X > 0 {
				ui.Text(c, fmt.Sprintf("%d × %d", v.size.X, v.size.Y)).FontSize(fsSmall).FontFeatures("tnum").TextColor(colors(t).faint).Shrink(0).Margin(0, 0, 0, 4)
			}
			ui.Spacer(c)
			if iconButton(c, iconCopy, m.ImagesCopyImage()).Clicked() {
				a.copyImage(m, imageItem{Item: media.Item{Path: abs}})
			}
			if iconButton(c, iconSave, m.ImagesSaveAs()).Clicked() {
				a.saveImageAs(m, abs)
			}
			if iconButton(c, iconFolder, m.CommonRevealInFinder()).Clicked() {
				mygo.Shell.ShowItemInFolder(abs)
			}
			if iconButton(c, iconOpen, m.ImagesOpenInPreview()).Clicked() {
				go exec.Command("open", "-a", "Preview", abs).Run()
			}
		})
	})
	panel.Padding(12).Gap(8)
}
