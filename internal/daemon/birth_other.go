//go:build !darwin

package daemon

import (
	"os"
	"time"
)

func birthTime(fi os.FileInfo) time.Time { return fi.ModTime() }
