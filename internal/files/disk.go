// Package files 是界面"文件"标签背后的纯逻辑：读写文件、目录树、文件清单、模糊匹配和全文检索。
// 不依赖界面，由窗口进程调用；常驻进程不用它。
package files

import (
	"bytes"
	"errors"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"syscall"
	"time"
)

const (
	// binaryProbe 是判断二进制时看的开头字节数：其中有 NUL 就算二进制。
	binaryProbe = 8 << 10
	// ReadOnlySize 以上的文本只读打开：编辑器只排版可见的行，但保存和检索都要整份过一遍。
	ReadOnlySize = 5 << 20
	// MaxSize 以上的文件不读进内存。
	MaxSize = 50 << 20
)

// Stamp 是文件在磁盘上的状态，用来发现别人（agent、编辑器）改过它。
// 带上 inode：写临时文件再改名的保存方式，大小和时间可能凑巧不变，inode 一定会变。
type Stamp struct {
	Size    int64
	ModTime time.Time
	Ino     uint64
	Mode    fs.FileMode
	Missing bool
}

// StatFile 取路径现在的磁盘戳；不存在时 Missing 为真，其它错误也按不存在处理。
func StatFile(path string) Stamp {
	fi, err := os.Stat(path)
	if err != nil {
		return Stamp{Missing: true}
	}
	return stampOf(fi)
}

func stampOf(fi fs.FileInfo) Stamp {
	s := Stamp{Size: fi.Size(), ModTime: fi.ModTime(), Mode: fi.Mode()}
	if st, ok := fi.Sys().(*syscall.Stat_t); ok {
		s.Ino = uint64(st.Ino)
	}
	return s
}

// Same 报告两个磁盘戳是不是同一个版本的文件。
func (s Stamp) Same(o Stamp) bool {
	if s.Missing || o.Missing {
		return s.Missing == o.Missing
	}
	return s.Size == o.Size && s.ModTime.Equal(o.ModTime) && s.Ino == o.Ino
}

// Kind 是打开的文件按什么方式显示。
type Kind uint8

const (
	Text Kind = iota
	Binary
	TooLarge
	Directory
)

// Content 是读出来的文件。
type Content struct {
	Kind     Kind
	Text     string
	ReadOnly bool // 文本太大，只读打开
	Stamp    Stamp
}

// ReadFile 读文件：二进制和超过 MaxSize 的只返回磁盘戳，不读内容。
func ReadFile(path string) (Content, error) {
	f, err := os.Open(path)
	if err != nil {
		return Content{}, err
	}
	defer f.Close()
	fi, err := f.Stat()
	if err != nil {
		return Content{}, err
	}
	c := Content{Stamp: stampOf(fi)}
	switch {
	case fi.IsDir():
		c.Kind = Directory
		return c, nil
	case fi.Size() > MaxSize:
		c.Kind = TooLarge
		return c, nil
	}
	b, err := io.ReadAll(io.LimitReader(f, MaxSize+1))
	if err != nil {
		return Content{}, err
	}
	if len(b) > MaxSize {
		c.Kind = TooLarge
		return c, nil
	}
	if IsBinary(b) {
		c.Kind = Binary
		return c, nil
	}
	c.Text, c.ReadOnly = string(b), len(b) > ReadOnlySize
	return c, nil
}

// IsBinary 看开头 binaryProbe 个字节里有没有 NUL。
func IsBinary(b []byte) bool {
	return bytes.IndexByte(b[:min(len(b), binaryProbe)], 0) >= 0
}

// WriteFile 原子地写文件：在同一目录写临时文件、设成原文件的权限位，再改名覆盖。
// 改名之前原文件一直完整，agent 不会读到写了一半或被清空的文件。路径是符号链接时写到它指向的文件。
// 返回写完后的磁盘戳，调用方据此认出这次写入，不当成别人的修改。
func WriteFile(path string, write func(io.Writer) error) (Stamp, error) {
	if real, err := filepath.EvalSymlinks(path); err == nil {
		path = real
	}
	mode := fs.FileMode(0o644)
	if fi, err := os.Stat(path); err == nil {
		if fi.IsDir() {
			return Stamp{}, errors.New("是一个目录")
		}
		mode = fi.Mode().Perm()
	}
	f, err := os.CreateTemp(filepath.Dir(path), "."+filepath.Base(path)+".posse-*")
	if err != nil {
		return Stamp{}, err
	}
	tmp := f.Name()
	ok := false
	defer func() {
		if !ok {
			f.Close()
			os.Remove(tmp)
		}
	}()
	if err := write(f); err != nil {
		return Stamp{}, err
	}
	if err := f.Chmod(mode); err != nil {
		return Stamp{}, err
	}
	if err := f.Close(); err != nil {
		return Stamp{}, err
	}
	if err := os.Rename(tmp, path); err != nil {
		return Stamp{}, err
	}
	ok = true
	return StatFile(path), nil
}
