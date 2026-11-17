package files

import (
	"context"
	"errors"
	"io"
	"os"
	"syscall"
)

const RangeLimit = 1 << 20

var ErrChanged = errors.New("file changed while reading; reload before trying again")

type Range struct {
	Data  []byte
	Stamp Stamp
	EOF   bool
}

func Stat(path string) (Stamp, error) {
	fi, err := os.Stat(path)
	if os.IsNotExist(err) {
		return Stamp{Missing: true}, nil
	}
	if err != nil {
		return Stamp{}, err
	}
	return stampOf(fi), nil
}

func ReadRange(ctx context.Context, path string, offset int64, limit int, expected *Stamp) (Range, error) {
	if err := ctx.Err(); err != nil {
		return Range{}, err
	}
	if offset < 0 {
		return Range{}, errors.New("offset must not be negative")
	}
	if limit < 1 || limit > RangeLimit {
		return Range{}, errors.New("limit must be between 1 and 1048576 bytes")
	}
	fi, err := os.Stat(path)
	if err != nil {
		return Range{}, err
	}
	if !fi.Mode().IsRegular() {
		return Range{}, errors.New("not a regular file")
	}
	if err := ctx.Err(); err != nil {
		return Range{}, err
	}
	// 检查后路径仍可能被替换成 FIFO；非阻塞打开避免等候写入端。
	f, err := os.OpenFile(path, os.O_RDONLY|syscall.O_NONBLOCK, 0)
	if err != nil {
		return Range{}, err
	}
	defer f.Close()
	fi, err = f.Stat()
	if err != nil {
		return Range{}, err
	}
	if !fi.Mode().IsRegular() {
		return Range{}, errors.New("not a regular file")
	}
	before := stampOf(fi)
	if expected != nil && !before.Same(*expected) {
		return Range{}, ErrChanged
	}
	data := make([]byte, limit)
	n := 0
	if offset < before.Size {
		n, err = f.ReadAt(data, offset)
	}
	if ctxErr := ctx.Err(); ctxErr != nil {
		return Range{}, ctxErr
	}
	if err != nil && err != io.EOF {
		return Range{}, err
	}
	fi, err = f.Stat()
	if err != nil {
		return Range{}, err
	}
	if !before.Same(stampOf(fi)) {
		return Range{}, ErrChanged
	}
	// 原文件描述符不随 rename 替换，必须再核对路径所指的文件。
	after, err := Stat(path)
	if err != nil {
		return Range{}, err
	}
	if !before.Same(after) {
		return Range{}, ErrChanged
	}
	if err := ctx.Err(); err != nil {
		return Range{}, err
	}
	return Range{Data: data[:n], Stamp: before, EOF: offset >= before.Size || int64(n) >= before.Size-offset}, nil
}
