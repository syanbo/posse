package mobile

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"strings"
	"time"

	"hive/internal/files"
)

// 手机文件页的限制，照 Orca 的 file-preview-budget.ts 和 file-search.ts。
const (
	fileReadMax  = 512 << 10 // files.read 超出部分截掉
	imageReadMax = 10 << 20  // files.readPreview 的图片
	fileListMax  = 5000      // files.list 最多返回多少个文件
	rangeMax     = 1 << 20   // files.readRange 一次最多给多少字节
	searchMax    = 32
)

// binaryExts 是手机按扩展名当二进制、不走 files.read 的文件（Orca 的 MOBILE_BINARY_EXTENSIONS）。
var binaryExts = map[string]bool{
	".avif": true, ".bmp": true, ".gif": true, ".heic": true, ".ico": true, ".jpeg": true, ".jpg": true,
	".mov": true, ".mp3": true, ".mp4": true, ".pdf": true, ".png": true, ".webp": true, ".zip": true,
}

var previewMime = map[string]string{
	".png": "image/png", ".jpg": "image/jpeg", ".jpeg": "image/jpeg", ".gif": "image/gif", ".svg": "image/svg+xml",
	".webp": "image/webp", ".bmp": "image/bmp", ".ico": "image/x-icon", ".pdf": "application/pdf",
}

type fileParams struct {
	Worktree     string `json:"worktree"`
	RelativePath string `json:"relativePath"`
	Query        string `json:"query"`
	Limit        int    `json:"limit"`
	Offset       int64  `json:"offset"`
}

// fileMethod 处理 files.*；不认识的方法返回 false。git ls-files 和读大文件可能要一会儿，
// 放到自己的 goroutine 里，不挡住同一连接上的终端输入。
func (c *conn) fileMethod(req request, p json.RawMessage) bool {
	var run func(root string, a fileParams) (any, error)
	switch req.Method {
	case "files.readDir":
		run = readDirReply
	case "files.list":
		run = listReply
	case "files.searchPaths":
		run = searchReply
	case "files.read":
		run = readReply
	case "files.readPreview":
		run = previewReply
	case "files.stat":
		run = statReply
	case "files.readRange":
		run = rangeReply
	default:
		return false
	}
	var a fileParams
	json.Unmarshal(p, &a)
	w, ok := workspaceOf(c.srv.host.State(), a.Worktree)
	go func() {
		var res any
		err := errorf("not_found", "workspace %s not found", a.Worktree)
		if ok {
			res, err = run(w.Path, a)
		}
		c.replyBulk(req.ID, res, err)
	}()
	return true
}

// joinRel 把手机给的相对路径接到工作区目录上，拒绝绝对路径和 ..（Orca 的 isSafeMobileRelativePath）。
// 空路径是工作区根目录，只有 allowRoot 时可以。
func joinRel(root, rel string, allowRoot bool) (string, error) {
	rel = strings.ReplaceAll(rel, `\`, "/")
	if rel == "" && allowRoot {
		return root, nil
	}
	if rel == "" || strings.HasPrefix(rel, "/") {
		return "", errorf("invalid_relative_path", "invalid_relative_path")
	}
	for part := range strings.SplitSeq(rel, "/") {
		if part == "" || part == "." || part == ".." {
			return "", errorf("invalid_relative_path", "invalid_relative_path")
		}
	}
	return filepath.Join(root, filepath.FromSlash(rel)), nil
}

func readDirReply(root string, a fileParams) (any, error) {
	dir, err := joinRel(root, a.RelativePath, true)
	if err != nil {
		return nil, err
	}
	es, _, err := files.ReadDir(dir)
	if err != nil {
		return nil, err
	}
	out := make([]map[string]any, 0, len(es))
	for _, e := range es {
		fi, _ := os.Lstat(filepath.Join(dir, e.Name))
		out = append(out, map[string]any{"name": e.Name, "isDirectory": e.Dir, "isSymlink": fi != nil && fi.Mode()&fs.ModeSymlink != 0})
	}
	return out, nil
}

func listFiles(root string) (*files.List, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	return files.ListFiles(ctx, root)
}

// fileRows 是 Orca 的 RuntimeFileListResult。
func fileRows(root string, paths []string, total int, truncated bool) map[string]any {
	rows := make([]map[string]any, 0, len(paths))
	for _, p := range paths {
		p = filepath.ToSlash(p)
		kind := "text"
		if binaryExts[strings.ToLower(path.Ext(p))] {
			kind = "binary"
		}
		rows = append(rows, map[string]any{"relativePath": p, "basename": path.Base(p), "kind": kind})
	}
	return map[string]any{"rootPath": root, "files": rows, "totalCount": total, "truncated": truncated}
}

func listReply(root string, _ fileParams) (any, error) {
	l, err := listFiles(root)
	if err != nil {
		return nil, err
	}
	paths := l.Files[:min(len(l.Files), fileListMax)]
	return fileRows(root, paths, len(l.Files), len(l.Files) > fileListMax || l.Truncated), nil
}

func searchReply(root string, a fileParams) (any, error) {
	l, err := listFiles(root)
	if err != nil {
		return nil, err
	}
	limit := a.Limit
	if limit <= 0 {
		limit = 16
	}
	limit = min(limit, searchMax)
	var paths []string
	for _, m := range files.Fuzzy(l.Files, a.Query, limit) {
		paths = append(paths, m.Path)
	}
	return fileRows(root, paths, len(paths), false), nil
}

func readReply(root string, a fileParams) (any, error) {
	name, err := joinRel(root, a.RelativePath, false)
	if err != nil {
		return nil, err
	}
	if binaryExts[strings.ToLower(filepath.Ext(name))] {
		return nil, errorf("binary_file", "binary_file")
	}
	f, err := os.Open(name)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	fi, err := f.Stat()
	if err != nil {
		return nil, err
	}
	if fi.IsDir() {
		return nil, errorf("not_a_file", "not_a_file")
	}
	b, err := io.ReadAll(io.LimitReader(f, fileReadMax))
	if err != nil {
		return nil, err
	}
	if files.IsBinary(b) {
		return nil, errorf("binary_file", "binary_file")
	}
	truncated := fi.Size() > fileReadMax
	if truncated {
		b = b[:utf8Complete(b)]
	}
	return map[string]any{"relativePath": a.RelativePath, "content": string(b), "truncated": truncated, "byteLength": fi.Size()}, nil
}

// previewReply 是图片预览：图片和 PDF 以 base64 返回，其他文本原样返回，二进制只报 isBinary。
func previewReply(root string, a fileParams) (any, error) {
	name, err := joinRel(root, a.RelativePath, false)
	if err != nil {
		return nil, err
	}
	mime := previewMime[strings.ToLower(filepath.Ext(name))]
	limit := int64(fileReadMax)
	if mime != "" {
		limit = imageReadMax
	}
	fi, err := os.Stat(name)
	if err != nil {
		return nil, err
	}
	if fi.IsDir() {
		return nil, errorf("not_a_file", "not_a_file")
	}
	if fi.Size() > limit {
		return nil, errorf("file_too_large", "file_too_large")
	}
	b, err := os.ReadFile(name)
	if err != nil {
		return nil, err
	}
	switch {
	case mime != "":
		return map[string]any{"content": b, "isBinary": true, "isImage": true, "mimeType": mime}, nil
	case files.IsBinary(b):
		return map[string]any{"content": "", "isBinary": true}, nil
	}
	return map[string]any{"content": string(b), "isBinary": false}, nil
}

func statReply(root string, a fileParams) (any, error) {
	name, err := joinRel(root, a.RelativePath, true)
	if err != nil {
		return nil, err
	}
	fi, err := os.Stat(name)
	if err != nil {
		return nil, err
	}
	return map[string]any{"size": fi.Size(), "isDirectory": fi.IsDir(), "mtime": fi.ModTime().UnixMilli()}, nil
}

// rangeReply 按段读文件（Hive 扩展，手机放视频时分段下载到本地再播），base64。
// size 是文件现在的大小，手机按它判断读完没有。
func rangeReply(root string, a fileParams) (any, error) {
	name, err := joinRel(root, a.RelativePath, false)
	if err != nil {
		return nil, err
	}
	f, err := os.Open(name)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	fi, err := f.Stat()
	if err != nil {
		return nil, err
	}
	if fi.IsDir() {
		return nil, errorf("not_a_file", "not_a_file")
	}
	n := a.Limit
	if n <= 0 || n > rangeMax {
		n = rangeMax
	}
	if a.Offset < 0 {
		return nil, errorf("bad_params", "offset")
	}
	b := make([]byte, n)
	got, err := f.ReadAt(b, a.Offset)
	if err != nil && !errors.Is(err, io.EOF) {
		return nil, err
	}
	return map[string]any{"content": b[:got], "size": fi.Size()}, nil
}
