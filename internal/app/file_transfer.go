package app

import (
	"context"
	"os"
	"path/filepath"
	"time"

	"github.com/egoist/mygo"
	"github.com/egoist/mygo/ui"
	"posse/internal/errtext"
	"posse/internal/files"
	"posse/locales"
)

var openDownloadedCopy = mygo.Shell.OpenPath

type fileTransfer struct {
	name string
}

func (r appRequest) transferCurrent(v *filesView, s *fileTransfer) bool {
	return r.current() && r.app.files[v.wsID] == v && v.transfer == s
}

func (a *app) chooseUpload(m locales.Messages, v *filesView, dir string) {
	r := a.request()
	if v.transfer != nil || !r.mayWrite() || r.client == nil || r.client.Desktop == nil {
		return
	}
	var dialogErr error
	a.inDialog(func() []string {
		paths, err := openDialog(mygo.OpenDialogOptions{Parent: a.win, Title: m.FilesUpload(), ButtonLabel: m.FilesUpload()})
		dialogErr = err
		return paths
	}, func(paths []string) {
		if !r.current() || a.files[v.wsID] != v {
			return
		}
		if dialogErr != nil {
			a.err = errtext.Text(dialogErr)
		} else if len(paths) > 0 && r.mayWrite() {
			r.prepareUpload(m, v, paths[0], filepath.Join(dir, filepath.Base(paths[0])))
		}
	})
}

func (r appRequest) prepareUpload(m locales.Messages, v *filesView, source, target string) {
	if v.transfer != nil || !r.mayWrite() {
		return
	}
	a := r.app
	s := &fileTransfer{name: filepath.Base(target)}
	v.transfer = s
	a.confirmSeq++
	seq := a.confirmSeq
	go func() {
		ctx, cancel := context.WithTimeout(a.windowContext(), 30*time.Second)
		base, err := r.client.FileVersion(ctx, target)
		cancel()
		a.update(func() {
			if !r.transferCurrent(v, s) || a.confirmSeq != seq {
				if v.transfer == s {
					v.transfer = nil
				}
				return
			}
			if err != nil {
				r.transferFinished(m, v, s, true, err)
				return
			}
			start := func() { r.uploadFile(m, v, s, source, target, base) }
			if base.Stamp.Missing {
				go start()
				return
			}
			a.confirm(m.FilesTransferReplaceTitle(s.name), m.FilesTransferReplaceBody(target), m.FilesTransferReplace(), start)
			a.confirmCancel = func() {
				if v.transfer == s {
					v.transfer = nil
				}
			}
		})
	}()
}

func (r appRequest) uploadFile(m locales.Messages, v *filesView, s *fileTransfer, source, target string, base files.Version) {
	// 确认回调在后台执行；窗口字段只在主线程里复查和更新。
	r.app.update(func() {
		if !r.transferCurrent(v, s) || !r.mayWrite() {
			if v.transfer == s {
				v.transfer = nil
			}
			return
		}
		go func() {
			ctx, cancel := context.WithTimeout(r.app.windowContext(), 2*time.Minute)
			_, err := r.client.UploadFile(ctx, source, target, base)
			cancel()
			r.app.update(func() { r.transferFinished(m, v, s, true, err) })
		}()
	})
}

func (a *app) chooseUploadFolder(m locales.Messages, v *filesView, dir string) {
	r := a.request()
	if v.transfer != nil || !r.mayWrite() || r.client == nil || r.client.Desktop == nil {
		return
	}
	var dialogErr error
	a.inDialog(func() []string {
		paths, err := openDialog(mygo.OpenDialogOptions{Parent: a.win, Title: m.FilesUploadFolder(), ButtonLabel: m.FilesUploadFolder(), Directory: true})
		dialogErr = err
		return paths
	}, func(paths []string) {
		if !r.current() || a.files[v.wsID] != v {
			return
		}
		if dialogErr != nil {
			a.err = errtext.Text(dialogErr)
		} else if len(paths) > 0 && r.mayWrite() {
			r.prepareUploadFolder(m, v, paths[0], filepath.Join(dir, filepath.Base(paths[0])))
		}
	})
}

func (r appRequest) prepareUploadFolder(m locales.Messages, v *filesView, source, target string) {
	if v.transfer != nil || !r.mayWrite() {
		return
	}
	a := r.app
	s := &fileTransfer{name: filepath.Base(target)}
	v.transfer = s
	a.confirmSeq++
	seq := a.confirmSeq
	go func() {
		ctx, cancel := context.WithTimeout(a.windowContext(), 30*time.Second)
		stats, err := r.client.FileStats(ctx, []string{target})
		cancel()
		a.update(func() {
			if !r.transferCurrent(v, s) || a.confirmSeq != seq {
				if v.transfer == s {
					v.transfer = nil
				}
				return
			}
			if err == nil && len(stats) == 1 && stats[0].Error != nil {
				err = stats[0].Error
			}
			if err != nil || len(stats) != 1 {
				if err == nil {
					err = os.ErrInvalid
				}
				r.transferFinished(m, v, s, true, err)
				return
			}
			if stats[0].Stamp.Missing {
				go r.uploadDirectory(m, v, s, source, target, false)
				return
			}
			a.confirm(m.FilesTransferReplaceTitle(s.name), m.FilesTransferReplaceDirBody(target), m.FilesTransferReplace(), func() {
				r.uploadDirectory(m, v, s, source, target, true)
			})
			a.confirmCancel = func() {
				if v.transfer == s {
					v.transfer = nil
				}
			}
		})
	}()
}

func (r appRequest) uploadDirectory(m locales.Messages, v *filesView, s *fileTransfer, source, target string, replaceExisting bool) {
	r.app.update(func() {
		if !r.transferCurrent(v, s) || !r.mayWrite() {
			if v.transfer == s {
				v.transfer = nil
			}
			return
		}
		go func() {
			ctx, cancel := context.WithTimeout(r.app.windowContext(), 5*time.Minute)
			err := r.client.UploadDirectory(ctx, source, target, replaceExisting)
			cancel()
			r.app.update(func() { r.transferFinished(m, v, s, true, err) })
		}()
	})
}

func (a *app) chooseDownloadFolder(m locales.Messages, v *filesView, target string) {
	r := a.request()
	if v.transfer != nil || !r.current() || r.client == nil || r.client.Desktop == nil {
		return
	}
	var dialogErr error
	a.inDialog(func() []string {
		path, err := saveDialog(mygo.SaveDialogOptions{Parent: a.win, Title: m.FilesDownloadFolder(), DefaultPath: filepath.Base(target), CreateDirectories: true})
		dialogErr = err
		if path == "" {
			return nil
		}
		return []string{path}
	}, func(paths []string) {
		if !r.current() || a.files[v.wsID] != v {
			return
		}
		if dialogErr != nil {
			a.err = errtext.Text(dialogErr)
		} else if len(paths) > 0 {
			r.prepareDownloadFolder(m, v, target, paths[0])
		}
	})
}

func (r appRequest) prepareDownloadFolder(m locales.Messages, v *filesView, target, destination string) {
	if v.transfer != nil || !r.current() {
		return
	}
	a := r.app
	s := &fileTransfer{name: filepath.Base(target)}
	v.transfer = s
	if _, err := os.Lstat(destination); os.IsNotExist(err) {
		go r.downloadDirectory(m, v, s, target, destination, false)
		return
	}
	a.confirm(m.FilesTransferReplaceTitle(filepath.Base(destination)), m.FilesDownloadReplaceBody(destination), m.FilesTransferReplace(), func() {
		r.downloadDirectory(m, v, s, target, destination, true)
	})
	a.confirmCancel = func() {
		if v.transfer == s {
			v.transfer = nil
		}
	}
}

func (r appRequest) downloadDirectory(m locales.Messages, v *filesView, s *fileTransfer, target, destination string, replaceExisting bool) {
	r.app.update(func() {
		if !r.transferCurrent(v, s) {
			if v.transfer == s {
				v.transfer = nil
			}
			return
		}
		go func() {
			ctx, cancel := context.WithTimeout(r.app.windowContext(), 5*time.Minute)
			err := r.client.DownloadDirectory(ctx, target, destination, replaceExisting)
			cancel()
			r.app.update(func() { r.transferFinished(m, v, s, false, err) })
		}()
	})
}

func (a *app) chooseDownload(m locales.Messages, v *filesView, target string, openCopy bool) {
	a.chooseDownloadChecked(m, v, target, openCopy, func() bool { return true })
}

func (a *app) chooseDownloadChecked(m locales.Messages, v *filesView, target string, openCopy bool, valid func() bool) {
	if !valid() {
		return
	}
	r := a.request()
	if v.transfer != nil || !r.current() || r.client == nil || r.client.Desktop == nil {
		return
	}
	title := m.FilesDownload()
	if openCopy {
		title = m.FilesSaveCopyAndOpen()
	}
	var dialogErr error
	a.inDialog(func() []string {
		path, err := saveDialog(mygo.SaveDialogOptions{Parent: a.win, Title: title, DefaultPath: filepath.Base(target), CreateDirectories: true})
		dialogErr = err
		if path == "" {
			return nil
		}
		return []string{path}
	}, func(paths []string) {
		if !r.current() || a.files[v.wsID] != v || !valid() {
			return
		}
		if dialogErr != nil {
			a.err = errtext.Text(dialogErr)
		} else if len(paths) > 0 {
			r.downloadFileChecked(m, v, target, paths[0], openCopy, valid)
		}
	})
}

func (r appRequest) downloadFile(m locales.Messages, v *filesView, target, destination string, openCopy bool) {
	r.downloadFileChecked(m, v, target, destination, openCopy, func() bool { return true })
}

func (r appRequest) downloadFileChecked(m locales.Messages, v *filesView, target, destination string, openCopy bool, valid func() bool) {
	if v.transfer != nil || !r.current() || !valid() {
		return
	}
	s := &fileTransfer{name: filepath.Base(target)}
	v.transfer = s
	go func() {
		ctx, cancel := context.WithTimeout(r.app.windowContext(), 2*time.Minute)
		defer cancel()
		base, err := files.ReadVersion(ctx, destination)
		if err == nil {
			err = r.client.DownloadFile(ctx, target, destination, base)
		}
		r.app.update(func() {
			current := r.transferCurrent(v, s) && valid()
			if !valid() {
				if v.transfer == s {
					v.transfer = nil
				}
				return
			}
			r.transferFinished(m, v, s, false, err)
			if current && err == nil && openCopy {
				if err := openDownloadedCopy(destination); err != nil {
					r.app.err = m.FilesOpenCopyFailed(s.name, errtext.Text(err))
				}
			}
		})
	}()
}

func (r appRequest) transferFinished(m locales.Messages, v *filesView, s *fileTransfer, upload bool, err error) {
	current := r.transferCurrent(v, s)
	if v.transfer == s {
		v.transfer = nil
	}
	if !current {
		return
	}
	a := r.app
	if err != nil {
		a.err = m.FilesTransferFailed(s.name, errtext.Text(err))
		return
	}
	title := m.FilesDownloaded(s.name)
	if upload {
		title = m.FilesUploaded(s.name)
		a.invalidateList(v)
		a.pollFileChanges(v)
	}
	a.toasts = append(a.toasts, ui.Toast{Title: title})
}
