package gitx

import (
	"context"
	"strings"
)

type commitOutputKey struct{}

// 提交与钩子的输出使用同一预算；说明经 stdin，不放进命令参数。
func CommitBounded(ctx context.Context, dir string, args []string, message string) (bool, error) {
	policy := &fetchOutputPolicy{}
	ctx = context.WithValue(context.WithValue(ctx, fetchOutputKey{}, policy), commitOutputKey{}, true)
	var stdin []byte
	if len(args) > 0 && args[0] == "commit" {
		stdin = []byte(message)
	}
	_, err := runRaw(ctx, dir, stdin, nil, args...)
	return policy.truncated, err
}

func CommitChangeBounded(ctx context.Context, dir, path string, content []byte, message string, started func()) (bool, error) {
	policy := &fetchOutputPolicy{}
	ctx = context.WithValue(context.WithValue(ctx, fetchOutputKey{}, policy), commitOutputKey{}, true)
	err := commitChange(ctx, dir, path, content, message, started)
	return policy.truncated, err
}

func LastMessageChecked(ctx context.Context, dir string) (string, error) {
	out, err := runRaw(ctx, dir, nil, nil, "log", "-1", "--format=%B")
	return strings.TrimRight(string(out), "\n"), err
}
