package gitx

import (
	"context"
	"errors"
)

// 进行中的操作（rebase、merge、cherry-pick、revert）：继续、跳过、中止，以及冲突文件采用一方。

// Continue 继续进行中的操作；需要提交信息时沿用 git 准备好的那份，不打开编辑器。
func Continue(ctx context.Context, dir, op string) error {
	if op == "" {
		return errors.New("没有进行中的操作")
	}
	_, err := runRaw(ctx, dir, nil, []string{"GIT_EDITOR=true"}, op, "--continue")
	return err
}

// Skip 跳过当前这个提交（只有 rebase 和 cherry-pick 能跳过）。
func Skip(ctx context.Context, dir, op string) error {
	if op != "rebase" && op != "cherry-pick" {
		return errors.New(op + " 不能跳过")
	}
	_, err := runRaw(ctx, dir, nil, []string{"GIT_EDITOR=true"}, op, "--skip")
	return err
}

// Abort 中止进行中的操作，回到开始之前的样子。
func Abort(ctx context.Context, dir, op string) error {
	if op == "" {
		return errors.New("没有进行中的操作")
	}
	_, err := runRaw(ctx, dir, nil, nil, op, "--abort")
	return err
}

// TakeSide 让冲突文件整个采用一方并标记为已解决：ours 为真时是 --ours，否则 --theirs。
// 注意 rebase 时两边是反的：ours 是要 rebase 到的分支，theirs 是正在重放的你的提交。
func TakeSide(ctx context.Context, dir, path string, ours bool) error {
	side := "--theirs"
	if ours {
		side = "--ours"
	}
	if _, err := runRaw(ctx, dir, nil, nil, "checkout", side, "--", literal(path)[0]); err != nil {
		return err
	}
	_, err := runRaw(ctx, dir, nil, nil, "add", "--", literal(path)[0])
	return err
}
