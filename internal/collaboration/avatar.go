package collaboration

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"image"
	"image/color"
	"image/jpeg"
	_ "image/png" // 头像原图可以是 PNG
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	"golang.org/x/image/draw"
)

const (
	// AvatarSide 是存下的头像边长：界面最大按 64pt 画，两倍屏再留余量。
	AvatarSide = 256
	// MaxAvatarBytes 是收别人头像的上限；本机压出来的 256 px JPEG 远小于它。
	MaxAvatarBytes = 256 << 10
	// MaxAvatarSource 是选的原图上限。
	MaxAvatarSource = 20 << 20
	// 原图解码前先看尺寸：像素太多的图解码会占几百 MB
	maxSourcePixels = 50_000_000
)

// ErrBadAvatar 是图片读不了、格式不对或太大。
var ErrBadAvatar = errors.New("the image must be a PNG or JPEG under 20 MB")

// AvatarPath 是头像在 POSSE_HOME 下的位置，按内容摘要命名：同一张图只存一份，换头像不覆盖旧文件。
func AvatarPath(home, sha string) string {
	return filepath.Join(home, "avatars", sha+".jpg")
}

// ValidSHA 是 64 位小写十六进制：摘要要拼进路径，别处来的先过这一关。
func ValidSHA(sha string) bool {
	if len(sha) != 64 {
		return false
	}
	for _, r := range sha {
		if (r < '0' || r > '9') && (r < 'a' || r > 'f') {
			return false
		}
	}
	return true
}

// NormalizeAvatar 把选的 PNG/JPEG 居中裁成正方形、缩到 AvatarSide、压成 JPEG（透明处垫白）。
func NormalizeAvatar(src []byte) ([]byte, error) {
	if len(src) == 0 || len(src) > MaxAvatarSource {
		return nil, ErrBadAvatar
	}
	cfg, format, err := image.DecodeConfig(bytes.NewReader(src))
	if err != nil || (format != "png" && format != "jpeg") || cfg.Width <= 0 || cfg.Height <= 0 || cfg.Width*cfg.Height > maxSourcePixels {
		return nil, ErrBadAvatar
	}
	img, _, err := image.Decode(bytes.NewReader(src))
	if err != nil {
		return nil, ErrBadAvatar
	}
	b := img.Bounds()
	side := min(b.Dx(), b.Dy())
	at := image.Pt(b.Min.X+(b.Dx()-side)/2, b.Min.Y+(b.Dy()-side)/2)
	crop := image.Rectangle{Min: at, Max: at.Add(image.Pt(side, side))}
	out := min(side, AvatarSide)
	dst := image.NewRGBA(image.Rect(0, 0, out, out))
	draw.Draw(dst, dst.Bounds(), image.NewUniform(color.White), image.Point{}, draw.Src)
	draw.CatmullRom.Scale(dst, dst.Bounds(), img, crop, draw.Over, nil)
	var buf bytes.Buffer
	if err := jpeg.Encode(&buf, dst, &jpeg.Options{Quality: 88}); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

// CheckAvatar 核对别处传来的头像：摘要对得上、不超限、是边长不超过 AvatarSide 的 JPEG。
func CheckAvatar(data []byte, sha string) error {
	if !ValidSHA(sha) || len(data) == 0 || len(data) > MaxAvatarBytes {
		return ErrBadAvatar
	}
	if got := sha256.Sum256(data); hex.EncodeToString(got[:]) != sha {
		return fmt.Errorf("avatar digest mismatch")
	}
	cfg, err := jpeg.DecodeConfig(bytes.NewReader(data))
	if err != nil || cfg.Width <= 0 || cfg.Height <= 0 || cfg.Width > AvatarSide || cfg.Height > AvatarSide {
		return ErrBadAvatar
	}
	if _, err := jpeg.Decode(bytes.NewReader(data)); err != nil {
		return ErrBadAvatar
	}
	return nil
}

// SaveAvatar 按摘要存下头像（已有就不写），返回摘要。data 应已经过 NormalizeAvatar 或 CheckAvatar。
func SaveAvatar(home string, data []byte) (string, error) {
	sum := sha256.Sum256(data)
	sha := hex.EncodeToString(sum[:])
	path := AvatarPath(home, sha)
	if _, err := os.Stat(path); err == nil {
		return sha, nil
	}
	if err := WriteFile(path, data); err != nil {
		return "", err
	}
	return sha, nil
}

// ReadAvatar 读本机存着的头像；没有返回 fs.ErrNotExist。
func ReadAvatar(home, sha string) ([]byte, error) {
	if !ValidSHA(sha) {
		return nil, fs.ErrNotExist
	}
	return os.ReadFile(AvatarPath(home, sha))
}

// HasAvatar 看本机有没有这份头像。
func HasAvatar(home, sha string) bool {
	if !ValidSHA(sha) {
		return false
	}
	_, err := os.Stat(AvatarPath(home, sha))
	return err == nil
}

// PruneAvatars 删掉 keep 之外的头像文件（换下来的、被拒的、已经没人用的）。
func PruneAvatars(home string, keep map[string]bool) error {
	dir := filepath.Join(home, "avatars")
	entries, err := os.ReadDir(dir)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil
		}
		return err
	}
	for _, e := range entries {
		sha, ok := strings.CutSuffix(e.Name(), ".jpg")
		if !ok || !ValidSHA(sha) || keep[sha] {
			continue
		}
		os.Remove(filepath.Join(dir, e.Name()))
	}
	return nil
}
