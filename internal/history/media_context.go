package history

import (
	"bufio"
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
	"syscall"
)

const MediaScanLimit = 4 << 20
const mediaLineLimit = ((50<<20)+2)/3*4 + (1 << 20)

var ErrMediaScanLimit = errors.New("session media scan exceeds the metadata limit")

// ErrImageChanged 让目标服务在旧偏移失效时丢弃对应扫描器，下次列表重新扫描。
var ErrImageChanged = errImageGone

func openMediaFile(path string) (*os.File, error) {
	f, err := os.OpenFile(path, os.O_RDONLY|syscall.O_NONBLOCK, 0)
	if err != nil {
		return nil, err
	}
	fi, err := f.Stat()
	if err == nil && !fi.Mode().IsRegular() {
		err = errors.New("not a regular file")
	}
	if err != nil {
		f.Close()
		return nil, err
	}
	return f, nil
}

// 新接口保留增量偏移，但不让单条图片记录或扫描元数据无限增长。
func mediaLines(ctx context.Context, f *os.File, fn func([]byte, int64) error) (int64, []byte, error) {
	br := bufio.NewReaderSize(f, 64<<10)
	var n int64
	var tail, line []byte
	for {
		if err := ctx.Err(); err != nil {
			return n, tail, err
		}
		part, err := br.ReadSlice('\n')
		if len(line)+len(part) > mediaLineLimit {
			return n, tail, errors.New("session media record exceeds the size limit")
		}
		line = append(line, part...)
		if errors.Is(err, bufio.ErrBufferFull) {
			continue
		}
		if err == io.EOF {
			return n, tail, nil
		}
		if err != nil {
			return n, tail, err
		}
		n += int64(len(line))
		if err := fn(line, n); err != nil {
			return n, tail, err
		}
		tail = append(tail[:0], line[max(0, len(line)-tailLen):]...)
		line = nil
	}
}

func (s *ImageScanner) metadataSize() int {
	n := len(s.images)*256 + len(s.tools)*96
	for id, ref := range s.tools {
		n += len(id) + len(ref.name) + len(ref.path)
	}
	for _, im := range s.images {
		n += len(im.Tool) + len(im.Path) + len(im.Media)
	}
	return n
}

func (s *ImageScanner) ScanContext(ctx context.Context) ([]ToolImage, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	f, err := openMediaFile(s.path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	fi, err := f.Stat()
	if err != nil {
		return nil, err
	}
	if fi.Size() < s.offset || !tailMatches(f, s.offset, s.tail) {
		s.offset, s.tail, s.tools, s.last, s.images = 0, nil, map[string]toolRef{}, nil, nil
	}
	if _, err := f.Seek(s.offset, io.SeekStart); err != nil {
		return nil, err
	}
	base := s.offset
	parse := map[string]func([]byte, int64){"claude": s.line, "codex": s.codexLine, "grok": s.grokLine, "cursor": s.cursorLine}[s.agent]
	if parse == nil {
		return nil, errors.New("unsupported media agent")
	}
	lines := 0
	n, tail, err := mediaLines(ctx, f, func(line []byte, end int64) error {
		if !containsAny(line, imageMarkers[s.agent]) {
			if s.agent == "grok" {
				s.last = nil
			}
			return nil
		}
		before := len(s.images)
		parse(line, base+end-int64(len(line)))
		if len(s.images) > before {
			sum := sha256.Sum256(line)
			for i := before; i < len(s.images); i++ {
				im := &s.images[i]
				im.LineHash = sum
				// 正则匹配的路径可能是长文本的子串，缓存不保留整段工具输出。
				im.Path, im.Tool, im.Media = strings.Clone(im.Path), strings.Clone(im.Tool), strings.Clone(im.Media)
			}
		}
		lines++
		// 分批计量避免工具映射越来越大时每行遍历，长行则立即核对。
		if lines%256 == 0 || len(line) > 64<<10 {
			if s.metadataSize() > MediaScanLimit {
				return ErrMediaScanLimit
			}
		}
		return ctx.Err()
	})
	if err != nil {
		return nil, err
	}
	if s.metadataSize() > MediaScanLimit {
		return nil, ErrMediaScanLimit
	}
	s.offset += n
	if n > 0 {
		s.tail = tail
	}
	return s.images, nil
}

func (s *VideoScanner) ScanContext(ctx context.Context) ([]ToolVideo, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	f, err := openMediaFile(s.path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	fi, err := f.Stat()
	if err != nil {
		return nil, err
	}
	if fi.Size() < s.offset || !tailMatches(f, s.offset, s.tail) {
		s.offset, s.tail, s.videos, s.seen = 0, nil, nil, map[string]int{}
	}
	if _, err := f.Seek(s.offset, io.SeekStart); err != nil {
		return nil, err
	}
	metadata := 0
	for _, v := range s.videos {
		metadata += len(v.Path)*2 + 96
	}
	n, tail, err := mediaLines(ctx, f, func(line []byte, _ int64) error {
		before := len(s.videos)
		if containsAny(line, videoMarkers) {
			s.line(line)
		}
		for i := before; i < len(s.videos); i++ {
			v := &s.videos[i]
			delete(s.seen, v.Path)
			v.Path = strings.Clone(v.Path)
			s.seen[v.Path] = i
			metadata += len(v.Path)*2 + 96
		}
		if metadata > MediaScanLimit {
			return ErrMediaScanLimit
		}
		return ctx.Err()
	})
	if err != nil {
		return nil, err
	}
	s.offset += n
	if n > 0 {
		s.tail = tail
	}
	return s.videos, nil
}

func ReadToolImageContext(ctx context.Context, path string, img ToolImage, maxBytes int64) ([]byte, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if maxBytes < 0 || img.Offset < 0 || img.Index < 0 {
		return nil, errors.New("invalid session image reference")
	}
	if img.OnDisk {
		path = img.Path
	}
	f, err := openMediaFile(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	if img.OnDisk {
		return ReadMediaBytes(ctx, f, maxBytes)
	}
	if _, err := f.Seek(img.Offset, io.SeekStart); err != nil {
		return nil, err
	}
	br := bufio.NewReaderSize(f, 64<<10)
	var line []byte
	for {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		part, err := br.ReadSlice('\n')
		if len(line)+len(part) > mediaLineLimit {
			return nil, errors.New("session media record exceeds the size limit")
		}
		line = append(line, part...)
		if errors.Is(err, bufio.ErrBufferFull) {
			continue
		}
		if err != nil && err != io.EOF {
			return nil, err
		}
		break
	}
	if img.LineHash != ([sha256.Size]byte{}) && sha256.Sum256(line) != img.LineHash {
		return nil, errImageGone
	}
	data, err := readToolImageLine(line, img)
	if err != nil {
		return nil, err
	}
	if int64(len(data)) > maxBytes {
		return nil, fmt.Errorf("file exceeds the maximum size of %d bytes", maxBytes)
	}
	if img.Digest != ([sha256.Size]byte{}) && sha256.Sum256(data) != img.Digest {
		return nil, errImageGone
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	return data, nil
}

// ReadMediaBytes 在流式读入时检查取消与限额，调用方负责只传普通文件。
func ReadMediaBytes(ctx context.Context, r io.Reader, maxBytes int64) ([]byte, error) {
	var out []byte
	buf := make([]byte, 64<<10)
	for {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		n, err := r.Read(buf)
		if int64(len(out))+int64(n) > maxBytes {
			return nil, fmt.Errorf("file exceeds the maximum size of %d bytes", maxBytes)
		}
		out = append(out, buf[:n]...)
		if err == io.EOF {
			return out, ctx.Err()
		}
		if err != nil {
			return nil, err
		}
	}
}
