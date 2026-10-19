package app

import "github.com/egoist/mygo/ui"

// dialogBox 让 Modal 对话框照 WebStorm 可以拖标题移动、拖四边和四角拉伸。
// 对话框有两种排版（如只有文件列表、右边带 diff），各记一份尺寸；同一次运行里再打开沿用，位置每次打开回到居中。
type dialogBox struct {
	mode   int
	dx, dy float32    // 相对居中的偏移
	w, h   [2]float32 // 拉伸过的尺寸，0 是按内容排
	min    [2]ui.Rect // 各排版最小的宽高（只用 W、H）
	last   ui.Rect    // 上一帧面板的位置和尺寸（含平移），第一次拉伸从它开始
	ax, ay float32    // 上一帧用的偏移
	win    ui.Rect    // 窗口的宽高，由居中的面板位置反推
}

// dialogMargin 是 Modal 面板离窗口边至少留的距离（mygo 的 MaxWidth 是窗口宽减两倍它）。
const dialogMargin = 20

// dialogPad 是 mygo Modal 面板的内边距（主题的 Space(5)）。
const dialogPad = 20

// reset 在打开对话框时调用：回到居中。
func (b *dialogBox) reset() { b.dx, b.dy, b.ax, b.ay = 0, 0, 0, 0 }

// sized 是当前排版拉伸过：内容要改成填满面板，不再用自己的固定尺寸。
func (b *dialogBox) sized() bool { return b.w[b.mode] > 0 }

// title 是对话框的标题行，按住拖动整个对话框。
func (b *dialogBox) title(c *ui.Context, text string) {
	row := ui.Row(c.Key("dialog-title")).AlignItems(ui.Center).Children(func() {
		titleText(c, text, fsCardTitle)
		ui.Spacer(c)
	})
	if dx, dy, held := row.Dragged(); held {
		b.move(dx, dy)
	}
}

func (b *dialogBox) move(dx, dy float32) {
	b.dx += dx
	b.dy += dy
	b.clampPos()
}

// grips 在 Modal 的内容最后调用，在面板四边和四角放拉伸的把手。
func (b *dialogBox) grips(c *ui.Context) {
	const edge, corner = 6, 14
	// 标题上面的内边距也能拖动整个对话框（先放，四边和四角的把手盖在它上面）
	top := ui.Box(c.Key("dialog-drag-top")).Absolute().Top(0).Left(0).Right(0).Height(dialogPad)
	if dx, dy, held := top.Dragged(); held {
		b.move(dx, dy)
	}
	grip := func(key string, sx, sy float32, cursor ui.Cursor) ui.Element {
		e := ui.Box(c.Key("dialog-grip-" + key)).Absolute().Cursor(cursor)
		if dx, dy, held := e.Dragged(); held {
			b.resize(sx*dx, sy*dy, sx, sy)
		}
		return e
	}
	grip("l", -1, 0, ui.CursorResizeEW).Left(0).Top(corner).Bottom(corner).Width(edge)
	grip("r", 1, 0, ui.CursorResizeEW).Right(0).Top(corner).Bottom(corner).Width(edge)
	grip("t", 0, -1, ui.CursorResizeNS).Top(0).Left(corner).Right(corner).Height(edge)
	grip("b", 0, 1, ui.CursorResizeNS).Bottom(0).Left(corner).Right(corner).Height(edge)
	grip("tl", -1, -1, ui.CursorResizeNWSE).Left(0).Top(0).Size(corner, corner)
	grip("br", 1, 1, ui.CursorResizeNWSE).Right(0).Bottom(0).Size(corner, corner)
	grip("tr", 1, -1, ui.CursorResizeNESW).Right(0).Top(0).Size(corner, corner)
	grip("bl", -1, 1, ui.CursorResizeNESW).Left(0).Bottom(0).Size(corner, corner)
}

// resize 按把手拖的方向改尺寸；对话框是居中排的，偏移跟着挪一半，对面的边才不动。
func (b *dialogBox) resize(dw, dh, sx, sy float32) {
	m := b.mode
	w, h := b.w[m], b.h[m]
	if w == 0 {
		w, h = b.last.W, b.last.H
	}
	// 不超过窗口（超过的部分 Modal 的 MaxWidth 会截掉，拖了没反应）
	nw := min(max(w+dw, b.min[m].W), max(b.win.W-2*dialogMargin, b.min[m].W))
	nh := min(max(h+dh, b.min[m].H), max(b.win.H-2*dialogMargin, b.min[m].H))
	b.dx += sx * (nw - w) / 2
	b.dy += sy * (nh - h) / 2
	b.w[m], b.h[m] = nw, nh
}

// clampPos 让标题留在窗口里，拖出去了还能拖回来。
func (b *dialogBox) clampPos() {
	if b.win.W == 0 {
		return
	}
	const keep = 80 // 左右至少留在窗口里的宽度
	mx := (b.win.W+b.last.W)/2 - keep
	b.dx = min(max(b.dx, -mx), mx)
	// 上边不出窗口，下边至少留出标题行
	b.dy = min(max(b.dy, -(b.win.H-b.last.H)/2), (b.win.H+b.last.H)/2-keep)
}

// apply 用在 Modal 返回的面板上：挪到拖到的位置、用拉伸过的尺寸，记下这一帧的尺寸。
func (b *dialogBox) apply(panel ui.Element) {
	// Bounds 是上一帧排好的，那一帧用的偏移是 ax、ay；面板在窗口里居中，反推出窗口尺寸
	if r := panel.Bounds(); r.W > 0 {
		b.last = r
		b.win = ui.Rect{W: 2*(r.X-b.ax) + r.W, H: 2*(r.Y-b.ay) + r.H}
	}
	panel.Translate(b.dx, b.dy)
	b.ax, b.ay = b.dx, b.dy
	if b.sized() {
		panel.Width(b.w[b.mode]).Height(b.h[b.mode])
	}
}
