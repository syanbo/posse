package client

import (
	"context"
	"encoding/json"
	"slices"

	"posse/internal/rpc"
)

type GitPushOperation struct {
	operation *DesktopOperation
	target    rpc.GitPushParams
}

type GitPushStatus struct {
	Operation rpc.DesktopOperation
	// Result 只在原操作完成且返回 typed Push 结果时非空；unknown 不表示未执行。
	Result *rpc.GitPushResult
}

func (c *Client) PrepareGitPush(p rpc.GitPushParams) (*GitPushOperation, error) {
	if !rpc.ValidGitPushParams(p) {
		return nil, rpc.Errorf(rpc.CodeBadRequest, "invalid Git push parameters")
	}
	op, err := c.PrepareDesktopOperation("git.push", p)
	if err != nil {
		return nil, err
	}
	return &GitPushOperation{operation: op, target: p}, nil
}

func (op *GitPushOperation) Ref() rpc.DesktopOperationRef {
	if op == nil {
		return rpc.DesktopOperationRef{}
	}
	return op.operation.Ref()
}

func (op *GitPushOperation) Execute(ctx context.Context) (rpc.GitPushResult, error) {
	var out rpc.GitPushResult
	if op == nil || op.operation == nil {
		return out, rpc.Errorf(rpc.CodeBadRequest, "invalid desktop operation reference")
	}
	if err := op.operation.Execute(ctx, &out); err != nil {
		return rpc.GitPushResult{}, err
	}
	if !validGitPushResult(out, op.target) {
		err := rpc.Errorf(rpc.CodeOutcomeUnknown, "invalid Git push result")
		err.Data, _ = json.Marshal(op.Ref())
		return rpc.GitPushResult{}, err
	}
	return out, nil
}

func (c *Client) GitPushStatus(ctx context.Context, ref rpc.DesktopOperationRef, target rpc.GitPushParams) (GitPushStatus, error) {
	if !rpc.ValidGitPushParams(target) {
		return GitPushStatus{}, rpc.Errorf(rpc.CodeBadRequest, "invalid Git push parameters")
	}
	op, err := c.DesktopOperationStatus(ctx, ref)
	if err != nil {
		return GitPushStatus{}, err
	}
	out := GitPushStatus{Operation: op}
	if op.State == "unknown" {
		return out, nil
	}
	if op.Method != "git.push" {
		return GitPushStatus{}, rpc.Errorf(rpc.CodeInternal, "invalid Git push result")
	}
	if op.State != "done" || op.Error != nil {
		return out, nil
	}
	var result rpc.GitPushResult
	if len(op.Result) > rpc.GitPushResultLimit || json.Unmarshal(op.Result, &result) != nil || !validGitPushResult(result, target) {
		return GitPushStatus{}, rpc.Errorf(rpc.CodeInternal, "invalid Git push result")
	}
	out.Result = &result
	return out, nil
}

func validGitPushResult(out rpc.GitPushResult, target rpc.GitPushParams) bool {
	if out.Target != target || out.Rejected && (!out.Started || out.Status != rpc.GitFetchFailed) {
		return false
	}
	common := rpc.GitFetchResult{Target: target.GitFetchParams, Status: out.Status, Started: out.Started, Error: out.Error, Logs: out.Logs, LogsTruncated: out.LogsTruncated}
	if !validGitWriteResult(common, target.GitFetchParams, "push", func(args []string) bool {
		expected := []string{"push"}
		if target.Force {
			expected = append(expected, "--force-with-lease", "--force-if-includes")
		}
		// 是否设置上游由目标端重新核对过的计划决定。
		if len(args) > len(expected) && args[len(expected)] == "--set-upstream" {
			expected = append(expected, "--set-upstream")
		}
		if target.Tags {
			expected = append(expected, "--follow-tags")
		}
		expected = append(expected, target.Remote, "refs/heads/"+target.Branch+":refs/heads/"+target.RemoteBranch)
		return slices.Equal(args, expected)
	}) {
		return false
	}
	raw, err := json.Marshal(out)
	return err == nil && len(raw) <= rpc.GitPushResultLimit
}
