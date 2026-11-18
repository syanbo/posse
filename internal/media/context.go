package media

import (
	"context"
	"crypto/sha256"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"syscall"

	"posse/internal/files"
	"posse/internal/history"
)

func (s *Scanner) TranscriptPath() string {
	if s.images != nil {
		return s.images.Path()
	}
	return s.transcript
}

// ScanContext 供目标服务使用；旧 Scan 的本机与手机行为保持不变。
func (s *Scanner) ScanContext(ctx context.Context) (pasted, made []Item, err error) {
	if err = ctx.Err(); err != nil {
		return
	}
	switch s.agent {
	case "claude":
		if s.dir == "" {
			s.dir = ClaudeImageDir(s.sid, s.transcript)
		}
		pasted, err = mediaDirectory(ctx, s.dir, true)
	case "antigravity":
		pasted, made, err = antigravityMedia(ctx, history.TranscriptPath(s.agent, s.sid, s.transcript))
	case "codex":
		if s.sid != "" {
			var home string
			home, err = os.UserHomeDir()
			if err == nil {
				made, err = mediaDirectory(ctx, filepath.Join(home, ".codex", "generated_images", s.sid), false)
			}
		}
	}
	if err != nil {
		return nil, nil, err
	}
	if s.images == nil {
		if path := history.TranscriptPath(s.agent, s.sid, s.transcript); path != "" {
			s.images = history.NewImageScanner(s.agent, path)
			s.videos = history.NewVideoScanner(s.agent, path)
		}
	}
	if s.images != nil {
		imgs, scanErr := s.images.ScanContext(ctx)
		if scanErr != nil {
			return nil, nil, scanErr
		}
		if s.agent == "codex" {
			made, err = s.withoutEmbeddedCopiesContext(ctx, made, imgs)
			if err != nil {
				return nil, nil, err
			}
		}
		p, m := SessionItems(s.images.Path(), imgs)
		pasted, made = append(pasted, p...), append(made, m...)
		if s.videos == nil {
			s.videos = history.NewVideoScanner(s.agent, s.images.Path())
		}
		videos, scanErr := s.videos.ScanContext(ctx)
		if scanErr != nil {
			return nil, nil, scanErr
		}
		for _, video := range videos {
			if err := ctx.Err(); err != nil {
				return nil, nil, err
			}
			made = append(made, VideoItems([]history.ToolVideo{video})...)
		}
	}
	if s.agent == "antigravity" {
		pasted, made = uniqueAntigravityMedia(pasted, made)
		slices.SortStableFunc(pasted, func(a, b Item) int { return b.At.Compare(a.At) })
	}
	slices.SortStableFunc(made, func(a, b Item) int { return b.At.Compare(a.At) })
	return pasted, made, ctx.Err()
}

func mediaDirectory(ctx context.Context, dir string, pasted bool) ([]Item, error) {
	if dir == "" {
		return nil, nil
	}
	f, err := os.OpenFile(dir, os.O_RDONLY|syscall.O_NONBLOCK, 0)
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	defer f.Close()
	var out []Item
	metadata := 0
	for {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		entries, err := f.ReadDir(256)
		for _, e := range entries {
			if e.IsDir() || !IsImageFile(e.Name()) {
				continue
			}
			it := Item{Path: filepath.Join(dir, e.Name()), Label: e.Name()}
			it.Key = it.Path
			if pasted {
				ext := strings.ToLower(filepath.Ext(e.Name()))
				if ext != ".png" && ext != ".jpg" && ext != ".jpeg" {
					continue
				}
				n, err := strconv.Atoi(strings.TrimSuffix(e.Name(), filepath.Ext(e.Name())))
				if err != nil {
					continue
				}
				it.N, it.Label = n, "#"+strconv.Itoa(n)
			} else {
				fi, err := e.Info()
				if err != nil {
					continue
				}
				it.At = fi.ModTime()
				switch {
				case strings.HasPrefix(e.Name(), "ig_"):
					it.Label, it.Tag = "", TagGenerated
				case strings.HasPrefix(e.Name(), "exec-"):
					it.Label, it.Tag = "", TagCodeOutput
				}
			}
			metadata += 256 + len(it.Path)*2 + len(it.Label)
			if metadata > history.MediaScanLimit {
				return nil, history.ErrMediaScanLimit
			}
			out = append(out, it)
		}
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, err
		}
	}
	if pasted {
		slices.SortFunc(out, func(a, b Item) int { return b.N - a.N })
	} else {
		slices.SortFunc(out, func(a, b Item) int { return b.At.Compare(a.At) })
	}
	return out, nil
}

type mediaReader struct {
	ctx context.Context
	r   io.Reader
}

func (r mediaReader) Read(b []byte) (int, error) {
	if err := r.ctx.Err(); err != nil {
		return 0, err
	}
	return r.r.Read(b)
}

func (s *Scanner) withoutEmbeddedCopiesContext(ctx context.Context, items []Item, images []history.ToolImage) ([]Item, error) {
	embedded := map[[sha256.Size]byte]bool{}
	for _, im := range images {
		if !im.Pasted && im.Digest != ([sha256.Size]byte{}) {
			embedded[im.Digest] = true
		}
	}
	cache := make(map[string]fileDigest)
	out := items[:0]
	for _, it := range items {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		d, ok := s.digests[it.Path]
		fi, err := os.Stat(it.Path)
		if len(embedded) == 0 || err != nil || !fi.Mode().IsRegular() || fi.Size() > files.MaxSize {
			out = append(out, it)
			continue
		}
		if !ok || !os.SameFile(d.info, fi) || d.info.Size() != fi.Size() || !d.info.ModTime().Equal(fi.ModTime()) {
			f, err := os.OpenFile(it.Path, os.O_RDONLY|syscall.O_NONBLOCK, 0)
			if err != nil {
				out = append(out, it)
				continue
			}
			opened, statErr := f.Stat()
			if statErr != nil || !opened.Mode().IsRegular() {
				f.Close()
				out = append(out, it)
				continue
			}
			h := sha256.New()
			n, err := io.Copy(h, io.LimitReader(mediaReader{ctx, f}, files.MaxSize+1))
			f.Close()
			if err != nil || n > files.MaxSize {
				out = append(out, it)
				continue
			}
			d = fileDigest{info: opened}
			copy(d.sum[:], h.Sum(nil))
		}
		cache[it.Path] = d
		if !embedded[d.sum] {
			out = append(out, it)
		}
	}
	s.digests = cache
	return out, ctx.Err()
}

// VideoThumbnailContext 仅在调用方的私有工作目录内生成，取消时停止 Quick Look 子进程。
func VideoThumbnailContext(ctx context.Context, path, dir string) (string, error) {
	if err := exec.CommandContext(ctx, "/usr/bin/qlmanage", "-t", "-s", strconv.Itoa(videoThumbWidth), "-o", dir, path).Run(); err != nil {
		if ctx.Err() != nil {
			return "", ctx.Err()
		}
		return "", errors.New("could not generate video thumbnail")
	}
	return filepath.Join(dir, filepath.Base(path)+".png"), ctx.Err()
}
