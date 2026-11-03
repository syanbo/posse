package app

import (
	"net/url"
	"path/filepath"

	"github.com/egoist/mygo"
	"github.com/egoist/mygo/ui"

	"posse/internal/errtext"
	"posse/internal/files"
)

// 文件标签里网页文件的预览：和 Markdown 一样默认显示预览、顶栏切到源码。页面是一个不带工具栏的 WebView，
// 只显示这个文件：点到别的地方的链接不在预览里跳走，网址交给网页标签、本地文件交给文件标签。
// 要前进后退、手机预览、给 agent 操作，从链接卡片在网页标签里打开。

// htmlPage 是网页预览的页面。
type htmlPage struct {
	view *mygo.WebView
	seen files.Stamp // 页面载入时文件的磁盘戳，轮询看到变了就重新载入
	err  string      // 建不了页面的原因，不每帧重试
}

func (p *htmlPage) destroy() {
	if p.view != nil {
		p.view.Destroy()
		p.view = nil
	}
}

// htmlPreview 画网页预览，第一次显示时建页面。
func (a *app) htmlPreview(c *ui.Context, w *workspace, d *fileDoc) {
	t := c.Theme()
	if d.page == nil {
		d.page = a.newHTMLPage(w, d)
	}
	pg := d.page
	if pg.err != "" || pg.view == nil {
		msg := pg.err
		if msg == "" {
			msg = L(c).WebUnavailable()
		}
		docMessage(c, d, msg)
		return
	}
	// agent 改写了文件就重新载入；被删掉时留着最后一次的页面，顶上的横条说明
	if d.seen != pg.seen && !d.seen.Missing {
		pg.seen = d.seen
		pg.view.Page().Reload()
	}
	ui.WebView(c.Key("html:"+d.path), pg.view).Grow(1).BorderWidth(hairline, 0, 0, 0).BorderColor(colors(t).line)
}

func (a *app) newHTMLPage(w *workspace, d *fileDoc) *htmlPage {
	pg := &htmlPage{seen: d.seen}
	if a.win == nil {
		return pg
	}
	wv, err := a.win.NewWebView(mygo.WebViewOptions{})
	if err != nil {
		pg.err = errtext.Text(err)
		return pg
	}
	pg.view = wv
	p := wv.Page()
	wsID, self := w.ID, filepath.Clean(d.path)
	p.OnWillNavigate(func(e *mygo.NavigateEvent) {
		// 页内锚点、页面自己刷新还是这个文件，照常跳
		if u, err := url.Parse(e.URL); err == nil && u.Scheme == "file" && filepath.Clean(u.Path) == self {
			return
		}
		e.PreventDefault()
		link := e.URL
		a.win.Update(func() { a.openPreviewLink(wsID, link) })
	})
	p.SetWindowOpenHandler(func(req mygo.WindowOpenRequest) *mygo.WindowOptions {
		link := req.URL
		a.win.Update(func() { a.openPreviewLink(wsID, link) })
		return nil
	})
	p.OnPageTitleUpdated(func(e *mygo.TitleEvent) { e.PreventDefault() })
	if err := p.LoadFile(d.path); err != nil {
		pg.err = errtext.Text(err)
	}
	return pg
}

// openPreviewLink 打开网页预览里点的链接：网址在网页标签里打开，本地文件在文件标签里打开（网页也是预览），
// 别的（mailto: 这类）和工作区已经不在时交给系统。
func (a *app) openPreviewLink(wsID, link string) {
	w := a.workspace(wsID)
	switch {
	case w != nil && isWebURL(link):
		a.openWeb(w, link)
	case w != nil && isFileURL(link):
		if path := fileURLPath(link); path != "" {
			a.openFile(w, path, 0, [2]int{})
		}
	default:
		mygo.Shell.OpenExternal(link)
	}
}
