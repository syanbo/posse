package app

import (
	"fmt"
	"image"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/egoist/mygo/plugins/terminal"
	"github.com/egoist/mygo/ui"
)

func TestResolveLinkPath(t *testing.T) {
	home := t.TempDir()
	setHome(t, home)
	base := filepath.Join(home, "proj")
	os.MkdirAll(filepath.Join(base, "internal/app"), 0o755)
	os.WriteFile(filepath.Join(base, "internal/app/view.go"), nil, 0o644)
	file := filepath.Join(base, "internal/app/view.go")
	for link, want := range map[string]string{
		"internal/app/view.go":        file,
		"internal/app/view.go:42":     file,
		"internal/app/view.go:42:7":   file,
		"./internal/app/view.go":      file,
		"../proj/internal/app":        filepath.Join(base, "internal/app"),
		file:                          file,
		"~/proj/internal/app/view.go": file,
		"~/proj":                      base,
		"internal/app/missing.go":     "",
	} {
		if got := resolveLinkPath(link, base); got != want {
			t.Errorf("resolveLinkPath(%q) = %q, want %q", link, got, want)
		}
	}
	if got := resolveLinkPath("internal/app/view.go", ""); got != "" {
		t.Errorf("relative path without base = %q", got)
	}
}

// file:// 后面直接拼的路径里有 #、? 或单独的 %：url.Parse 截掉或报错时按原文找到文件；百分号编码的照常解码。
func TestFileLinkPath(t *testing.T) {
	dir := t.TempDir()
	a := &app{}
	for _, name := range []string{"a#b.png", "a?b.png", "a b.png", "100%.png", "a%zz#b.png"} {
		file := filepath.Join(dir, name)
		os.WriteFile(file, nil, 0o644)
		links := []string{"file://" + file, "file://localhost" + file}
		if name == "a#b.png" {
			links = append(links, "file://"+dir+"/a%23b.png")
		}
		if name == "a b.png" {
			links = append(links, "file://"+dir+"/a%20b.png")
		}
		for _, link := range links {
			if got, local := a.linkPath(nil, link); got != file || !local {
				t.Errorf("linkPath(%q) = %q %v, want %q", link, got, local, file)
			}
		}
	}
	if got, local := a.linkPath(nil, "file://"+dir+"/gone#x.png"); got != "" || !local {
		t.Errorf("missing file = %q %v", got, local)
	}
}

// 在真的终端里点链接：OSC 8 超链接（Claude 的 [Image #N] 就是这样指向 file:// 的）和打印出来的路径单击弹出链接卡片，
// Cmd+点击直接打开（文件在文件标签里，设置成单击直接打开时反过来）；从链接上拖动是选文字，不打开。网址单击弹出卡片，Cmd+点击在网页标签里打开，Shift+Cmd+点击交给浏览器。
func TestClickOpensLinkCard(t *testing.T) {
	dir := t.TempDir()
	file := filepath.Join(dir, "notes.txt")
	os.WriteFile(file, nil, 0o644)
	sub := filepath.Join(dir, "sub")
	os.Mkdir(sub, 0o755)

	a := previewApp(t)
	v := &termView{id: "t1"}
	var calls []string
	done := make(chan struct{})
	t.Cleanup(func() { close(done) })
	term, err := terminal.New(terminal.Options{Conn: nopConn{done}, Font: a.termFont(),
		LinkClick: func(link string, mods ui.Modifiers) bool {
			calls = append(calls, link)
			return a.openLink(v, link, mods)
		},
		LinkUsable: func(link string) bool { return a.linkUsable(v, link) }})
	if err != nil {
		t.Skip("terminal unavailable:", err)
	}
	t.Cleanup(func() { term.Close() })
	v.term = term
	a.panes[0] = v
	var feed string
	for range 4 {
		feed += "\x1b]8;;file://" + file + "\x1b\\[Image #1]\x1b]8;;\x1b\\\r\n"
	}
	// 路径后面跟着中文句号，句号不算路径
	for range 4 {
		feed += sub + "。\r\n"
	}
	// 第 8–11 行：指向不存在文件的超链接和路径，不能点
	for range 2 {
		feed += "\x1b]8;;file://" + dir + "/gone.png\x1b\\[Image #2]\x1b]8;;\x1b\\\r\n"
	}
	for range 2 {
		feed += dir + "/gone.txt\r\n"
	}
	// 第 12–13 行：网址
	const site = "https://play.google.com/store/apps/details?id=com.ironhidegames.android.kingdomrush"
	for range 2 {
		feed += site + "\r\n"
	}
	term.Feed([]byte(feed))
	tt := newPreviewTester(a, true)

	// 第 0–3 行是超链接，第 4–7 行是路径；行高约 18.5，第一行约在 y=51
	// 从链接上拖开：选文字，不打开
	tt.Move(termX(42), 80)
	tt.Frame()
	tt.Press(termX(42), 80)
	tt.Frame()
	tt.Move(termX(82), 120)
	tt.Frame()
	tt.Release(termX(82), 120)
	tt.Frame()
	if a.linkCard != nil {
		t.Fatal("drag from a link opened the card")
	}
	// 悬停时链接下面有下划线
	tt.Move(termX(42), 80)
	tt.Frame()
	tt.Frame()
	if os.Getenv("POSSE_PREVIEW_DIR") != "" {
		savePNG(t, tt, "link-hover.png")
	}
	// 原地单击打开
	tt.ClickAt(termX(42), 80)
	tt.Frame()
	tt.Frame()
	if a.linkCard == nil || a.linkCard.path != file || a.linkCard.isDir {
		t.Fatalf("plain click on hyperlink: %+v err=%q calls=%q", a.linkCard, a.err, calls)
	}
	tt.Frame()
	if os.Getenv("POSSE_PREVIEW_DIR") != "" {
		tt.Advance(time.Second)
		savePNG(t, tt, "link-path-card.png")
	}
	if !a.linkCard.placed {
		t.Fatal("card not placed")
	}
	// 点卡片外面关掉
	tt.ClickAt(600, 600)
	tt.Frame()
	if a.linkCard != nil {
		t.Fatal("outside click did not close the card")
	}
	// Cmd+点击当前工作区外的目录：文件栏里显示不了，弹卡片
	cmdClick(tt, termX(42), 155)
	if a.linkCard == nil || a.linkCard.path != sub || !a.linkCard.isDir {
		t.Fatalf("Cmd+click on path: %+v", a.linkCard)
	}
	a.linkCard = nil
	tt.Frame()
	// 设置成单击直接打开：反过来，Cmd+点击弹卡片
	a.state.LinkClickOpens = true
	cmdClick(tt, termX(42), 80)
	if a.linkCard == nil || a.linkCard.path != file {
		t.Fatalf("Cmd+click with LinkClickOpens: %+v", a.linkCard)
	}
	a.linkCard = nil
	a.state.LinkClickOpens = false
	tt.Frame()

	// 不存在的文件：单击和 Cmd+点击都没反应（第 8 行超链接、第 10 行路径）
	calls = nil
	for _, y := range []float32{51 + 8.5*18.5, 51 + 10.5*18.5} {
		tt.ClickAt(termX(42), y)
		tt.Frame()
		cmdClick(tt, termX(42), y)
	}
	if a.linkCard != nil || len(calls) != 0 || a.err != "" {
		t.Fatalf("missing files opened: card %+v calls %q err %q", a.linkCard, calls, a.err)
	}

	// 网址：单击弹出卡片、不打开，Cmd+点击直接打开、不弹卡片
	urlY := float32(51 + 12.5*18.5)
	tt.ClickAt(termX(42), urlY)
	tt.Frame()
	tt.Frame()
	if a.linkCard == nil || a.linkCard.url != site || len(tt.OpenedURLs()) != 0 {
		t.Fatalf("plain click on URL: %+v opened %q calls %q", a.linkCard, tt.OpenedURLs(), calls)
	}
	if os.Getenv("POSSE_PREVIEW_DIR") != "" {
		tt.Advance(time.Second)
		savePNG(t, tt, "link-url-card.png")
	}
	a.linkCard = nil
	tt.Frame()
	// 本地网页的卡片：最前面是在网页标签里打开
	page := filepath.Join(dir, "report.html")
	if err := os.WriteFile(page, []byte("<h1>hi</h1>"), 0o644); err != nil {
		t.Fatal(err)
	}
	a.openLink(v, page, 0)
	tt.Frame()
	if a.linkCard == nil || a.linkCard.path != page || a.linkCard.ws == nil {
		t.Fatalf("plain click on HTML: card %+v", a.linkCard)
	}
	if os.Getenv("POSSE_PREVIEW_DIR") != "" {
		tt.Advance(time.Second)
		savePNG(t, tt, "link-html-card.png")
	}
	a.linkCard = nil
	tt.Frame()
	// Shift+Cmd+点击：交给浏览器，不开网页标签
	tt.HoldModifiers(ui.Cmd | ui.Shift)
	tt.Move(termX(42), urlY)
	tt.Frame()
	tt.ClickAtWith(ui.Cmd|ui.Shift, termX(42), urlY)
	tt.Frame()
	tt.HoldModifiers(0)
	tt.Frame()
	if a.linkCard != nil || len(tt.OpenedURLs()) != 1 || tt.OpenedURLs()[0] != site || a.files[a.selected] != nil && len(a.files[a.selected].docs) != 0 {
		t.Fatalf("Shift+Cmd+click on URL: card %+v opened %q", a.linkCard, tt.OpenedURLs())
	}
	// Cmd+点击：在网页标签里打开
	cmdClick(tt, termX(42), urlY)
	fv := a.files[a.selected]
	if a.linkCard != nil || len(tt.OpenedURLs()) != 1 || fv == nil || fv.cur == nil || fv.cur.web == nil || fv.cur.web.url != site {
		t.Fatalf("Cmd+click on URL: card %+v opened %q files %+v", a.linkCard, tt.OpenedURLs(), fv)
	}

	// 直接打开文件：Cmd+点击，或设置成单击直接打开时的单击，在文件标签里打开、跳到 :行
	// 设置互换时 Shift+Cmd+点击在 Posse 里打开
	for _, c := range []struct {
		opens, swap bool
		mods        ui.Modifiers
	}{{false, false, ui.Cmd}, {true, false, 0}, {false, true, ui.Cmd | ui.Shift}, {true, true, ui.Cmd | ui.Shift}} {
		a.state.LinkClickOpens, a.state.LinkSwapSystem = c.opens, c.swap
		delete(a.files, a.selected)
		a.openLink(v, file+":1", c.mods)
		if fv := a.files[a.selected]; a.linkCard != nil || fv == nil || fv.cur == nil || fv.cur.path != file {
			t.Fatalf("open file directly (LinkClickOpens=%v LinkSwapSystem=%v): card %+v files %+v", c.opens, c.swap, a.linkCard, fv)
		}
	}
	a.state.LinkClickOpens, a.state.LinkSwapSystem = false, false
	// 本地网页直接打开的是网页标签，源码从卡片的「在文件标签中打开」看
	delete(a.files, a.selected)
	a.openLink(v, page, ui.Cmd)
	if fv := a.files[a.selected]; a.linkCard != nil || fv == nil || fv.cur == nil || fv.cur.web == nil || fv.cur.web.url != fileURL(page) {
		t.Fatalf("open HTML directly: card %+v files %+v", a.linkCard, fv)
	}
	// 没有工作区能放这个文件：弹卡片，不是点了没反应
	sel := a.selected
	a.selected = "gone"
	a.openLink(&termView{id: "gone"}, file, ui.Cmd)
	if a.linkCard == nil || a.linkCard.path != file {
		t.Fatalf("open file with no workspace: card %+v", a.linkCard)
	}
	a.selected, a.linkCard = sel, nil
}

func TestLinkLine(t *testing.T) {
	for link, want := range map[string]int{
		"/tmp/a.go:12":         12,
		"a.go:3:4":             3,
		"/tmp/a.go":            0,
		"file:///tmp/log:2024": 0,
	} {
		if got := linkLine(link); got != want {
			t.Errorf("linkLine(%q) = %d, want %d", link, got, want)
		}
	}
}

// 程序开着鼠标跟踪（Claude、Codex 都开）时，从链接上拖开照样选文字，程序收不到这次按下、移动和松开；原地单击仍打开链接。
func TestDragFromLinkWithMouseTracking(t *testing.T) {
	dir := t.TempDir()
	file := filepath.Join(dir, "notes.txt")
	os.WriteFile(file, nil, 0o644)

	a := previewApp(t)
	v := &termView{id: "t1"}
	done := make(chan struct{})
	t.Cleanup(func() { close(done) })
	conn := &recordConn{nopConn: nopConn{done}}
	var opened []string
	term, err := terminal.New(terminal.Options{Conn: conn, Font: a.termFont(),
		OpenLink:   func(link string) bool { opened = append(opened, link); return true },
		LinkUsable: func(link string) bool { return a.linkUsable(v, link) }})
	if err != nil {
		t.Skip("terminal unavailable:", err)
	}
	t.Cleanup(func() { term.Close() })
	v.term = term
	a.panes[0] = v
	feed := "\x1b[?1000h\x1b[?1006h"
	for range 4 {
		feed += "\x1b]8;;file://" + file + "\x1b\\[Image #1]\x1b]8;;\x1b\\\r\n"
	}
	term.Feed([]byte(feed))
	tt := newPreviewTester(a, true)

	tt.Move(termX(42), 80)
	tt.Frame()
	tt.Press(termX(42), 80)
	tt.Frame()
	tt.Move(termX(92), 120)
	tt.Frame()
	tt.Release(termX(92), 120)
	tt.Frame()
	if len(opened) != 0 {
		t.Fatalf("drag from a link opened it: %q", opened)
	}
	if sel := term.Selection(); !strings.Contains(sel, "Image #1") {
		t.Fatalf("drag from a link selected %q", sel)
	}
	if w := conn.written(); strings.Contains(w, "\x1b[<") {
		t.Fatalf("program got mouse reports %q", w)
	}

	tt.Move(termX(42), 80)
	tt.Frame()
	tt.ClickAt(termX(42), 80)
	tt.Frame()
	if len(opened) != 1 || term.Selection() != "" {
		t.Fatalf("click on a link: opened %q selection %q", opened, term.Selection())
	}
	if w := conn.written(); strings.Contains(w, "\x1b[<") {
		t.Fatalf("program got mouse reports %q", w)
	}
}

// recordConn 记下终端写给程序的输入。
type recordConn struct {
	nopConn
	mu  sync.Mutex
	out []byte
}

func (c *recordConn) Write(p []byte) (int, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.out = append(c.out, p...)
	return len(p), nil
}

func (c *recordConn) written() string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return string(c.out)
}

// cmdClick 按住 Cmd 移到 (x, y) 再点，像用户那样：终端按指针下的格子和修饰键判断是不是在点链接。
func cmdClick(tt *ui.Tester, x, y float32) {
	tt.HoldModifiers(ui.Cmd)
	tt.Move(x, y)
	tt.Frame()
	tt.ClickAtWith(ui.Cmd, x, y)
	tt.Frame()
	tt.HoldModifiers(0)
	tt.Frame()
}

// 点在终端下半部分时卡片往上弹，卡片的下边紧挨着点击处，矮的（文件夹）高的（图片）都一样；上半部分往下弹。
func TestLinkCardPlacement(t *testing.T) {
	dir := t.TempDir()
	for _, tc := range []struct {
		name    string
		y       float32
		preview bool
	}{
		{"folder low", 560, false},
		{"image low", 560, true},
		{"folder high", 100, false},
		{"tall image low", 560, true},
		{"tall image middle", 330, true},
	} {
		a := previewApp(t)
		card := &linkCard{path: dir, isDir: !tc.preview, x: 200, y: tc.y, placed: true}
		if tc.preview {
			w, h := 640, 400
			if strings.HasPrefix(tc.name, "tall") {
				w, h = 600, 1300 // 手机截图
			}
			card.preview = ui.NewBitmap(image.NewRGBA(image.Rect(0, 0, w, h)))
		}
		a.linkCard = card
		tt := newPreviewTester(a, true)
		tt.Frame()
		tt.Frame() // 第二帧按量出的卡片高度压图片
		// 最后一行：文件夹是"在访达中打开"，文件是"在访达中显示"
		last := "在访达中打开"
		if !card.isDir {
			last = "在访达中显示"
		}
		r, ok := tt.Find(last)
		if !ok {
			t.Fatalf("%s: card missing", tc.name)
		}
		top, ok := tt.Find(shortPath(dir))
		if !ok {
			t.Fatalf("%s: path missing", tc.name)
		}
		// 终端区域从窗口 y≈40 开始；卡片的 card.y 相对终端区域
		click := 40 + tc.y
		// 整张卡片都在终端区域里（窗口 760 高，底下是状态栏）
		if top.Y < 40 || r.Y+r.H > 730 {
			t.Errorf("%s: card spans %.0f..%.0f, outside the terminal", tc.name, top.Y, r.Y+r.H)
		}
		if tc.name == "tall image middle" {
			continue
		}
		if tc.y > 300 {
			if bottom := r.Y + r.H; bottom > click || click-bottom > 40 {
				t.Errorf("%s: card bottom %.0f, click at %.0f", tc.name, bottom, click)
			}
		} else if top.Y < click || top.Y-click > 40 {
			t.Errorf("%s: card top %.0f, click at %.0f", tc.name, top.Y, click)
		}
	}
}

// 悬停预览放进终端区域、不盖住指针；竖图放在指针旁边，比只放上下大。
func TestPlaceHoverPreview(t *testing.T) {
	const bw, bh = 1000, 800
	for _, tc := range []struct {
		name         string
		x, y, iw, ih float32
		side         bool // 应该在指针左右
	}{
		{"wide top", 300, 100, 1600, 900, false},
		{"wide bottom", 300, 700, 1600, 900, false},
		{"phone middle", 300, 400, 1179, 2556, true},
		{"phone right edge", 950, 300, 1179, 2556, true},
		{"long screenshot", 300, 100, 1000, 20000, true},
		{"phone near top", 500, 30, 1179, 2556, true},
	} {
		r := placeHoverPreview(bw, bh, tc.x, tc.y, tc.iw, tc.ih)
		if r.X < 8 || r.Y < 8 || r.X+r.W > bw-8 || r.Y+r.H > bh-8 {
			t.Errorf("%s: %+v outside area", tc.name, r)
		}
		if tc.x >= r.X && tc.x <= r.X+r.W && tc.y >= r.Y && tc.y <= r.Y+r.H {
			t.Errorf("%s: %+v covers pointer", tc.name, r)
		}
		iw, ih := r.W-2*hoverPreviewPad, r.H-2*hoverPreviewPad
		if d := iw/ih - tc.iw/tc.ih; d > 0.01 || d < -0.01 {
			t.Errorf("%s: aspect %v, want %v", tc.name, iw/ih, tc.iw/tc.ih)
		}
		if side := tc.y >= r.Y && tc.y <= r.Y+r.H; side != tc.side {
			t.Errorf("%s: %+v side=%v", tc.name, r, side)
		}
	}
	// 竖图用满终端的高度
	if r := placeHoverPreview(bw, bh, 300, 400, 1179, 2556); r.H < bh-20 {
		t.Errorf("phone preview only %v high", r.H)
	}
	// 宽随终端变：大终端里横图比原来的 360 宽，最多 800
	if r := placeHoverPreview(2000, 1000, 500, 550, 2624, 2024); r.W < 700 || r.W > hoverPreviewMaxWidth {
		t.Errorf("wide preview in a big terminal is %v wide", r.W)
	}
	if r := placeHoverPreview(600, 800, 100, 100, 1600, 900); r.W > hoverPreviewMinWidth {
		t.Errorf("preview in a narrow terminal is %v wide", r.W)
	}
	// 小图不放大到糊
	if r := placeHoverPreview(2000, 1000, 500, 300, 200, 200); r.W > hoverPreviewMinWidth {
		t.Errorf("small image blown up to %v", r.W)
	}
}

// 路径后面紧跟全角标点和中文（中间没有空格）时，标点和后面的字不算路径，链接照样能用。
func TestPathBeforeFullWidthPunct(t *testing.T) {
	dir := t.TempDir()
	file := filepath.Join(dir, "a.md")
	os.WriteFile(file, nil, 0o644)

	a := previewApp(t)
	v := &termView{id: "t1"}
	done := make(chan struct{})
	t.Cleanup(func() { close(done) })
	var hovered []string
	term, err := terminal.New(terminal.Options{Conn: nopConn{done}, Font: a.termFont(),
		LinkClick:   func(link string, mods ui.Modifiers) bool { return a.openLink(v, link, mods) },
		LinkUsable:  func(link string) bool { return a.linkUsable(v, link) },
		OnHoverLink: func(link string) { hovered = append(hovered, link) }})
	if err != nil {
		t.Skip("terminal unavailable:", err)
	}
	t.Cleanup(func() { term.Close() })
	v.term = term
	a.panes[0] = v
	// 路径照 Claude Code 的代码片段带颜色，悬停的下划线跟文字同色
	term.Feed([]byte("见 (\x1b[38;2;177;185;249m" + file + ")\x1b[39m，去掉右括号后展开\r\n"))
	tt := newPreviewTester(a, true)
	for x := float32(250); x < 1100; x += 6 {
		tt.Move(x, 51)
		tt.Frame()
	}
	if len(hovered) == 0 || hovered[0] != file {
		t.Fatalf("hovered %q, want %q", hovered, file)
	}
	if os.Getenv("POSSE_PREVIEW_DIR") != "" {
		tt.Move(400, 51)
		tt.Frame()
		tt.Frame()
		savePNG(t, tt, "link-hover-color.png")
	}
}

func TestWebAddress(t *testing.T) {
	for in, want := range map[string]string{
		"https://example.com/a": "https://example.com/a",
		"example.com/a?b=1":     "https://example.com/a?b=1",
		"localhost:3000":        "http://localhost:3000",
		"127.0.0.1:8080/x":      "http://127.0.0.1:8080/x",
		"192.168.1.5:3000":      "http://192.168.1.5:3000",
		"0.0.0.0:8080":          "http://0.0.0.0:8080",
		"myhost.local:5173":     "http://myhost.local:5173",
		"[::1]:3000":            "http://[::1]:3000",
		"localhost.evil.com":    "https://localhost.evil.com",
		"  go.dev  ":            "https://go.dev",
		"hello":                 "",
		"two words.com":         "",
		"ftp://example.com":     "",
		"":                      "",
		"/tmp/a b/r.html":       "file:///tmp/a%20b/r.html",
		"/tmp/x/../r.html":      "file:///tmp/r.html",
		"file:///tmp/r.html":    "file:///tmp/r.html",
	} {
		if got := webAddress(in); got != want {
			t.Errorf("webAddress(%q) = %q, want %q", in, got, want)
		}
	}
}

// termX 是主区域里离侧栏右边 dx 的横坐标（默认宽的侧栏），终端、标签栏的坐标都按它算，侧栏宽度变了不用改测试。
func termX(dx float32) float32 { return sidebarDefault() + dx }

// 链接指向别的工作区里的文件时，在链接所在终端的工作区里打开，不切走。
func TestOpenLinkFileStaysInWorkspace(t *testing.T) {
	here, there := t.TempDir(), t.TempDir()
	file := filepath.Join(there, "report.md")
	if err := os.WriteFile(file, nil, 0o644); err != nil {
		t.Fatal(err)
	}
	a := &app{activeTerm: map[string]string{}, selected: "w1", workspaces: []workspace{
		{ID: "w1", Path: here, Terminals: []terminalInfo{{ID: "t1"}}},
		{ID: "w2", Path: there},
	}}
	// 卡片记的是旧的一份工作区：按 ID 现查
	stale := *a.workspace("w1")
	if !a.openLinkFile(&linkCard{path: file, ws: &stale}) {
		t.Fatal("not opened")
	}
	if a.selected != "w1" || a.activeTerm["w1"] != filesTabID {
		t.Fatalf("selected %q, active tab %q", a.selected, a.activeTerm["w1"])
	}
	if v := a.files["w1"]; v == nil || v.cur == nil || v.cur.path != file {
		t.Fatal("file not current in w1's files tab")
	}
	if a.files["w2"] != nil {
		t.Fatal("opened in the file's own workspace")
	}
	// 工作区已经不在了：不打开
	a.workspaces = a.workspaces[1:]
	if a.openLinkFile(&linkCard{path: file, ws: &stale}) {
		t.Fatal("opened in a removed workspace")
	}
}

// 别的工作区里的文件没有相对这个工作区的路径：复制相对路径、跳到定义都不用 ../.. 的路径。
func TestRelInside(t *testing.T) {
	for _, tc := range []struct {
		path string
		rel  string
		ok   bool
	}{
		{"/p/main/a/b.go", "a/b.go", true},
		{"/p/main", ".", true},
		{"/p/wt/a.go", "", false},
		{"/p/main..x/a.go", "", false},
		{"/p", "", false},
	} {
		if rel, ok := relInside("/p/main", tc.path); rel != tc.rel || ok != tc.ok {
			t.Errorf("%s: %q %v, want %q %v", tc.path, rel, ok, tc.rel, tc.ok)
		}
	}
}

// 链接卡片开着时终端里按 Esc 关掉卡片、不交给程序；别的分栏的卡片不管。
func TestEscClosesLinkCard(t *testing.T) {
	a := previewApp(t)
	a.linkCard = &linkCard{path: "/x.png", term: "t2"}
	if a.termKey(&termView{}, "t1", 0, ui.KeyEscape) || a.linkCard == nil {
		t.Fatal("Esc in another pane closed the card")
	}
	if !a.termKey(&termView{}, "t2", 0, ui.KeyEscape) || a.linkCard != nil {
		t.Fatal("Esc did not close the card")
	}
	if a.termKey(&termView{}, "t2", 0, ui.KeyEscape) {
		t.Fatal("Esc without a card was taken")
	}
}

// Claude 输入框里还没发送的 [Image #N] 指向会话图片目录里的 N.png；别的 agent、别的终端、还没存下的都不算链接。
func TestPastedImageTag(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("TMPDIR", tmp)
	sid := "posse-test-" + filepath.Base(tmp)
	dir := filepath.Join(tmp, fmt.Sprintf("claude-%d", os.Getuid()), "-proj", sid, "images")
	os.MkdirAll(dir, 0o755)
	img := filepath.Join(dir, "2.png")
	os.WriteFile(img, []byte("x"), 0o644)

	a := previewApp(t)
	a.workspaces[0].Terminals = append(a.workspaces[0].Terminals,
		terminalInfo{ID: "tc", Agent: "claude", AgentState: &agentState{SessionID: sid}},
		terminalInfo{ID: "tx", Agent: "codex", AgentState: &agentState{SessionID: sid}})
	claude, codex := &termView{id: "tc"}, &termView{id: "tx"}
	if p, local := a.linkPath(claude, "[Image #2]"); p != img || !local {
		t.Fatalf("[Image #2] = %q, want %q", p, img)
	}
	if !a.linkUsable(claude, "[Image #2]") {
		t.Fatal("[Image #2] not usable")
	}
	if a.linkUsable(claude, "[Image #3]") {
		t.Fatal("[Image #3] usable before Claude saved it")
	}
	if a.linkUsable(codex, "[Image #2]") {
		t.Fatal("[Image #2] in a codex terminal usable")
	}
}
