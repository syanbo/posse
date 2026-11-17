package app

import (
	"context"
	"errors"
	"net"
	"net/http"
	"net/url"
	"path/filepath"
	"strings"
	"time"

	"posse/internal/files"
	"posse/internal/media"
)

func (r appRequest) readDocImage(ctx context.Context, path string) (files.Stamp, media.PreviewImage, *animPlayer, error) {
	if !r.targetFiles() {
		st := files.StatFile(path)
		pi, anim, err := decodeDocImage(path)
		return st, pi, anim, err
	}
	if !r.current() || r.client == nil {
		return files.Stamp{}, media.PreviewImage{}, nil, context.Canceled
	}
	// 这是用户明确打开的文件；嵌入 Markdown 的资源另受文档根目录限制。
	data, st, err := r.client.ReadResource(ctx, string(filepath.Separator), strings.TrimPrefix(path, string(filepath.Separator)), files.MaxSize)
	if err != nil {
		return st, media.PreviewImage{}, nil, err
	}
	if !r.current() {
		return st, media.PreviewImage{}, nil, context.Canceled
	}
	if err := ctx.Err(); err != nil {
		return st, media.PreviewImage{}, nil, err
	}
	pi, anim, err := decodeDocImageBytes(data)
	if err := ctx.Err(); err != nil {
		return st, media.PreviewImage{}, nil, err
	}
	return st, pi, anim, err
}

func (a *app) docImages(d *fileDoc, workspaceRoot string) *mdImages {
	dir, root := filepath.Dir(d.path), workspaceRoot
	remote := a.remoteHost()
	if remote {
		root = previewResourceRoot(d.path, workspaceRoot)
	}
	var version uint64
	if d.ed.Buffer != nil {
		version = d.ed.Buffer.Version()
	}
	l := d.mdImgs
	if l != nil && (l.dir != dir || l.root != root || remote && (l.request.app == nil || !l.request.current() || l.version != version)) {
		l.close()
		l, d.mdImgs = nil, nil
	}
	if l == nil {
		l = newMDImages(dir, root)
		if remote {
			l.request, l.version = a.request(), version
			l.readLocal = l.readTargetImage
		}
		d.mdImgs = l
	}
	return l
}

func (l *mdImages) readTargetImage(src string) ([]byte, string, error) {
	r := l.request
	if !r.current() || r.client == nil {
		return nil, "", context.Canceled
	}
	path, err := targetImagePath(l.dir, l.root, src)
	if err != nil {
		return nil, "", err
	}
	select {
	case mdFetching <- struct{}{}:
	case <-l.ctx.Done():
		return nil, "", errMDClosed
	}
	defer func() { <-mdFetching }()
	if !r.current() {
		return nil, "", context.Canceled
	}
	ctx, cancel := context.WithTimeout(l.ctx, mdFetchTime)
	defer cancel()
	data, _, err := r.client.ReadResource(ctx, l.root, path, mdMaxDownload)
	if err != nil {
		return nil, "", err
	}
	if !r.current() {
		return nil, "", context.Canceled
	}
	return data, path, nil
}

func targetImageHTTP() *http.Client {
	transport := http.DefaultTransport.(*http.Transport).Clone()
	// 代理会自行解析目标域名，无法保证最终连接仍使用这里核验过的地址。
	transport.Proxy = nil
	dialer := &net.Dialer{Timeout: mdFetchTime, KeepAlive: 30 * time.Second}
	transport.DialContext = targetImageDialer(net.DefaultResolver.LookupIPAddr, dialer.DialContext)
	return &http.Client{Transport: transport, Timeout: mdFetchTime, CheckRedirect: func(req *http.Request, via []*http.Request) error {
		if localImageURL(req.URL.String()) {
			return targetFileError("files.httpResource")
		}
		if len(via) >= 10 {
			return errors.New("stopped after 10 redirects")
		}
		return nil
	}}
}

func targetImageDialer(lookup func(context.Context, string) ([]net.IPAddr, error), dial func(context.Context, string, string) (net.Conn, error)) func(context.Context, string, string) (net.Conn, error) {
	return func(ctx context.Context, network, address string) (net.Conn, error) {
		ctx, cancel := context.WithTimeout(ctx, mdFetchTime)
		defer cancel()
		host, port, err := net.SplitHostPort(address)
		if err != nil {
			return nil, err
		}
		ips, err := lookup(ctx, host)
		if err != nil {
			return nil, err
		}
		for _, ip := range ips {
			if ip.IP.IsLoopback() || ip.IP.IsUnspecified() {
				return nil, targetFileError("files.httpResource")
			}
		}
		var last error = &net.DNSError{Err: "no such host", Name: host}
		deadline, _ := ctx.Deadline()
		for i, ip := range ips {
			// 给剩余地址保留时间，避免首个无响应的 IPv6 地址耗尽整个请求期限。
			attempt, stop := context.WithTimeout(ctx, time.Until(deadline)/time.Duration(len(ips)-i))
			// 只拨核验后的 IP，不再把域名交给 DialContext 重新解析。
			conn, err := dial(attempt, network, net.JoinHostPort(ip.String(), port))
			stop()
			if err == nil {
				return conn, nil
			}
			last = err
			if ctx.Err() != nil {
				return nil, ctx.Err()
			}
		}
		return nil, last
	}
}

func localImageURL(src string) bool {
	u, err := url.Parse(src)
	if err != nil {
		return false
	}
	host := strings.TrimSuffix(strings.ToLower(u.Hostname()), ".")
	if host == "localhost" || strings.HasSuffix(host, ".localhost") {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && (ip.IsLoopback() || ip.IsUnspecified())
}

func previewResourceRoot(path, workspaceRoot string) string {
	dir := filepath.Dir(path)
	if rel, err := filepath.Rel(workspaceRoot, dir); err != nil || !filepath.IsAbs(workspaceRoot) || !filepath.IsLocal(rel) {
		return dir
	}
	return workspaceRoot
}

func targetImagePath(dir, root, src string) (string, error) {
	rel, err := filepath.Rel(root, dir)
	if err != nil {
		return "", errors.New("file resource is outside the preview root")
	}
	return targetResourcePath(rel, root, src)
}

// dir 是根内的原始相对目录，不能 Clean：CSS 本身也可能经 link/.. 打开。
func targetResourcePath(dir, root, src string) (string, error) {
	u, err := url.Parse(src)
	if err != nil || u.Scheme != "" && u.Scheme != "file" || u.Host != "" && u.Host != "localhost" {
		return "", errors.New("unsupported image url")
	}
	path := u.Path
	if u.Opaque != "" {
		path, err = url.PathUnescape(u.Opaque)
		if err != nil {
			return "", errors.New("unsupported image url")
		}
	}
	outside := errors.New("file resource is outside the preview root")
	if strings.HasPrefix(path, "~/") || strings.ContainsRune(path, '\x00') {
		return "", outside
	}
	if filepath.IsAbs(path) {
		prefix := strings.TrimRight(root, string(filepath.Separator)) + string(filepath.Separator)
		if rel, ok := strings.CutPrefix(path, prefix); ok {
			if !filepath.IsLocal(rel) {
				return "", outside
			}
			return rel, nil
		}
		if u.Scheme == "file" {
			return "", outside
		}
		// Markdown 的 /images/x.png 按仓库根目录解释，不借此读取根外的绝对路径。
		path = strings.TrimLeft(path, string(filepath.Separator))
	} else {
		if !filepath.IsLocal(dir) {
			return "", outside
		}
		// 保留 link/.. 给目标按文件系统语义解析，不在窗口提前 Clean。
		path = dir + string(filepath.Separator) + path
	}
	if !filepath.IsLocal(path) {
		return "", outside
	}
	return path, nil
}
