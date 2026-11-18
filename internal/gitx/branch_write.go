package gitx

import (
	"context"
	"errors"
	"strings"
)

var ErrBranchChanged = errors.New("Git branch inputs changed; refresh the branches")

// ResolveBranchCommit 在锁内把输入固定成一个提交，不把可变引用传给 switch。
func ResolveBranchCommit(ctx context.Context, dir, revision string) (string, error) {
	if !ValidReadRevision(revision) {
		return "", ErrBranchChanged
	}
	raw, err := runRaw(ctx, dir, nil, nil, "rev-parse", "--verify", "--end-of-options", revision+"^{commit}")
	if err != nil {
		return "", err
	}
	oid := strings.TrimSpace(string(raw))
	if !ValidBlobOID(oid) || strings.Trim(oid, "0") == "" {
		return "", ErrBranchChanged
	}
	return oid, nil
}

// BranchBounded 只执行桌面分支操作已经在仓库锁内生成的命令，诊断沿用有界写输出。
func BranchBounded(ctx context.Context, dir string, args []string) (bool, error) {
	if ctx.Value(readLimitKey{}) != nil {
		return false, errors.New("Git branch requires a write context")
	}
	policy := &fetchOutputPolicy{}
	_, err := runRaw(context.WithValue(ctx, fetchOutputKey{}, policy), dir, nil, nil, args...)
	return policy.truncated, err
}

// BranchOccupied 逐项检查，不能只看 for-each-ref 的一个 worktreepath：外部工具可能强制重复检出。
// except 只在检出时放行当前仓库，删除时为空，任何检出都阻止删除。
func BranchOccupied(ctx context.Context, dir, name, except string) (bool, error) {
	raw, err := runRaw(ctx, dir, nil, nil, "worktree", "list", "--porcelain", "-z")
	if err != nil {
		return false, err
	}
	path := ""
	for _, field := range strings.Split(string(raw), "\x00") {
		if value, ok := strings.CutPrefix(field, "worktree "); ok {
			path = value
		} else if field == "branch refs/heads/"+name && path != except {
			return true, nil
		} else if field == "" {
			path = ""
		}
	}
	return false, nil
}
