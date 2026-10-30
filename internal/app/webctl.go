package app

import (
	"context"
	_ "embed"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/egoist/mygo"

	"posse/internal/rpc"
)

// posse web 的窗口一侧：网页标签在窗口进程里，常驻进程看不到，CLI 直接连窗口进程的 app.sock。

//go:embed webctl.js
var webctlJS string

// CtlSocket 是窗口进程接 posse web 请求的 socket。
func CtlSocket(home string) string { return filepath.Join(home, "app.sock") }

// errCtlBusy 是同一 home 已有窗口进程在接 posse web 的请求。
var errCtlBusy = errors.New("another Posse window already serves app.sock")

// listenCtl 监听窗口的 socket：有人应答就不接管，没人应答的残留文件删掉重建。
func listenCtl(path string) (net.Listener, error) {
	if c, err := net.DialTimeout("unix", path, time.Second); err == nil {
		c.Close()
		return nil, errCtlBusy
	}
	os.Remove(path)
	ln, err := net.Listen("unix", path)
	if err != nil {
		return nil, err
	}
	// 和常驻进程的 socket 同一信任边界：只有同一用户能连
	if err := os.Chmod(path, 0o600); err != nil {
		ln.Close()
		return nil, err
	}
	return ln, nil
}

// serveCtl 在 ln 上起 app.sock 的服务，返回的函数停掉它并删掉 socket。
func (a *app) serveCtl(ln net.Listener) (stop func()) {
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		defer close(done)
		a.ctlServer().Serve(ctx, ln)
	}()
	return func() {
		cancel()
		<-done
	}
}

func (a *app) ctlServer() *rpc.Server {
	s := rpc.NewServer()
	s.Handle("app.focus", a.ctlFocus)
	s.Handle("web.list", a.webList)
	s.Handle("web.open", a.webOpen)
	s.Handle("web.nav", a.webNav)
	s.Handle("web.run", a.webRun)
	s.Handle("web.eval", a.webEval)
	s.Handle("web.screenshot", a.webScreenshot)
	s.Handle("web.device", a.webDeviceSet)
	return s
}

// ctlFocus 把窗口叫到前面：同一 home 再开窗口时，新进程调它后退出。
func (a *app) ctlFocus(ctx context.Context, _ json.RawMessage) (any, error) {
	return nil, a.onMain(ctx, func() {
		a.win.Show()
		a.win.Focus()
	})
}

// onMain 在主线程上跑 fn 并等它跑完；请求先结束就不等了。
func (a *app) onMain(ctx context.Context, fn func()) error {
	done := make(chan struct{})
	update := a.mainUpdate
	if update == nil {
		update = a.win.Update
	}
	update(func() {
		defer close(done)
		// 请求已经结束（超时、CLI 断开）就不做了：CLI 已经报了失败，再开标签、跳转会让它重试时做两遍
		if ctx.Err() == nil {
			fn()
		}
	})
	select {
	case <-done:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

// webTarget 是请求指定的网页标签：workspace 为空时用侧栏选中的工作区，tab 为 0 时用最近看过的那个。
type webTarget struct {
	Workspace string `json:"workspace,omitempty"`
	Tab       int    `json:"tab,omitempty"`
}

// webTab 是 web.list 的一项。
type webTab struct {
	Tab     int    `json:"tab"`
	URL     string `json:"url"`
	Title   string `json:"title"`
	Loading bool   `json:"loading"`
	Failed  string `json:"failed,omitempty"`
	// Current 是不带 --tab 时操作的那个
	Current bool `json:"current"`
}

// webWorkspace 找请求的工作区。只在主线程调。
func (a *app) webWorkspace(id string) (*workspace, error) {
	if id == "" {
		id = a.selected
	}
	if w := a.workspace(id); w != nil {
		return w, nil
	}
	if id == "" {
		return nil, rpc.Errorf(rpc.CodeNotFound, "no workspace selected in the Posse window; pass --workspace")
	}
	return nil, rpc.Errorf(rpc.CodeNotFound, "workspace %s not found in the Posse window", id)
}

// webDocs 是工作区里的网页标签，按标签栏的顺序。只在主线程调。
func (a *app) webDocs(w *workspace) []*webDoc {
	var out []*webDoc
	if v := a.files[w.ID]; v != nil {
		for _, d := range v.docs {
			if d.web != nil && d.web.view != nil && !d.web.view.IsDestroyed() {
				out = append(out, d.web)
			}
		}
	}
	return out
}

// currentWeb 是不指定编号时操作的网页标签：最近显示过的，都没显示过就是最后开的。
func currentWeb(docs []*webDoc) *webDoc {
	var cur *webDoc
	for _, wd := range docs {
		if cur == nil || wd.used.After(cur.used) || (wd.used.Equal(cur.used) && wd.id > cur.id) {
			cur = wd
		}
	}
	return cur
}

// findWeb 按请求找网页标签。只在主线程调。
func (a *app) findWeb(t webTarget) (*webDoc, error) {
	w, err := a.webWorkspace(t.Workspace)
	if err != nil {
		return nil, err
	}
	docs := a.webDocs(w)
	if len(docs) == 0 {
		return nil, rpc.Errorf(rpc.CodeNotFound, "workspace %s has no web tab; open one with posse web open <url>", w.Label)
	}
	if t.Tab == 0 {
		return currentWeb(docs), nil
	}
	for _, wd := range docs {
		if wd.id == t.Tab {
			return wd, nil
		}
	}
	return nil, rpc.Errorf(rpc.CodeNotFound, "no web tab %d in workspace %s; see posse web list", t.Tab, w.Label)
}

// webHandle 是交给后台用的网页标签：view 在主线程上取出，标签关掉时 webDoc.view 会被清掉。
type webHandle struct {
	id   int
	view *mygo.WebView
}

// target 在主线程上找网页标签，交回它的页面供后台调用（Eval、截图会等页面，不能占着主线程）。
func (a *app) target(ctx context.Context, t webTarget) (webHandle, error) {
	var h webHandle
	var ferr error
	err := a.onMain(ctx, func() {
		wd, err := a.findWeb(t)
		switch {
		case err != nil:
			ferr = err
		case wd.failed != "":
			ferr = rpc.Errorf(rpc.CodeConflict, "tab %d failed to load %s: %s", wd.id, wd.url, wd.failed)
		default:
			h = webHandle{wd.id, wd.view}
		}
	})
	if err != nil {
		return h, err
	}
	return h, ferr
}

// closedErr 把页面已销毁时的报错换成标签已关。
func (h webHandle) closedErr(err error) error {
	if err != nil && h.view.IsDestroyed() {
		return rpc.Errorf(rpc.CodeNotFound, "web tab %d was closed", h.id)
	}
	return err
}

func (a *app) webList(ctx context.Context, params json.RawMessage) (any, error) {
	var p webTarget
	if err := rpc.Decode(params, &p); err != nil {
		return nil, err
	}
	var out []webTab
	var ferr error
	err := a.onMain(ctx, func() {
		w, err := a.webWorkspace(p.Workspace)
		if err != nil {
			ferr = err
			return
		}
		docs := a.webDocs(w)
		cur := currentWeb(docs)
		out = []webTab{}
		for _, wd := range docs {
			a.syncWeb(wd)
			out = append(out, webTab{Tab: wd.id, URL: wd.url, Title: wd.title, Loading: wd.loading, Failed: wd.failed, Current: wd == cur})
		}
	})
	if err != nil {
		return nil, err
	}
	return out, ferr
}

func (a *app) webOpen(ctx context.Context, params json.RawMessage) (any, error) {
	var p struct {
		webTarget
		URL string `json:"url"`
	}
	if err := rpc.Decode(params, &p); err != nil {
		return nil, err
	}
	link := webAddress(p.URL)
	if link == "" {
		return nil, rpc.Errorf(rpc.CodeBadRequest, "not a web address: %q", p.URL)
	}
	var tab webTab
	var ferr error
	err := a.onMain(ctx, func() {
		w, err := a.webWorkspace(p.Workspace)
		if err != nil {
			ferr = err
			return
		}
		// 不抢键盘：用户可能正在别的地方打字
		wd, d := a.openWebTab(w, link, false)
		if d.err != "" {
			ferr = rpc.Errorf(rpc.CodeInternal, "%s", d.err)
			return
		}
		wd.used = time.Now()
		tab = webTab{Tab: wd.id, URL: link, Loading: true, Current: true}
	})
	if err != nil {
		return nil, err
	}
	return tab, ferr
}

func (a *app) webNav(ctx context.Context, params json.RawMessage) (any, error) {
	var p struct {
		webTarget
		Action string `json:"action"` // goto、reload、back、forward
		URL    string `json:"url,omitempty"`
	}
	if err := rpc.Decode(params, &p); err != nil {
		return nil, err
	}
	link := ""
	if p.Action == "goto" {
		if link = webAddress(p.URL); link == "" {
			return nil, rpc.Errorf(rpc.CodeBadRequest, "not a web address: %q", p.URL)
		}
	}
	var ferr error
	err := a.onMain(ctx, func() {
		wd, err := a.findWeb(p.webTarget)
		if err != nil {
			ferr = err
			return
		}
		pg := wd.view.Page()
		switch p.Action {
		case "goto":
			a.loadWeb(wd, link)
		case "reload":
			a.reloadWeb(wd)
		case "back":
			if !pg.CanGoBack() {
				ferr = rpc.Errorf(rpc.CodeConflict, "no page to go back to")
				return
			}
			pg.GoBack()
		case "forward":
			if !pg.CanGoForward() {
				ferr = rpc.Errorf(rpc.CodeConflict, "no page to go forward to")
				return
			}
			pg.GoForward()
		default:
			ferr = rpc.Errorf(rpc.CodeBadRequest, "unknown action %q", p.Action)
		}
	})
	if err != nil {
		return nil, err
	}
	return nil, ferr
}

// webRun 调页面里 webctl.js 的一个函数：snapshot、click、fill、press、check、console。
func (a *app) webRun(ctx context.Context, params json.RawMessage) (any, error) {
	var p struct {
		webTarget
		Fn   string `json:"fn"`
		Args []any  `json:"args,omitempty"`
	}
	if err := rpc.Decode(params, &p); err != nil {
		return nil, err
	}
	switch p.Fn {
	case "snapshot", "click", "fill", "press", "check", "console":
	default:
		return nil, rpc.Errorf(rpc.CodeBadRequest, "unknown function %q", p.Fn)
	}
	args, err := json.Marshal(p.Args)
	if err != nil {
		return nil, err
	}
	if p.Args == nil {
		args = []byte("[]")
	}
	// 载入中的页面也调：check 要在载入中返回 false，载入完的页面这时可能还没有 __posse
	// 包成一个表达式：mygo 先按表达式编译，编译不过再当函数体跑，那次 SyntaxError 会进页面的 error 事件
	return a.eval(ctx, p.webTarget, fmt.Sprintf("(() => {\n%s\nreturn window.__posse.%s(...%s)\n})()", webctlJS, p.Fn, args), p.Fn == "check")
}

func (a *app) webEval(ctx context.Context, params json.RawMessage) (any, error) {
	var p struct {
		webTarget
		JS string `json:"js"`
	}
	if err := rpc.Decode(params, &p); err != nil {
		return nil, err
	}
	return a.eval(ctx, p.webTarget, p.JS, false)
}

// eval 在页面里跑脚本。loading 为 true 时页面还在载入就回 false 而不是去跑。
func (a *app) eval(ctx context.Context, t webTarget, js string, loading bool) (any, error) {
	h, err := a.target(ctx, t)
	if err != nil {
		return nil, err
	}
	pg := h.view.Page()
	if loading && pg.IsLoading() {
		return false, nil
	}
	v, err := pg.EvalContext(ctx, js)
	if je := (*mygo.EvalError)(nil); errors.As(err, &je) {
		return nil, rpc.Errorf("js_error", "%s", je.Message)
	}
	return v, h.closedErr(err)
}

func (a *app) webScreenshot(ctx context.Context, params json.RawMessage) (any, error) {
	var p webTarget
	if err := rpc.Decode(params, &p); err != nil {
		return nil, err
	}
	h, err := a.target(ctx, p)
	if err != nil {
		return nil, err
	}
	png, err := h.view.CapturePage()
	if err = h.closedErr(err); err != nil {
		var re *rpc.Error
		if errors.As(err, &re) {
			return nil, err
		}
		return nil, rpc.Errorf(rpc.CodeInternal, "capture page: %v", err)
	}
	// 从没显示过的页面没有尺寸，WebKit 交回空图：工作区在窗口里没选中过时就是这样
	if len(png) == 0 {
		return nil, rpc.Errorf(rpc.CodeConflict, "tab %d has never been on screen, so there is nothing to capture; it shows once its workspace is selected in the Posse window (snapshot works without it)", h.id)
	}
	return struct {
		Tab int    `json:"tab"`
		PNG []byte `json:"png"`
	}{h.id, png}, nil
}

// webDeviceSet 开关手机预览（webdevice.go）：device 是机型 ID，off 或空是关掉。
func (a *app) webDeviceSet(ctx context.Context, params json.RawMessage) (any, error) {
	var p struct {
		webTarget
		Device    string `json:"device"`
		Landscape bool   `json:"landscape,omitempty"`
	}
	if err := rpc.Decode(params, &p); err != nil {
		return nil, err
	}
	if p.Device == "off" {
		p.Device = ""
	}
	dev, ok := deviceByID(p.Device)
	if p.Device != "" && !ok {
		ids := make([]string, 0, len(webDevices))
		for _, d := range webDevices {
			ids = append(ids, d.id)
		}
		return nil, rpc.Errorf(rpc.CodeBadRequest, "unknown device %q; use one of %s or off", p.Device, strings.Join(ids, ", "))
	}
	var tab int
	var ferr error
	err := a.onMain(ctx, func() {
		wd, err := a.findWeb(p.webTarget)
		if err != nil {
			ferr = err
			return
		}
		a.setWebDevice(wd, p.Device, p.Landscape)
		tab = wd.id
	})
	if err != nil {
		return nil, err
	}
	if ferr != nil {
		return nil, ferr
	}
	out := map[string]any{"tab": tab, "device": p.Device}
	if ok {
		w, h := dev.size(p.Landscape)
		out["name"], out["width"], out["height"] = dev.name, w, h
	}
	return out, nil
}
