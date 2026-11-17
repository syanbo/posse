package app

import (
	"bytes"
	"context"
	"os"
	"time"

	"posse/internal/errtext"
	"posse/internal/files"
	"posse/internal/rpc"
)

// pasteImage 在界面线程捕获终端身份，上传期间不借用新接入者的控制权。
func (a *app) pasteImage(v *termView, png []byte) {
	if !a.live(v) || v.term == nil {
		return
	}
	r := v.request
	if r.app == nil {
		r = a.request()
	}
	if !r.mayWrite() || v.term.ReadOnly() {
		return
	}
	if len(png) > files.MaxSize {
		a.err = appL().AppPasteImageError(errtext.Text(rpc.Errorf(rpc.CodeBadRequest, "file exceeds the maximum size of %d bytes", files.MaxSize)))
		return
	}
	data := bytes.Clone(png)
	ctx, cancel := context.WithTimeout(a.windowContext(), 2*time.Minute)
	local := r.client == nil || (!r.client.Remote && r.client.Desktop == nil)
	go func() {
		defer cancel()
		var path string
		var err error
		if local {
			path, err = files.PasteImage(ctx, data)
		} else {
			path, err = r.client.PasteImage(ctx, data)
		}
		a.update(func() {
			if !r.current() || !a.live(v) || v.term.ReadOnly() {
				if local && path != "" {
					_ = os.Remove(path)
				}
				return
			}
			if err != nil {
				a.err = appL().AppPasteImageError(errtext.Text(err))
				return
			}
			v.term.Paste(path)
		})
	}()
}
