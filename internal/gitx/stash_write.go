package gitx

import (
	"context"
	"strings"
)

// StashRefOID 在仓库锁内核对刚列出的 reflog，不将可变位置存进请求。
func StashRefOID(ctx context.Context, dir, ref string) (string, error) {
	raw, err := runRaw(ctx, dir, nil, nil, "rev-parse", "--verify", "--end-of-options", ref+"^{commit}")
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(raw)), nil
}
