package vt

import "unsafe"

// 只有测试用 RenderState 按界面的方式读回屏幕，与快照还原后的结果比较；常驻进程不需要它，
// 所以它的函数在测试里才加入加载列表。

var (
	fnRenderStateNew, fnRenderStateFree, fnRenderStateBeginUpdate, fnRenderStateEndUpdate, fnRenderStateGet,
	fnRowIteratorNew, fnRowIteratorFree, fnRowIteratorNext, fnRowGet,
	fnRowCellsNew, fnRowCellsFree, fnRowCellsSelect, fnRowCellsGet uintptr
)

func init() {
	for _, s := range []struct {
		name string
		addr *uintptr
	}{
		{"ghostty_render_state_new", &fnRenderStateNew},
		{"ghostty_render_state_free", &fnRenderStateFree},
		{"ghostty_render_state_begin_update", &fnRenderStateBeginUpdate},
		{"ghostty_render_state_end_update", &fnRenderStateEndUpdate},
		{"ghostty_render_state_get", &fnRenderStateGet},
		{"ghostty_render_state_row_iterator_new", &fnRowIteratorNew},
		{"ghostty_render_state_row_iterator_free", &fnRowIteratorFree},
		{"ghostty_render_state_row_iterator_next", &fnRowIteratorNext},
		{"ghostty_render_state_row_get", &fnRowGet},
		{"ghostty_render_state_row_cells_new", &fnRowCellsNew},
		{"ghostty_render_state_row_cells_free", &fnRowCellsFree},
		{"ghostty_render_state_row_cells_select", &fnRowCellsSelect},
		{"ghostty_render_state_row_cells_get", &fnRowCellsGet},
	} {
		symbols = append(symbols, s)
	}
}

// RenderState is a GhosttyRenderState: what a frame draws of a terminal.
type RenderState struct {
	h     uintptr
	rows  uintptr // the row iterator
	cells uintptr // the cells of the iterator's row
	// raw holds the cells of the row read last.
	raw []Cell
}

// Render state data (GhosttyRenderStateData and its row and cell kinds).
const (
	rsDataRowIterator = 4

	rowDataCells    = 3
	rowDataCellsRaw = 5

	cellDataStyle = 2
)

// cellsView is a GhosttyCellsView.
type cellsView struct {
	ptr, len uintptr
}

func NewRenderState() (*RenderState, error) {
	r := &RenderState{}
	if err := result(call(fnRenderStateNew, 0, uintptr(unsafe.Pointer(&r.h)))); err != nil {
		return nil, err
	}
	if err := result(call(fnRowIteratorNew, 0, uintptr(unsafe.Pointer(&r.rows)))); err != nil {
		r.Free()
		return nil, err
	}
	if err := result(call(fnRowCellsNew, 0, uintptr(unsafe.Pointer(&r.cells)))); err != nil {
		r.Free()
		return nil, err
	}
	return r, nil
}

func (r *RenderState) Free() {
	if r.cells != 0 {
		call(fnRowCellsFree, r.cells)
	}
	if r.rows != 0 {
		call(fnRowIteratorFree, r.rows)
	}
	if r.h != 0 {
		call(fnRenderStateFree, r.h)
	}
	*r = RenderState{}
}

// Update updates the render state from the terminal.
func (r *RenderState) Update(t *Terminal) error {
	if err := result(call(fnRenderStateBeginUpdate, r.h, t.h)); err != nil {
		return err
	}
	call(fnRenderStateEndUpdate, r.h)
	return nil
}

// Rows starts going through the rows of the frame, top to bottom, with
// NextRow.
func (r *RenderState) Rows() {
	call(fnRenderStateGet, r.h, rsDataRowIterator, uintptr(unsafe.Pointer(&r.rows)))
}

// NextRow moves to the next row, and reports whether there is one.
func (r *RenderState) NextRow() bool { return cbool(call(fnRowIteratorNext, r.rows)) }

func (r *RenderState) rowGet(data int, out unsafe.Pointer) bool {
	return ok(call(fnRowGet, r.rows, uintptr(data), uintptr(out)))
}

// Cell is a cell of a row.
type Cell struct {
	Codepoint     rune
	Grapheme      bool
	Style         uint16
	Wide          Wide
	Hyperlink     bool
	Background    RGB
	HasBackground bool
	Palette       bool
}

// RowCells returns the cells of the row, valid until the next update.
func (r *RenderState) RowCells() []Cell {
	var v cellsView
	if !r.rowGet(rowDataCellsRaw, unsafe.Pointer(&v)) || v.ptr == 0 {
		return nil
	}
	raw := unsafe.Slice((*uint64)(mem(v.ptr)), v.len)
	r.raw = r.raw[:0]
	for _, c := range raw {
		var out Cell
		switch cell.tag.of(c) {
		case 0, 1: // a codepoint, of a grapheme cluster for 1
			out.Codepoint = rune(cell.codepoint.of(c))
			out.Grapheme = cell.tag.of(c) == 1
		case 2:
			out.HasBackground, out.Palette = true, true
			out.Background.R = uint8(cell.paletteIndex.of(c))
		case 3:
			out.HasBackground = true
			out.Background = RGB{uint8(cell.r.of(c)), uint8(cell.g.of(c)), uint8(cell.b.of(c))}
		}
		out.Style = uint16(cell.styleID.of(c))
		out.Wide = Wide(cell.wide.of(c))
		out.Hyperlink = cell.hyperlink.of(c) != 0
		r.raw = append(r.raw, out)
	}
	// The cells of the row for the style lookups.
	r.rowGet(rowDataCells, unsafe.Pointer(&r.cells))
	return r.raw
}

// Color is the color of a style: none (the default), an index of the
// palette, or an RGB color.
type Color struct {
	Kind  uint8 // 0 none, 1 palette, 2 RGB
	Index uint8
	RGB   RGB
}

func (c styleColor) color() Color {
	switch c.tag {
	case colorPalette:
		return Color{Kind: 1, Index: c.value[0]}
	case colorRGB:
		return Color{Kind: 2, RGB: RGB{c.value[0], c.value[1], c.value[2]}}
	}
	return Color{}
}

// Style is how a cell's text shows.
type Style struct {
	FG, BG, UnderlineColor                                                  Color
	Bold, Italic, Faint, Blink, Inverse, Invisible, Strikethrough, Overline bool
	Underline                                                               int
}

// CellStyle returns the style of cell x of the row RowCells read.
func (r *RenderState) CellStyle(x int) Style {
	s := style{size: unsafe.Sizeof(style{})}
	if ok(call(fnRowCellsSelect, r.cells, uintptr(x))) {
		call(fnRowCellsGet, r.cells, cellDataStyle, uintptr(unsafe.Pointer(&s)))
	}
	return Style{
		FG: s.fg.color(), BG: s.bg.color(), UnderlineColor: s.underlineColor.color(),
		Bold: s.bold, Italic: s.italic, Faint: s.faint, Blink: s.blink, Inverse: s.inverse,
		Invisible: s.invisible, Strikethrough: s.strikethrough, Overline: s.overline, Underline: int(s.underline),
	}
}
