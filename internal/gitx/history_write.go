package gitx

import (
	"context"
	"errors"
	"slices"
	"strings"
)

func RemotesContainingChecked(ctx context.Context, dir, oid string) ([]string, error) {
	out, err := run(ctx, dir, "branch", "-r", "--contains", oid, "--format=%(refname)")
	if err != nil {
		return nil, err
	}
	names := []string{}
	for _, line := range strings.Split(out, "\n") {
		line = strings.TrimSpace(line)
		if line != "" && !strings.HasSuffix(line, "/HEAD") {
			names = append(names, strings.TrimPrefix(line, "refs/remotes/"))
		}
	}
	slices.Sort(names)
	return slices.Compact(names), nil
}

func FullMessageChecked(ctx context.Context, dir, oid string) (string, error) {
	out, err := runRaw(ctx, dir, nil, nil, "log", "-1", "--format=%B", oid)
	return strings.TrimRight(string(out), "\n"), err
}

// squash 要保留父提交在待办中，因此起点是父提交的父提交。
func HistoryBase(ctx context.Context, dir, oid, action string) (string, string, error) {
	if action == "reset" {
		return "", oid, nil
	}
	if action == "squash" {
		parents, err := run(ctx, dir, "rev-list", "--parents", "-n", "1", oid)
		if err != nil {
			return "", "", err
		}
		switch len(strings.Fields(parents)) {
		case 1:
			return "", "", errors.New("this is the first commit; there is no earlier commit to squash into")
		case 2:
		default:
			return "", "", ErrRewriteMerges
		}
	}
	base, err := rewriteBase(ctx, dir, oid)
	if err != nil {
		return "", "", err
	}
	if action != "squash" {
		return base, oid, nil
	}
	if base == "" {
		return "", "", errors.New("this is the first commit; there is no earlier commit to squash into")
	}
	parent := base
	base, err = rewriteBase(ctx, dir, parent)
	return base, parent, err
}

// 十分钟、净化输出与普通提交相同；只有写命令记日志，说明与只读查询不进入结果。
func HistoryBounded(ctx context.Context, dir, action, oid, head, mode, base, message string, started func()) (bool, error) {
	if action == "reset" {
		started()
		return IntegrateBounded(ctx, dir, []string{"reset", "--" + mode, oid}, "", false)
	}
	if action == "reword" && oid == head {
		started()
		return CommitBounded(ctx, dir, []string{"commit", "--amend", "--only", "-F", "-"}, message)
	}
	readCtx := WithTrace(ctx, nil)
	before, _ := stashTop(readCtx, dir)
	policy := &fetchOutputPolicy{}
	ctx = context.WithValue(context.WithValue(ctx, fetchOutputKey{}, policy), commitOutputKey{}, true)
	err := rebaseTodoAt(ctx, dir, base, map[string]string{oid: action}, message, true, started)
	if err == nil {
		err = autostashLeft(readCtx, dir, before)
	}
	return policy.truncated, err
}
