package client

import (
	"context"
	"encoding/json"
	"reflect"
	"slices"

	"posse/internal/rpc"
)

type GitPathsOperation struct {
	operation *DesktopOperation
	target    rpc.GitPathsParams
}

type GitPathsStatus struct {
	Operation rpc.DesktopOperation
	// Result 只在原操作完成且返回 typed Paths 结果时非空；unknown 不表示未执行。
	Result *rpc.GitPathsResult
}

func (c *Client) PrepareGitPaths(p rpc.GitPathsParams) (*GitPathsOperation, error) {
	if !rpc.ValidGitPathsParams(p) {
		return nil, rpc.Errorf(rpc.CodeBadRequest, "invalid Git paths parameters")
	}
	raw, _ := json.Marshal(p)
	var owned rpc.GitPathsParams
	if err := json.Unmarshal(raw, &owned); err != nil {
		return nil, err
	}
	p = owned
	op, err := c.PrepareDesktopOperation("git.paths", p)
	if err != nil {
		return nil, err
	}
	return &GitPathsOperation{operation: op, target: p.Metadata()}, nil
}

func (op *GitPathsOperation) Ref() rpc.DesktopOperationRef {
	if op == nil {
		return rpc.DesktopOperationRef{}
	}
	return op.operation.Ref()
}

func (op *GitPathsOperation) Execute(ctx context.Context) (rpc.GitPathsResult, error) {
	var out rpc.GitPathsResult
	if op == nil || op.operation == nil {
		return out, rpc.Errorf(rpc.CodeBadRequest, "invalid desktop operation reference")
	}
	if err := op.operation.Execute(ctx, &out); err != nil {
		return rpc.GitPathsResult{}, err
	}
	if !validGitPathsResult(out, op.target) {
		err := rpc.Errorf(rpc.CodeOutcomeUnknown, "invalid Git paths result")
		err.Data, _ = json.Marshal(op.Ref())
		return rpc.GitPathsResult{}, err
	}
	return out, nil
}

func (c *Client) GitPathsStatus(ctx context.Context, ref rpc.DesktopOperationRef, target rpc.GitPathsParams) (GitPathsStatus, error) {
	if !rpc.ValidGitPathsTarget(target) {
		return GitPathsStatus{}, rpc.Errorf(rpc.CodeBadRequest, "invalid Git paths parameters")
	}
	op, err := c.DesktopOperationStatus(ctx, ref)
	if err != nil {
		return GitPathsStatus{}, err
	}
	out := GitPathsStatus{Operation: op}
	if op.State == "unknown" {
		return out, nil
	}
	if op.Method != "git.paths" {
		return GitPathsStatus{}, rpc.Errorf(rpc.CodeInternal, "invalid Git paths result")
	}
	if op.State != "done" || op.Error != nil {
		return out, nil
	}
	var result rpc.GitPathsResult
	if len(op.Result) > rpc.GitPathsResultLimit || json.Unmarshal(op.Result, &result) != nil || !validGitPathsResult(result, target) {
		return GitPathsStatus{}, rpc.Errorf(rpc.CodeInternal, "invalid Git paths result")
	}
	out.Result = &result
	return out, nil
}

func validGitPathsResult(out rpc.GitPathsResult, target rpc.GitPathsParams) bool {
	if !reflect.DeepEqual(out.Target, target.Metadata()) || !rpc.ValidGitPathsTarget(target) {
		return false
	}
	common := rpc.GitFetchResult{Target: target.GitFetchParams, Status: out.Status, Started: out.Started, Error: out.Error, Logs: out.Logs, LogsTruncated: out.LogsTruncated}
	// 写文件没有 Git 日志；开始后的保存冲突仍保留 Started。
	if common.Status == rpc.GitFetchConflict && common.Started {
		if common.Error == nil || common.Error.Code != rpc.CodeConflict {
			return false
		}
		common.Status = rpc.GitFetchFailed
		common.Error = rpc.Errorf(rpc.CodeInternal, "%s", common.Error.Message)
	}
	if target.Action == rpc.GitPathsWrite {
		if len(out.Logs) != 0 || out.LogsTruncated {
			return false
		}
		if out.Status == rpc.GitFetchSucceeded {
			if !out.Started || out.Version == nil || out.Version.Hash != target.Hash || out.Version.Path != target.Paths[0].Version.Path || out.Version.Stamp.Missing {
				return false
			}
		} else if out.Version != nil {
			return false
		}
		common.LogsTruncated = common.Started
	} else if out.Version != nil {
		return false
	}
	commands := rpc.GitPathsCommands(target)
	if !validGitWriteResult(common, target.GitFetchParams, "paths", func(args []string) bool {
		return slices.ContainsFunc(commands, func(cmd []string) bool { return slices.Equal(args, cmd) })
	}) {
		return false
	}
	raw, err := json.Marshal(out)
	return err == nil && len(raw) <= rpc.GitPathsResultLimit
}
