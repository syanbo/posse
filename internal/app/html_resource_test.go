package app

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/egoist/mygo"
	"github.com/egoist/mygo/ui/editor"
	"golang.org/x/net/html"

	"posse/internal/client"
	"posse/internal/files"
	"posse/internal/rpc"
)

func previewResourceStamp(data []byte) files.Stamp {
	return files.Stamp{Ino: 42, Size: int64(len(data)), Mode: 0600, ModTime: time.Unix(123, 0)}
}

// 使用真正的查看身份和客户端分段/摘要协议；测试没有 HTTP 监听器。
func installPreviewResources(t *testing.T, a *app, f *hostControlFixture, root string, contents map[string][]byte) *atomic.Int32 {
	t.Helper()
	identity := *a.request().client.Desktop
	var calls atomic.Int32
	f.mu.Lock()
	f.desktopCall = func(_ context.Context, p rpc.DesktopCall) (any, error) {
		if p.Sequence != 0 || p.Desktop != identity {
			t.Error("resource read changed viewing identity")
		}
		var q rpc.FileResourceRangeParams
		if err := rpc.Decode(p.Params, &q); err != nil {
			return nil, err
		}
		if q.Root != root {
			t.Errorf("root = %q, want %q", q.Root, root)
		}
		data, ok := contents[q.Path]
		if !ok {
			return nil, errors.New("unknown resource: " + q.Path)
		}
		stamp := previewResourceStamp(data)
		switch p.Method {
		case "files.resourceRange":
			calls.Add(1)
			return rpc.FileRangeResult{Data: data, Stamp: stamp, EOF: true}, nil
		case "files.resourceVersion":
			return imageResourceVersion(root+"/"+q.Path, data, stamp), nil
		default:
			t.Errorf("unexpected method: %s", p.Method)
			return nil, errors.New("unexpected method")
		}
	}
	f.mu.Unlock()
	return &calls
}

func servePreview(raw string) *httptest.ResponseRecorder {
	w := httptest.NewRecorder()
	serveHTMLResource(w, httptest.NewRequest(http.MethodGet, raw, nil))
	return w
}

func previewAttr(t *testing.T, source, tag, key string) string {
	t.Helper()
	z := html.NewTokenizer(strings.NewReader(source))
	for z.Next() != html.ErrorToken {
		token := z.Token()
		if token.Data != tag {
			continue
		}
		for _, a := range token.Attr {
			if a.Key == key {
				return a.Val
			}
		}
	}
	t.Fatalf("missing %s[%s] in %s", tag, key, source)
	return ""
}

func TestRemoteHTMLResources(t *testing.T) {
	a, f := requestTestApp(t, "remote")
	root := t.TempDir()
	if err := os.Mkdir(filepath.Join(root, "docs"), 0700); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(root, "docs/index.html")
	body := []byte(`<link href="link/../theme.css" rel="stylesheet"><img src="../images/x.png"><script src="app.js"></script>`)
	if err := os.WriteFile(path, []byte("poison HTML"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "docs/theme.css"), []byte("poison CSS"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(root, "images"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "images/x.png"), []byte("poison image"), 0600); err != nil {
		t.Fatal(err)
	}
	css := []byte(`body { background: url("../font.woff2"); }`)
	resources := map[string][]byte{"docs/index.html": body, "docs/link/../theme.css": css, "docs/../images/x.png": []byte("target image"), "docs/app.js": []byte("window.target=true"), "docs/link/../../font.woff2": []byte("target font")}
	calls := installPreviewResources(t, a, f, root, resources)
	s, err := newHTMLResource(a.request(), previewResourceRoot(path, root), path, previewResourceStamp(body), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer s.close()
	if strings.Contains(s.mainURL, root) || strings.Contains(s.mainURL, "docs") {
		t.Fatal("URL leaked target directories")
	}
	w := servePreview(s.mainURL)
	if w.Code != 200 || strings.Contains(w.Body.String(), "poison") {
		t.Fatalf("HTML: %d %s", w.Code, w.Body)
	}
	for _, tc := range []struct{ tag, key, want string }{{"img", "src", "target image"}, {"script", "src", "window.target=true"}} {
		r := servePreview(previewAttr(t, w.Body.String(), tc.tag, tc.key))
		if r.Code != 200 || r.Body.String() != tc.want {
			t.Fatalf("%s: %d %s", tc.tag, r.Code, r.Body)
		}
	}
	style := servePreview(previewAttr(t, w.Body.String(), "link", "href"))
	start := strings.Index(style.Body.String(), `url("`) + len(`url("`)
	end := strings.Index(style.Body.String()[start:], `"`) + start
	font := servePreview(style.Body.String()[start:end])
	if style.Code != 200 || font.Body.String() != "target font" {
		t.Fatalf("CSS/font: %s / %s", style.Body, font.Body)
	}
	if calls.Load() != 5 {
		t.Fatalf("resource reads = %d", calls.Load())
	}
	if a.request().snapshot.Controlled {
		t.Fatal("preview took control")
	}
	// 相同 token 的 reload 必须再次经过 ReadResource。
	if again := servePreview(s.mainURL); again.Code != 200 || calls.Load() != 6 {
		t.Fatal("reload reused cached main bytes")
	}
}

func TestRemoteHTMLBoundariesAndRewrite(t *testing.T) {
	a, f := requestTestApp(t, "remote")
	calls := installPreviewResources(t, a, f, "/repo", nil)
	s, err := newHTMLResource(a.request(), "/repo", "/repo/docs/index.html", files.Stamp{}, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer s.close()
	for _, raw := range []string{"../../outside.html", "file:///outside.html", "~/image.png", "x%00.png", "/repo/../outside.png"} {
		if got := s.rewriteURL(s.path, raw); got != "about:blank" {
			t.Errorf("unsafe %q = %q", raw, got)
		}
	}
	for _, raw := range []string{"http://localhost:99/x", "//test.localhost/x", "HTTP://LOCALHOST./x", "http://127.0.0.1/x", "http://[::1]/x", "http://[::]/x", "http://0.0.0.0/x", "http://127.1/x", "http://2130706433/x", "http://0x7f000001/x", "http://0177.0.0.1/x", "http://0/x", "http:127.0.0.1/x", "http:///localhost/x", "https:/localhost/x"} {
		body := s.rewriteHTML(s.path, []byte(`<base href="file:///tmp/"><img src="`+raw+`"><a href="`+raw+`">x</a><style>@import "`+raw+`";x{background:url('`+raw+`')}</style><p style="background:url(&quot;`+raw+`&quot;)"></p>`))
		if strings.Contains(string(body), raw) || strings.Contains(string(body), "file:") {
			t.Errorf("unsafe HTML remained: %s", body)
		}
	}
	css := s.rewriteCSS(s.path, `@import /* x */ "http://local\68 ost/x";x{background:u\72l(http://127.1/x);content:"http://localhost/x"}`)
	if strings.Contains(css, "http:") || !strings.Contains(css, "about:blank") {
		t.Fatalf("CSS escapes: %s", css)
	}
	if got := s.rewriteURL(s.path, "https://example.com/a.css"); got != "https://example.com/a.css" {
		t.Fatal(got)
	}
	for _, raw := range []string{s.mainURL + "/../../../escape", htmlResourceScheme + "://unknown/x", htmlResourceScheme + "://" + s.token + "/bad/../../escape"} {
		if w := servePreview(raw); w.Code < 400 {
			t.Fatalf("accepted %s", raw)
		}
	}
	if calls.Load() != 0 {
		t.Fatal("rejected paths invoked resource reads")
	}
	if root := previewResourceRoot("/outside/index.html", "/repo"); root != "/outside" {
		t.Fatal(root)
	}
}

func TestRemoteHTMLCancellation(t *testing.T) {
	for _, mode := range []string{"closed", "host", "generation", "identity", "stamp"} {
		t.Run(mode, func(t *testing.T) {
			a, f := requestTestApp(t, "remote")
			body := []byte("target HTML")
			st := previewResourceStamp(body)
			s, err := newHTMLResource(a.request(), "/repo", "/repo/index.html", st, nil)
			if err != nil {
				t.Fatal(err)
			}
			defer s.close()
			d := &fileDoc{path: "/repo/index.html", kind: docText, seen: st, ed: editor.State{Buffer: editor.NewBuffer("source")}}
			pg := &htmlPage{remote: true, seen: st, request: a.request(), resource: s}
			d.page = pg
			a.files = map[string]*filesView{"w": {docs: []*fileDoc{d}}}
			entered, release := make(chan struct{}), make(chan struct{})
			var once sync.Once
			defer once.Do(func() { close(release) })
			var calls atomic.Int32
			f.mu.Lock()
			f.desktopCall = func(ctx context.Context, p rpc.DesktopCall) (any, error) {
				if p.Sequence != 0 {
					t.Error("write sequence")
				}
				if p.Method == "files.resourceVersion" {
					return imageResourceVersion("/repo/index.html", body, st), nil
				}
				calls.Add(1)
				close(entered)
				select {
				case <-release:
				case <-ctx.Done():
					return nil, ctx.Err()
				}
				return rpc.FileRangeResult{Data: body, Stamp: st, EOF: true}, nil
			}
			f.mu.Unlock()
			done := make(chan *httptest.ResponseRecorder, 1)
			go func() { done <- servePreview(s.mainURL) }()
			waitConnectionEvent(t, entered)
			switch mode {
			case "closed":
				closeWeb(d)
			case "host":
				local := newHostApp(&client.Client{}, "")
				v := newHostViews(local)
				v.add("remote", a)
				v.active.Store(a)
				v.show(localHostID)
			case "generation":
				a.viewGeneration.Add(1)
				a.closeTargetPreviews()
			case "identity":
				takeRequestTestControl(t, a)
			case "stamp":
				newStamp := st
				newStamp.Ino++
				a.diskChanged(d, newStamp, files.Content{Stamp: newStamp, Text: "new source"}, nil)
			}
			once.Do(func() { close(release) })
			select {
			case w := <-done:
				if w.Code < 400 || strings.Contains(w.Body.String(), "target HTML") {
					t.Fatalf("late result: %d %s", w.Code, w.Body)
				}
			case <-time.After(5 * time.Second):
				t.Fatal("resource did not cancel")
			}
			if w := servePreview(s.mainURL); w.Code < 400 || calls.Load() != 1 {
				t.Fatal("obsolete token read again")
			}
		})
	}
}

type htmlLoadSpy struct{ file, url string }

func (s *htmlLoadSpy) LoadFile(path string) error { s.file = path; return nil }
func (s *htmlLoadSpy) LoadURL(raw string) error   { s.url = raw; return nil }

func TestHTMLLocalAndRemoteLoad(t *testing.T) {
	a, f := requestTestApp(t, "remote")
	calls := installPreviewResources(t, a, f, "/local", nil)
	c := *a.request().client
	c.Remote = false
	local := &app{c: &c}
	pg := local.newHTMLPage(&workspace{Path: "/local"}, &fileDoc{path: "/local/index.html"})
	if pg.remote || pg.resource != nil {
		t.Fatal("local preview acquired target resources")
	}
	p := &htmlLoadSpy{}
	if err := loadHTMLDocument(p, "/local/index.html", ""); err != nil || p.file != "/local/index.html" || p.url != "" {
		t.Fatal("local LoadFile route changed")
	}
	if calls.Load() != 0 {
		t.Fatal("local preview requested resourceRange")
	}
	p = &htmlLoadSpy{}
	if err := loadHTMLDocument(p, "/remote/index.html", htmlResourceScheme+"://token/main"); err != nil || p.file != "" || !strings.HasPrefix(p.url, htmlResourceScheme+":") {
		t.Fatal("remote page granted local file access")
	}
	registerHTMLResources()
	if !mygo.Protocol.IsHandled(htmlResourceScheme) {
		t.Fatal("scheme not registered")
	}
}

func TestRemoteHTMLFailureKeepsPage(t *testing.T) {
	a, _ := requestTestApp(t, "remote")
	st := files.Stamp{Ino: 42}
	s, err := newHTMLResource(a.request(), "/repo", "/repo/index.html", st, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer s.close()
	// 没有原生窗口也能验证失败不会碰已经展示的 WebView。
	view := &mygo.WebView{}
	pg := &htmlPage{remote: true, view: view, seen: st, resource: s}
	d := &fileDoc{seen: st, ed: editor.State{Buffer: editor.NewBuffer("original source")}}
	a.finishRemoteHTML(d, pg, s, errors.New("unavailable"))
	if pg.view != view || pg.resource != nil || pg.err == "" || a.err == "" || d.ed.Buffer.String() != "original source" {
		t.Fatal("failure replaced successful page/source")
	}
	if w := servePreview(s.mainURL); w.Code != http.StatusGone {
		t.Fatal("failed token still usable")
	}
}

func TestRemoteHTMLRootURLs(t *testing.T) {
	a, _ := requestTestApp(t, "remote")
	s, err := newHTMLResource(a.request(), "/repo", "/repo/docs/index.html", files.Stamp{}, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer s.close()
	for src, want := range map[string]string{"/images/x.png": "images/x.png", "link/../other.html": "docs/link/../other.html", "file:///repo/docs/x.html": "docs/x.html"} {
		u, err := url.Parse(s.rewriteURL(s.path, src))
		if err != nil {
			t.Fatal(err)
		}
		got, err := s.resolve(u)
		if err != nil || strings.TrimPrefix(got, "./") != want {
			t.Fatalf("%s => %q, %v", src, got, err)
		}
	}
}

func TestRemoteHTMLDocumentVersion(t *testing.T) {
	a, f := requestTestApp(t, "remote")
	body := []byte(`<img src="http://localhost/x">`)
	installPreviewResources(t, a, f, "/repo", map[string][]byte{"./index.xhtml": body, "./child": []byte(`<!DOCTYPE html><img src="http://localhost/x">`)})
	st := previewResourceStamp(body)
	st.Ino++
	var failure error
	s, err := newHTMLResource(a.request(), "/repo", "/repo/index.xhtml", st, func(_ *htmlResource, err error) { failure = err })
	if err != nil {
		t.Fatal(err)
	}
	defer s.close()
	w := servePreview(s.mainURL)
	if w.Code < 400 || !errors.Is(failure, files.ErrChanged) || strings.Contains(w.Body.String(), "localhost") {
		t.Fatalf("changed main document: %d %v %s", w.Code, failure, w.Body)
	}
	s.close()
	s, err = newHTMLResource(a.request(), "/repo", "/repo/index.xhtml", previewResourceStamp(body), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer s.close()
	w = servePreview(s.mainURL)
	if w.Code != 200 || strings.Contains(w.Body.String(), "localhost") {
		t.Fatalf("XHTML was not sanitized: %d %s", w.Code, w.Body)
	}
	w = servePreview(s.rewriteURL(s.path, "child"))
	if w.Code != 200 || strings.Contains(w.Body.String(), "localhost") {
		t.Fatalf("sniffed HTML was not sanitized: %d %s", w.Code, w.Body)
	}
}

func TestRemoteHTMLRequestCancelledReportsFailure(t *testing.T) {
	a, f := requestTestApp(t, "remote")
	entered := make(chan struct{})
	f.mu.Lock()
	f.desktopCall = func(ctx context.Context, _ rpc.DesktopCall) (any, error) {
		close(entered)
		<-ctx.Done()
		return nil, ctx.Err()
	}
	f.mu.Unlock()
	var failure error
	s, err := newHTMLResource(a.request(), "/repo", "/repo/index.html", files.Stamp{}, func(_ *htmlResource, err error) { failure = err })
	if err != nil {
		t.Fatal(err)
	}
	defer s.close()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	w := httptest.NewRecorder()
	done := make(chan struct{})
	go func() {
		serveHTMLResource(w, httptest.NewRequest(http.MethodGet, s.mainURL, nil).WithContext(ctx))
		close(done)
	}()
	waitConnectionEvent(t, entered)
	cancel()
	waitConnectionEvent(t, done)
	if w.Code < 400 || failure == nil {
		t.Fatalf("cancelled main load was treated as success: %d, %v", w.Code, failure)
	}
}
