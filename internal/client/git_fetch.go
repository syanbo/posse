package client

import (
	"context"
	"encoding/json"
	"slices"
	"unicode/utf8"

	"posse/internal/rpc"
)

type GitFetchOperation struct {
	operation *DesktopOperation
	target    rpc.GitFetchParams
}

type GitFetchStatus struct {
	Operation rpc.DesktopOperation
	// Result 只在原操作完成且返回 typed Fetch 结果时非空；unknown 不表示未执行。
	Result *rpc.GitFetchResult
}

func (c *Client) PrepareGitFetch(p rpc.GitFetchParams) (*GitFetchOperation, error) {
	if !rpc.ValidGitFetchParams(p) {
		return nil, rpc.Errorf(rpc.CodeBadRequest, "invalid Git fetch parameters")
	}
	op, err := c.PrepareDesktopOperation("git.fetch", p)
	if err != nil {
		return nil, err
	}
	return &GitFetchOperation{operation: op, target: p}, nil
}

func (op *GitFetchOperation) Ref() rpc.DesktopOperationRef {
	if op == nil {
		return rpc.DesktopOperationRef{}
	}
	return op.operation.Ref()
}

func (op *GitFetchOperation) Execute(ctx context.Context) (rpc.GitFetchResult, error) {
	var out rpc.GitFetchResult
	if op == nil || op.operation == nil {
		return out, rpc.Errorf(rpc.CodeBadRequest, "invalid desktop operation reference")
	}
	if err := op.operation.Execute(ctx, &out); err != nil {
		return rpc.GitFetchResult{}, err
	}
	if !validGitFetchResult(out, op.target) {
		err := rpc.Errorf(rpc.CodeOutcomeUnknown, "invalid Git fetch result")
		err.Data, _ = json.Marshal(op.Ref())
		return rpc.GitFetchResult{}, err
	}
	return out, nil
}

func (c *Client) GitFetchStatus(ctx context.Context, ref rpc.DesktopOperationRef, target rpc.GitFetchParams) (GitFetchStatus, error) {
	if !rpc.ValidGitFetchParams(target) {
		return GitFetchStatus{}, rpc.Errorf(rpc.CodeBadRequest, "invalid Git fetch parameters")
	}
	op, err := c.DesktopOperationStatus(ctx, ref)
	if err != nil {
		return GitFetchStatus{}, err
	}
	out := GitFetchStatus{Operation: op}
	if op.State == "unknown" {
		return out, nil
	}
	if op.Method != "git.fetch" {
		return GitFetchStatus{}, rpc.Errorf(rpc.CodeInternal, "invalid Git fetch result")
	}
	if op.State != "done" || op.Error != nil {
		return out, nil
	}
	var result rpc.GitFetchResult
	if len(op.Result) > rpc.GitFetchResultLimit || json.Unmarshal(op.Result, &result) != nil || !validGitFetchResult(result, target) {
		return GitFetchStatus{}, rpc.Errorf(rpc.CodeInternal, "invalid Git fetch result")
	}
	out.Result = &result
	return out, nil
}

func validGitFetchResult(out rpc.GitFetchResult, target rpc.GitFetchParams) bool {
	return validGitWriteResult(out, target, "fetch", func(args []string) bool { return slices.Equal(args, []string{"fetch", "--all"}) })
}

func validGitWriteResult(out rpc.GitFetchResult, target rpc.GitFetchParams, operation string, validArgs func([]string) bool) bool {
	if out.Target != target || out.Logs == nil || len(out.Logs) > rpc.GitFetchLogLimit || !out.Started && (len(out.Logs) != 0 || out.LogsTruncated) {
		return false
	}
	code := ""
	switch out.Status {
	case rpc.GitFetchSucceeded:
		if !out.Started || out.Error != nil {
			return false
		}
	case rpc.GitFetchFailed:
		code = rpc.CodeInternal
	case rpc.GitFetchCanceled:
		code = "canceled"
	case rpc.GitFetchTimedOut:
		code = rpc.CodeTimeout
	case rpc.GitFetchConflict:
		code = rpc.CodeConflict
		if out.Started {
			return false
		}
	default:
		return false
	}
	if code != "" && (out.Error == nil || out.Error.Code != code || out.Error.Message == "" || !utf8.ValidString(out.Error.Message) || len(out.Error.Message) > 5<<10 || len(out.Error.Data) != 0) {
		return false
	}
	if out.Started && len(out.Logs) == 0 && !out.LogsTruncated {
		return false
	}
	seen := map[int64]bool{}
	var opID int64
	for _, log := range out.Logs {
		if log.ID <= 0 || seen[log.ID] || log.OpID <= 0 || log.Op != operation || log.Dir != target.Expected.Root || !validArgs(log.Args) || log.Start.IsZero() || log.Took < 0 || !log.Done || len(log.Out) > 5<<10 || len(log.Err) > 5<<10 || !utf8.ValidString(log.Out) || !utf8.ValidString(log.Err) {
			return false
		}
		if opID != 0 && log.OpID != opID {
			return false
		}
		opID = log.OpID
		seen[log.ID] = true
		if log.Canceled && out.Status != rpc.GitFetchCanceled || operation == "fetch" && log.Canceled != (out.Status == rpc.GitFetchCanceled) {
			return false
		}
		if out.Status == rpc.GitFetchSucceeded && log.Err != "" || operation == "fetch" && out.Status != rpc.GitFetchSucceeded && log.Err == "" && !out.LogsTruncated {
			return false
		}
	}
	raw, err := json.Marshal(out)
	return err == nil && len(raw) <= rpc.GitFetchResultLimit
}
