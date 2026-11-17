package app

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"time"

	"github.com/egoist/mygo/ui/editor"

	"posse/internal/errtext"
	"posse/internal/files"
	"posse/internal/rpc"
	"posse/locales"
)

// 一份提交从检查到结果核对始终绑定原文档及快照；后来的编辑不改变这份提交。
type fileSave struct {
	path     string
	buffer   *editor.Buffer
	snapshot editor.Snapshot
	seq      uint64
	expected files.Version
	hash     string
	ref      *rpc.DesktopOperationRef
	busy     bool
}

func (s *fileSave) matches(d *fileDoc) bool {
	return d.path == s.path && d.ed.Buffer == s.buffer && d.loadSeq == s.seq
}

func saveDone(done func(bool), ok bool) {
	if done != nil {
		done(ok)
	}
}

func (a *app) saveDoc(m locales.Messages, d *fileDoc, done func(bool)) {
	a.request().saveDoc(m, d, done)
}

func (r appRequest) saveAllowed() bool {
	return r.bound() && (r.host == nil || r.snapshot.Controlled) &&
		(r.client == nil || !r.client.Remote || r.client.Desktop != nil)
}

func (r appRequest) saveError(m locales.Messages, d *fileDoc, err error) {
	if r.bound() {
		r.app.err = m.FilesSaveFailed(d.name(m), errtext.Text(err))
	}
}

func (r appRequest) saveVersion(ctx context.Context, path string) (files.Version, error) {
	if !r.bound() {
		return files.Version{}, context.Canceled
	}
	if r.client != nil && r.client.Desktop != nil {
		return r.client.FileVersion(ctx, path)
	}
	if r.client != nil && r.client.Remote {
		return files.Version{}, rpc.Errorf(rpc.CodeReadOnly, "desktop identity required for file saves")
	}
	return files.ReadVersion(ctx, path)
}

func (r appRequest) saveDoc(m locales.Messages, d *fileDoc, done func(bool)) {
	if !r.bound() || d.kind != docText || d.ed.Buffer == nil {
		r.saveError(m, d, rpc.Errorf(rpc.CodeReadOnly, "desktop control is no longer held"))
		saveDone(done, false)
		return
	}
	if s := d.save; s != nil {
		if s.busy {
			r.saveError(m, d, errors.New("file save is still running"))
			saveDone(done, false)
		} else {
			r.checkSave(m, d, s, done)
		}
		return
	}
	if !r.saveAllowed() || d.readOnly {
		r.saveError(m, d, rpc.Errorf(rpc.CodeReadOnly, "desktop control is no longer held"))
		saveDone(done, false)
		return
	}
	d.cancelLoad()
	s := &fileSave{path: d.path, buffer: d.ed.Buffer, snapshot: d.ed.Buffer.Snapshot(), seq: d.loadSeq, expected: d.baseVersion, busy: true}
	d.save, d.transient = s, false
	base := d.base
	go func() {
		ctx, cancel := context.WithTimeout(r.app.windowContext(), 30*time.Second)
		defer cancel()
		version, err := r.saveVersion(ctx, s.path)
		r.app.update(func() {
			if d.save != s || !s.matches(d) || !r.saveAllowed() {
				if d.save == s {
					d.save = nil
				}
				saveDone(done, false)
				return
			}
			if err != nil {
				d.save = nil
				r.saveError(m, d, err)
				saveDone(done, false)
				return
			}
			same := s.expected.Same(version)
			if s.expected.Path == "" {
				// 旧窗口文档及明确迁移路径的文档仍有磁盘戳；保留已有摘要的额外约束。
				same = base.Same(version.Stamp) && base.Mode == version.Stamp.Mode && (s.expected.Hash == "" || s.expected.Hash == version.Hash)
			}
			s.expected = version
			if !same {
				d.save = nil
				d.conflict = true
				r.confirmSave(m, d, s, done)
				return
			}
			r.writeSave(m, d, s, done)
		})
	}()
}

func (r appRequest) confirmSave(m locales.Messages, d *fileDoc, s *fileSave, done func(bool)) {
	shown := r.app.displayed()
	decided := false
	seq := shown.confirmSeq + 1
	shown.confirm(m.FilesOverwriteConfirm(), m.FilesOverwriteBody(d.name(m)), m.FilesSaveAnyway(), func() {
		r.app.update(func() {
			if decided {
				return
			}
			decided = true
			if shown.confirmSeq != seq {
				saveDone(done, false)
				return
			}
			shown.confirmCancel = nil
			if !r.saveAllowed() || !s.matches(d) || d.save != nil {
				saveDone(done, false)
				return
			}
			d.save, s.busy = s, true
			r.writeSave(m, d, s, done)
		})
	})
	shown.confirmCancel = func() {
		if !decided {
			decided = true
			saveDone(done, false)
		}
	}
}

// 仍未启用桌面身份的本机窗口保留直接写路径，但同样使用完整版本的条件替换。
// 一旦 Client 带身份，本机也只经目标执行器保存，不回退到直接写盘。
func (r appRequest) writeSave(m locales.Messages, d *fileDoc, s *fileSave, done func(bool)) {
	go func() {
		ctx, cancel := context.WithTimeout(r.app.windowContext(), 30*time.Second)
		defer cancel()
		var out rpc.FileSaveResult
		var err error
		var hash string
		if !r.saveAllowed() {
			err = context.Canceled
		} else if r.client != nil && r.client.Desktop != nil {
			var body saveBuffer
			_, err = s.snapshot.WriteTo(&body)
			if err == nil {
				sum := sha256.Sum256(body.Bytes())
				hash = hex.EncodeToString(sum[:])
				out, err = r.client.SaveFile(ctx, rpc.FileSaveParams{Path: s.path, Expected: s.expected, Data: body.Bytes(), Hash: hash, Revision: s.snapshot.Version()})
			}
		} else {
			out.Version, err = files.WriteFileCoordinated(ctx, s.path, s.expected, func(w io.Writer) error {
				_, err := s.snapshot.WriteTo(w)
				return err
			})
			out.Revision = s.snapshot.Version()
			hash = out.Version.Hash
		}
		r.app.update(func() {
			if d.save != s {
				saveDone(done, false)
				return
			}
			s.busy, s.hash = false, hash
			var re *rpc.Error
			unknown := errors.As(err, &re) && re.Code == rpc.CodeOutcomeUnknown
			if unknown {
				var ref rpc.DesktopOperationRef
				if json.Unmarshal(re.Data, &ref) == nil && ref.Sequence != 0 {
					s.ref = &ref
				}
			} else if err != nil {
				d.save = nil
				if errors.Is(err, files.ErrSaveConflict) || errors.As(err, &re) && re.Code == rpc.CodeConflict {
					d.conflict = true
				}
			} else if out.Revision != s.snapshot.Version() || out.Version.Hash != hash || out.Version.Stamp.Missing || out.Version.Path == "" {
				err = errors.New("invalid file save response; inspect the file before saving again")
			} else if r.bound() && s.matches(d) {
				d.savedSnapshot(s, out.Version)
			}
			if err != nil {
				r.saveError(m, d, err)
			}
			saveDone(done, err == nil && r.bound() && d.save == nil && !d.isModified())
		})
	}()
}

func (d *fileDoc) savedSnapshot(s *fileSave, version files.Version) {
	s.buffer.MarkSnapshotSaved(s.snapshot)
	d.save, d.baseVersion = nil, version
	d.base, d.seen = version.Stamp, version.Stamp
	d.conflict, d.deleted, d.modStale = false, false, true
}

// 未知结果只能先核对。即使内容不同，也结束本次保存动作；再次覆盖须重新发起并确认。
func (r appRequest) checkSave(m locales.Messages, d *fileDoc, s *fileSave, done func(bool)) {
	s.busy = true
	path := d.path
	go func() {
		ctx, cancel := context.WithTimeout(r.app.windowContext(), 30*time.Second)
		defer cancel()
		var version files.Version
		var err error
		pending := false
		if s.ref != nil && r.client != nil {
			check, checkErr := r.client.CheckFileSave(ctx, *s.ref, path, s.hash)
			version, pending, err = check.Version, check.Pending, checkErr
		} else {
			version, err = r.saveVersion(ctx, path)
		}
		r.app.update(func() {
			if d.save != s {
				saveDone(done, false)
				return
			}
			s.busy = false
			if !r.bound() || d.path != path {
				saveDone(done, false)
				return
			}
			if pending {
				err = errors.New("file save is still running")
			}
			if err != nil {
				if version.Path != "" && !pending {
					d.save = nil
					d.conflict = true
				}
				r.saveError(m, d, err)
				saveDone(done, false)
				return
			}
			if s.matches(d) && !version.Stamp.Missing && s.hash != "" && version.Hash == s.hash {
				d.savedSnapshot(s, version)
				saveDone(done, !d.isModified())
				return
			}
			d.save = nil
			d.conflict = true
			r.saveError(m, d, errors.New("saved content could not be confirmed; inspect the file before saving again"))
			saveDone(done, false)
		})
	}()
}

type saveBuffer struct{ body bytes.Buffer }

func (b *saveBuffer) Bytes() []byte { return b.body.Bytes() }

func (b *saveBuffer) Write(p []byte) (int, error) {
	if len(p) > files.MaxSize-b.body.Len() {
		return 0, fmt.Errorf("file exceeds the maximum size of %d bytes", files.MaxSize)
	}
	return b.body.Write(p)
}
