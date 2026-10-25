// Package media 列出会话里的图片和视频，桌面的图片面板和手机共用：
//   - 粘贴的：终端里显示成 [Image #N]。Claude 存在会话的图片目录里，Codex 内嵌在会话文件里，
//     Grok、Cursor 存在自己的 assets 目录、会话文件里列出路径；
//   - agent 生成的：Claude、Grok 工具返回的图片（截图、读进来的图片文件），内嵌在会话文件里；
//     Codex 图像生成和跑代码产出的图片，存在 ~/.codex/generated_images/<会话 ID>/；
//     Claude、Codex、Grok 工具输出里提到的视频文件（录屏工具返回的路径等），缩略图由 Quick Look 取第一帧。
package media

import (
	"bytes"
	"context"
	"crypto/sha1"
	"errors"
	"fmt"
	"image"
	_ "image/gif"
	_ "image/jpeg"
	_ "image/png"
	"io"
	"math"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"time"

	"golang.org/x/image/draw"
	_ "golang.org/x/image/webp"

	"posse/internal/history"
)

// Item 是会话里的一张图片或一个视频。
type Item struct {
	Key   string // 粘贴的和 Codex 的是文件路径，Claude 工具返回的是它在会话文件里的位置，视频是路径加修改时间
	N     int    // 粘贴的图片的序号
	Label string
	At    time.Time
	Path  string             // 磁盘上的图片或视频
	Tool  *history.ToolImage // 工具返回的图片，内容在会话文件 Src 里
	Src   string
	Video bool
}

// Supported 报告能不能列这个 agent 的图片。
func Supported(agent string) bool {
	return agent == "claude" || agent == "codex" || agent == "grok" || agent == "cursor"
}

// Source 是图片从哪来：磁盘上的文件，或 agent 读的文件、返回它的工具。
func (it Item) Source() string {
	switch {
	case it.Tool == nil:
		return it.Path
	case it.Tool.Path != "":
		return it.Tool.Path
	}
	return it.Tool.Tool
}

// Mime 是图片的类型；视频和认不出的为空。
func (it Item) Mime() string {
	if it.Video {
		return ""
	}
	if it.Tool != nil && it.Tool.Media != "" {
		return it.Tool.Media
	}
	switch strings.ToLower(filepath.Ext(it.Path)) {
	case ".png":
		return "image/png"
	case ".jpg", ".jpeg":
		return "image/jpeg"
	case ".gif":
		return "image/gif"
	case ".webp":
		return "image/webp"
	}
	return ""
}

// Scanner 增量找一个会话的图片和视频，不是并发安全的。
type Scanner struct {
	agent, sid, transcript string
	dir                    string // Claude 会话的图片目录，找到后不再找
	images                 *history.ImageScanner
	videos                 *history.VideoScanner
}

func NewScanner(agent, sid, transcript string) *Scanner {
	return &Scanner{agent: agent, sid: sid, transcript: transcript}
}

// Same 报告 s 是不是这个会话的；会话 ID 和会话文件是 agent 启动后才报上来的，变了要换一个 Scanner。nil 不是任何会话的。
func (s *Scanner) Same(agent, sid, transcript string) bool {
	return s != nil && s.agent == agent && s.sid == sid && s.transcript == transcript
}

// Scan 返回粘贴的和 agent 生成的，都是新的在前。
func (s *Scanner) Scan() (pasted, made []Item) {
	switch s.agent {
	case "claude":
		if s.dir == "" {
			s.dir = ClaudeImageDir(s.sid, s.transcript)
		}
		pasted = ListImages(s.dir)
	case "codex":
		made = CodexImages(s.sid)
	}
	// 会话文件要等 agent 写下第一条记录才出现，找不到就下次再找
	if s.images == nil {
		if path := history.TranscriptPath(s.agent, s.sid, s.transcript); path != "" {
			s.images = history.NewImageScanner(s.agent, path)
		}
	}
	if s.images != nil {
		if imgs, err := s.images.Scan(); err == nil {
			p, m := SessionItems(s.images.Path(), imgs)
			pasted = append(pasted, p...)
			made = append(made, m...)
		}
	}
	if s.videos == nil {
		if path := history.TranscriptPath(s.agent, s.sid, s.transcript); path != "" {
			s.videos = history.NewVideoScanner(s.agent, path)
		}
	}
	if s.videos != nil {
		if vs, err := s.videos.Scan(); err == nil {
			made = append(made, VideoItems(vs)...)
		}
	}
	slices.SortStableFunc(made, func(a, b Item) int { return b.At.Compare(a.At) })
	return pasted, made
}

// ClaudeImageDir 找 Claude 会话保存粘贴图片的目录：/private/tmp/claude-<uid>/<项目目录编码>/<会话 ID>/images。
// 项目目录编码和会话文件所在目录同名；拿不到会话文件时按会话 ID 找。
func ClaudeImageDir(sid, transcript string) string {
	if sid == "" || strings.ContainsAny(sid, `/\*?[`) {
		return ""
	}
	isDir := func(p string) bool {
		fi, err := os.Stat(p)
		return err == nil && fi.IsDir()
	}
	roots := []string{fmt.Sprintf("/private/tmp/claude-%d", os.Getuid()), filepath.Join(os.TempDir(), fmt.Sprintf("claude-%d", os.Getuid()))}
	if transcript != "" {
		for _, root := range roots {
			if p := filepath.Join(root, filepath.Base(filepath.Dir(transcript)), sid, "images"); isDir(p) {
				return p
			}
		}
	}
	for _, root := range roots {
		if m, _ := filepath.Glob(filepath.Join(root, "*", sid, "images")); len(m) > 0 {
			return m[0]
		}
	}
	return ""
}

// ListImages 列出目录里以序号命名的图片（1.png、2.jpg…），序号大的（新的）在前。
func ListImages(dir string) []Item {
	entries, _ := os.ReadDir(dir)
	var out []Item
	for _, e := range entries {
		ext := strings.ToLower(filepath.Ext(e.Name()))
		if e.IsDir() || (ext != ".png" && ext != ".jpg" && ext != ".jpeg") {
			continue
		}
		n, err := strconv.Atoi(strings.TrimSuffix(e.Name(), filepath.Ext(e.Name())))
		if err != nil {
			continue
		}
		path := filepath.Join(dir, e.Name())
		out = append(out, Item{Key: path, N: n, Label: fmt.Sprintf("#%d", n), Path: path})
	}
	slices.SortFunc(out, func(a, b Item) int { return b.N - a.N })
	return out
}

// CodexImages 列出 Codex 会话生成的图片：ig_ 开头的是图像生成工具画的，exec- 开头的是跑代码产出的。新的在前。
func CodexImages(sid string) []Item {
	home, err := os.UserHomeDir()
	if sid == "" || strings.ContainsAny(sid, `/\*?[`) || err != nil {
		return nil
	}
	dir := filepath.Join(home, ".codex", "generated_images", sid)
	entries, _ := os.ReadDir(dir)
	var out []Item
	for _, e := range entries {
		if e.IsDir() || !IsImageFile(e.Name()) {
			continue
		}
		fi, err := e.Info()
		if err != nil {
			continue
		}
		label := e.Name()
		switch {
		case strings.HasPrefix(label, "ig_"):
			label = "生成的图片"
		case strings.HasPrefix(label, "exec-"):
			label = "代码产出"
		}
		path := filepath.Join(dir, e.Name())
		out = append(out, Item{Key: path, Label: label, At: fi.ModTime(), Path: path})
	}
	slices.SortFunc(out, func(a, b Item) int { return b.At.Compare(a.At) })
	return out
}

// VideoItems 把工具输出里提到、磁盘上还在的视频排成新的在前。Key 带上修改时间：
// 录屏开始时工具就返回了路径，文件还没写完时取不到缩略图，写完后换了 Key 再取。
func VideoItems(videos []history.ToolVideo) []Item {
	var out []Item
	for i := len(videos) - 1; i >= 0; i-- {
		v := videos[i]
		fi, err := os.Stat(v.Path)
		if err != nil || fi.IsDir() {
			continue
		}
		at := v.At
		if at.IsZero() {
			at = fi.ModTime()
		}
		key := fmt.Sprintf("%s@%d", v.Path, fi.ModTime().UnixNano())
		out = append(out, Item{Key: key, Label: filepath.Base(v.Path), At: at, Path: v.Path, Video: true})
	}
	return out
}

func IsImageFile(name string) bool {
	switch strings.ToLower(filepath.Ext(name)) {
	case ".png", ".jpg", ".jpeg", ".gif", ".webp":
		return true
	}
	return false
}

// SessionItems 把会话文件里找到的图片分成粘贴的和 agent 生成的，都是新的在前：
// 粘贴的标上序号（agent 每次启动序号从 #1 重新数，所以 Key 用位置或文件路径），
// 生成的标上读的文件名或工具名。
func SessionItems(src string, imgs []history.ToolImage) (pasted, made []Item) {
	for i := len(imgs) - 1; i >= 0; i-- {
		im := imgs[i]
		it := Item{Key: fmt.Sprintf("%s:%d:%d", src, im.Offset, im.Index), At: im.At, Tool: &im, Src: src}
		if im.OnDisk {
			it.Key, it.Path, it.Tool, it.Src = im.Path, im.Path, nil, ""
		}
		if im.Pasted {
			it.N, it.Label = im.N, "粘贴"
			if im.N > 0 {
				it.Label = fmt.Sprintf("#%d", im.N)
			}
			pasted = append(pasted, it)
			continue
		}
		it.Label = im.Tool
		if im.Path != "" {
			it.Label = filepath.Base(im.Path)
		} else if j := strings.LastIndex(it.Label, "__"); j >= 0 {
			// mcp__iphone-use__phone_screenshot 只写工具名
			it.Label = it.Label[j+2:]
		}
		made = append(made, it)
	}
	return pasted, made
}

// ScaledImage 解码图片并缩到最宽 width 像素；本来就更小的不放大。
func ScaledImage(path string, width int) (*image.RGBA, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	img, _, err := decodeScaled(f, width)
	return img, err
}

// ScaledImageBytes 是 ScaledImage 读内存里的图片，顺带返回原始尺寸。
func ScaledImageBytes(b []byte, width int) (*image.RGBA, image.Point, error) {
	return decodeScaled(bytes.NewReader(b), width)
}

// PreviewImage 是 DecodePreview 解出的图片。
type PreviewImage struct {
	Img    *image.RGBA
	Size   image.Point // 原始尺寸
	Format string      // image.Decode 认出的格式，如 png、jpeg
}

// DecodePreview 解码文件标签里预览的图片：按原分辨率，超过 maxPixels 个像素才等比缩小，放大看细节时还是原来的像素。
func DecodePreview(path string, maxPixels int) (PreviewImage, error) {
	f, err := os.Open(path)
	if err != nil {
		return PreviewImage{}, err
	}
	defer f.Close()
	src, format, err := image.Decode(f)
	if err != nil {
		return PreviewImage{}, err
	}
	b := src.Bounds()
	w, h := b.Dx(), b.Dy()
	if n := w * h; n > maxPixels {
		k := math.Sqrt(float64(maxPixels) / float64(n))
		w, h = max(int(float64(w)*k), 1), max(int(float64(h)*k), 1)
	}
	dst := image.NewRGBA(image.Rect(0, 0, w, h))
	if w == b.Dx() {
		draw.Draw(dst, dst.Bounds(), src, b.Min, draw.Src)
	} else {
		draw.CatmullRom.Scale(dst, dst.Bounds(), src, b, draw.Src, nil)
	}
	return PreviewImage{Img: dst, Size: b.Size(), Format: format}, nil
}

func decodeScaled(r io.Reader, width int) (*image.RGBA, image.Point, error) {
	src, _, err := image.Decode(r)
	if err != nil {
		return nil, image.Point{}, err
	}
	b := src.Bounds()
	w, h := b.Dx(), b.Dy()
	if w > width {
		h = max(h*width/w, 1)
		w = width
	}
	dst := image.NewRGBA(image.Rect(0, 0, w, h))
	draw.CatmullRom.Scale(dst, dst.Bounds(), src, b, draw.Src, nil)
	return dst, b.Size(), nil
}

// Decode 解码一张图片（视频取第一帧）并缩到最宽 width 像素。
func Decode(it Item, width int) (*image.RGBA, error) {
	if it.Video {
		thumb, err := VideoThumb(it)
		if err != nil {
			return nil, err
		}
		return ScaledImage(thumb, width)
	}
	if it.Tool == nil {
		return ScaledImage(it.Path, width)
	}
	b, err := history.ReadToolImage(it.Src, *it.Tool)
	if err != nil {
		return nil, err
	}
	img, _, err := ScaledImageBytes(b, width)
	return img, err
}

// ReadImage 读图片的原始内容：磁盘上的文件，或从会话文件里取出工具返回的那张（是 agent 当时看到的样子）。
func ReadImage(it Item) ([]byte, error) {
	if it.Video {
		return nil, errors.New("not an image")
	}
	if it.Tool == nil {
		return os.ReadFile(it.Path)
	}
	return history.ReadToolImage(it.Src, *it.Tool)
}

// videoThumbWidth 是 Quick Look 取缩略图的尺寸，够桌面悬停放大用。
const videoThumbWidth = 1120

// VideoThumb 用 Quick Look 取视频的缩略图（第一帧），存在临时目录里，同一个 Key 只取一次。
func VideoThumb(it Item) (string, error) {
	dir := filepath.Join(os.TempDir(), "posse-video-thumbs")
	sum := sha1.Sum([]byte(it.Key))
	out := filepath.Join(dir, fmt.Sprintf("%x.png", sum[:8]))
	if _, err := os.Stat(out); err == nil {
		return out, nil
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", err
	}
	tmp, err := os.MkdirTemp(dir, "ql-")
	if err != nil {
		return "", err
	}
	defer os.RemoveAll(tmp)
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	if err := exec.CommandContext(ctx, "/usr/bin/qlmanage", "-t", "-s", strconv.Itoa(videoThumbWidth), "-o", tmp, it.Path).Run(); err != nil {
		return "", err
	}
	// qlmanage 取不到缩略图时退出码也是 0，只是不出文件
	if err := os.Rename(filepath.Join(tmp, filepath.Base(it.Path)+".png"), out); err != nil {
		return "", err
	}
	return out, nil
}

// WriteToolImage 把工具返回的图片从会话文件写到临时目录，返回写出的路径。
func WriteToolImage(it Item) (string, error) {
	b, err := ReadImage(it)
	if err != nil {
		return "", err
	}
	ext := ".png"
	switch it.Mime() {
	case "image/jpeg":
		ext = ".jpg"
	case "image/gif":
		ext = ".gif"
	case "image/webp":
		ext = ".webp"
	}
	dir := filepath.Join(os.TempDir(), "posse-images")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", err
	}
	name := strings.TrimPrefix(it.Label, "#")
	if ext2 := filepath.Ext(name); ext2 != "" {
		name = strings.TrimSuffix(name, ext2)
	}
	sum := sha1.Sum([]byte(it.Key))
	path := filepath.Join(dir, fmt.Sprintf("%s-%x%s", name, sum[:4], ext))
	return path, os.WriteFile(path, b, 0o600)
}
