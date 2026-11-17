package files

import (
	"context"
	"io"
	"runtime"
	"strings"

	"golang.org/x/text/unicode/norm"

	"posse/internal/writecoord"
)

var localWrites writecoord.Coordinator

func localWriteScope(path string) writecoord.Scope {
	if runtime.GOOS == "darwin" {
		// 与目标执行器一致：默认 APFS 忽略大小写与 Unicode 组合形式。
		path = strings.ToLower(norm.NFC.String(path))
	}
	return writecoord.Scope{Paths: []string{path}}
}

// WriteFileCoordinated 让同进程的本机保存和下载共用条件写入锁；不替代目标执行器的资源锁。
// expected.Path 已由 ReadVersion 解析，持锁后的路径与版本复查仍交给 WriteFileChecked。
func WriteFileCoordinated(ctx context.Context, path string, expected Version, write func(io.Writer) error) (Version, error) {
	release, err := localWrites.Acquire(ctx, localWriteScope(expected.Path))
	if err != nil {
		return Version{}, err
	}
	defer release()
	return WriteFileChecked(ctx, path, expected, write)
}
