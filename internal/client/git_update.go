package client

import (
	"context"
	"encoding/json"
	"slices"
	"strings"

	"posse/internal/gitx"
	"posse/internal/rpc"
)

type GitUpdateOperation struct {
	operation *DesktopOperation
	target    rpc.GitUpdateParams
}

type GitUpdateStatus struct {
	Operation rpc.DesktopOperation
	// Result 只在原操作完成且返回 typed Update 结果时非空；unknown 不表示未执行。
	Result *rpc.GitUpdateResult
}

func (c *Client) PrepareGitUpdate(p rpc.GitUpdateParams) (*GitUpdateOperation, error) {
	if !rpc.ValidGitUpdateParams(p) {
		return nil, rpc.Errorf(rpc.CodeBadRequest, "invalid Git update parameters")
	}
	op, err := c.PrepareDesktopOperation("git.update", p)
	if err != nil {
		return nil, err
	}
	return &GitUpdateOperation{operation: op, target: p}, nil
}

func (op *GitUpdateOperation) Ref() rpc.DesktopOperationRef {
	if op == nil {
		return rpc.DesktopOperationRef{}
	}
	return op.operation.Ref()
}

func (op *GitUpdateOperation) Execute(ctx context.Context) (rpc.GitUpdateResult, error) {
	var out rpc.GitUpdateResult
	if op == nil || op.operation == nil {
		return out, rpc.Errorf(rpc.CodeBadRequest, "invalid desktop operation reference")
	}
	if err := op.operation.Execute(ctx, &out); err != nil {
		return rpc.GitUpdateResult{}, err
	}
	if !validGitUpdateResult(out, op.target) {
		err := rpc.Errorf(rpc.CodeOutcomeUnknown, "invalid Git update result")
		err.Data, _ = json.Marshal(op.Ref())
		return rpc.GitUpdateResult{}, err
	}
	return out, nil
}

func (c *Client) GitUpdateStatus(ctx context.Context, ref rpc.DesktopOperationRef, target rpc.GitUpdateParams) (GitUpdateStatus, error) {
	if !rpc.ValidGitUpdateParams(target) {
		return GitUpdateStatus{}, rpc.Errorf(rpc.CodeBadRequest, "invalid Git update parameters")
	}
	op, err := c.DesktopOperationStatus(ctx, ref)
	if err != nil {
		return GitUpdateStatus{}, err
	}
	out := GitUpdateStatus{Operation: op}
	if op.State == "unknown" {
		return out, nil
	}
	if op.Method != "git.update" {
		return GitUpdateStatus{}, rpc.Errorf(rpc.CodeInternal, "invalid Git update result")
	}
	if op.State != "done" || op.Error != nil {
		return out, nil
	}
	var result rpc.GitUpdateResult
	if len(op.Result) > rpc.GitUpdateResultLimit || json.Unmarshal(op.Result, &result) != nil || !validGitUpdateResult(result, target) {
		return GitUpdateStatus{}, rpc.Errorf(rpc.CodeInternal, "invalid Git update result")
	}
	out.Result = &result
	return out, nil
}

func validGitUpdateResult(out rpc.GitUpdateResult, target rpc.GitUpdateParams) bool {
	if out.Target != target || out.Incoming < 0 || out.Status != rpc.GitFetchSucceeded && (out.Incoming != 0 || out.Autostash != nil) {
		return false
	}
	if left := out.Autostash; left != nil && (!target.Current || !gitx.ValidBlobOID(left.Stash) || len(left.Stash) != len(target.BranchOID) || strings.Trim(left.Stash, "0") == "") {
		return false
	}
	common := rpc.GitFetchResult{Target: target.GitFetchParams, Status: out.Status, Started: out.Started, Error: out.Error, Logs: out.Logs, LogsTruncated: out.LogsTruncated}
	if !validGitWriteResult(common, target.GitFetchParams, "update", func(args []string) bool {
		expected := []string{"pull", "--rebase", "--autostash"}
		if !target.Current {
			remote, branch, _ := strings.Cut(target.Upstream, "/")
			if strings.HasPrefix(branch, "+") {
				branch = "refs/heads/" + branch
			}
			expected = []string{"fetch", remote, branch + ":refs/heads/" + target.Branch}
		}
		return slices.Equal(args, expected)
	}) {
		return false
	}
	raw, err := json.Marshal(out)
	return err == nil && len(raw) <= rpc.GitUpdateResultLimit
}
