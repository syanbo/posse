package files

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"syscall"
)

// EntryVersion 标识目录项本身；最终一段软链不解析，移到废纸篓只移动链接。
type EntryVersion struct {
	Path   string `json:"path"`
	Stamp  Stamp  `json:"stamp"`
	Device uint64 `json:"device"`
	Link   string `json:"link,omitempty"`
}

func (v EntryVersion) Same(other EntryVersion) bool {
	return v.Path == other.Path && v.Device == other.Device && v.Stamp.Same(other.Stamp) && v.Stamp.Mode == other.Stamp.Mode && v.Link == other.Link
}

var ErrEntryChanged = errors.New("file or directory changed; refresh before moving it to trash")

func ReadEntryVersion(ctx context.Context, path string) (EntryVersion, error) {
	if err := ctx.Err(); err != nil {
		return EntryVersion{}, err
	}
	if !filepath.IsAbs(path) || strings.ContainsRune(path, 0) {
		return EntryVersion{}, errors.New("file entry path must be absolute and contain no NUL")
	}
	// 先解析父目录再拼最后一段，保留软链/.. 的文件系统语义。
	parent, name := filepath.Split(strings.TrimRight(path, string(filepath.Separator)))
	if name == "" || name == "." || name == ".." {
		return EntryVersion{}, errors.New("file entry path must name a file or directory below the root")
	}
	parent, err := filepath.EvalSymlinks(parent)
	if err != nil {
		return EntryVersion{}, err
	}
	real := filepath.Join(parent, name)
	fi, err := os.Lstat(real)
	if err != nil {
		return EntryVersion{}, err
	}
	v := EntryVersion{Path: real, Stamp: stampOf(fi)}
	if st, ok := fi.Sys().(*syscall.Stat_t); ok {
		v.Device = uint64(st.Dev)
	}
	if fi.Mode()&os.ModeSymlink != 0 {
		v.Link, err = os.Readlink(real)
		if err != nil {
			return EntryVersion{}, err
		}
		after, err := os.Lstat(real)
		if err != nil {
			return EntryVersion{}, err
		}
		if !os.SameFile(fi, after) || !v.Stamp.Same(stampOf(after)) {
			return EntryVersion{}, ErrEntryChanged
		}
	}
	if err := ctx.Err(); err != nil {
		return EntryVersion{}, err
	}
	return v, nil
}

// 调用方在读取和移动之间持资源锁；外部程序仍可在最后检查后改变目录项。
func TrashEntryChecked(ctx context.Context, path string, expected EntryVersion, move func(string) (string, error)) (string, error) {
	current, err := ReadEntryVersion(ctx, path)
	if err != nil {
		return "", err
	}
	if !current.Same(expected) {
		return "", ErrEntryChanged
	}
	if err := ctx.Err(); err != nil {
		return "", err
	}
	return move(current.Path)
}
