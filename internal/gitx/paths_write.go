package gitx

import "context"

// PathsBounded 使用写操作的诊断预算；调用方已经在协调锁内检查状态和文件版本。
func PathsBounded(ctx context.Context, dir string, args []string) (bool, error) {
	policy := &fetchOutputPolicy{}
	_, err := runRaw(context.WithValue(ctx, fetchOutputKey{}, policy), dir, nil, nil, args...)
	return policy.truncated, err
}
