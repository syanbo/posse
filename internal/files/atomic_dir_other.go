//go:build !darwin

package files

import (
	"io/fs"
	"os"
)

type atomicDir struct{ *os.Root }

func openAtomicDirectory(path string) (atomicDirectory, error) {
	r, err := os.OpenRoot(path)
	if err != nil {
		return nil, err
	}
	return &atomicDir{r}, nil
}

func (d *atomicDir) Stat() (fs.FileInfo, error) {
	return d.Root.Stat(".")
}

func (d *atomicDir) Create(name string) (*os.File, error) {
	return d.OpenFile(name, os.O_RDWR|os.O_CREATE|os.O_EXCL, 0o600)
}
