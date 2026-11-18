package client

import (
	"context"
	"encoding/json"
	"reflect"
	"slices"

	"posse/internal/rpc"
)

type GitStashOperation struct {
	operation *DesktopOperation
	target    rpc.GitStashParams
}

type GitStashStatus struct {
	Operation rpc.DesktopOperation
	// Result 只在原操作完成且返回 typed Stash 结果时非空；unknown 不表示未执行。
	Result *rpc.GitStashResult
}

func (c *Client) PrepareGitStash(p rpc.GitStashParams) (*GitStashOperation, error) {
	if !rpc.ValidGitStashParams(p) {
		return nil, rpc.Errorf(rpc.CodeBadRequest, "invalid Git stash parameters")
	}
	raw, _ := json.Marshal(p)
	var owned rpc.GitStashParams
	if err := json.Unmarshal(raw, &owned); err != nil {
		return nil, err
	}
	p = owned
	op, err := c.PrepareDesktopOperation("git.stash", p)
	if err != nil {
		return nil, err
	}
	return &GitStashOperation{operation: op, target: p.Metadata()}, nil
}

func (op *GitStashOperation) Ref() rpc.DesktopOperationRef {
	if op == nil {
		return rpc.DesktopOperationRef{}
	}
	return op.operation.Ref()
}

func (op *GitStashOperation) Execute(ctx context.Context) (rpc.GitStashResult, error) {
	var out rpc.GitStashResult
	if op == nil || op.operation == nil {
		return out, rpc.Errorf(rpc.CodeBadRequest, "invalid desktop operation reference")
	}
	if err := op.operation.Execute(ctx, &out); err != nil {
		return rpc.GitStashResult{}, err
	}
	if !validGitStashResult(out, op.target) {
		err := rpc.Errorf(rpc.CodeOutcomeUnknown, "invalid Git stash result")
		err.Data, _ = json.Marshal(op.Ref())
		return rpc.GitStashResult{}, err
	}
	return out, nil
}

func (c *Client) GitStashStatus(ctx context.Context, ref rpc.DesktopOperationRef, target rpc.GitStashParams) (GitStashStatus, error) {
	if !rpc.ValidGitStashTarget(target) {
		return GitStashStatus{}, rpc.Errorf(rpc.CodeBadRequest, "invalid Git stash parameters")
	}
	op, err := c.DesktopOperationStatus(ctx, ref)
	if err != nil {
		return GitStashStatus{}, err
	}
	out := GitStashStatus{Operation: op}
	if op.State == "unknown" {
		return out, nil
	}
	if op.Method != "git.stash" {
		return GitStashStatus{}, rpc.Errorf(rpc.CodeInternal, "invalid Git stash result")
	}
	if op.State != "done" || op.Error != nil {
		return out, nil
	}
	var result rpc.GitStashResult
	if len(op.Result) > rpc.GitStashResultLimit || json.Unmarshal(op.Result, &result) != nil || !validGitStashResult(result, target) {
		return GitStashStatus{}, rpc.Errorf(rpc.CodeInternal, "invalid Git stash result")
	}
	out.Result = &result
	return out, nil
}

func validGitStashResult(out rpc.GitStashResult, target rpc.GitStashParams) bool {
	if !reflect.DeepEqual(out.Target, target.Metadata()) || !rpc.ValidGitStashTarget(target) {
		return false
	}
	common := rpc.GitFetchResult{Target: target.GitFetchParams, Status: out.Status, Started: out.Started, Error: out.Error, Logs: out.Logs, LogsTruncated: out.LogsTruncated}
	if !validGitWriteResult(common, target.GitFetchParams, "stash", func(args []string) bool {
		switch target.Action {
		case rpc.GitStashPush:
			want := []string{"stash", "push", "--include-untracked"}
			if target.MessageHash != rpc.GitCommitHash(nil) {
				want = append(want, "-m", "[message omitted]")
			}
			return slices.Equal(args, want)
		case rpc.GitStashApply, rpc.GitStashPop:
			return len(args) == 4 && args[0] == "stash" && args[1] == target.Action && args[2] == "--index" && rpc.ValidGitStashRef(args[3])
		case rpc.GitStashDrop:
			return len(args) == 3 && args[0] == "stash" && args[1] == "drop" && rpc.ValidGitStashRef(args[2])
		}
		return false
	}) {
		return false
	}
	if out.OID != "" && (target.Action != rpc.GitStashPush || out.Status != rpc.GitFetchSucceeded || !rpc.ValidGitBranchOID(out.OID)) {
		return false
	}
	raw, err := json.Marshal(out)
	return err == nil && len(raw) <= rpc.GitStashResultLimit
}
