package daemon

import (
	"os"
	"syscall"
	"time"
)

// birthTime 是文件的创建时间（APFS 记着），拿不到时退回修改时间。
func birthTime(fi os.FileInfo) time.Time {
	if st, ok := fi.Sys().(*syscall.Stat_t); ok {
		return time.Unix(st.Birthtimespec.Sec, st.Birthtimespec.Nsec)
	}
	return fi.ModTime()
}
