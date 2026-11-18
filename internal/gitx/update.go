package gitx

import (
	"context"
	"errors"
)

var ErrUpdateChanged = errors.New("Git update inputs changed; refresh the branches")

// UpdateBounded 仅对写命令净化诊断；储藏哈希和冲突检查保留原始结构化内容。
func UpdateBounded(ctx context.Context, dir string, b Branch) (bool, error) {
	if ctx.Value(readLimitKey{}) != nil {
		return false, errors.New("Git update requires a write context")
	}
	policy := &fetchOutputPolicy{}
	writeCtx := context.WithValue(ctx, fetchOutputKey{}, policy)
	readCtx := WithReadLimit(WithTrace(ctx, nil), 8<<20)
	err := update(readCtx, dir, b, func(args ...string) error {
		_, err := runRaw(writeCtx, dir, nil, nil, args...)
		return err
	})
	return policy.truncated, err
}
