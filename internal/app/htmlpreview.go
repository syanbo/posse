package app

import (
	"context"
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
	view     *mygo.WebView
	seen     files.Stamp // 页面载入时文件的磁盘戳，轮询看到变了就重新载入
	err      string      // 建不了页面的原因，不每帧重试
	remote   bool
	request  appRequest
	resource *htmlResource
	pending  *mygo.WebView // 重载成功才替换旧页面，失败时仍能保留已经显示的内容。
}

func (p *htmlPage) destroy() {
	p.invalidateResource()
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
	if pg.remote {
		if !pg.request.current() {
			pg.destroy()
			d.page = a.newHTMLPage(w, d)
			pg = d.page
		} else if d.seen != pg.seen && !d.seen.Missing {
			a.reloadRemoteHTML(w, d, pg)
		}
	}
	view := pg.view
	if view == nil && pg.remote && pg.err == "" {
		view = pg.pending
	}
	if view == nil || !pg.remote && pg.err != "" {
		msg := pg.err
		if msg == "" {
			msg = L(c).WebUnavailable()
		}
		a.docMessage(c, a.files[w.ID], d, msg)
		return
	}
	// agent 改写了文件就重新载入；被删掉时留着最后一次的页面，顶上的横条说明
	if !pg.remote && d.seen != pg.seen && !d.seen.Missing {
		pg.seen = d.seen
		pg.view.Page().Reload()
	}
	ui.WebView(c.Key("html:"+d.path), view).Grow(1).BorderWidth(hairline, 0, 0, 0).BorderColor(colors(t).line)
}

func (a *app) newHTMLPage(w *workspace, d *fileDoc) *htmlPage {
	pg := &htmlPage{seen: d.seen}
	if a.remoteHost() {
		pg.remote, pg.request = true, a.request()
		a.reloadRemoteHTML(w, d, pg)
		return pg
	}
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
	if err := loadHTMLDocument(p, d.path, ""); err != nil {
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

// 本机仍由 WebKit 读文件；远端只给自定义 scheme URL，绝不传本机目录权限。
func loadHTMLDocument(p interface {
	LoadFile(string) error
	LoadURL(string) error
}, path, resourceURL string) error {
	if resourceURL != "" {
		return p.LoadURL(resourceURL)
	}
	return p.LoadFile(path)
}

func (pg *htmlPage) invalidateResource() {
	if pg.resource != nil {
		pg.resource.close()
		pg.resource = nil
	}
	if pg.pending != nil {
		pg.pending.Destroy()
		pg.pending = nil
	}
}

func invalidateHTMLDocument(d *fileDoc) {
	if pg := d.page; pg != nil && pg.remote && pg.seen != d.seen {
		pg.invalidateResource()
	}
}

func (a *app) reloadRemoteHTML(w *workspace, d *fileDoc, pg *htmlPage) {
	pg.seen = d.seen
	a.loadRemoteHTML(w, d, pg, previewResourceRoot(d.path, w.Path), d.path, d.seen)
}

func (a *app) loadRemoteHTML(w *workspace, d *fileDoc, pg *htmlPage, root, path string, stamp files.Stamp) {
	pg.invalidateResource()
	pg.err = ""
	if !pg.request.current() || pg.request.client == nil || a.win == nil {
		pg.err = appL().WebUnavailable()
		return
	}
	s, err := newHTMLResource(pg.request, root, path, stamp, func(s *htmlResource, err error) {
		a.update(func() { a.finishRemoteHTML(d, pg, s, err) })
	})
	if err != nil {
		pg.err, a.err = appL().WebUnavailable(), errtext.Text(err)
		return
	}
	pg.resource = s
	wv, err := a.win.NewWebView(mygo.WebViewOptions{})
	if err != nil {
		a.finishRemoteHTML(d, pg, s, err)
		return
	}
	pg.pending = wv
	p := wv.Page()
	p.OnPageTitleUpdated(func(e *mygo.TitleEvent) { e.PreventDefault() })
	p.OnDidFailLoad(func(e *mygo.LoadError) {
		a.update(func() { a.finishRemoteHTML(d, pg, s, e) })
	})
	p.OnDidFinishLoad(func() {
		a.update(func() { a.finishRemoteHTML(d, pg, s, nil) })
	})
	p.OnWillNavigate(func(e *mygo.NavigateEvent) {
		u, err := url.Parse(e.URL)
		main, _ := url.Parse(s.mainURL)
		if err == nil && u.Scheme == main.Scheme && u.Host == main.Host && u.Path == main.Path && u.RawQuery == main.RawQuery && u.Fragment != "" && s.current() {
			return
		}
		e.PreventDefault()
		link := e.URL
		a.update(func() { a.remoteHTMLLink(w, d, pg, s, link) })
	})
	p.SetWindowOpenHandler(func(req mygo.WindowOpenRequest) *mygo.WindowOptions {
		link := req.URL
		a.update(func() { a.remoteHTMLLink(w, d, pg, s, link) })
		return nil
	})
	if err := loadHTMLDocument(p, "", s.mainURL); err != nil {
		a.finishRemoteHTML(d, pg, s, err)
	}
}

func (a *app) finishRemoteHTML(d *fileDoc, pg *htmlPage, s *htmlResource, err error) {
	if pg.resource != s || !s.current() || d.seen != pg.seen {
		return
	}
	if err != nil {
		pg.err, a.err = appL().WebUnavailable(), errtext.Text(err)
		pg.invalidateResource()
		return
	}
	if pg.pending != nil {
		if pg.view != nil {
			pg.view.Destroy()
		}
		pg.view, pg.pending = pg.pending, nil
	}
}

func (a *app) remoteHTMLLink(w *workspace, d *fileDoc, pg *htmlPage, s *htmlResource, link string) {
	if pg.resource != s || !s.current() {
		return
	}
	u, err := url.Parse(link)
	if err != nil {
		return
	}
	if (u.Scheme == "http" || u.Scheme == "https") && !previewLocalURL(link) {
		a.openPreviewLink(w.ID, link)
		return
	}
	path, err := s.resolve(u)
	if err != nil || !isHTML(path) {
		return
	}
	// 点击只允许同一根内的 HTML；先在目标核验，再为新文档签发独立 token。
	go func() {
		ctx, cancel := context.WithTimeout(s.ctx, mdFetchTime)
		defer cancel()
		_, st, err := s.request.client.ReadResource(ctx, s.root, path, files.MaxSize)
		a.update(func() {
			if pg.resource != s || !s.current() || d.seen != pg.seen {
				return
			}
			if err != nil {
				a.err = errtext.Text(err)
				return
			}
			a.loadRemoteHTML(w, d, pg, s.root, s.root+"/"+path, st)
		})
	}()
}
