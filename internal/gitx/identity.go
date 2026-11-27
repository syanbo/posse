package gitx

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// CommonDirChecked 只把明确的非仓库当作空身份，不把取消、权限及损坏的 gitdir 当作非仓库；
// 目标机未安装 git 且沿途不存在 .git 时同样视作非仓库，避免阻断新建文件夹与保存普通文件。
func CommonDirChecked(ctx context.Context, dir string) (string, error) {
	out, err := runRaw(ctx, dir, nil, nil, "rev-parse", "--path-format=absolute", "--git-common-dir")
	if ctx.Err() != nil {
		return "", ctx.Err()
	}
	if err != nil {
		var ge *Error
		if errors.As(err, &ge) && notRepository(ge.Msg) {
			return "", nil
		}
		if errors.Is(err, exec.ErrNotFound) && !hasGitEntryInAncestors(dir) {
			return "", nil
		}
		return "", err
	}
	return filepath.EvalSymlinks(strings.TrimSuffix(string(out), "\n"))
}

func hasGitEntryInAncestors(dir string) bool {
	cur := filepath.Clean(dir)
	for {
		if _, err := os.Lstat(filepath.Join(cur, ".git")); err == nil {
			return true
		}
		parent := filepath.Dir(cur)
		if parent == cur {
			return false
		}
		cur = parent
	}
}

func notRepository(message string) bool {
	return strings.HasPrefix(message, "fatal: not a git repository (or any of the parent directories): .git") ||
		strings.HasPrefix(message, "fatal: not a git repository (or any parent up to mount point ")
}
