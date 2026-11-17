package app

import (
	"context"
	"crypto/rand"
	"errors"
	"mime"
	"net/http"
	"net/url"
	"path/filepath"
	"strings"
	"sync"

	"github.com/egoist/mygo"

	"posse/internal/files"
)

const htmlResourceScheme = "posse-preview"

var htmlResources = struct {
	sync.Mutex
	pages map[string]*htmlResource
}{pages: map[string]*htmlResource{}}

var registerHTMLResources = sync.OnceFunc(func() {
	if err := mygo.Protocol.Handle(htmlResourceScheme, http.HandlerFunc(serveHTMLResource)); err != nil {
		panic(err)
	}
})

// 每次载入独占 token；URL 只带随机目录标识和文件名，不暴露目标的绝对路径。
// 请求协程只碰捕获的身份与不可变的文档版本，不读界面线程上的 fileDoc。
type htmlResource struct {
	request                    appRequest
	root, path, token, mainURL string
	stamp                      files.Stamp
	ctx                        context.Context
	cancel                     context.CancelFunc
	mu                         sync.Mutex
	dirs                       map[string]string
	dirIDs                     map[string]string
	failed                     func(*htmlResource, error)
}

func newHTMLResource(r appRequest, root, path string, stamp files.Stamp, failed func(*htmlResource, error)) (*htmlResource, error) {
	u := url.URL{Scheme: "file", Path: path}
	rel, err := targetImagePath(filepath.Dir(path), root, u.String())
	if err != nil {
		return nil, err
	}
	if !strings.Contains(rel, "/") {
		rel = "./" + rel
	}
	ctx, cancel := context.WithCancel(r.app.windowContext())
	s := &htmlResource{request: r, root: root, path: rel, token: strings.ToLower(rand.Text()), stamp: stamp, ctx: ctx, cancel: cancel, dirs: map[string]string{}, dirIDs: map[string]string{}, failed: failed}
	s.mainURL = s.resourceURL(rel)
	htmlResources.Lock()
	htmlResources.pages[s.token] = s
	htmlResources.Unlock()
	return s, nil
}

func (s *htmlResource) close() {
	s.cancel()
	htmlResources.Lock()
	delete(htmlResources.pages, s.token)
	htmlResources.Unlock()
}

func (s *htmlResource) current() bool {
	return s.ctx.Err() == nil && s.request.current()
}

func resourceDir(path string) string {
	if i := strings.LastIndexByte(path, '/'); i >= 0 {
		return path[:i]
	}
	return "."
}

func (s *htmlResource) resourceURL(path string) string {
	dir := resourceDir(path)
	s.mu.Lock()
	id := s.dirIDs[dir]
	if id == "" {
		id = strings.ToLower(rand.Text())
		s.dirIDs[dir], s.dirs[id] = id, dir
	}
	s.mu.Unlock()
	u := url.URL{Scheme: htmlResourceScheme, Host: s.token, Path: "/" + id + "/" + filepath.Base(path)}
	return u.String()
}

func (s *htmlResource) resolve(u *url.URL) (string, error) {
	if u.Scheme != htmlResourceScheme || u.Host != s.token || u.User != nil || u.Opaque != "" {
		return "", errors.New("invalid preview resource")
	}
	id, tail, ok := strings.Cut(strings.TrimPrefix(u.Path, "/"), "/")
	s.mu.Lock()
	dir, exists := s.dirs[id]
	s.mu.Unlock()
	if !ok || !exists || !filepath.IsLocal(tail) || strings.ContainsRune(tail, 0) || strings.HasPrefix(tail, "~/") {
		return "", errors.New("file resource is outside the preview root")
	}
	path := dir + "/" + tail
	if !filepath.IsLocal(path) {
		return "", errors.New("file resource is outside the preview root")
	}
	return path, nil
}

func serveHTMLResource(w http.ResponseWriter, req *http.Request) {
	htmlResources.Lock()
	s := htmlResources.pages[req.URL.Host]
	htmlResources.Unlock()
	if s == nil || !s.current() {
		http.Error(w, "preview closed", http.StatusGone)
		return
	}
	if req.Method != http.MethodGet && req.Method != http.MethodHead {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	path, err := s.resolve(req.URL)
	if err != nil {
		http.Error(w, "invalid preview resource", http.StatusForbidden)
		return
	}
	ctx, cancel := context.WithTimeout(s.ctx, mdFetchTime)
	stop := context.AfterFunc(req.Context(), cancel)
	defer stop()
	defer cancel()
	var data []byte
	var stamp files.Stamp
	if s.request.client == nil {
		err = context.Canceled
	} else {
		data, stamp, err = s.request.client.ReadResource(ctx, s.root, path, files.MaxSize)
	}
	if err == nil && path == s.path && (!stamp.Same(s.stamp) || stamp.Mode != s.stamp.Mode) {
		err = files.ErrChanged
	}
	if !s.current() {
		http.Error(w, "preview closed", http.StatusGone)
		return
	}
	if err == nil {
		err = ctx.Err()
	}
	if err != nil {
		if path == s.path && s.failed != nil {
			s.failed(s, err)
		}
		http.Error(w, "preview resource unavailable", http.StatusBadGateway)
		return
	}
	kind := mime.TypeByExtension(strings.ToLower(filepath.Ext(path)))
	if kind == "" {
		kind = http.DetectContentType(data)
	}
	switch {
	case isHTML(path) || strings.HasPrefix(kind, "text/html"):
		data = s.rewriteHTML(path, data)
		kind = "text/html; charset=utf-8"
	case strings.HasPrefix(kind, "text/css"):
		data = []byte(s.rewriteCSS(path, string(data)))
		kind = "text/css; charset=utf-8"
	}
	if !s.current() {
		http.Error(w, "preview closed", http.StatusGone)
		return
	}
	if err := ctx.Err(); err != nil {
		if path == s.path && s.failed != nil {
			s.failed(s, err)
		}
		http.Error(w, "preview resource unavailable", http.StatusBadGateway)
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Referrer-Policy", "no-referrer")
	if kind != "" {
		w.Header().Set("Content-Type", kind)
	}
	if req.Method != http.MethodHead {
		_, _ = w.Write(data)
	}
}

// 静态引用先换成不含 .. 的 URL，防止 WebKit 在请求到达目标前折叠软链路径。
func (s *htmlResource) rewriteURL(path, raw string) string {
	raw = strings.TrimSpace(strings.NewReplacer("\t", "", "\r", "", "\n", "").Replace(raw))
	if raw == "" || strings.HasPrefix(raw, "#") {
		return raw
	}
	u, err := url.Parse(raw)
	if err != nil || strings.ContainsRune(raw, '\\') {
		return "about:blank"
	}
	u.Scheme = strings.ToLower(u.Scheme)
	if u.Scheme == "http" || u.Scheme == "https" || u.Scheme == "" && u.Host != "" {
		if u.Host == "" || previewLocalURL(raw) {
			return "about:blank"
		}
		if u.Scheme == "" {
			u.Scheme = "https"
		}
		return u.String()
	}
	if u.Scheme == "data" || u.Scheme == "mailto" || u.Scheme == "tel" {
		return raw
	}
	if u.Scheme == htmlResourceScheme {
		if _, err := s.resolve(u); err == nil {
			return raw
		}
		return "about:blank"
	}
	if u.Scheme != "" && u.Scheme != "file" {
		return "about:blank"
	}
	rel, err := targetResourcePath(resourceDir(path), s.root, raw)
	if err != nil {
		return "about:blank"
	}
	out, _ := url.Parse(s.resourceURL(rel))
	out.RawQuery, out.Fragment = u.RawQuery, u.Fragment
	return out.String()
}
