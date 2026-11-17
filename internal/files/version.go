package files

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"syscall"
)

var ErrSaveConflict = errors.New("file changed before saving; reload before trying again")

// Version 的指纹对应磁盘原始字节，包含换行形式，不是编辑器归一化后的文字。
type Version struct {
	Path  string `json:"path"`
	Stamp Stamp  `json:"stamp"`
	Hash  string `json:"hash"`
}

func (v Version) Same(other Version) bool {
	return v.Path == other.Path && v.Stamp.Same(other.Stamp) && v.Stamp.Mode == other.Stamp.Mode && v.Hash == other.Hash
}

// ReadVersion 只给出完整读取且路径仍指向同一文件的版本；权限错误不当作不存在。
func ReadVersion(ctx context.Context, path string) (Version, error) {
	if err := ctx.Err(); err != nil {
		return Version{}, err
	}
	real, err := savePath(path)
	if err != nil {
		return Version{}, err
	}
	st, err := Stat(real)
	if err != nil {
		return Version{}, err
	}
	if st.Missing {
		return Version{Path: real, Stamp: st}, nil
	}
	if !st.Mode.IsRegular() {
		return Version{}, errors.New("not a regular file")
	}
	f, err := os.OpenFile(real, os.O_RDONLY|syscall.O_NONBLOCK, 0)
	if err != nil {
		return Version{}, err
	}
	defer f.Close()
	fi, err := f.Stat()
	if err != nil {
		return Version{}, err
	}
	if !fi.Mode().IsRegular() {
		return Version{}, errors.New("not a regular file")
	}
	before := stampOf(fi)
	if !st.Same(before) || st.Mode != before.Mode {
		return Version{}, ErrChanged
	}
	if before.Size > MaxSize {
		return Version{}, fmt.Errorf("file exceeds the maximum size of %d bytes", MaxSize)
	}
	h := sha256.New()
	n, err := io.Copy(h, io.LimitReader(versionReader{ctx, f}, MaxSize+1))
	if err != nil {
		return Version{}, err
	}
	fi, err = f.Stat()
	if err != nil {
		return Version{}, err
	}
	after, err := Stat(path)
	if err != nil {
		return Version{}, err
	}
	if n != before.Size || !before.Same(stampOf(fi)) || fi.Mode() != before.Mode || !before.Same(after) || after.Mode != before.Mode {
		return Version{}, ErrChanged
	}
	if err := ctx.Err(); err != nil {
		return Version{}, err
	}
	currentPath, err := savePath(path)
	if err != nil {
		return Version{}, err
	}
	if currentPath != real {
		return Version{}, ErrChanged
	}
	return Version{Path: real, Stamp: before, Hash: hex.EncodeToString(h.Sum(nil))}, nil
}

type versionReader struct {
	ctx context.Context
	r   io.Reader
}

func (r versionReader) Read(p []byte) (int, error) {
	if err := r.ctx.Err(); err != nil {
		return 0, err
	}
	return r.r.Read(p)
}

// savePath 固定实际写入位置；断链不能退化成用普通文件替换链接本身。
func savePath(path string) (string, error) {
	if !filepath.IsAbs(path) {
		cwd, err := os.Getwd()
		if err != nil {
			return "", err
		}
		// Abs/Join 会先清掉 link/..，必须让文件系统按软链目标解释它。
		path = cwd + string(filepath.Separator) + path
	}
	real, err := filepath.EvalSymlinks(path)
	if err == nil || !os.IsNotExist(err) {
		return real, err
	}
	if _, statErr := os.Lstat(path); !os.IsNotExist(statErr) {
		return "", err
	}
	dir, name := filepath.Split(path)
	parent, err := filepath.EvalSymlinks(dir)
	if err != nil {
		return "", err
	}
	return filepath.Join(parent, name), nil
}

// WriteFileChecked 要求调用方持有资源协调锁，覆盖检查到替换的整个过程。
// 外部程序不参与锁，最后一次检查和 rename 之间仍可能有外部写入；这不是文件系统 CAS。
func WriteFileChecked(ctx context.Context, path string, expected Version, write func(io.Writer) error) (Version, error) {
	if err := ctx.Err(); err != nil {
		return Version{}, err
	}
	real, err := savePath(path)
	if err != nil {
		return Version{}, err
	}
	if real != expected.Path {
		return Version{}, ErrSaveConflict
	}
	check := func() error {
		currentPath, err := savePath(path)
		if err != nil {
			return err
		}
		if currentPath != real {
			return ErrSaveConflict
		}
		st, err := Stat(real)
		if err != nil {
			return err
		}
		if !st.Same(expected.Stamp) || st.Mode != expected.Stamp.Mode {
			return ErrSaveConflict
		}
		current, err := ReadVersion(ctx, real)
		if errors.Is(err, ErrChanged) {
			return ErrSaveConflict
		}
		if err != nil {
			return err
		}
		if !expected.Same(current) {
			return ErrSaveConflict
		}
		return ctx.Err()
	}
	if err := check(); err != nil {
		return Version{}, err
	}
	mode := expected.Stamp.Mode.Perm()
	if expected.Stamp.Missing {
		mode = 0o644
	}
	h := sha256.New()
	st, err := writeAtomic(real, mode, func(w io.Writer) error {
		out := &versionWriter{ctx: ctx, w: io.MultiWriter(w, h), remaining: MaxSize}
		if err := write(out); err != nil {
			return err
		}
		return out.err
	}, check)
	if err != nil {
		return Version{}, err
	}
	return Version{Path: real, Stamp: st, Hash: hex.EncodeToString(h.Sum(nil))}, nil
}

type versionWriter struct {
	ctx       context.Context
	w         io.Writer
	remaining int
	err       error
}

func (w *versionWriter) Write(p []byte) (int, error) {
	if w.err != nil {
		return 0, w.err
	}
	if err := w.ctx.Err(); err != nil {
		w.err = err
	} else if len(p) > w.remaining {
		w.err = fmt.Errorf("file exceeds the maximum size of %d bytes", MaxSize)
	}
	if w.err != nil {
		return 0, w.err
	}
	n, err := w.w.Write(p)
	w.remaining -= n
	if n != len(p) && err == nil {
		err = io.ErrShortWrite
	}
	w.err = err
	return n, err
}
