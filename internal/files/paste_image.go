package files

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"image/png"
	"os"
	"path/filepath"
)

var ErrInvalidPasteImage = errors.New("invalid PNG image")

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
