package client

import (
	"context"
	"encoding/json"
	"reflect"
	"slices"
	"strings"

	"posse/internal/rpc"
)

type GitCommitOperation struct {
	operation *DesktopOperation
	target    rpc.GitCommitParams
}

type GitCommitStatus struct {
	Operation rpc.DesktopOperation
	// Result 只在原操作完成且返回 typed Commit 结果时非空；unknown 不表示未执行。
	Result *rpc.GitCommitResult
}

func (c *Client) PrepareGitCommit(p rpc.GitCommitParams) (*GitCommitOperation, error) {
	if !rpc.ValidGitCommitParams(p) {
		return nil, rpc.Errorf(rpc.CodeBadRequest, "invalid Git commit parameters")
	}
	raw, _ := json.Marshal(p)
	var owned rpc.GitCommitParams
	if err := json.Unmarshal(raw, &owned); err != nil {
		return nil, err
	}
	p = owned
	op, err := c.PrepareDesktopOperation("git.commit", p)
	if err != nil {
		return nil, err
	}
	return &GitCommitOperation{operation: op, target: p.Metadata()}, nil
}

func (op *GitCommitOperation) Ref() rpc.DesktopOperationRef {
	if op == nil {
		return rpc.DesktopOperationRef{}
	}
	return op.operation.Ref()
}

func (op *GitCommitOperation) Execute(ctx context.Context) (rpc.GitCommitResult, error) {
	var out rpc.GitCommitResult
	if op == nil || op.operation == nil {
		return out, rpc.Errorf(rpc.CodeBadRequest, "invalid desktop operation reference")
	}
	if err := op.operation.Execute(ctx, &out); err != nil {
		return rpc.GitCommitResult{}, err
	}
	if !validGitCommitResult(out, op.target) {
		err := rpc.Errorf(rpc.CodeOutcomeUnknown, "invalid Git commit result")
		err.Data, _ = json.Marshal(op.Ref())
		return rpc.GitCommitResult{}, err
	}
	return out, nil
}

func (c *Client) GitCommitStatus(ctx context.Context, ref rpc.DesktopOperationRef, target rpc.GitCommitParams) (GitCommitStatus, error) {
	if !rpc.ValidGitCommitTarget(target) {
		return GitCommitStatus{}, rpc.Errorf(rpc.CodeBadRequest, "invalid Git commit parameters")
	}
	op, err := c.DesktopOperationStatus(ctx, ref)
	if err != nil {
		return GitCommitStatus{}, err
	}
	out := GitCommitStatus{Operation: op}
	if op.State == "unknown" {
		return out, nil
	}
	if op.Method != "git.commit" {
		return GitCommitStatus{}, rpc.Errorf(rpc.CodeInternal, "invalid Git commit result")
	}
	if op.State != "done" || op.Error != nil {
		return out, nil
	}
	var result rpc.GitCommitResult
	if len(op.Result) > rpc.GitCommitResultLimit || json.Unmarshal(op.Result, &result) != nil || !validGitCommitResult(result, target) {
		return GitCommitStatus{}, rpc.Errorf(rpc.CodeInternal, "invalid Git commit result")
	}
	out.Result = &result
	return out, nil
}

func validGitCommitResult(out rpc.GitCommitResult, target rpc.GitCommitParams) bool {
	if !reflect.DeepEqual(out.Target, target.Metadata()) || !rpc.ValidGitCommitTarget(target) {
		return false
	}
	common := rpc.GitFetchResult{Target: target.GitFetchParams, Status: out.Status, Started: out.Started, Error: out.Error, Logs: out.Logs, LogsTruncated: out.LogsTruncated}
	commands := rpc.GitCommitCommands(target)
	if !validGitWriteResult(common, target.GitFetchParams, "commit", func(args []string) bool {
		if target.Action == rpc.GitCommitChange {
			if slices.Equal(args, []string{"hash-object", "-w", "--stdin", "--path", target.Path}) || slices.Equal(args, []string{"read-tree", "HEAD"}) || slices.Equal(args, []string{"commit", "-q", "-F", "-"}) {
				return true
			}
			if len(args) == 3 && args[0] == "update-index" && args[1] == "--cacheinfo" {
				parts := strings.SplitN(args[2], ",", 3)
				return len(parts) == 3 && (parts[0] == "100644" || parts[0] == "100755" || parts[0] == "120000") && rpc.ValidGitBranchOID(parts[1]) && parts[2] == target.Path
			}
			return false
		}
		return slices.ContainsFunc(commands, func(cmd []string) bool { return slices.Equal(args, cmd) })
	}) {
		return false
	}
	raw, err := json.Marshal(out)
	return err == nil && len(raw) <= rpc.GitCommitResultLimit
}
