package client

import (
	"context"
	"encoding/json"
	"slices"

	"posse/internal/rpc"
)

type GitBranchOperation struct {
	operation *DesktopOperation
	target    rpc.GitBranchParams
}

type GitBranchStatus struct {
	Operation rpc.DesktopOperation
	// Result 只在原操作完成且返回 typed Branch 结果时非空；unknown 不表示未执行。
	Result *rpc.GitBranchResult
}

func (c *Client) PrepareGitBranch(p rpc.GitBranchParams) (*GitBranchOperation, error) {
	if !rpc.ValidGitBranchParams(p) {
		return nil, rpc.Errorf(rpc.CodeBadRequest, "invalid Git branch parameters")
	}
	op, err := c.PrepareDesktopOperation("git.branch", p)
	if err != nil {
		return nil, err
	}
	return &GitBranchOperation{operation: op, target: p}, nil
}

func (op *GitBranchOperation) Ref() rpc.DesktopOperationRef {
	if op == nil {
		return rpc.DesktopOperationRef{}
	}
	return op.operation.Ref()
}

func (op *GitBranchOperation) Execute(ctx context.Context) (rpc.GitBranchResult, error) {
	var out rpc.GitBranchResult
	if op == nil || op.operation == nil {
		return out, rpc.Errorf(rpc.CodeBadRequest, "invalid desktop operation reference")
	}
	if err := op.operation.Execute(ctx, &out); err != nil {
		return rpc.GitBranchResult{}, err
	}
	if !validGitBranchResult(out, op.target) {
		err := rpc.Errorf(rpc.CodeOutcomeUnknown, "invalid Git branch result")
		err.Data, _ = json.Marshal(op.Ref())
		return rpc.GitBranchResult{}, err
	}
	return out, nil
}

func (c *Client) GitBranchStatus(ctx context.Context, ref rpc.DesktopOperationRef, target rpc.GitBranchParams) (GitBranchStatus, error) {
	if !rpc.ValidGitBranchParams(target) {
		return GitBranchStatus{}, rpc.Errorf(rpc.CodeBadRequest, "invalid Git branch parameters")
	}
	op, err := c.DesktopOperationStatus(ctx, ref)
	if err != nil {
		return GitBranchStatus{}, err
	}
	out := GitBranchStatus{Operation: op}
	if op.State == "unknown" {
		return out, nil
	}
	if op.Method != "git.branch" {
		return GitBranchStatus{}, rpc.Errorf(rpc.CodeInternal, "invalid Git branch result")
	}
	if op.State != "done" || op.Error != nil {
		return out, nil
	}
	var result rpc.GitBranchResult
	if len(op.Result) > rpc.GitBranchResultLimit || json.Unmarshal(op.Result, &result) != nil || !validGitBranchResult(result, target) {
		return GitBranchStatus{}, rpc.Errorf(rpc.CodeInternal, "invalid Git branch result")
	}
	out.Result = &result
	return out, nil
}

func validGitBranchResult(out rpc.GitBranchResult, target rpc.GitBranchParams) bool {
	if out.Target != target || !rpc.ValidGitBranchParams(target) {
		return false
	}
	common := rpc.GitFetchResult{Target: target.GitFetchParams, Status: out.Status, Started: out.Started, Error: out.Error, Logs: out.Logs, LogsTruncated: out.LogsTruncated}
	if out.Unmerged {
		if target.Action != rpc.GitBranchDelete || target.Remote || target.Force || out.Status != rpc.GitFetchSucceeded || out.Started || out.OID != "" || out.Error != nil {
			return false
		}
		common.Started, common.Status = true, rpc.GitFetchFailed
		common.Error = rpc.Errorf(rpc.CodeInternal, "branch not fully merged")
	} else if out.Status == rpc.GitFetchSucceeded {
		if !rpc.ValidGitBranchOID(out.OID) {
			return false
		}
		expected := target.BranchOID
		switch target.Action {
		case rpc.GitBranchCheckout:
			if target.Remote && target.LocalOID != "" {
				expected = target.LocalOID
			}
		case rpc.GitBranchCreate:
			expected = target.FromOID
		case rpc.GitBranchRevision:
			expected = target.RevisionOID
		}
		if expected != "" && out.OID != expected {
			return false
		}
		if !out.Started {
			return target.Action == rpc.GitBranchCheckout && (!target.Remote || target.LocalOID != "") && out.Error == nil && out.Logs != nil && len(out.Logs) == 0 && !out.LogsTruncated
		}
	} else if out.OID != "" {
		return false
	}
	if !validGitWriteResult(common, target.GitFetchParams, "branch", func(args []string) bool {
		oid := out.OID
		if target.Action == rpc.GitBranchRevision && oid == "" {
			if len(args) != 3 || !rpc.ValidGitBranchOID(args[2]) || target.RevisionOID != "" && args[2] != target.RevisionOID {
				return false
			}
			oid = args[2]
		}
		return slices.Equal(args, rpc.GitBranchArgs(target, oid))
	}) {
		return false
	}
	raw, err := json.Marshal(out)
	return err == nil && len(raw) <= rpc.GitBranchResultLimit
}
