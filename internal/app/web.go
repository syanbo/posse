package app

import (
	"net"
	"net/url"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/egoist/mygo"
	"github.com/egoist/mygo/ui"

	"posse/internal/errtext"
	"posse/locales"
)

// 网页标签：在文件标签里用系统的 WebKit（mygo 的 WebView）打开网址，和 diff、比较一样不对应磁盘文件。
// 网页跑在系统的 WebContent 进程里，切到别的标签时页面留着，关掉标签才销毁。

// webDoc 是网页标签的状态。跳转、载入、标题由页面事件更新；单页应用用 pushState 换地址不发事件，
// 看得见时再每秒对一次（webPoll）。
type webDoc struct {
	id      int // posse web 里的标签编号，窗口进程内不重复
	view    *mygo.WebView
	url     string // 当前地址
	title   string
	addr    string // 地址栏里的文字，编辑中和 url 不同
	editing bool   // 地址栏有焦点：页面跳转不覆盖正在输入的文字
	loading bool
	back    bool
	forward bool
	failed  string // 载入失败的原因，页面位置显示它；重新跳转时清掉
	// used 是最近一次显示的时间，posse web 不带 --tab 时操作最近显示过的那个
	used time.Time
	// device 是手机预览的机型（webdevice.go），空是电脑；zoom 是已经设给页面的缩放，0 是没设过
	device    string
	landscape bool
	zoom      float64
}

var webSeq int

// isWebURL 报告链接是不是 http、https 网址。
func isWebURL(link string) bool {
	u, err := url.Parse(link)
	return err == nil && (u.Scheme == "http" || u.Scheme == "https") && u.Host != ""
}

// isFileURL 报告链接是不是本地文件的 file:// 网址。
func isFileURL(link string) bool {
	u, err := url.Parse(link)
	return err == nil && u.Scheme == "file" && u.Path != ""
}

// webOpenable 报告链接能不能在网页标签里打开：http、https 和本地文件。
func webOpenable(link string) bool { return isWebURL(link) || isFileURL(link) }

// fileURL 是本地路径的 file:// 网址。
func fileURL(path string) string { return (&url.URL{Scheme: "file", Path: path}).String() }

// loadPage 让页面载入 link。WebKit 用 loadRequest 打不开 file://，要经 LoadFile，
// 它允许页面读文件所在的目录（同目录和子目录里的样式、脚本、图片），上级目录读不到。
func loadPage(p *mygo.Page, link string) error {
	if isFileURL(link) {
		u, _ := url.Parse(link)
		return p.LoadFile(u.Path)
	}
	return p.LoadURL(link)
}

// openWeb 在工作区的文件标签里新开一个网页标签，键盘交给页面。
func (a *app) openWeb(w *workspace, link string) { a.openWebTab(w, link, true) }

// openWebTab 新开网页标签并切过去；focus 为 false 时键盘留在原处（posse web open）。
func (a *app) openWebTab(w *workspace, link string, focus bool) (*webDoc, *fileDoc) {
	v := a.showFiles(w)
	webSeq++
	wd := &webDoc{id: webSeq, url: link, addr: link, loading: true}
	d := &fileDoc{path: "web:" + strconv.Itoa(webSeq), web: wd, started: true, focus: focus}
	v.insertDoc(d)
	if a.win == nil {
		d.kind, d.errMsg = docError, locales.Messages.WebUnavailable
		return wd, d
	}
	// posse web 的脚本先注入一次，console 从页面自己的脚本运行前就开始记。
	// 网址在下面挂好事件后再载入：本地文件要经 LoadFile，不能交给 WebViewOptions.URL
	wv, err := a.win.NewWebView(mygo.WebViewOptions{Page: mygo.PageOptions{PreloadScript: webctlJS}})
	if err != nil {
		d.kind, d.err = docError, errtext.Text(err)
		return wd, d
	}
	wd.view = wv
	p := wv.Page()
	// 页面事件不一定在主线程上来，统一经 Update 回到界面
	p.OnDidNavigate(func(string) {
		a.win.Update(func() {
			// 新页面可能没有 <title>，不留上一页的标题
			wd.title, wd.failed = "", ""
			a.syncWeb(wd)
		})
	})
	p.OnDidFinishLoad(func() { a.win.Update(func() { a.syncWeb(wd) }) })
	p.OnDidFailLoad(func(e *mygo.LoadError) {
		a.win.Update(func() {
			a.syncWeb(wd)
			// -999 是被新的跳转打断，不算失败
			if e.Code != -999 {
				wd.failed = e.Description
				if e.URL != "" {
					wd.url = e.URL
					if !wd.editing {
						wd.addr = wd.url
					}
				}
			}
		})
	})
	p.OnPageTitleUpdated(func(e *mygo.TitleEvent) {
		e.PreventDefault()
		title := e.Title
		a.win.Update(func() { wd.title = title })
	})
	// target=_blank 和 window.open 在旁边再开一个网页标签，不交给系统浏览器。
	// 工作区列表每次轮询都会换新，按 ID 重新找；工作区已经不在了就交给浏览器
	wsID := w.ID
	p.SetWindowOpenHandler(func(req mygo.WindowOpenRequest) *mygo.WindowOptions {
		if webOpenable(req.URL) {
			a.win.Update(func() {
				if w := a.workspace(wsID); w != nil {
					a.openWeb(w, req.URL)
				} else {
					mygo.Shell.OpenExternal(req.URL)
				}
			})
		}
		return nil
	})
	a.loadWeb(wd, link)
	return wd, d
}

// syncWeb 从 WebKit 读回地址和导航状态。只在主线程调。
func (a *app) syncWeb(wd *webDoc) {
	if wd.view == nil || wd.view.IsDestroyed() {
		return
	}
	p := wd.view.Page()
	wd.loading, wd.back, wd.forward = p.IsLoading(), p.CanGoBack(), p.CanGoForward()
	// 载入失败时没有提交过页面，WebKit 给的地址是空的，留着原来的
	if u := p.URL(); u != "" && wd.failed == "" {
		wd.url = u
	}
	if !wd.editing {
		wd.addr = wd.url
	}
}

// loadWeb 在网页标签里跳到新网址：地址栏回车和 posse web goto 共用。只在主线程调。
func (a *app) loadWeb(wd *webDoc, link string) {
	wd.failed, wd.loading = "", true
	if !wd.editing {
		wd.addr = link
	}
	if err := loadPage(wd.view.Page(), link); err != nil {
		wd.url, wd.loading, wd.failed = link, false, errtext.Text(err)
	}
}

// reloadWeb 重新载入。载入失败的页面没有提交过，Reload 不知道载入什么，按原地址重新载入。
func (a *app) reloadWeb(wd *webDoc) {
	p := wd.view.Page()
	if wd.failed != "" {
		a.loadWeb(wd, wd.url)
		return
	}
	p.Reload()
}

// webPoll 是看得见时对一次导航状态的间隔：pushState、hash 跳转不发页面事件，停止载入有时也不发。
const webPoll = time.Second

// closeWeb 销毁网页标签和网页预览的页面。
func closeWeb(d *fileDoc) {
	if d.web != nil && d.web.view != nil {
		d.web.view.Destroy()
		d.web.view = nil
	}
	if d.page != nil {
		d.page.destroy()
		d.page = nil
	}
}

// name 是网页标签上的名字：页面标题，还没有时是主机名，本地文件是文件名。
func (wd *webDoc) name(m locales.Messages) string {
	if wd.title != "" {
		return wd.title
	}
	if u, err := url.Parse(wd.url); err == nil && u.Host != "" {
		return u.Host
	} else if err == nil && u.Scheme == "file" && u.Path != "" {
		return filepath.Base(u.Path)
	}
	return m.WebTabName()
}

// webView 画网页标签：上面一行导航和地址栏，下面是页面。
func (a *app) webView(c *ui.Context, d *fileDoc) {
	wd := d.web
	t := c.Theme()
	p := wd.view.Page()
	a.syncWeb(wd)
	wd.used = time.Now()
	c.After(webPoll)
	ui.Row(c).Height(sz(36)).Shrink(0).Padding(0, 8).Gap(2).AlignItems(ui.Center).Children(func() {
		navButton := func(key string, icon *ui.SVG, label string, on bool) bool {
			b := iconButton(c.Key(key), icon, label)
			if !on {
				b.Opacity(0.35)
			}
			return on && b.Clicked()
		}
		if navButton("web-back", iconBack, L(c).WebBack(), wd.back) {
			p.GoBack()
		}
		if navButton("web-forward", iconForward, L(c).WebForward(), wd.forward) {
			p.GoForward()
		}
		if wd.loading {
			if navButton("web-stop", iconClose, L(c).WebStop(), true) {
				// 页面已经显示出来、只差子资源时停下，WebKit 不一定再发载入结束的事件
				p.Stop()
				wd.loading = false
			}
		} else if navButton("web-reload", iconRefresh, L(c).WebReload(), true) {
			a.reloadWeb(wd)
		}
		ui.Box(c).Width(4)
		var in ui.Element
		ui.Column(c).Grow(1).Shrink(1).Children(func() { in = fieldInput(c, &wd.addr, L(c).WebAddressPlaceholder()) })
		wd.editing = in.Focused()
		if in.Submitted() {
			if link := webAddress(wd.addr); link != "" {
				wd.editing = false
				a.loadWeb(wd, link)
				wd.view.Focus()
			}
		}
		if in.Shortcut(0, ui.KeyEscape) {
			wd.addr = wd.url
			wd.view.Focus()
		}
		ui.Box(c).Width(4)
		phone := iconButton(c.Key("web-device"), iconPhone, a.withKey(L(c).WebMobilePreview(), "webMobile"))
		if wd.device != "" {
			phone.Background(soft(t, t.Accent))
		}
		menuButton(c, phone, func(m *popMenu) { a.deviceMenu(L(c), m, wd) })
		if iconButton(c.Key("web-copy"), iconCopy, L(c).LinksCopyLink()).Clicked() {
			c.WriteClipboard(wd.url)
		}
		if iconButton(c.Key("web-browser"), iconOpen, L(c).LinksOpenInBrowser()).Clicked() {
			c.OpenURL(wd.url)
		}
	})
	if wd.failed != "" {
		// 不建 WebView 就不显示它，页面留着；重新载入或跳转后再显示
		ui.Column(c).Grow(1).Center().Gap(8).Padding(24).BorderWidth(hairline, 0, 0, 0).BorderColor(colors(t).line).Children(func() {
			titleText(c, L(c).WebLoadFailed(wd.url), fsTitle)
			ui.Text(c, wd.failed).FontSize(fsBody).TextColor(t.TextMuted).Selectable()
			if pressable(textButton(c, L(c).WebReload())).Clicked() {
				a.reloadWeb(wd)
			}
		})
	} else if wd.device != "" {
		a.deviceStage(c, d)
	} else {
		ui.WebView(c.Key("web:"+d.path), wd.view).Grow(1).BorderWidth(hairline, 0, 0, 0).BorderColor(colors(t).line)
	}
	if d.focus {
		d.focus = false
		wd.view.Focus()
	}
}

// webAddress 把地址栏里的文字变成网址：没写协议的补 https://，本机、局域网地址和 .local 补 http://
// （开发服务器一般没有证书），file:// 网址和绝对路径是本地文件，不像网址的返回空。
func webAddress(s string) string {
	s = strings.TrimSpace(s)
	if filepath.IsAbs(s) {
		return fileURL(filepath.Clean(s))
	}
	if isFileURL(s) {
		return s
	}
	if s == "" || strings.ContainsAny(s, " \t") {
		return ""
	}
	if !strings.Contains(s, "://") {
		u, err := url.Parse("http://" + s)
		if err != nil || u.Host == "" {
			return ""
		}
		host := u.Hostname()
		switch {
		case plainHTTP(host):
			s = "http://" + s
		case strings.Contains(host, "."):
			s = "https://" + s
		default:
			return ""
		}
	}
	if !isWebURL(s) {
		return ""
	}
	return s
}

// plainHTTP 报告主机是不是本机或局域网：localhost、IP 地址（v4、v6）、.local 的 mDNS 名字。
func plainHTTP(host string) bool {
	if host == "localhost" || strings.HasSuffix(host, ".localhost") || strings.HasSuffix(host, ".local") {
		return true
	}
	return net.ParseIP(host) != nil
}
