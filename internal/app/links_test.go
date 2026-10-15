package app

import (
	"image"
	"os"
	"path/filepath"
	"testing"

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

// 在真的终端里点链接：OSC 8 超链接（Claude 的 [Image #N] 就是这样指向 file:// 的）和打印出来的路径都弹出链接卡片。
// 原地单击和 Cmd+点击都打开；从链接上拖动是选文字，不打开。
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
		OpenLink:   func(link string) bool { calls = append(calls, link); return a.openLink(v, link) },
		LinkUsable: func(link string) bool { return a.linkUsable(v, link) }})
	if err != nil {
		t.Skip("terminal unavailable:", err)
	}
	t.Cleanup(func() { term.Close() })
	v.term = term
	a.view = v
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
	term.Feed([]byte(feed))
	tt := newPreviewTester(a, true)

	// 第 0–3 行是超链接，第 4–7 行是路径；行高约 18.5，第一行约在 y=51
	// 从链接上拖开：选文字，不打开
	tt.Move(290, 80)
	tt.Frame()
	tt.Press(290, 80)
	tt.Frame()
	tt.Move(330, 120)
	tt.Frame()
	tt.Release(330, 120)
	tt.Frame()
	if a.linkCard != nil {
		t.Fatal("drag from a link opened the card")
	}
	// 悬停时链接下面有下划线
	tt.Move(290, 80)
	tt.Frame()
	tt.Frame()
	if os.Getenv("HIVE_PREVIEW_DIR") != "" {
		savePNG(t, tt, "link-hover.png")
	}
	// 原地单击打开
	tt.ClickAt(290, 80)
	tt.Frame()
	tt.Frame()
	if a.linkCard == nil || a.linkCard.path != file {
		t.Fatalf("plain click on hyperlink: %+v calls=%q", a.linkCard, calls)
	}
	a.linkCard = nil
	tt.Frame()
	cmdClick(tt, 290, 80)
	if a.linkCard == nil || a.linkCard.path != file || a.linkCard.isDir {
		t.Fatalf("Cmd+click on hyperlink: %+v err=%q calls=%q", a.linkCard, a.err, calls)
	}
	tt.Frame()
	if !a.linkCard.placed {
		t.Fatal("card not placed")
	}
	// 点卡片外面关掉
	tt.ClickAt(600, 600)
	tt.Frame()
	if a.linkCard != nil {
		t.Fatal("outside click did not close the card")
	}
	cmdClick(tt, 290, 155)
	if a.linkCard == nil || a.linkCard.path != sub || !a.linkCard.isDir {
		t.Fatalf("Cmd+click on path: %+v", a.linkCard)
	}
	a.linkCard = nil
	tt.Frame()

	// 不存在的文件：单击和 Cmd+点击都没反应（第 8 行超链接、第 10 行路径）
	calls = nil
	for _, y := range []float32{51 + 8.5*18.5, 51 + 10.5*18.5} {
		tt.ClickAt(290, y)
		tt.Frame()
		cmdClick(tt, 290, y)
	}
	if a.linkCard != nil || len(calls) != 0 || a.err != "" {
		t.Fatalf("missing files opened: card %+v calls %q err %q", a.linkCard, calls, a.err)
	}
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
	} {
		a := previewApp(t)
		card := &linkCard{path: dir, isDir: !tc.preview, x: 200, y: tc.y, placed: true}
		if tc.preview {
			card.preview = ui.NewBitmap(image.NewRGBA(image.Rect(0, 0, 640, 400)))
		}
		a.linkCard = card
		tt := newPreviewTester(a, true)
		tt.Frame()
		r, ok := tt.Find("复制路径")
		if !ok {
			t.Fatalf("%s: card missing", tc.name)
		}
		top, ok := tt.Find(shortPath(dir))
		if !ok {
			t.Fatalf("%s: path missing", tc.name)
		}
		// 终端区域从窗口 y≈40 开始；卡片的 card.y 相对终端区域
		click := 40 + tc.y
		if tc.y > 300 {
			if bottom := r.Y + r.H; bottom > click || click-bottom > 40 {
				t.Errorf("%s: card bottom %.0f, click at %.0f", tc.name, bottom, click)
			}
		} else if top.Y < click || top.Y-click > 40 {
			t.Errorf("%s: card top %.0f, click at %.0f", tc.name, top.Y, click)
		}
	}
}
