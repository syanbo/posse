package trash

import (
	"errors"
	"path/filepath"
	"strings"
)

func Move(path string) (destination string, err error) {
	if strings.IndexByte(path, 0) >= 0 {
		return "", errors.New("trash: path contains NUL")
	}
	if !filepath.IsAbs(path) {
		return "", errors.New("trash: path must be absolute")
	}
	return move(path)
}
