package gitx

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"unicode/utf8"
)

// MERGE_MSG 按原始字节交给 commit -F -，既保留第二父提交，也让提交钩子正常运行。
func MergeMessage(gitDir string, limit int64) (string, error) {
	f, err := os.Open(filepath.Join(gitDir, "MERGE_MSG"))
	if err != nil {
		return "", err
	}
	defer f.Close()
	raw, err := io.ReadAll(io.LimitReader(f, limit+1))
	if err != nil {
		return "", err
	}
	if len(bytes.TrimSpace(raw)) == 0 || int64(len(raw)) > limit || bytes.ContainsRune(raw, 0) || !utf8.Valid(raw) {
		return "", errors.New("invalid Git merge message")
	}
	return string(raw), nil
}

// 沿用整体提交的十分钟与净化输出策略；取消只停止命令，绝不补发 abort。
func IntegrateBounded(ctx context.Context, dir string, args []string, message string, noEditor bool) (bool, error) {
	if ctx.Value(readLimitKey{}) != nil {
		return false, errors.New("Git integration requires a write context")
	}
	if len(args) > 0 && args[0] == "commit" {
		return CommitBounded(ctx, dir, args, message)
	}
	policy := &fetchOutputPolicy{}
	ctx = context.WithValue(context.WithValue(ctx, fetchOutputKey{}, policy), commitOutputKey{}, true)
	var env []string
	if noEditor {
		env = []string{"GIT_EDITOR=true"}
	}
	_, err := runRaw(ctx, dir, nil, env, args...)
	return policy.truncated, err
}
