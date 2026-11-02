package app

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/egoist/mygo/ui"

	"posse/internal/files"
	"posse/internal/history"
)

// previewScene 是一张预览：setup 准备好界面数据，act（可空）在取图前操作一下（右键弹菜单）。
type previewScene struct {
	name  string
	setup func(t *testing.T, a *app)
	act   func(t *testing.T, tt *ui.Tester)
}

// TestPreviewBatchC 把文件、历史、图片、命令速查、手机和网页这些界面按中文、英文和伪语言（en-XA 把英文加长、加重音）
// 各出深浅一套，看英文有没有截断、重叠；设置 POSSE_PREVIEW_DIR 才运行。
func TestPreviewBatchC(t *testing.T) {
	if os.Getenv("POSSE_PREVIEW_DIR") == "" {
		t.Skip("set POSSE_PREVIEW_DIR to render previews")
	}
	scenes := []previewScene{
		{name: "c-files", setup: func(t *testing.T, a *app) {
			v := previewFiles(t, a, "internal/app/view.go", "README.md", "main.go")
			v.doc(filepath.Join(v.root, "README.md")).ed.Insert("<!-- draft -->\n")
		}},
		// 磁盘上的文件被改了、自己也有没保存的修改：顶上的提示条
		{name: "c-files-conflict", setup: func(t *testing.T, a *app) {
			v := previewFiles(t, a, "README.md", "main.go")
			v.cur.conflict = true
			v.cur.readOnly = true
		}},
		{name: "c-files-search", setup: func(t *testing.T, a *app) {
			v := previewFiles(t, a, "main.go")
			v.side, v.search.query.Text, v.search.showFilter = 1, "config", true
			a.startSearch(v, 0)
			until(t, newTester(a.build, 1200, 760), "search", func() bool { return !v.search.running && len(v.search.hits) > 0 })
		}},
		{name: "c-files-markdown-find", setup: func(t *testing.T, a *app) {
			v := previewFiles(t, a, "README.md")
			v.cur.preview = true
			v.cur.find = docFind{open: true, query: "nothing-here", scroll: true}
		}},
		{name: "c-quick-open", setup: func(t *testing.T, a *app) {
			previewFiles(t, a, "main.go")
			a.openQuick(&a.workspaces[0])
			a.quick.query = "nothing-matches"
		}},
		{name: "c-definition-pick", setup: func(t *testing.T, a *app) {
			previewFiles(t, a, "main.go")
			a.defPick = defPick{open: true, ws: "w1", word: "openAlbum", defs: []files.Definition{
				{Path: "lib/MediaPickerHelper.java", Line: 41, Text: "    public static void openAlbum(Activity activity) {", Span: [2]int{23, 32}, Kind: "method", Decl: true},
				{Path: "app/ChatActivity.java", Line: 88, Text: "    private void openAlbum(int code) {", Span: [2]int{17, 26}, Kind: "method", Decl: true},
			}}
		}},
		// 关窗口时还有两个文件没保存
		{name: "c-files-unsaved", setup: func(t *testing.T, a *app) {
			v := previewFiles(t, a, "README.md", "main.go")
			a.askUnsaved(v.docs, func() {})
		}},
		{name: "c-files-image", setup: func(t *testing.T, a *app) { previewImageDoc(t, a, 0) }},
		{name: "c-history", setup: func(t *testing.T, a *app) { previewHistory(a, "") }},
		{name: "c-history-search", setup: func(t *testing.T, a *app) { previewHistory(a, "磁盘") }},
		{name: "c-history-empty", setup: func(t *testing.T, a *app) {
			previewHistory(a, "nothing")
			a.history.hits, a.history.sel, a.history.detail = nil, "", nil
		}},
		{name: "c-history-indexing", setup: func(t *testing.T, a *app) {
			previewHistory(a, "")
			a.history.sel, a.history.detail = "", nil
			a.history.status.Indexing, a.history.status.Progress = true, history.Progress{Done: 1200, Total: 3400}
		}},
		{name: "c-history-markdown", setup: func(t *testing.T, a *app) {
			previewHistory(a, "")
			previewMarkdown(a)
		}},
		{name: "c-images", setup: func(t *testing.T, a *app) {
			a.state.RightPanel = panelImages
			a.images = previewImages()
		}},
		{name: "c-images-menu", setup: func(t *testing.T, a *app) {
			a.state.RightPanel = panelImages
			a.images = previewImages()
		}, act: func(t *testing.T, tt *ui.Tester) {
			r, ok := tt.Find("phone_screenshot")
			if !ok {
				t.Fatal("images: phone_screenshot not found")
			}
			tt.RightClickAt(r.X+r.W/2, r.Y-60)
			settleMenu(tt)
		}},
		{name: "c-cheat", setup: func(t *testing.T, a *app) {
			a.state.RightPanel = panelCheat
			a.state.CheatTab = cheatSkills
			a.cheat = cheatView{term: "t1", agent: "claude", loaded: true, version: "9.9.9", poll: panelPoll{at: time.Now()},
				skills: []cheatSkill{{name: "computer-use", desc: "Operate native macOS desktop apps"}}}
		}},
		{name: "c-cheat-empty", setup: func(t *testing.T, a *app) {
			a.state.RightPanel = panelCheat
			a.state.CheatTab = cheatSkills
			a.cheat = cheatView{term: "t1", agent: "claude", loaded: true, version: "0.0.1", poll: panelPoll{at: time.Now()}}
		}},
		{name: "c-mobile", setup: func(t *testing.T, a *app) {
			previewMobile(t, a)
			a.mobile.relay = relayView{loaded: true, loadedAt: time.Now(), st: relayStatus{Configured: true, Admin: true, State: "clock"}, invite: "posse-relay:abc123"}
			a.daemon.DeskViewer, a.daemon.DeskMissing = "iPhone 16 Pro", []string{"screen", "accessibility"}
		}},
		// 滚到下面：中转的邀请、已配对的手机和展开的其他配对
		{name: "c-mobile-bottom", setup: func(t *testing.T, a *app) {
			previewMobile(t, a)
			a.mobile.relay = relayView{loaded: true, loadedAt: time.Now(), st: relayStatus{Configured: true, Admin: true, State: "ready"}, invite: "posse-relay:abc123"}
		}, act: func(t *testing.T, tt *ui.Tester) {
			tt.Scroll(1200, 800, 0, 3000)
			pass(tt, 300*time.Millisecond)
			tt.Frame()
		}},
		{name: "c-mobile-relay", setup: func(t *testing.T, a *app) {
			previewMobile(t, a)
			a.mobile.othersOpen = false
			a.mobile.relay = relayView{loaded: true, loadedAt: time.Now()}
		}},
		{name: "c-web", setup: func(t *testing.T, a *app) {
			// 测试里没有窗口，开不了 WebView：网页标签显示打不开
			previewFiles(t, a, "main.go")
			a.openWebTab(&a.workspaces[0], "https://example.com/docs", true)
		}},
	}
	for _, dark := range []bool{true, false} {
		suffix := map[bool]string{true: "dark", false: "light"}[dark]
		for _, s := range scenes {
			for _, lang := range []string{"zh-Hans", "en", "en-XA"} {
				a := previewApp(t)
				s.setup(t, a)
				tt := newPreviewTester(a, dark)
				tt.SetLocale(lang)
				for range 3 {
					tt.Frame()
				}
				if s.act != nil {
					s.act(t, tt)
				}
				savePNG(t, tt, s.name+"-"+lang+"-"+suffix+".png")
			}
		}
	}
}

// previewMobile 是手机页：二维码、两台 Posse Mobile 手机和两个其他配对（展开）。
func previewMobile(t *testing.T, a *app) {
	url := "orca://pair?code=eyJ2IjoyLCJlbmRwb2ludCI6IndzOi8vMTcyLjMxLjAuMjMwOjY3ODgifQ"
	img, err := qrImage(url)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	a.selected = mobileBoardID
	a.mobile = mobileView{shown: true, loadedAt: now, url: url, endpoint: "ws://172.31.0.230:6788", qr: ui.NewBitmap(img),
		othersOpen: true, locals: []string{"172.31.0.230", "100.101.102.103"},
		devices: []mobileDevice{
			{ID: "d1", Name: "iPhone 16 Pro", Phone: "p1", PairedAt: now.Add(-72 * time.Hour), LastSeen: now, Links: 2, Online: true},
			{ID: "d2", Name: "Pixel 8", Phone: "p2", PairedAt: now.Add(-50 * time.Hour), LastSeen: now.Add(-10 * time.Hour), Links: 1},
			{ID: "d3", Name: "Phone A", PairedAt: now.Add(-60 * time.Hour), LastSeen: now, Links: 1, Online: true},
			{ID: "d4", Name: "Phone B", PairedAt: now.Add(-59 * time.Hour), LastSeen: now.Add(-40 * time.Hour), Links: 1},
		}}
}
