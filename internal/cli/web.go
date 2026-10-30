package cli

import (
	"context"
	_ "embed"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"posse/internal/app"
	"posse/internal/rpc"
)

//go:embed web_guide.md
var webGuide string

// codeNoWindow 是 Posse 窗口没开：网页标签在窗口进程里，不替它拉起窗口。
const codeNoWindow = "no_window"

var errNoWindow = &rpc.Error{Code: codeNoWindow, Message: "the Posse window is not open; web tabs live in the window (ask the user to open Posse)"}

// webFlags 是各 web 子命令共用的旗标。
type webFlags struct {
	fs        *flag.FlagSet
	workspace *string
	tab       *int
	timeout   *time.Duration
}

func newWebFlags(name string, timeout time.Duration) *webFlags {
	fs := flag.NewFlagSet(name, flag.ContinueOnError)
	return &webFlags{
		fs:        fs,
		workspace: fs.String("workspace", "", ""),
		tab:       fs.Int("tab", 0, ""),
		timeout:   fs.Duration("timeout", timeout, ""),
	}
}

// params 是请求的目标：不带 --workspace 时用本终端所在的工作区，不在 Posse 终端里时由窗口取侧栏选中的。
func (f *webFlags) params(extra map[string]any) map[string]any {
	p := map[string]any{}
	ws := *f.workspace
	if ws == "" {
		ws = os.Getenv("POSSE_WORKSPACE_ID")
	}
	if ws != "" {
		p["workspace"] = ws
	}
	if *f.tab != 0 {
		p["tab"] = *f.tab
	}
	for k, v := range extra {
		p[k] = v
	}
	return p
}

// callApp 向窗口进程发请求。连不上 socket 就是窗口没开。
func (e *env) callApp(timeout time.Duration, method string, params, out any) error {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if timeout > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, timeout)
		defer cancel()
	}
	err := rpc.Call(ctx, app.CtlSocket(e.home), method, params, out)
	var op *net.OpError
	switch {
	case err == nil:
		return nil
	// 先认超时：拨号时就已经到期的也是 dial 的 OpError，不能报成窗口没开
	case errors.Is(err, context.DeadlineExceeded):
		return rpc.Errorf(rpc.CodeTimeout, "no answer from the web page within %s", timeout)
	case errors.As(err, &op) && op.Op == "dial":
		return errNoWindow
	case errors.Is(err, rpc.ErrNoResponse):
		return &rpc.Error{Code: codeNoWindow, Message: "the Posse window closed before answering"}
	}
	return err
}

// doApp 是 callApp 加输出：--json 原样输出结果，否则调用 human。
func (e *env) doApp(f *webFlags, method string, params, v any, human func()) error {
	var raw json.RawMessage
	if err := e.callApp(*f.timeout, method, params, &raw); err != nil {
		return err
	}
	if v != nil && len(raw) > 0 {
		if err := json.Unmarshal(raw, v); err != nil {
			return err
		}
	}
	return e.emit(raw, human)
}

func cmdWeb(e *env, args []string) error {
	if len(args) == 0 {
		fmt.Fprint(e.out, webGuide)
		return nil
	}
	return fmt.Errorf("unknown command %q; see posse web guide", "web "+args[0])
}

func cmdWebGuide(e *env, args []string) error {
	fmt.Fprint(e.out, webGuide)
	return nil
}

type webTab struct {
	Tab     int    `json:"tab"`
	URL     string `json:"url"`
	Title   string `json:"title"`
	Loading bool   `json:"loading"`
	Failed  string `json:"failed"`
	Current bool   `json:"current"`
}

func cmdWebOpen(e *env, args []string) error {
	f := newWebFlags("web open", 30*time.Second)
	pos, err := parse(f.fs, args)
	if err != nil {
		return err
	}
	if err := need(pos, 1, "web open"); err != nil {
		return err
	}
	var r webTab
	return e.doApp(f, "web.open", f.params(map[string]any{"url": pos[0]}), &r, func() {
		fmt.Fprintf(e.out, "tab %d  %s\n", r.Tab, r.URL)
	})
}

func cmdWebList(e *env, args []string) error {
	f := newWebFlags("web list", 30*time.Second)
	if _, err := parse(f.fs, args); err != nil {
		return err
	}
	var r []webTab
	return e.doApp(f, "web.list", f.params(nil), &r, func() {
		if len(r) == 0 {
			fmt.Fprintln(e.out, "no web tabs (posse web open <url>)")
		}
		for _, t := range r {
			mark := " "
			if t.Current {
				mark = "*"
			}
			state := ""
			switch {
			case t.Failed != "":
				state = "  [failed: " + t.Failed + "]"
			case t.Loading:
				state = "  [loading]"
			}
			fmt.Fprintf(e.out, "%s %d  %s  %s%s\n", mark, t.Tab, t.Title, t.URL, state)
		}
	})
}

// webNav 是 goto、reload、back、forward。
func webNav(action string, withURL bool) func(e *env, args []string) error {
	return func(e *env, args []string) error {
		f := newWebFlags("web "+action, 30*time.Second)
		pos, err := parse(f.fs, args)
		if err != nil {
			return err
		}
		extra := map[string]any{"action": action}
		if withURL {
			if err := need(pos, 1, "web "+action); err != nil {
				return err
			}
			extra["url"] = pos[0]
		}
		return e.doApp(f, "web.nav", f.params(extra), nil, func() { fmt.Fprintln(e.out, "ok") })
	}
}

// webRun 调页面里的一个函数（webctl.js），结果按 JSON 收回。
func (e *env) webRun(f *webFlags, fn string, args []any, out any) error {
	return e.callApp(*f.timeout, "web.run", f.params(map[string]any{"fn": fn, "args": args}), out)
}

func cmdWebWait(e *env, args []string) error {
	f := newWebFlags("web wait", 30*time.Second)
	kind := f.fs.String("for", "load", "")
	pos, err := parse(f.fs, args)
	if err != nil {
		return err
	}
	arg := strings.Join(pos, " ")
	switch *kind {
	case "load":
	case "selector", "text":
		if arg == "" {
			return fmt.Errorf("usage: posse web wait --for %s <%s>", *kind, map[string]string{"selector": "css", "text": "text"}[*kind])
		}
	default:
		return fmt.Errorf("--for must be load, selector or text")
	}
	deadline := time.Now().Add(*f.timeout)
	for {
		// 每次检查单独限时：页面卡住时也按 --timeout 结束
		left := max(time.Until(deadline), time.Millisecond)
		check := *f
		check.timeout = &left
		var ok bool
		err := e.webRun(&check, "check", []any{*kind, arg}, &ok)
		if err == nil && ok {
			return e.emit(map[string]any{"for": *kind}, func() { fmt.Fprintln(e.out, "ok") })
		}
		// 跳转中页面换掉了、脚本跑到一半被打断时 WebKit 报的是原生错误（internal），下一次再查；
		// js_error 是检查本身出错（选择器写错），等下去也不会好
		if err != nil {
			var re *rpc.Error
			if !errors.As(err, &re) || (re.Code != rpc.CodeInternal && re.Code != rpc.CodeTimeout) {
				return err
			}
		}
		if time.Now().After(deadline) {
			return rpc.Errorf(rpc.CodeTimeout, "web page did not reach %s within %s", strings.TrimSpace(*kind+" "+arg), *f.timeout)
		}
		time.Sleep(250 * time.Millisecond)
	}
}

func cmdWebSnapshot(e *env, args []string) error {
	f := newWebFlags("web snapshot", 30*time.Second)
	if _, err := parse(f.fs, args); err != nil {
		return err
	}
	var r struct {
		URL       string `json:"url"`
		Title     string `json:"title"`
		Tree      string `json:"tree"`
		Count     int    `json:"count"`
		Truncated bool   `json:"truncated"`
	}
	if err := e.webRun(f, "snapshot", nil, &r); err != nil {
		return err
	}
	return e.emit(r, func() {
		fmt.Fprintf(e.out, "%s\n%s\n\n", r.Title, r.URL)
		if r.Tree == "" {
			fmt.Fprintln(e.out, "(no visible interactive elements)")
		} else {
			fmt.Fprintln(e.out, r.Tree)
		}
		if r.Truncated {
			fmt.Fprintf(e.out, "… truncated at %d lines\n", r.Count)
		}
	})
}

// webAct 是 click、fill、press：成功时打印操作后的地址。
func webAct(fn string, n int) func(e *env, args []string) error {
	return func(e *env, args []string) error {
		f := newWebFlags("web "+fn, 30*time.Second)
		target := f.fs.String("on", "", "")
		pos, err := parse(f.fs, args)
		if err != nil {
			return err
		}
		if err := need(pos, n, "web "+fn); err != nil {
			return err
		}
		var a []any
		switch fn {
		case "fill":
			a = []any{pos[0], strings.Join(pos[1:], " ")}
		case "press":
			a = []any{pos[0], *target}
		default:
			a = []any{pos[0]}
		}
		var r struct {
			URL string `json:"url"`
		}
		if err := e.webRun(f, fn, a, &r); err != nil {
			return err
		}
		return e.emit(r, func() { fmt.Fprintln(e.out, "ok", r.URL) })
	}
}

func cmdWebEval(e *env, args []string) error {
	f := newWebFlags("web eval", 30*time.Second)
	pos, err := parse(f.fs, args)
	if err != nil {
		return err
	}
	if err := need(pos, 1, "web eval"); err != nil {
		return err
	}
	js := strings.Join(pos, " ")
	if js == "-" {
		b, err := io.ReadAll(os.Stdin)
		if err != nil {
			return err
		}
		js = string(b)
	}
	var raw json.RawMessage
	if err := e.callApp(*f.timeout, "web.eval", f.params(map[string]any{"js": js}), &raw); err != nil {
		return err
	}
	if len(raw) == 0 {
		raw = json.RawMessage("null")
	}
	return e.emit(raw, func() { fmt.Fprintln(e.out, string(raw)) })
}

func cmdWebScreenshot(e *env, args []string) error {
	f := newWebFlags("web screenshot", 30*time.Second)
	out := f.fs.String("o", "", "")
	if _, err := parse(f.fs, args); err != nil {
		return err
	}
	var r struct {
		Tab int    `json:"tab"`
		PNG []byte `json:"png"`
	}
	if err := e.callApp(*f.timeout, "web.screenshot", f.params(nil), &r); err != nil {
		return err
	}
	path := *out
	if path == "" {
		path = filepath.Join(os.TempDir(), fmt.Sprintf("posse-web-%d-%s.png", r.Tab, time.Now().Format("150405.000")))
	}
	if err := os.WriteFile(path, r.PNG, 0o644); err != nil {
		return err
	}
	return e.emit(map[string]any{"tab": r.Tab, "path": path}, func() { fmt.Fprintln(e.out, path) })
}

func cmdWebConsole(e *env, args []string) error {
	f := newWebFlags("web console", 30*time.Second)
	if _, err := parse(f.fs, args); err != nil {
		return err
	}
	var r []struct {
		Level string `json:"level"`
		Text  string `json:"text"`
		Time  int64  `json:"time"`
	}
	if err := e.webRun(f, "console", nil, &r); err != nil {
		return err
	}
	return e.emit(r, func() {
		if len(r) == 0 {
			fmt.Fprintln(e.out, "(no console output since the page loaded)")
		}
		for _, l := range r {
			fmt.Fprintf(e.out, "%s [%s] %s\n", time.UnixMilli(l.Time).Format("15:04:05"), l.Level, l.Text)
		}
	})
}
