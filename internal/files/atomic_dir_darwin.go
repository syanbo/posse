package files

import (
	"os"
	"path/filepath"

	"golang.org/x/sys/unix"
)

type atomicDir struct{ *os.File }

func openAtomicDirectory(path string) (atomicDirectory, error) {
	// O_RDONLY 会额外要求能列目录；已知文件名的保存只需目录的写入与搜索权限。
	fd, err := unix.Open(path, unix.O_SEARCH|unix.O_CLOEXEC, 0)
	if err != nil {
		return nil, &os.PathError{Op: "open", Path: path, Err: err}
	}
	return &atomicDir{os.NewFile(uintptr(fd), path)}, nil
}

func (d *atomicDir) Create(name string) (*os.File, error) {
	path := filepath.Join(d.Name(), name)
	fd, err := unix.Openat(int(d.Fd()), name, unix.O_RDWR|unix.O_CREAT|unix.O_EXCL|unix.O_CLOEXEC, 0o600)
	if err != nil {
		return nil, &os.PathError{Op: "open", Path: path, Err: err}
	}
	return os.NewFile(uintptr(fd), path), nil
}

func (d *atomicDir) Remove(name string) error {
	if err := unix.Unlinkat(int(d.Fd()), name, 0); err != nil {
		return &os.PathError{Op: "remove", Path: filepath.Join(d.Name(), name), Err: err}
	}
	return nil
}

func (d *atomicDir) Rename(oldname, newname string) error {
	if err := unix.Renameat(int(d.Fd()), oldname, int(d.Fd()), newname); err != nil {
		return &os.LinkError{Op: "rename", Old: filepath.Join(d.Name(), oldname), New: filepath.Join(d.Name(), newname), Err: err}
	}
	return nil
}
