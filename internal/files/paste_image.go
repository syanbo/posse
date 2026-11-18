package files

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"image/png"
	"os"
	"path/filepath"
	"strings"
	"unicode"
)

var (
	ErrInvalidPasteImage = errors.New("invalid PNG image")
	ErrInvalidPasteFile  = errors.New("invalid file name")
)

// PasteImage 保留成功写完的临时文件，供终端里的 agent 随后读取。
func PasteImage(ctx context.Context, data []byte) (string, error) {
	if err := ctx.Err(); err != nil {
		return "", err
	}
	if len(data) > MaxSize {
		return "", fmt.Errorf("file exceeds the maximum size of %d bytes", MaxSize)
	}
	// 只核对 PNG 头和配置，避免按图片声明的像素尺寸分配整图内存。
	if _, err := png.DecodeConfig(bytes.NewReader(data)); err != nil {
		return "", ErrInvalidPasteImage
	}
	f, err := os.CreateTemp("", "posse-paste-*.png")
	if err != nil {
		return "", err
	}
	kept := false
	defer func() {
		if !kept {
			f.Close()
			os.Remove(f.Name())
		}
	}()
	path, err := filepath.Abs(f.Name())
	if err != nil {
		return "", err
	}
	for offset := 0; offset < len(data); {
		if err := ctx.Err(); err != nil {
			return "", err
		}
		end := min(offset+(64<<10), len(data))
		if _, err := f.Write(data[offset:end]); err != nil {
			return "", err
		}
		offset = end
	}
	if err := f.Close(); err != nil {
		return "", err
	}
	if err := ctx.Err(); err != nil {
		return "", err
	}
	kept = true
	return path, nil
}

// PasteFile 将本地拖入或复制的文件写入目标临时目录并保留原始文件名，供远端终端里的 agent 读取。
func PasteFile(ctx context.Context, name string, data []byte) (string, error) {
	if err := ctx.Err(); err != nil {
		return "", err
	}
	if !validPasteFileName(name) {
		return "", ErrInvalidPasteFile
	}
	if len(data) > MaxSize {
		return "", fmt.Errorf("file exceeds the maximum size of %d bytes", MaxSize)
	}
	dir, err := os.MkdirTemp("", "posse-drop-*")
	if err != nil {
		return "", err
	}
	kept := false
	target := filepath.Join(dir, name)
	var f *os.File
	defer func() {
		if !kept {
			if f != nil {
				f.Close()
			}
			_ = os.RemoveAll(dir)
		}
	}()
	f, err = os.OpenFile(target, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return "", err
	}
	for offset := 0; offset < len(data); {
		if err := ctx.Err(); err != nil {
			return "", err
		}
		end := min(offset+(64<<10), len(data))
		if _, err := f.Write(data[offset:end]); err != nil {
			return "", err
		}
		offset = end
	}
	if err := f.Close(); err != nil {
		f = nil
		return "", err
	}
	f = nil
	path, err := filepath.Abs(target)
	if err != nil {
		return "", err
	}
	if err := ctx.Err(); err != nil {
		return "", err
	}
	kept = true
	return path, nil
}

func validPasteFileName(name string) bool {
	if name == "" || name == "." || name == ".." || len(name) > 255 || filepath.Base(name) != name || strings.ContainsAny(name, "/\\\x00") {
		return false
	}
	for _, r := range name {
		if unicode.IsControl(r) {
			return false
		}
	}
	return true
}

// MakeDir 在资源协调锁内创建目录；路径已是目录时幂等成功，遇到文件或断链报错。
func MakeDir(ctx context.Context, path string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if info, err := os.Stat(path); err == nil {
		if !info.IsDir() {
			return ErrSaveConflict
		}
		return nil
	} else if !os.IsNotExist(err) {
		return err
	}
	if _, lerr := os.Lstat(path); lerr == nil {
		// 断链不能被静默当成不存在的目录。
		return ErrSaveConflict
	}
	if err := os.MkdirAll(path, 0o755); err != nil {
		return err
	}
	info, err := os.Stat(path)
	if err != nil {
		return err
	}
	if !info.IsDir() {
		return ErrSaveConflict
	}
	return ctx.Err()
}
