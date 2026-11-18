package client

import (
	"context"
	"encoding/json"
	"reflect"
	"slices"

	"posse/internal/rpc"
)

type GitIntegrateOperation struct {
	operation *DesktopOperation
	target    rpc.GitIntegrateParams
}

type GitIntegrateStatus struct {
	Operation rpc.DesktopOperation
	// Result 只在原操作完成且返回 typed Integrate 结果时非空；unknown 不表示未执行。
	Result *rpc.GitIntegrateResult
}

func (c *Client) PrepareGitIntegrate(p rpc.GitIntegrateParams) (*GitIntegrateOperation, error) {
	if !rpc.ValidGitIntegrateParams(p) {
		return nil, rpc.Errorf(rpc.CodeBadRequest, "invalid Git integration parameters")
	}
	raw, _ := json.Marshal(p)
	var owned rpc.GitIntegrateParams
	if err := json.Unmarshal(raw, &owned); err != nil {
		return nil, err
	}
	p = owned
	op, err := c.PrepareDesktopOperation("git.integrate", p)
	if err != nil {
		return nil, err
	}
	return &GitIntegrateOperation{operation: op, target: p}, nil
}

func (op *GitIntegrateOperation) Ref() rpc.DesktopOperationRef {
	if op == nil {
		return rpc.DesktopOperationRef{}
	}
	return op.operation.Ref()
}

func (op *GitIntegrateOperation) Execute(ctx context.Context) (rpc.GitIntegrateResult, error) {
	var out rpc.GitIntegrateResult
	if op == nil || op.operation == nil {
		return out, rpc.Errorf(rpc.CodeBadRequest, "invalid desktop operation reference")
	}
	if err := op.operation.Execute(ctx, &out); err != nil {
		return rpc.GitIntegrateResult{}, err
	}
	if !validGitIntegrateResult(out, op.target) {
		err := rpc.Errorf(rpc.CodeOutcomeUnknown, "invalid Git integration result")
		err.Data, _ = json.Marshal(op.Ref())
		return rpc.GitIntegrateResult{}, err
	}
	return out, nil
}

func (c *Client) GitIntegrateStatus(ctx context.Context, ref rpc.DesktopOperationRef, target rpc.GitIntegrateParams) (GitIntegrateStatus, error) {
	if !rpc.ValidGitIntegrateParams(target) {
		return GitIntegrateStatus{}, rpc.Errorf(rpc.CodeBadRequest, "invalid Git integration parameters")
	}
	op, err := c.DesktopOperationStatus(ctx, ref)
	if err != nil {
		return GitIntegrateStatus{}, err
	}
	out := GitIntegrateStatus{Operation: op}
	if op.State == "unknown" {
		return out, nil
	}
	if op.Method != "git.integrate" {
		return GitIntegrateStatus{}, rpc.Errorf(rpc.CodeInternal, "invalid Git integration result")
	}
	if op.State != "done" || op.Error != nil {
		return out, nil
	}
	var result rpc.GitIntegrateResult
	if len(op.Result) > rpc.GitIntegrateResultLimit || json.Unmarshal(op.Result, &result) != nil || !validGitIntegrateResult(result, target) {
		return GitIntegrateStatus{}, rpc.Errorf(rpc.CodeInternal, "invalid Git integration result")
	}
	out.Result = &result
	return out, nil
}

func validGitIntegrateResult(out rpc.GitIntegrateResult, target rpc.GitIntegrateParams) bool {
	if !reflect.DeepEqual(out.Target, target) || !rpc.ValidGitIntegrateParams(target) {
		return false
	}
	common := rpc.GitFetchResult{Target: target.GitFetchParams, Status: out.Status, Started: out.Started, Error: out.Error, Logs: out.Logs, LogsTruncated: out.LogsTruncated}
	if !validGitWriteResult(common, target.GitFetchParams, "integrate", func(args []string) bool { return slices.Equal(args, rpc.GitIntegrateArgs(target)) }) {
		return false
	}
	raw, err := json.Marshal(out)
	return err == nil && len(raw) <= rpc.GitIntegrateResultLimit
}
