package app

import (
	"bytes"
	"context"
	"os"
	"slices"
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

// pasteFiles 处理访达拖入或复制粘贴到终端的文件：本机直接贴路径，远端先上传到目标临时目录再贴目标路径。
func (a *app) pasteFiles(v *termView, paths []string, agent string) {
	if len(paths) == 0 || !a.live(v) || v.term == nil {
		return
	}
	r := v.request
	if r.app == nil {
		r = a.request()
	}
	if !r.mayWrite() || v.term.ReadOnly() {
		return
	}
	if r.client == nil || !r.client.Remote {
		sendPieces(v.term, dropPieces(paths, agent))
		a.focusTerm = true
		return
	}
	sources := slices.Clone(paths)
	ctx, cancel := context.WithTimeout(a.windowContext(), 2*time.Minute)
	go func() {
		defer cancel()
		uploaded := make([]string, 0, len(sources))
		var err error
		for _, src := range sources {
			var remotePath string
			remotePath, err = r.client.PasteFile(ctx, src)
			if err != nil {
				break
			}
			uploaded = append(uploaded, remotePath)
		}
		a.update(func() {
			if !r.current() || !a.live(v) || v.term == nil || v.term.ReadOnly() {
				return
			}
			if err != nil {
				a.err = appL().AppPasteFileError(errtext.Text(err))
				return
			}
			sendPieces(v.term, dropPieces(uploaded, agent))
			a.focusTerm = true
		})
	}()
}
