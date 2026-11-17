package app

import (
	"context"
	"time"

	"posse/internal/errtext"
	"posse/internal/files"
	"posse/internal/rpc"
	"posse/internal/trash"
	"posse/locales"
)

func (r appRequest) fileEntry(ctx context.Context, path string) (files.EntryVersion, error) {
	if !r.current() {
		return files.EntryVersion{}, context.Canceled
	}
	if r.client != nil && r.client.Desktop != nil {
		return r.client.FileEntry(ctx, path)
	}
	if r.targetFiles() {
		return files.EntryVersion{}, rpc.Errorf(rpc.CodeReadOnly, "desktop identity required for file trash")
	}
	return files.ReadEntryVersion(ctx, path)
}

func (r appRequest) trashEntry(m locales.Messages, v *filesView, path string, dir bool, expected files.EntryVersion) {
	if !r.mayWrite() {
		return
	}
	ctx, cancel := context.WithTimeout(r.app.windowContext(), time.Minute)
	defer cancel()
	var err error
	if r.client != nil && r.client.Desktop != nil {
		_, err = r.client.TrashFile(ctx, rpc.FileTrashParams{Path: path, Expected: expected})
	} else if r.targetFiles() {
		err = rpc.Errorf(rpc.CodeReadOnly, "desktop identity required for file trash")
	} else {
		_, err = files.TrashEntryChecked(ctx, path, expected, trash.Move)
	}
	r.update(func() {
		if err != nil {
			r.app.err = m.TrashMoveError(errtext.Text(err))
			return
		}
		r.app.forgetTrashed(v, path, dir)
	})
}
