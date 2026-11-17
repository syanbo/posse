package app

import (
	"context"
	"time"

	"posse/internal/errtext"
	"posse/internal/files"
	"posse/internal/rpc"
)

func (a *app) pollFileChanges(v *filesView) {
	r := a.request()
	v.pollSeq++
	seq := v.pollSeq
	type dirRequest struct {
		path  string
		stamp files.Stamp
		seq   uint64
	}
	type docRequest struct {
		d            *fileDoc
		path         string
		seen, base   files.Stamp
		baseVersion  files.Version
		seq          uint64
		image, dirty bool
	}
	type docResult struct {
		q       docRequest
		stamp   files.Stamp
		content files.Content
		version files.Version
		err     error
	}
	var dirs []dirRequest
	var docs []docRequest
	var paths []string
	if v.dirSeq == nil {
		v.dirSeq = map[string]uint64{}
	}
	for _, dir := range v.tree.Visible() {
		v.dirSeq[dir]++
		st, ok := v.tree.Stamp(dir)
		if !ok {
			st = files.Stamp{Missing: true}
		}
		dirs = append(dirs, dirRequest{dir, st, v.dirSeq[dir]})
		paths = append(paths, dir)
	}
	for _, d := range v.docs {
		if s := d.save; s != nil && !s.busy && r.bound() {
			r.checkSave(appL(), d, s, nil)
		}
		if d.kind != docLoading && !d.virtual() && d.save == nil && d.loadCancel == nil {
			docs = append(docs, docRequest{d, d.path, d.seen, d.base, d.baseVersion, d.loadSeq, d.kind == docImage, d.isModified()})
			paths = append(paths, d.path)
		}
	}
	go func() {
		ctx, cancel := context.WithTimeout(a.windowContext(), 30*time.Second)
		defer cancel()
		stats, err := r.fileStats(ctx, paths)
		var dr []rpc.FileDirectoryResult
		var cr []docResult
		var failures []error
		if err == nil {
			var changed []string
			for i, q := range dirs {
				item := stats[i]
				if item.Error != nil {
					failures = append(failures, filePathError(q.path, item.Error))
					continue
				}
				if !item.Stamp.Same(q.stamp) {
					changed = append(changed, q.path)
				}
			}
			if len(changed) > 0 {
				dr, err = r.readDirectories(ctx, changed)
			}
			for i, q := range docs {
				item := stats[len(dirs)+i]
				if item.Error != nil {
					failures = append(failures, filePathError(q.path, item.Error))
					continue
				}
				if item.Stamp.Same(q.seen) {
					continue
				}
				result := docResult{q: q, stamp: item.Stamp}
				if !item.Stamp.Missing && !q.image && !q.dirty {
					result.content, result.version, result.err = r.readFile(ctx, q.path)
					if result.err == nil {
						result.stamp = result.content.Stamp
					}
				}
				cr = append(cr, result)
			}
		}
		a.update(func() {
			if v.pollSeq != seq {
				return
			}
			v.poll.done()
			if !r.current() {
				return
			}
			if current := a.files[v.wsID]; current != nil && current != v {
				return
			}
			if err != nil {
				a.err = errtext.Text(err)
			}
			for _, failure := range failures {
				a.err = errtext.Text(failure)
			}
			for _, item := range dr {
				for _, q := range dirs {
					if q.path == item.Path && v.dirSeq[q.path] == q.seq {
						a.applyDirectory(v, item)
						break
					}
				}
			}
			for _, item := range cr {
				q, d := item.q, item.q.d
				if d.path != q.path || d.loadSeq != q.seq || !d.base.Same(q.base) || !d.baseVersion.Same(q.baseVersion) {
					continue
				}
				if item.err != nil {
					a.err = errtext.Text(item.err)
					continue
				}
				clean := !d.isModified()
				// 读取时有草稿、回来时已撤销到干净状态：下一轮再取全文，不用空内容重载。
				if q.dirty && clean && !item.stamp.Missing {
					continue
				}
				a.diskChanged(d, item.stamp, item.content, nil)
				if clean && !item.stamp.Missing && !q.image {
					d.baseVersion = item.version
				}
			}
		})
	}()
}
