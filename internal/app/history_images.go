package app

import (
	"context"
	"net/url"
	"path/filepath"

	"github.com/yuin/goldmark/text"

	"posse/internal/history"
)

func (d *historyDetail) closeImages() {
	if d != nil && d.imgs != nil {
		d.imgs.close()
		d.imgs = nil
	}
}

func (a *app) historyImages(s history.Session) *mdImages {
	h := &a.history
	d := h.detail
	if d == nil || sessionKey(d.Session) != sessionKey(s) || d.imageReq != h.detailReq {
		return nil
	}
	r := d.request
	if r.app == nil {
		r = a.request()
		d.request = r
	}
	if !r.current() {
		d.closeImages()
		return nil
	}
	if d.imgs != nil {
		return d.imgs
	}
	root := s.Cwd
	if !filepath.IsAbs(root) {
		root = filepath.Dir(s.Path)
	}
	if !filepath.IsAbs(root) {
		return nil
	}
	l := newMDImages(root, root)
	if r.targetFiles() {
		l.request = r
		l.readLocal = l.readTargetImage
	} else {
		// 历史的 /images 是会话根内路径；先约束路径，再复用本地图片读取。
		local := &mdImages{dir: root, root: root}
		l.readLocal = func(src string) ([]byte, string, error) {
			if l.ctx.Err() != nil || !r.current() {
				return nil, "", context.Canceled
			}
			path, err := targetImagePath(root, root, src)
			if err != nil {
				return nil, "", err
			}
			u := url.URL{Scheme: "file", Path: root + "/" + path}
			data, name, err := local.read(u.String())
			if l.ctx.Err() != nil || !r.current() {
				return nil, "", context.Canceled
			}
			return data, name, err
		}
	}
	d.imgs = l
	return l
}

// 主机切换时页面对象也要释放，不能只靠下一帧拒收旧结果。
func (a *app) closeTargetPreviews() {
	for _, v := range a.files {
		for _, d := range v.docs {
			if d.page != nil && d.page.remote {
				d.page.destroy()
				d.page = nil
			}
		}
	}
	a.history.detail.closeImages()
}

func parseHistoryMarkdown(src string) []mdBlock {
	meta, body := frontMatter(src)
	b := []byte(body)
	out := mdConv{src: b, images: true}.blocks(mdParser.Parse(text.NewReader(b)))
	if len(meta) > 0 {
		out = append([]mdBlock{{kind: mdMeta, meta: meta}}, out...)
	}
	return out
}
