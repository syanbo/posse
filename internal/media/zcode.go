package media

import (
	"bufio"
	"bytes"
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"syscall"

	"posse/internal/agent"
	"posse/internal/files"
	"posse/internal/history"
)

const zcodeArtifactID = `tool-result-[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}`

var zcodeArtifactName = regexp.MustCompile(`-(` + zcodeArtifactID + `)\.[a-z0-9]+$`)
var zcodeArtifactRef = regexp.MustCompile(`^` + zcodeArtifactID + `$`)

const maxInlineEncoded = (files.MaxSize+2)/3*4 + 64

// ZCode 将剪贴板图片的 data URL 写入本会话目录的 prompt-attachment-*.txt，二进制工具产出另有原始文件。
func (s *Scanner) scanZCode(ctx context.Context) (pasted, made []Item, err error) {
	s.zcodeFound = false
	if !agent.IsZCodeSession(s.sid) {
		return nil, nil, nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return nil, nil, err
	}
	base := filepath.Join(agent.ZCodeStorageRoot(home), "cli", "artifacts")
	store, err := os.OpenRoot(base)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil, nil
	}
	if err != nil {
		return nil, nil, err
	}
	defer store.Close()
	refs, err := history.ZCodeArtifactRefs(ctx, home, s.sid)
	if err != nil {
		return nil, nil, err
	}
	sources := map[string]map[string]bool{s.sid: nil}
	for _, ref := range refs {
		uri, ok := strings.CutPrefix(ref, "zcode-artifact://")
		sid, id, split := strings.Cut(uri, "/")
		if !ok || !split || sid == s.sid || !agent.IsZCodeSession(sid) || !zcodeArtifactRef.MatchString(id) {
			continue
		}
		if sources[sid] == nil {
			sources[sid] = map[string]bool{}
		}
		sources[sid][id] = true
	}
	for sid, filter := range sources {
		items, found, err := scanZCodeArtifacts(ctx, store, base, sid, filter)
		if err != nil {
			return nil, nil, err
		}
		s.zcodeFound = s.zcodeFound || found
		for _, it := range items {
			if it.Tag == TagPasted {
				pasted = append(pasted, it)
			} else {
				made = append(made, it)
			}
		}
	}
	newest := func(a, b Item) int {
		if byTime := b.At.Compare(a.At); byTime != 0 {
			return byTime
		}
		return strings.Compare(a.Key, b.Key)
	}
	slices.SortFunc(pasted, newest)
	slices.SortFunc(made, newest)
	return pasted, made, nil
}

func scanZCodeArtifacts(ctx context.Context, store *os.Root, base, sid string, filter map[string]bool) ([]Item, bool, error) {
	root, err := store.OpenRoot(sid)
	if errors.Is(err, os.ErrNotExist) {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, err
	}
	defer root.Close()
	f, err := root.Open(".")
	if err != nil {
		return nil, false, err
	}
	defer f.Close()
	var items []Item
	for {
		entries, readErr := f.ReadDir(128)
		for _, e := range entries {
			if err := ctx.Err(); err != nil {
				return nil, false, err
			}
			name := e.Name()
			if e.IsDir() || e.Type()&os.ModeSymlink != 0 {
				continue
			}
			match := zcodeArtifactName.FindStringSubmatch(name)
			if len(match) == 0 || filter != nil && !filter[match[1]] {
				continue
			}
			fi, err := e.Info()
			if err != nil || !fi.Mode().IsRegular() {
				continue
			}
			path := filepath.Join(base, sid, name)
			it := Item{Path: path, Key: fmt.Sprintf("%s:%d:%d", path, fi.Size(), fi.ModTime().UnixNano()), At: fi.ModTime(), Tag: TagCodeOutput}
			attachment := strings.HasPrefix(name, "prompt-attachment-")
			switch strings.ToLower(filepath.Ext(name)) {
			case ".txt":
				if fi.Size() > maxInlineEncoded {
					continue
				}
				image, err := root.OpenFile(name, os.O_RDONLY|syscall.O_NONBLOCK, 0)
				if err != nil {
					continue
				}
				header, _ := bufio.NewReaderSize(image, 64).ReadSlice(',')
				image.Close()
				mime, ok := strings.CutSuffix(strings.TrimPrefix(string(header), "data:"), ";base64,")
				ext := inlineVideoExt(mime)
				if !ok || !strings.HasPrefix(string(header), "data:") || !IsInlineImageMime(mime) && ext == "" {
					continue
				}
				it.InlineMime = mime
				if ext != "" {
					it.Video, it.Label, it.Tag = true, strings.TrimSuffix(name, ".txt")+ext, ""
				}
			case ".png", ".jpg", ".jpeg", ".gif", ".webp":
				if fi.Size() > files.MaxSize {
					continue
				}
			case ".mp4", ".mov", ".webm":
				it.Video, it.Label, it.Tag = true, name, ""
			default:
				continue
			}
			if attachment {
				it.Tag = TagPasted
			}
			items = append(items, it)
		}
		if errors.Is(readErr, io.EOF) {
			break
		}
		if readErr != nil {
			return nil, false, readErr
		}
	}
	return items, true, nil
}

func IsInlineImageMime(mime string) bool {
	switch mime {
	case "image/png", "image/jpeg", "image/gif", "image/webp":
		return true
	}
	return false
}

func ReadInlineImage(ctx context.Context, it Item) ([]byte, error) {
	if !IsInlineImageMime(it.InlineMime) {
		return nil, errors.New("invalid session image reference")
	}
	f, fi, offset, err := openInlineMedia(it)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	r := io.NewSectionReader(f, offset, fi.Size()-offset)
	data, err := history.ReadMediaBytes(ctx, base64.NewDecoder(base64.StdEncoding, r), files.MaxSize)
	if err != nil {
		return nil, err
	}
	if after, err := f.Stat(); err != nil || fi.Size() != after.Size() || !fi.ModTime().Equal(after.ModTime()) {
		return nil, files.ErrChanged
	}
	return data, nil
}

func inlineVideoExt(mime string) string {
	switch mime {
	case "video/mp4":
		return ".mp4"
	case "video/quicktime":
		return ".mov"
	case "video/webm":
		return ".webm"
	default:
		return ""
	}
}

func IsInlineVideoMime(mime string) bool { return inlineVideoExt(mime) != "" }

func openInlineMedia(it Item) (*os.File, os.FileInfo, int64, error) {
	f, err := os.OpenFile(it.Path, os.O_RDONLY|syscall.O_NONBLOCK, 0)
	if err != nil {
		return nil, nil, 0, err
	}
	fi, err := f.Stat()
	if err != nil {
		f.Close()
		return nil, nil, 0, err
	}
	if !fi.Mode().IsRegular() || fi.Size() > maxInlineEncoded {
		f.Close()
		return nil, nil, 0, errors.New("invalid session media item")
	}
	prefix := []byte("data:" + it.InlineMime + ";base64,")
	header := make([]byte, len(prefix))
	if _, err := io.ReadFull(f, header); err != nil || !bytes.Equal(header, prefix) {
		f.Close()
		return nil, nil, 0, errors.New("invalid session media item")
	}
	return f, fi, int64(len(prefix)), nil
}

// InlineVideo 可按解码后的偏移读取 ZCode 的单行 base64，不必为每个手机请求读整段视频。
type InlineVideo struct {
	file                      *os.File
	dataOffset, encoded, size int64
}

func OpenInlineVideo(it Item) (*InlineVideo, error) {
	if !it.Video || !IsInlineVideoMime(it.InlineMime) {
		return nil, errors.New("invalid session media item")
	}
	f, fi, offset, err := openInlineMedia(it)
	if err != nil {
		return nil, err
	}
	encoded := fi.Size() - offset
	var tail [2]byte
	if encoded == 0 || encoded%4 != 0 {
		f.Close()
		return nil, errors.New("invalid session media item")
	}
	if _, err := f.ReadAt(tail[:], fi.Size()-2); err != nil {
		f.Close()
		return nil, err
	}
	size := encoded / 4 * 3
	for _, b := range tail {
		if b == '=' {
			size--
		}
	}
	if size > files.MaxSize {
		f.Close()
		return nil, fmt.Errorf("file exceeds the maximum size of %d bytes", files.MaxSize)
	}
	return &InlineVideo{file: f, dataOffset: offset, encoded: encoded, size: size}, nil
}

func (v *InlineVideo) Close() error { return v.file.Close() }
func (v *InlineVideo) Size() int64  { return v.size }

func (v *InlineVideo) ReadAt(p []byte, offset int64) (int, error) {
	if offset < 0 || offset >= v.size {
		return 0, io.EOF
	}
	// 四个 base64 字符对应三个字节。只多解码一次起始分组里前面的字节。
	start := offset / 3 * 4
	r := base64.NewDecoder(base64.StdEncoding, io.NewSectionReader(v.file, v.dataOffset+start, v.encoded-start))
	if _, err := io.CopyN(io.Discard, r, offset%3); err != nil {
		return 0, err
	}
	n, err := io.ReadFull(r, p[:min(int64(len(p)), v.size-offset)])
	if err == nil && n < len(p) {
		err = io.EOF
	}
	return n, err
}

// WriteInlineVideo 为预览器生成一个有正确扩展名的普通视频文件。
func WriteInlineVideo(ctx context.Context, it Item, dir string) (string, error) {
	v, err := OpenInlineVideo(it)
	if err != nil {
		return "", err
	}
	defer v.Close()
	f, err := os.CreateTemp(dir, "posse-video-*"+inlineVideoExt(it.InlineMime))
	if err != nil {
		return "", err
	}
	path := f.Name()
	r := io.NewSectionReader(v, 0, v.Size())
	buf := make([]byte, 64<<10)
	for remaining := v.Size(); remaining > 0 && err == nil; {
		if err = ctx.Err(); err != nil {
			break
		}
		var n int
		n, err = io.ReadFull(r, buf[:min(remaining, int64(len(buf)))])
		if err == nil {
			_, err = f.Write(buf[:n])
		}
		remaining -= int64(n)
	}
	if err := errors.Join(err, f.Close(), ctx.Err()); err != nil {
		os.Remove(path)
		return "", err
	}
	return path, nil
}
