package client

import (
	"context"
	"encoding/json"
	"posse/internal/gitx"
	"reflect"
	"slices"
	"strings"

	"posse/internal/rpc"
)

type GitHistoryOperation struct {
	operation *DesktopOperation
	target    rpc.GitHistoryParams
}

type GitHistoryStatus struct {
	Operation rpc.DesktopOperation
	// Result 只在原操作完成且返回 typed History 结果时非空；unknown 不表示未执行。
	Result *rpc.GitHistoryResult
}

func (c *Client) PrepareGitHistory(p rpc.GitHistoryParams) (*GitHistoryOperation, error) {
	if !rpc.ValidGitHistoryParams(p) {
		return nil, rpc.Errorf(rpc.CodeBadRequest, "invalid Git history parameters")
	}
	raw, _ := json.Marshal(p)
	var owned rpc.GitHistoryParams
	if err := json.Unmarshal(raw, &owned); err != nil {
		return nil, err
	}
	p = owned
	op, err := c.PrepareDesktopOperation("git.history", p)
	if err != nil {
		return nil, err
	}
	return &GitHistoryOperation{operation: op, target: p.Metadata()}, nil
}

func (op *GitHistoryOperation) Ref() rpc.DesktopOperationRef {
	if op == nil {
		return rpc.DesktopOperationRef{}
	}
	return op.operation.Ref()
}

func (op *GitHistoryOperation) Execute(ctx context.Context) (rpc.GitHistoryResult, error) {
	var out rpc.GitHistoryResult
	if op == nil || op.operation == nil {
		return out, rpc.Errorf(rpc.CodeBadRequest, "invalid desktop operation reference")
	}
	if err := op.operation.Execute(ctx, &out); err != nil {
		return rpc.GitHistoryResult{}, err
	}
	if !validGitHistoryResult(out, op.target) {
		err := rpc.Errorf(rpc.CodeOutcomeUnknown, "invalid Git history result")
		err.Data, _ = json.Marshal(op.Ref())
		return rpc.GitHistoryResult{}, err
	}
	return out, nil
}

func (c *Client) GitHistoryStatus(ctx context.Context, ref rpc.DesktopOperationRef, target rpc.GitHistoryParams) (GitHistoryStatus, error) {
	if !rpc.ValidGitHistoryTarget(target) {
		return GitHistoryStatus{}, rpc.Errorf(rpc.CodeBadRequest, "invalid Git history parameters")
	}
	op, err := c.DesktopOperationStatus(ctx, ref)
	if err != nil {
		return GitHistoryStatus{}, err
	}
	out := GitHistoryStatus{Operation: op}
	if op.State == "unknown" {
		return out, nil
	}
	if op.Method != "git.history" {
		return GitHistoryStatus{}, rpc.Errorf(rpc.CodeInternal, "invalid Git history result")
	}
	if op.State != "done" || op.Error != nil {
		return out, nil
	}
	var result rpc.GitHistoryResult
	if len(op.Result) > rpc.GitHistoryResultLimit || json.Unmarshal(op.Result, &result) != nil || !validGitHistoryResult(result, target) {
		return GitHistoryStatus{}, rpc.Errorf(rpc.CodeInternal, "invalid Git history result")
	}
	out.Result = &result
	return out, nil
}

func validGitHistoryResult(out rpc.GitHistoryResult, target rpc.GitHistoryParams) bool {
	if !reflect.DeepEqual(out.Target, target.Metadata()) || !rpc.ValidGitHistoryTarget(target) {
		return false
	}
	common := rpc.GitFetchResult{Target: target.GitFetchParams, Status: out.Status, Started: out.Started, Error: out.Error, Logs: out.Logs, LogsTruncated: out.LogsTruncated}
	if !validGitWriteResult(common, target.GitFetchParams, "history", func(args []string) bool {
		if target.Action == rpc.GitHistoryReset {
			return slices.Equal(args, []string{"reset", "--" + target.Mode, target.OID})
		}
		if target.Action == rpc.GitHistoryReword && target.OID == target.Head {
			return slices.Equal(args, []string{"commit", "--amend", "--only", "-F", "-"})
		}
		prefix := []string{"-c", "commit.cleanup=whitespace", "-c", "rebase.abbreviateCommands=false", "rebase", "-i", "--autostash", "--no-autosquash"}
		return len(args) == len(prefix)+1 && slices.Equal(args[:len(prefix)], prefix) && (args[len(prefix)] == "--root" || rpc.ValidGitBranchOID(args[len(prefix)]))
	}) {
		return false
	}
	if left := out.Autostash; left != nil && (out.Status != rpc.GitFetchSucceeded || !rpc.ValidGitBranchOID(left.Stash) || target.Action == rpc.GitHistoryReset || target.Action == rpc.GitHistoryReword && target.OID == target.Head) {
		return false
	}
	raw, err := json.Marshal(out)
	return err == nil && len(raw) <= rpc.GitHistoryResultLimit
}

func (c *Client) GitRemotesContaining(ctx context.Context, p rpc.GitRemotesContainingParams) (rpc.GitRemotesContainingResult, error) {
	var out rpc.GitRemotesContainingResult
	if !validGitIdentity(p.Expected) || !rpc.ValidGitBranchOID(p.OID) {
		return out, gitx.ErrLogQuery
	}
	if err := c.gitRead(ctx, "git.remotesContaining", p.Path, p, &out); err != nil {
		return out, err
	}
	if out.Identity != p.Expected || out.OID != p.OID || out.Remotes == nil || !slices.IsSorted(out.Remotes) {
		return rpc.GitRemotesContainingResult{}, gitx.ErrLogOutput
	}
	for i, name := range out.Remotes {
		if !gitx.ValidPushBranch(name) || strings.HasSuffix(name, "/HEAD") || i > 0 && name == out.Remotes[i-1] {
			return rpc.GitRemotesContainingResult{}, gitx.ErrLogOutput
		}
	}
	return out, ctx.Err()
}
