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
	"strings"
	"syscall"
)

var ErrResourcePath = errors.New("resource path must be relative and stay within the root")

// ReadResourceRange 保留原始二进制；根目录句柄限制实际打开的文件，路径复查发现读取期间的替换。
func ReadResourceRange(ctx context.Context, root, path string, offset int64, limit int, expected *Stamp) (Range, error) {
	if err := ctx.Err(); err != nil {
		return Range{}, err
	}
	if offset < 0 {
		return Range{}, errors.New("offset must not be negative")
	}
	if limit < 1 || limit > RangeLimit {
		return Range{}, errors.New("limit must be between 1 and 1048576 bytes")
	}
	var result Range
	err := readResource(ctx, root, path, expected, func(f *os.File, before Stamp, _ string) error {
		data := make([]byte, limit)
		n := 0
		var err error
		if offset < before.Size {
			n, err = f.ReadAt(data, offset)
		}
		if err != nil && err != io.EOF {
			return err
		}
		result = Range{Data: data[:n], Stamp: before, EOF: offset >= before.Size || int64(n) >= before.Size-offset}
		return nil
	})
	if err != nil {
		return Range{}, err
	}
	return result, nil
}

// ReadResourceVersion 在同一根限制内流式计算原始字节摘要，不能用未受限的 ReadVersion 替代。
func ReadResourceVersion(ctx context.Context, root, path string) (Version, error) {
	if err := ctx.Err(); err != nil {
		return Version{}, err
	}
	var result Version
	err := readResource(ctx, root, path, nil, func(f *os.File, before Stamp, target string) error {
		if before.Size > MaxSize {
			return fmt.Errorf("file exceeds the maximum size of %d bytes", MaxSize)
		}
		h := sha256.New()
		n, err := io.Copy(h, io.LimitReader(versionReader{ctx, f}, MaxSize+1))
		if err != nil {
			return err
		}
		if n > MaxSize {
			return fmt.Errorf("file exceeds the maximum size of %d bytes", MaxSize)
		}
		if n != before.Size {
			return ErrChanged
		}
		result = Version{Path: target, Stamp: before, Hash: hex.EncodeToString(h.Sum(nil))}
		return nil
	})
	if err != nil {
		return Version{}, err
	}
	return result, nil
}

// 两种读取共用打开与复查边界，摘要计算也不能在软链改向后读取根外文件。
func readResource(ctx context.Context, root, path string, expected *Stamp, read func(*os.File, Stamp, string) error) error {
	if !filepath.IsAbs(root) || strings.ContainsRune(root, 0) || len(root) > 4096 {
		return errors.New("file path must be absolute and contain no NUL")
	}
	if path == "" || filepath.IsAbs(path) || strings.ContainsRune(path, 0) || len(path) > 4096 {
		return ErrResourcePath
	}
	realRoot, err := filepath.EvalSymlinks(root)
	if err != nil {
		return err
	}
	rootInfo, err := os.Stat(realRoot)
	if err != nil {
		return err
	}
	target, err := resourceTarget(realRoot, path)
	if err != nil {
		return err
	}
	info, err := os.Stat(target)
	if err != nil {
		return err
	}
	if !info.Mode().IsRegular() {
		return errors.New("not a regular file")
	}
	before := stampOf(info)
	if expected != nil && (!before.Same(*expected) || before.Mode != expected.Mode) {
		return ErrChanged
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	r, err := os.OpenRoot(realRoot)
	if err != nil {
		return err
	}
	defer r.Close()
	openedRoot, err := r.Stat(".")
	if err != nil {
		return err
	}
	if !os.SameFile(rootInfo, openedRoot) {
		return ErrChanged
	}
	rel, err := filepath.Rel(realRoot, target)
	if err != nil {
		return err
	}
	// os.Root 拒绝所有绝对软链；先解析根内链接，再由固定根句柄防止验证后改向根外。
	// 非阻塞打开防止检查过的普通文件被替换成 FIFO 后挂住。
	f, err := r.OpenFile(rel, os.O_RDONLY|syscall.O_NONBLOCK, 0)
	if err != nil {
		return err
	}
	defer f.Close()
	check := func() error {
		fi, err := f.Stat()
		if err != nil {
			return err
		}
		if !fi.Mode().IsRegular() || !os.SameFile(info, fi) || !before.Same(stampOf(fi)) || before.Mode != fi.Mode() {
			return ErrChanged
		}
		currentRoot, err := filepath.EvalSymlinks(root)
		if err != nil || currentRoot != realRoot {
			return ErrChanged
		}
		currentInfo, err := os.Stat(root)
		if err != nil || !os.SameFile(rootInfo, currentInfo) {
			return ErrChanged
		}
		currentTarget, err := resourceTarget(realRoot, path)
		if err != nil || currentTarget != target {
			return ErrChanged
		}
		fi, err = os.Stat(currentTarget)
		if err != nil || !os.SameFile(info, fi) || !before.Same(stampOf(fi)) || before.Mode != fi.Mode() {
			return ErrChanged
		}
		return nil
	}
	if err := check(); err != nil {
		return err
	}
	err = read(f, before, target)
	if ctxErr := ctx.Err(); ctxErr != nil {
		return ctxErr
	}
	if err != nil {
		return err
	}
	if err := check(); err != nil {
		return err
	}
	return ctx.Err()
}

func resourceTarget(root, path string) (string, error) {
	current, resolved := root, root
	for _, part := range strings.Split(path, string(filepath.Separator)) {
		// 不用 Join：软链之后的 .. 必须按文件系统语义解析，不能先词法折叠。
		current += string(filepath.Separator) + part
		var err error
		resolved, err = filepath.EvalSymlinks(current)
		if err != nil {
			return "", err
		}
		rel, err := filepath.Rel(root, resolved)
		if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) || filepath.IsAbs(rel) {
			return "", ErrResourcePath
		}
	}
	return resolved, nil
}
