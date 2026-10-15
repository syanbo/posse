package vt

import (
	"bytes"
	"strconv"
	"unicode/utf8"
	"unsafe"
)

// Snapshot 返回能在同尺寸终端里还原当前状态的 VT 序列。
//
// 模式、滚动区域、光标等状态沿用 libghostty 格式化输出的前后两段，中间的格子内容由这里逐格生成。
// 格式化输出的内容部分有两处缺陷：省略末尾空行（光标按绝对坐标恢复，回滚区非空时内容整体错位），
// 以及用空格填补被跳过的格子时沿用前一格的样式（例如 Claude Code 图标旁出现黑块）。
func (t *Terminal) Snapshot() []byte {
	full := t.VT()
	// 完整输出是"状态 + 内容 + 状态"，各项状态在内容前后的位置不固定，所以直接定位内容段替换；
	// 内容很短又出现多次时无法确定位置，退回原输出
	content := t.format(formatterOptions{emit: formatVT})
	i := bytes.Index(full, content)
	if i < 0 || (len(content) < 16 && bytes.Count(full, content) > 1) {
		return full
	}
	var out bytes.Buffer
	out.Grow(len(full) + len(full)/4)
	out.Write(full[:i])
	t.writeCells(&out)
	out.Write(full[i+len(content):])
	return out.Bytes()
}

const pointTagScreen = 2

// rowDataWrap 是 GHOSTTY_ROW_DATA_WRAP：该行软换行到下一行。
const rowDataWrap = 1

func (t *Terminal) refAt(x, y int) (GridRef, bool) {
	ref := GridRef{size: unsafe.Sizeof(GridRef{})}
	p := point{tag: pointTagScreen, x: uint16(x), y: uint32(y)}
	return ref, terminalGridRef(t.h, p, &ref) == 0
}

type snapCell struct {
	ref      GridRef
	tag      uint64
	cp       rune
	styleID  uint64
	wide     Wide
	link     bool
	bg       styleColor // 只有背景色的格子的颜色
	hasText  bool
	bgOnly   bool
	styled   bool // 没有文字但有样式
	writable bool
}

// cellAt 读取 row 所在行第 x 列的格子；同一行的格子在同一页里，只需改引用的列号。
func (t *Terminal) cellAt(row GridRef, x int) (snapCell, bool) {
	ref := row
	ref.x = uint16(x)
	var raw uint64
	if !ok(call(fnGridRefCell, uintptr(unsafe.Pointer(&ref)), uintptr(unsafe.Pointer(&raw)))) {
		return snapCell{}, false
	}
	c := snapCell{
		ref: ref, tag: cell.tag.of(raw), styleID: cell.styleID.of(raw),
		wide: Wide(cell.wide.of(raw)), link: cell.hyperlink.of(raw) != 0,
	}
	switch c.tag {
	case 0, 1:
		c.cp = rune(cell.codepoint.of(raw))
		c.hasText = c.cp != 0
	case 2:
		c.bgOnly = true
		c.bg = styleColor{tag: colorPalette}
		c.bg.value[0] = uint8(cell.paletteIndex.of(raw))
	case 3:
		c.bgOnly = true
		c.bg = styleColor{tag: colorRGB}
		c.bg.value[0], c.bg.value[1], c.bg.value[2] = uint8(cell.r.of(raw)), uint8(cell.g.of(raw)), uint8(cell.b.of(raw))
	}
	c.styled = !c.hasText && !c.bgOnly && c.styleID != 0
	c.writable = c.hasText || c.bgOnly || c.styled || c.link
	return c, true
}

func (t *Terminal) rowWrapped(ref GridRef) bool {
	var row uint64
	if !ok(call(fnGridRefRow, uintptr(unsafe.Pointer(&ref)), uintptr(unsafe.Pointer(&row)))) {
		return false
	}
	var wrapped bool
	call(fnRowDataGet, uintptr(row), rowDataWrap, uintptr(unsafe.Pointer(&wrapped)))
	return wrapped
}

// styleKey 标识一种样式：样式号只在所属页内有效。
type styleKey struct {
	node uintptr
	id   uint64
}

func (t *Terminal) cellStyle(c *snapCell, cache map[styleKey]style) style {
	if c.styleID == 0 {
		return style{size: unsafe.Sizeof(style{})}
	}
	k := styleKey{c.ref.node, c.styleID}
	if s, ok := cache[k]; ok {
		return s
	}
	s := style{size: unsafe.Sizeof(style{})}
	call(fnGridRefStyle, uintptr(unsafe.Pointer(&c.ref)), uintptr(unsafe.Pointer(&s)))
	cache[k] = s
	return s
}

func (t *Terminal) cellText(c *snapCell, buf []uint32, out []byte) []byte {
	if c.tag == 1 {
		var n uintptr
		if ok(call(fnGridRefGraphemes, uintptr(unsafe.Pointer(&c.ref)), uintptr(unsafe.Pointer(&buf[0])), uintptr(len(buf)), uintptr(unsafe.Pointer(&n)))) && n > 0 {
			for _, r := range buf[:n] {
				out = utf8.AppendRune(out, rune(r))
			}
			return out
		}
	}
	return utf8.AppendRune(out, c.cp)
}

func (t *Terminal) cellLink(c *snapCell) string {
	if !c.link {
		return ""
	}
	var buf [2048]byte
	var n uintptr
	if !ok(call(fnGridRefHyperlinkURI, uintptr(unsafe.Pointer(&c.ref)), uintptr(unsafe.Pointer(&buf[0])), uintptr(len(buf)), uintptr(unsafe.Pointer(&n)))) {
		return ""
	}
	return string(buf[:n])
}

// writeCells 逐行写出回滚区和屏幕的全部行（含末尾空行），使还原后回滚行数与屏幕布局一致。
// 行内被跳过的空格子用光标右移越过而不写入，只有背景色的格子用带背景色的擦除还原。
func (t *Terminal) writeCells(b *bytes.Buffer) {
	cols, _ := t.Size()
	total := int(t.Scrollbar().Total)
	var cur style // 当前已输出的样式（零值为默认）
	curSet := false
	link := ""
	var gbuf [16]uint32
	var text []byte
	styles := map[styleKey]style{}

	setStyle := func(s style) {
		if curSet && styleEqual(cur, s) {
			return
		}
		b.WriteString(sgr(s))
		cur, curSet = s, true
	}
	setLink := func(uri string) {
		if uri == link {
			return
		}
		b.WriteString("\x1b]8;;")
		b.WriteString(uri)
		b.WriteString("\x1b\\")
		link = uri
	}

	for y := range total {
		row, found := t.refAt(0, y)
		if !found {
			break
		}
		col := 0 // 还原时光标所在列
		for x := 0; x < cols; x++ {
			c, ok := t.cellAt(row, x)
			if !ok {
				break
			}
			if c.wide == SpacerTail || c.wide == SpacerHead || !c.writable {
				continue
			}
			if x > col {
				b.WriteString("\x1b[")
				b.WriteString(strconv.Itoa(x - col))
				b.WriteByte('C')
				col = x
			}
			setLink(t.cellLink(&c))
			if c.bgOnly {
				// 连续同色背景格合并成一次擦除
				n := 1
				for x+n < cols {
					next, ok := t.cellAt(row, x+n)
					if !ok || !next.bgOnly || next.bg != c.bg || next.link != c.link {
						break
					}
					n++
				}
				setStyle(style{size: unsafe.Sizeof(style{}), bg: c.bg})
				b.WriteString("\x1b[")
				b.WriteString(strconv.Itoa(n))
				b.WriteByte('X')
				x += n - 1
				continue // 擦除不移动光标，下一个可写格子前会右移
			}
			setStyle(t.cellStyle(&c, styles))
			if c.hasText {
				text = t.cellText(&c, gbuf[:], text[:0])
				b.Write(text)
			} else {
				b.WriteByte(' ')
			}
			col = x + 1
			if c.wide == WideChar {
				col++
			}
		}
		if y == total-1 {
			break
		}
		// 软换行的行写满到最后一列时靠自动换行衔接，保留换行标记以便尺寸变化时重排
		if col >= cols && t.rowWrapped(row) {
			if nextRow, found := t.refAt(0, y+1); found {
				if next, ok := t.cellAt(nextRow, 0); ok && next.hasText {
					continue
				}
			}
		}
		// 换行可能滚屏，新行按当前背景色填充（BCE），所以先清掉背景色
		if curSet && cur.bg.tag != 0 {
			setStyle(style{size: unsafe.Sizeof(style{})})
		}
		b.WriteString("\r\n")
	}
	setLink("")
	if curSet {
		b.WriteString("\x1b[0m")
	}
}

func styleEqual(a, b style) bool {
	a.size, b.size = 0, 0
	return a == b
}

// sgr 生成把画笔设为 s 的完整 SGR 序列（先重置）。
func sgr(s style) string {
	p := []byte("\x1b[0")
	add := func(v string) { p = append(p, ';'); p = append(p, v...) }
	if s.bold {
		add("1")
	}
	if s.faint {
		add("2")
	}
	if s.italic {
		add("3")
	}
	switch s.underline {
	case 0:
	case 1:
		add("4")
	default:
		add("4:" + strconv.Itoa(int(s.underline)))
	}
	if s.blink {
		add("5")
	}
	if s.inverse {
		add("7")
	}
	if s.invisible {
		add("8")
	}
	if s.strikethrough {
		add("9")
	}
	if s.overline {
		add("53")
	}
	add2 := func(c styleColor, base int) {
		switch c.tag {
		case colorPalette:
			add(strconv.Itoa(base+8) + ";5;" + strconv.Itoa(int(c.value[0])))
		case colorRGB:
			add(strconv.Itoa(base+8) + ";2;" + strconv.Itoa(int(c.value[0])) + ";" + strconv.Itoa(int(c.value[1])) + ";" + strconv.Itoa(int(c.value[2])))
		}
	}
	add2(s.fg, 30)
	add2(s.bg, 40)
	add2(s.underlineColor, 50)
	return string(append(p, 'm'))
}

func (t *Terminal) format(opts formatterOptions) []byte {
	opts.size = unsafe.Sizeof(formatterOptions{})
	opts.extra.size = unsafe.Sizeof(opts.extra)
	opts.extra.screen.size = unsafe.Sizeof(opts.extra.screen)
	var f uintptr
	if formatterTerminalNew(0, &f, t.h, opts) != 0 {
		return nil
	}
	defer call(fnFormatterFree, f)
	var p, n uintptr
	if !ok(call(fnFormatterFormatAlloc, f, 0, uintptr(unsafe.Pointer(&p)), uintptr(unsafe.Pointer(&n)))) {
		return nil
	}
	b := []byte(goString(p, n))
	if p != 0 {
		call(fnFree, 0, p, n)
	}
	return b
}
