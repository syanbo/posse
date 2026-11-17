package gitx

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
)

// CommonDirChecked 只把明确的非仓库当作空身份，不把取消、权限及损坏的 gitdir 当作非仓库。
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
		return "", err
	}
	return filepath.EvalSymlinks(strings.TrimSuffix(string(out), "\n"))
}

func notRepository(message string) bool {
	return strings.HasPrefix(message, "fatal: not a git repository (or any of the parent directories): .git") ||
		strings.HasPrefix(message, "fatal: not a git repository (or any parent up to mount point ")
}
