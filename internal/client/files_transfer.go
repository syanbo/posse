package client

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

	"posse/internal/files"
	"posse/internal/rpc"
)

const (
	DirectoryItemLimit  = 500
	DirectoryBytesLimit = 256 << 20
	DirectoryDepthLimit = 32
)

// UploadFile 的 expected 来自目标 FileVersion；未知结果由 SaveFile 保留操作引用，不重发。
func (c *Client) UploadFile(ctx context.Context, localPath, targetPath string, expected files.Version) (rpc.FileSaveResult, error) {
	if err := ctx.Err(); err != nil {
		return rpc.FileSaveResult{}, err
	}
	if c.Desktop == nil {
		return rpc.FileSaveResult{}, rpc.Errorf(rpc.CodeReadOnly, "desktop identity required for file saves")
	}
	before, err := files.ReadVersion(ctx, localPath)
	if err != nil {
		return rpc.FileSaveResult{}, err
	}
	if before.Stamp.Missing {
		return rpc.FileSaveResult{}, &os.PathError{Op: "open", Path: localPath, Err: os.ErrNotExist}
	}
	data, err := readUploadSource(ctx, localPath, before)
	if err != nil {
		return rpc.FileSaveResult{}, err
	}
	return c.SaveFile(ctx, rpc.FileSaveParams{Path: targetPath, Expected: expected, Data: data, Hash: before.Hash})
}

func readUploadSource(ctx context.Context, path string, before files.Version) ([]byte, error) {
	data := make([]byte, 0, int(before.Stamp.Size))
	for {
		r, err := files.ReadResourceRange(ctx, filepath.Dir(before.Path), filepath.Base(before.Path), int64(len(data)), files.RangeLimit, &before.Stamp)
		if err != nil {
			return nil, err
		}
		data = append(data, r.Data...)
		if r.EOF {
			break
		}
	}
	// 原地改写并恢复 mtime 可以绕过磁盘戳；前后完整版本和实际读到的摘要都必须一致。
	after, err := files.ReadVersion(ctx, path)
	if err != nil {
		return nil, err
	}
	sum := sha256.Sum256(data)
	if !before.Same(after) || int64(len(data)) != before.Stamp.Size || hex.EncodeToString(sum[:]) != before.Hash {
		return nil, files.ErrChanged
	}
	return data, ctx.Err()
}

// DownloadFile 的 expected 来自本机 ReadVersion；目标完整校验成功后才开始本机条件原子写入。
func (c *Client) DownloadFile(ctx context.Context, targetPath, localPath string, expected files.Version) error {
	if !filepath.IsAbs(targetPath) || strings.ContainsRune(targetPath, 0) {
		return errors.New("file path must be absolute and contain no NUL")
	}
	// 不清理 link/..，让目标按真实软链语义解析路径。
	data, _, err := c.ReadResource(ctx, "/", strings.TrimPrefix(targetPath, "/"), files.MaxSize)
	if err != nil {
		return err
	}
	_, err = files.WriteFileCoordinated(ctx, localPath, expected, func(w io.Writer) error {
		_, err := w.Write(data)
		return err
	})
	return err
}

func (c *Client) MakeDirectory(ctx context.Context, targetPath string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if c.Desktop == nil {
		return rpc.Errorf(rpc.CodeReadOnly, "desktop identity required for file saves")
	}
	if !filepath.IsAbs(targetPath) || strings.ContainsRune(targetPath, 0) {
		return errors.New("file path must be absolute and contain no NUL")
	}
	return c.Call(ctx, "files.mkdir", rpc.FileMkdirParams{Path: targetPath}, nil)
}

func skipTransferName(name string) bool {
	return name == ".git" || name == ".DS_Store"
}

// UploadDirectory 递归上传本地普通文件夹到目标路径，跳过 .git 和 .DS_Store，拒绝符号链接与特殊文件。
func (c *Client) UploadDirectory(ctx context.Context, localDir, targetDir string, replaceExisting bool) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if c.Desktop == nil {
		return rpc.Errorf(rpc.CodeReadOnly, "desktop identity required for file saves")
	}
	if !filepath.IsAbs(targetDir) || strings.ContainsRune(targetDir, 0) {
		return errors.New("file path must be absolute and contain no NUL")
	}
	rootInfo, err := os.Stat(localDir)
	if err != nil {
		return err
	}
	if !rootInfo.IsDir() {
		return errors.New("not a directory")
	}
	type filePair struct {
		src, dst string
	}
	var dirs []string
	var fileList []filePair
	var totalBytes int64
	items := 0
	var walk func(srcDir, dstDir string, depth int) error
	walk = func(srcDir, dstDir string, depth int) error {
		if err := ctx.Err(); err != nil {
			return err
		}
		if depth > DirectoryDepthLimit {
			return fmt.Errorf("directory exceeds the maximum depth of %d", DirectoryDepthLimit)
		}
		entries, err := os.ReadDir(srcDir)
		if err != nil {
			return err
		}
		for _, d := range entries {
			name := d.Name()
			if skipTransferName(name) {
				continue
			}
			items++
			if items > DirectoryItemLimit {
				return fmt.Errorf("directory exceeds the maximum of %d items", DirectoryItemLimit)
			}
			srcPath := filepath.Join(srcDir, name)
			dstPath := filepath.Join(dstDir, name)
			mode := d.Type()
			if mode&os.ModeSymlink != 0 {
				return errors.New("symbolic links and special files cannot be transferred in a directory")
			}
			if d.IsDir() {
				dirs = append(dirs, dstPath)
				if err := walk(srcPath, dstPath, depth+1); err != nil {
					return err
				}
				continue
			}
			if !mode.IsRegular() {
				return errors.New("symbolic links and special files cannot be transferred in a directory")
			}
			info, err := d.Info()
			if err != nil {
				return err
			}
			if !info.Mode().IsRegular() {
				return errors.New("symbolic links and special files cannot be transferred in a directory")
			}
			if info.Size() > files.MaxSize {
				return fmt.Errorf("file exceeds the maximum size of %d bytes", files.MaxSize)
			}
			totalBytes += info.Size()
			if totalBytes > DirectoryBytesLimit {
				return fmt.Errorf("directory exceeds the maximum size of %d bytes", DirectoryBytesLimit)
			}
			fileList = append(fileList, filePair{src: srcPath, dst: dstPath})
		}
		return nil
	}
	if err := walk(localDir, targetDir, 1); err != nil {
		return err
	}
	if !replaceExisting {
		stats, err := c.FileStats(ctx, []string{targetDir})
		if err != nil {
			return err
		}
		if len(stats) != 1 || stats[0].Path != targetDir {
			return errors.New("invalid file status response")
		}
		if stats[0].Error != nil {
			return fileReadError(targetDir, stats[0].Error)
		}
		if !stats[0].Stamp.Missing {
			return files.ErrSaveConflict
		}
	}
	if err := c.MakeDirectory(ctx, targetDir); err != nil {
		return err
	}
	for _, dir := range dirs {
		if err := c.MakeDirectory(ctx, dir); err != nil {
			return err
		}
	}
	for _, f := range fileList {
		ver, err := c.FileVersion(ctx, f.dst)
		if err != nil {
			return err
		}
		if !replaceExisting && !ver.Stamp.Missing {
			return files.ErrSaveConflict
		}
		if _, err := c.UploadFile(ctx, f.src, f.dst, ver); err != nil {
			return err
		}
	}
	return ctx.Err()
}

// DownloadDirectory 递归下载目标文件夹到本机路径，查看身份可用；文件读取限制在 targetDir 根内并逐个校验摘要。
func (c *Client) DownloadDirectory(ctx context.Context, targetDir, localDir string, replaceExisting bool) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if !filepath.IsAbs(targetDir) || strings.ContainsRune(targetDir, 0) || localDir == "" || strings.ContainsRune(localDir, 0) {
		return errors.New("file path must be absolute and contain no NUL")
	}
	stats, err := c.FileStats(ctx, []string{targetDir})
	if err != nil {
		return err
	}
	if len(stats) != 1 || stats[0].Path != targetDir {
		return errors.New("invalid file status response")
	}
	if stats[0].Error != nil {
		return fileReadError(targetDir, stats[0].Error)
	}
	if stats[0].Stamp.Missing {
		return &os.PathError{Op: "read", Path: targetDir, Err: os.ErrNotExist}
	}
	if !stats[0].Stamp.Mode.IsDir() {
		return errors.New("not a directory")
	}
	if linfo, err := os.Lstat(localDir); err == nil {
		if !replaceExisting || !linfo.IsDir() {
			return files.ErrSaveConflict
		}
	} else if !os.IsNotExist(err) {
		return err
	}

	type dirItem struct {
		rel   string
		depth int
	}
	var localDirs []string
	var relFiles []string
	queue := []dirItem{{rel: "", depth: 1}}
	items := 0
	for len(queue) > 0 {
		if err := ctx.Err(); err != nil {
			return err
		}
		batchN := min(len(queue), rpc.FileBatchLimit)
		batch := queue[:batchN]
		queue = queue[batchN:]
		paths := make([]string, len(batch))
		for i, it := range batch {
			if it.rel == "" {
				paths[i] = targetDir
			} else {
				paths[i] = filepath.Join(targetDir, filepath.FromSlash(it.rel))
			}
		}
		res, err := c.ReadDirectories(ctx, paths)
		if err != nil {
			return err
		}
		if len(res) != len(paths) {
			return errors.New("invalid directory response")
		}
		for i, r := range res {
			if r.Path != paths[i] {
				return errors.New("invalid directory response")
			}
			if r.Error != nil {
				return fileReadError(r.Path, r.Error)
			}
			parent := batch[i]
			for _, e := range r.Entries {
				if skipTransferName(e.Name) {
					continue
				}
				if e.Name == "" || e.Name == "." || e.Name == ".." || strings.ContainsAny(e.Name, "/\\\x00") {
					return errors.New("invalid directory entry")
				}
				if e.Link != "" {
					return errors.New("symbolic links and special files cannot be transferred in a directory")
				}
				items++
				if items > DirectoryItemLimit {
					return fmt.Errorf("directory exceeds the maximum of %d items", DirectoryItemLimit)
				}
				rel := e.Name
				if parent.rel != "" {
					rel = parent.rel + "/" + e.Name
				}
				if e.Dir {
					if parent.depth+1 > DirectoryDepthLimit {
						return fmt.Errorf("directory exceeds the maximum depth of %d", DirectoryDepthLimit)
					}
					localDirs = append(localDirs, filepath.Join(localDir, filepath.FromSlash(rel)))
					queue = append(queue, dirItem{rel: rel, depth: parent.depth + 1})
				} else {
					relFiles = append(relFiles, rel)
				}
			}
		}
	}

	if err := files.MakeDir(ctx, localDir); err != nil {
		return err
	}
	for _, dir := range localDirs {
		if err := files.MakeDir(ctx, dir); err != nil {
			return err
		}
	}
	var totalBytes int64
	for _, rel := range relFiles {
		remaining := DirectoryBytesLimit - totalBytes
		if remaining <= 0 {
			return fmt.Errorf("directory exceeds the maximum size of %d bytes", DirectoryBytesLimit)
		}
		limit := min(int64(files.MaxSize), remaining)
		data, _, err := c.ReadResource(ctx, targetDir, rel, limit)
		if err != nil {
			return err
		}
		totalBytes += int64(len(data))
		if totalBytes > DirectoryBytesLimit {
			return fmt.Errorf("directory exceeds the maximum size of %d bytes", DirectoryBytesLimit)
		}
		dstPath := filepath.Join(localDir, filepath.FromSlash(rel))
		base, err := files.ReadVersion(ctx, dstPath)
		if err != nil {
			return err
		}
		if !replaceExisting && !base.Stamp.Missing {
			return files.ErrSaveConflict
		}
		if _, err := files.WriteFileCoordinated(ctx, dstPath, base, func(w io.Writer) error {
			_, err := w.Write(data)
			return err
		}); err != nil {
			return err
		}
	}
	return ctx.Err()
}
