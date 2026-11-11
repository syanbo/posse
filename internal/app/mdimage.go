package app

import (
	"bytes"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/egoist/mygo/ui"

	"posse/internal/media"
)

// mdImages 载入文件预览里的图片，按地址缓存在打开的文档上，关掉标签就释放。
// 相对路径按文档所在目录找，/ 开头的找不到时按工作区根目录找（GitHub 上 / 指仓库根目录）；
// http(s) 的图片照 GitHub 下载；data: 地址直接解。
type mdImages struct {
	dir, root string
	mu        sync.Mutex
	m         map[string]*mdImgEntry
	closed    chan struct{} // 文档关掉时关闭：排队等下载名额的图不再发请求
	closeOnce sync.Once
}

// errMDClosed 是文档关掉了、没去下载的图。不会显示：文档已经不在了。
var errMDClosed = errors.New("document closed")

func (l *mdImages) close() { l.closeOnce.Do(func() { close(l.closed) }) }

type mdImgEntry struct {
	src  ui.ImageSource
	w, h float32 // 原图尺寸
	err  error
	done bool
}

// 解出的位图最多这么多像素，超过的等比缩小：README 的截图常有 2400 宽，预览版心只有 820。
const mdMaxPixels = 4 << 20

// 远程图片的大小上限和超时。
const (
	mdMaxDownload = 20 << 20
	mdFetchTime   = 20 * time.Second
)

var errSVGText = errors.New("svg with text")

var mdHTTP = &http.Client{Timeout: mdFetchTime}

// mdFetchSlots 是同时下载的远程图片数：一篇文档里几十张徽章、截图时不一起发请求、一起占着内存（每张最多 mdMaxDownload）。
// 排队不算进超时，拿到名额才开始计时
const mdFetchSlots = 4

var mdFetching = make(chan struct{}, mdFetchSlots)

func newMDImages(dir, root string) *mdImages {
	return &mdImages{dir: dir, root: root, m: map[string]*mdImgEntry{}, closed: make(chan struct{})}
}

// get 返回图片现在的样子，第一次问时在后台开始载入，载完重画。
func (l *mdImages) get(c *ui.Context, src string) mdImgEntry {
	l.mu.Lock()
	e, ok := l.m[src]
	if !ok {
		e = &mdImgEntry{}
		l.m[src] = e
		services := c.Services()
		go func() {
			img, w, h, err := l.load(src)
			l.mu.Lock()
			e.src, e.w, e.h, e.err, e.done = img, w, h, err, true
			l.mu.Unlock()
			services.Invalidate()
		}()
	}
	out := *e
	l.mu.Unlock()
	return out
}

func (l *mdImages) load(src string) (ui.ImageSource, float32, float32, error) {
	data, name, err := l.read(src)
	if err != nil {
		return nil, 0, 0, err
	}
	return decodeMDImage(data, name)
}

// read 取图片的内容，name 是判断格式用的文件名或 Content-Type。
func (l *mdImages) read(src string) ([]byte, string, error) {
	switch {
	case strings.HasPrefix(src, "data:"):
		head, body, ok := strings.Cut(src[len("data:"):], ",")
		if !ok {
			return nil, "", errors.New("bad data url")
		}
		if strings.HasSuffix(head, ";base64") {
			b, err := base64.StdEncoding.DecodeString(body)
			return b, head, err
		}
		s, err := url.PathUnescape(body)
		return []byte(s), head, err
	case webURL(src):
		// 排队时文档关了就不下了，让出位置；已经在下载的照常下完
		select {
		case mdFetching <- struct{}{}:
		case <-l.closed:
			return nil, "", errMDClosed
		}
		defer func() { <-mdFetching }()
		select {
		case <-l.closed:
			return nil, "", errMDClosed
		default:
		}
		resp, err := mdHTTP.Get(src)
		if err != nil {
			return nil, "", err
		}
		defer resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			return nil, "", fmt.Errorf("http %d", resp.StatusCode)
		}
		b, err := io.ReadAll(io.LimitReader(resp.Body, mdMaxDownload+1))
		if err == nil && len(b) > mdMaxDownload {
			err = errors.New("image too large")
		}
		return b, resp.Header.Get("Content-Type") + " " + resp.Request.URL.Path, err
	case strings.Contains(src, "://") && !strings.HasPrefix(src, "file://"):
		return nil, "", errors.New("unsupported image url")
	}
	p := strings.TrimPrefix(strings.TrimPrefix(src, "file://"), "file:")
	// GitHub 上常写 x.png?raw=true，本地文件不带这些
	if i := strings.IndexAny(p, "?#"); i >= 0 {
		p = p[:i]
	}
	if u, err := url.PathUnescape(p); err == nil {
		p = u
	}
	if strings.HasPrefix(p, "~/") {
		if home, err := os.UserHomeDir(); err == nil {
			p = filepath.Join(home, p[2:])
		}
	}
	cands := []string{p}
	if !filepath.IsAbs(p) {
		cands = []string{filepath.Join(l.dir, filepath.FromSlash(p))}
	} else if l.root != "" {
		cands = append(cands, filepath.Join(l.root, filepath.FromSlash(p)))
	}
	var err error
	for _, path := range cands {
		var st os.FileInfo
		if st, err = os.Stat(path); err == nil {
			if st.Size() > mdMaxDownload {
				return nil, "", errors.New("image too large")
			}
			b, err := os.ReadFile(path)
			return b, path, err
		}
	}
	return nil, "", err
}

// decodeMDImage 解码图片：SVG 交给 mygo 画（它不画文字，带文字的 SVG 如徽章当解不开，显示说明文字），其余解成位图。
func decodeMDImage(data []byte, name string) (ui.ImageSource, float32, float32, error) {
	head := data[:min(len(data), 1024)]
	if strings.Contains(strings.ToLower(name), "svg") || bytes.Contains(head, []byte("<svg")) {
		if bytes.Contains(data, []byte("<text")) {
			return nil, 0, 0, errSVGText
		}
		s, err := ui.ParseSVG(data)
		if err != nil {
			return nil, 0, 0, err
		}
		w, h := s.Size()
		return s, w, h, nil
	}
	pi, err := media.DecodePreviewBytes(data, mdMaxPixels)
	if err != nil {
		return nil, 0, 0, err
	}
	return ui.NewBitmap(pi.Img), float32(pi.Size.X), float32(pi.Size.Y), nil
}
