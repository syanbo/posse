package daemon

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"time"

	"posse/internal/files"
	"posse/internal/history"
	"posse/internal/media"
	"posse/internal/rpc"
)

const mediaScannerLimit = 8

type mediaScanEntry struct {
	scanner *media.Scanner
	used    uint64
}

type mediaArtifact struct {
	path string
	size int64
	used uint64
}

type mediaState struct {
	once      sync.Once
	gate      chan struct{}
	ctx       context.Context
	cancel    context.CancelFunc
	root      string
	scanners  map[rpc.MediaSource]*mediaScanEntry
	artifacts map[string]mediaArtifact
	clock     uint64
	bytes     int64
	maxFiles  int
	maxBytes  int64
	thumbnail func(context.Context, string, string) (string, error)
}

func (s *mediaState) init() {
	s.once.Do(func() {
		s.gate = make(chan struct{}, 1)
		s.ctx, s.cancel = context.WithCancel(context.Background())
		s.scanners = make(map[rpc.MediaSource]*mediaScanEntry)
		s.artifacts = make(map[string]mediaArtifact)
		s.maxFiles, s.maxBytes = 64, 128<<20
		s.thumbnail = media.VideoThumbnailContext
	})
}

// 一个可取消的入口串行维护扫描器和派生缓存，IO 不占 daemon 主锁。
func (s *mediaState) enter(ctx context.Context) (context.Context, func(), error) {
	s.init()
	ctx, cancel := context.WithCancel(ctx)
	stop := context.AfterFunc(s.ctx, cancel)
	cleanup := func() { stop(); cancel() }
	select {
	case s.gate <- struct{}{}:
		if err := ctx.Err(); err != nil {
			<-s.gate
			cleanup()
			return nil, nil, err
		}
		if err := s.ctx.Err(); err != nil {
			<-s.gate
			cleanup()
			return nil, nil, err
		}
		return ctx, func() { <-s.gate; cleanup() }, nil
	case <-ctx.Done():
		cleanup()
		return nil, nil, ctx.Err()
	}
}

func (s *mediaState) close() {
	s.init()
	s.cancel()
	s.gate <- struct{}{}
	defer func() { <-s.gate }()
	if s.root != "" {
		os.RemoveAll(s.root)
	}
	s.root, s.scanners, s.artifacts, s.bytes = "", nil, nil, 0
}

func mediaReadError(ctx context.Context, err error) error {
	if ctx.Err() != nil {
		return ctx.Err()
	}
	return fileRPCError(err)
}

func mediaDiskIdentity(fi os.FileInfo) string {
	var dev, ino uint64
	if stat, ok := fi.Sys().(*syscall.Stat_t); ok {
		dev, ino = uint64(stat.Dev), stat.Ino
	}
	return fmt.Sprintf("%d:%d:%d:%d", dev, ino, fi.Size(), fi.ModTime().UnixNano())
}

func mediaDesktop(ctx context.Context) error {
	request, ok := ctx.Value(desktopCallContext{}).(desktopRequestContext)
	if !ok || !request.readOnly {
		return rpc.Errorf(rpc.CodeReadOnly, "desktop identity required for session media")
	}
	return ctx.Err()
}

func (d *Daemon) mediaList(ctx context.Context, raw json.RawMessage) (any, error) {
	if err := mediaDesktop(ctx); err != nil {
		return nil, err
	}
	var source rpc.MediaSource
	if len(raw) > 16<<10 {
		return nil, rpc.Errorf(rpc.CodeBadRequest, "invalid session media source")
	}
	if err := rpc.Decode(raw, &source); err != nil {
		return nil, err
	}
	if !media.Supported(source.Agent) || len(source.SessionID) > 256 || strings.ContainsAny(source.SessionID, "/\\*?[\x00") {
		return nil, rpc.Errorf(rpc.CodeBadRequest, "invalid session media source")
	}
	if source.TranscriptPath != "" {
		if err := filePath(source.TranscriptPath); err != nil {
			return nil, err
		}
	}
	s := &d.media
	ctx, release, err := s.enter(ctx)
	if err != nil {
		return nil, err
	}
	defer release()
	s.clock++
	entry := s.scanners[source]
	if entry == nil {
		if len(s.scanners) >= mediaScannerLimit {
			var oldest rpc.MediaSource
			var age uint64 = ^uint64(0)
			for key, value := range s.scanners {
				if value.used < age {
					oldest, age = key, value.used
				}
			}
			delete(s.scanners, oldest)
		}
		entry = &mediaScanEntry{scanner: media.NewScanner(source.Agent, source.SessionID, source.TranscriptPath)}
		s.scanners[source] = entry
	}
	entry.used = s.clock
	pasted, made, err := entry.scanner.ScanContext(ctx)
	if err != nil {
		// 取消或超限时扫描器可能只推进了部分解析状态，不复用半份结果。
		delete(s.scanners, source)
		return nil, mediaReadError(ctx, err)
	}
	result := rpc.MediaListResult{Pasted: pasted, Made: made[:min(40, len(made))], MadeAll: len(made), Found: entry.scanner.Found()}
	for _, items := range [][]media.Item{result.Pasted, result.Made} {
		for i := range items {
			if err := ctx.Err(); err != nil {
				return nil, err
			}
			if it := &items[i]; it.Tool != nil {
				it.Key += fmt.Sprintf(":%x", it.Tool.LineHash)
			} else if fi, err := os.Stat(it.Path); err == nil {
				it.Key += ":" + mediaDiskIdentity(fi)
			}
		}
	}
	encoded, err := json.Marshal(result)
	if err != nil {
		return nil, err
	}
	if len(encoded) > rpc.MediaListLimit {
		return nil, rpc.Errorf(rpc.CodeBadRequest, "session media list exceeds the response limit")
	}
	return result, ctx.Err()
}

func validateMediaItem(it media.Item) error {
	if len(it.Key) > 16384 || len(it.Label) > 4096 || len(it.Tag) > 128 {
		return rpc.Errorf(rpc.CodeBadRequest, "invalid session media item")
	}
	if it.Tool == nil {
		return filePath(it.Path)
	}
	if it.Video || it.Tool.OnDisk || it.Tool.Offset < 0 || it.Tool.Index < 0 || it.Tool.Index > 65536 || it.Tool.LineHash == ([sha256.Size]byte{}) || len(it.Tool.Path) > 4096 || len(it.Tool.Tool) > 4096 || len(it.Tool.Media) > 256 {
		return rpc.Errorf(rpc.CodeBadRequest, "invalid session image reference")
	}
	return filePath(it.Src)
}

func mediaFileInfo(path string, maxBytes int64) (os.FileInfo, error) {
	fi, err := os.Stat(path)
	if err != nil {
		return nil, err
	}
	if !fi.Mode().IsRegular() {
		return nil, errors.New("not a regular file")
	}
	if maxBytes >= 0 && fi.Size() > maxBytes {
		return nil, fmt.Errorf("file exceeds the maximum size of %d bytes", maxBytes)
	}
	return fi, nil
}

func mediaUnchanged(a, b os.FileInfo) bool {
	return os.SameFile(a, b) && a.Size() == b.Size() && a.ModTime().Equal(b.ModTime())
}

func (d *Daemon) mediaPath(ctx context.Context, raw json.RawMessage) (any, error) {
	if err := mediaDesktop(ctx); err != nil {
		return nil, err
	}
	if len(raw) > 64<<10 {
		return nil, rpc.Errorf(rpc.CodeBadRequest, "invalid session media item")
	}
	var p rpc.MediaPathParams
	if err := rpc.Decode(raw, &p); err != nil {
		return nil, err
	}
	it := p.Item
	if err := validateMediaItem(it); err != nil {
		return nil, err
	}
	if it.Tool == nil && !it.Video {
		path := it.Path
		if strings.Contains("/"+path+"/", "/../") {
			// 文档入口会清理父目录段，先按目标文件系统解析，避免 link/.. 指向另一张图。
			resolved, err := filepath.EvalSymlinks(path)
			if err != nil {
				return nil, mediaReadError(ctx, err)
			}
			path = resolved
		}
		if _, err := mediaFileInfo(path, files.MaxSize); err != nil {
			return nil, mediaReadError(ctx, err)
		}
		return rpc.MediaPathResult{Path: path}, ctx.Err()
	}
	s := &d.media
	ctx, release, err := s.enter(ctx)
	if err != nil {
		return nil, err
	}
	defer release()
	if s.root == "" {
		s.root, err = os.MkdirTemp("", "posse-media-*")
		if err != nil {
			return nil, mediaReadError(ctx, err)
		}
		absolute, err := filepath.Abs(s.root)
		if err != nil {
			os.RemoveAll(s.root)
			s.root = ""
			return nil, mediaReadError(ctx, err)
		}
		s.root = absolute
	}
	identity, _ := json.Marshal(it)
	var data []byte
	key := ""
	if it.Video {
		fi, err := mediaFileInfo(it.Path, -1)
		if err != nil {
			return nil, mediaReadError(ctx, err)
		}
		version := string(identity) + ":" + mediaDiskIdentity(fi)
		key = fmt.Sprintf("video:%x", sha256.Sum256([]byte(version)))
		if path := s.cached(key); path != "" {
			return rpc.MediaPathResult{Path: path}, ctx.Err()
		}
		work, err := os.MkdirTemp(s.root, "thumbnail-*")
		if err != nil {
			return nil, err
		}
		defer os.RemoveAll(work)
		thumbCtx, cancel := context.WithTimeout(ctx, 15*time.Second)
		path, err := s.thumbnail(thumbCtx, it.Path, work)
		cancel()
		if err != nil {
			return nil, mediaReadError(ctx, err)
		}
		// 生成器只能交付自己的工作目录，避免把外部路径当作缓存产物。
		if rel, err := filepath.Rel(work, path); err != nil || !filepath.IsLocal(rel) {
			return nil, rpc.Errorf(rpc.CodeInternal, "invalid video thumbnail path")
		}
		if _, err := mediaFileInfo(path, files.MaxSize); err != nil {
			return nil, mediaReadError(ctx, err)
		}
		f, err := os.OpenFile(path, os.O_RDONLY|syscall.O_NONBLOCK, 0)
		if err != nil {
			return nil, mediaReadError(ctx, err)
		}
		opened, err := f.Stat()
		if err == nil && !opened.Mode().IsRegular() {
			err = errors.New("not a regular file")
		}
		if err == nil {
			data, err = history.ReadMediaBytes(ctx, f, files.MaxSize)
		}
		f.Close()
		if err != nil {
			return nil, mediaReadError(ctx, err)
		}
		after, err := mediaFileInfo(it.Path, -1)
		if err != nil || !mediaUnchanged(fi, after) {
			return nil, fileRPCError(files.ErrChanged)
		}
	} else {
		data, err = history.ReadToolImageContext(ctx, it.Src, *it.Tool, files.MaxSize)
		if err != nil {
			if errors.Is(err, history.ErrImageChanged) {
				// 尾部没变的等长重写无法由增量探针发现；确知旧行失效后才重扫。
				for source, entry := range s.scanners {
					if entry.scanner.TranscriptPath() == it.Src {
						delete(s.scanners, source)
					}
				}
			}
			return nil, mediaReadError(ctx, err)
		}
		key = fmt.Sprintf("image:%x:%x", sha256.Sum256(identity), sha256.Sum256(data))
		if path := s.cached(key); path != "" {
			return rpc.MediaPathResult{Path: path}, ctx.Err()
		}
	}
	path, err := s.keep(ctx, key, data)
	if err != nil {
		return nil, mediaReadError(ctx, err)
	}
	return rpc.MediaPathResult{Path: path}, ctx.Err()
}

func (s *mediaState) cached(key string) string {
	entry, ok := s.artifacts[key]
	if !ok {
		return ""
	}
	if fi, err := mediaFileInfo(entry.path, files.MaxSize); err != nil || fi.Size() != entry.size {
		s.remove(key)
		return ""
	}
	s.clock++
	entry.used = s.clock
	s.artifacts[key] = entry
	return entry.path
}

func (s *mediaState) remove(key string) {
	entry := s.artifacts[key]
	os.Remove(entry.path)
	s.bytes -= entry.size
	delete(s.artifacts, key)
}

func (s *mediaState) keep(ctx context.Context, key string, data []byte) (string, error) {
	if int64(len(data)) > files.MaxSize || int64(len(data)) > s.maxBytes {
		return "", fmt.Errorf("file exceeds the maximum size of %d bytes", min(int64(files.MaxSize), s.maxBytes))
	}
	if err := ctx.Err(); err != nil {
		return "", err
	}
	for len(s.artifacts) >= s.maxFiles || s.bytes+int64(len(data)) > s.maxBytes {
		oldest, age := "", ^uint64(0)
		for k, v := range s.artifacts {
			if v.used < age {
				oldest, age = k, v.used
			}
		}
		s.remove(oldest)
	}
	ext := ".png"
	switch http.DetectContentType(data) {
	case "image/jpeg":
		ext = ".jpg"
	case "image/gif":
		ext = ".gif"
	case "image/webp":
		ext = ".webp"
	}
	f, err := os.CreateTemp(s.root, "resource-*"+ext)
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
	s.clock++
	s.artifacts[key] = mediaArtifact{path: f.Name(), size: int64(len(data)), used: s.clock}
	s.bytes += int64(len(data))
	kept = true
	return f.Name(), nil
}
