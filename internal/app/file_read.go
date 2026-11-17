package app

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"posse/locales"

	"posse/internal/errtext"
	"posse/internal/files"
	"posse/internal/rpc"
)

func (a *app) canEditDoc(d *fileDoc) bool {
	r := a.request()
	return !d.readOnly && r.current() && (r.host == nil || r.snapshot.Controlled)
}

func (a *app) openTargetWiki(m locales.Messages, w *workspace, from, target string) {
	r := a.request()
	name, _, _ := strings.Cut(target, "#")
	path := filepath.Join(filepath.Dir(from), name)
	paths := []string{path}
	if filepath.Ext(name) != ".md" {
		paths = append(paths, path+".md")
	}
	go func() {
		ctx, cancel := context.WithTimeout(a.windowContext(), 20*time.Second)
		defer cancel()
		stats, err := r.fileStats(ctx, paths)
		r.update(func() {
			if err != nil {
				a.err = errtext.Text(err)
				return
			}
			for _, item := range stats {
				if item.Error != nil {
					a.err = errtext.Text(filePathError(item.Path, item.Error))
					return
				}
				if !item.Stamp.Missing {
					a.openFile(w, item.Path, 0, [2]int{})
					return
				}
			}
			a.err = m.FilesWikiNotFound(target, paths[len(paths)-1])
		})
	}()
}

func (a *app) targetFileUnsupported(method string) bool {
	if !a.remoteHost() {
		return false
	}
	a.err = errtext.Text(targetFileError(method))
	return true
}

func targetFileError(method string) error {
	return fmt.Errorf("method %q is not available through the desktop connection", method)
}

func (r appRequest) targetFiles() bool { return r.app.remoteHost() }

func filePathError(path string, err *rpc.Error) error {
	if err == nil {
		return nil
	}
	switch err.Code {
	case rpc.CodeNotFound:
		return &os.PathError{Op: "read", Path: path, Err: os.ErrNotExist}
	case rpc.CodePermissionDenied:
		return &os.PathError{Op: "read", Path: path, Err: os.ErrPermission}
	}
	return err
}

func (r appRequest) readDirectories(ctx context.Context, paths []string) ([]rpc.FileDirectoryResult, error) {
	if !r.current() {
		return nil, context.Canceled
	}
	if !r.targetFiles() {
		out := make([]rpc.FileDirectoryResult, 0, len(paths))
		for _, path := range paths {
			if err := ctx.Err(); err != nil {
				return nil, err
			}
			es, st, err := files.ReadDir(path)
			item := rpc.FileDirectoryResult{Path: path, Entries: es, Stamp: st}
			if err != nil {
				code := rpc.CodeInternal
				if os.IsNotExist(err) {
					code = rpc.CodeNotFound
				} else if os.IsPermission(err) {
					code = rpc.CodePermissionDenied
				}
				item.Error = rpc.Errorf(code, "%v", err)
			}
			out = append(out, item)
		}
		return out, nil
	}
	if r.client == nil {
		return nil, context.Canceled
	}
	var out []rpc.FileDirectoryResult
	for len(paths) > 0 {
		n := min(len(paths), rpc.FileBatchLimit)
		part, err := r.client.ReadDirectories(ctx, paths[:n])
		if err != nil {
			return nil, err
		}
		if len(part) != n {
			return nil, errors.New("invalid directory listing response")
		}
		for i := range part {
			if part[i].Path != paths[i] {
				return nil, errors.New("invalid directory listing response")
			}
		}
		out = append(out, part...)
		paths = paths[n:]
	}
	return out, nil
}

func (r appRequest) fileStats(ctx context.Context, paths []string) ([]rpc.FileStatResult, error) {
	if !r.current() {
		return nil, context.Canceled
	}
	if !r.targetFiles() {
		out := make([]rpc.FileStatResult, 0, len(paths))
		for _, path := range paths {
			if err := ctx.Err(); err != nil {
				return nil, err
			}
			st, err := files.Stat(path)
			item := rpc.FileStatResult{Path: path, Stamp: st}
			if err != nil {
				item.Error = rpc.Errorf(rpc.CodeInternal, "%v", err)
			}
			out = append(out, item)
		}
		return out, nil
	}
	if r.client == nil {
		return nil, context.Canceled
	}
	var out []rpc.FileStatResult
	for len(paths) > 0 {
		n := min(len(paths), rpc.FileBatchLimit)
		part, err := r.client.FileStats(ctx, paths[:n])
		if err != nil {
			return nil, err
		}
		if len(part) != n {
			return nil, errors.New("invalid file status response")
		}
		for i := range part {
			if part[i].Path != paths[i] {
				return nil, errors.New("invalid file status response")
			}
		}
		out = append(out, part...)
		paths = paths[n:]
	}
	return out, nil
}

func (r appRequest) readFile(ctx context.Context, path string) (files.Content, files.Version, error) {
	if !r.current() {
		return files.Content{}, files.Version{}, context.Canceled
	}
	if r.targetFiles() {
		if r.client == nil {
			return files.Content{}, files.Version{}, context.Canceled
		}
		return r.client.ReadFileVersion(ctx, path)
	}
	return files.ReadFileVersion(ctx, path)
}

func (a *app) applyDirectory(v *filesView, r rpc.FileDirectoryResult) {
	err := filePathError(r.Path, r.Error)
	if err != nil && !os.IsNotExist(err) {
		// 暂时读不到不等于目录消失，保留已有树及展开状态。
		a.err = errtext.Text(err)
		return
	}
	v.tree.Set(r.Path, r.Entries, r.Stamp, err)
}
