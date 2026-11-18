package daemon

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"posse/internal/gitx"
	"posse/internal/rpc"
	"slices"
	"strings"
	"unicode/utf8"
)

func (d *Daemon) gitHistory(ctx context.Context, raw json.RawMessage) (any, error) {
	request, ok := ctx.Value(desktopCallContext{}).(desktopRequestContext)
	if !ok || request.readOnly {
		return nil, rpc.Errorf(rpc.CodeReadOnly, "accepted desktop write required for Git history")
	}
	if len(raw) > rpc.GitHistoryParamsLimit || !utf8.Valid(raw) {
		return nil, rpc.Errorf(rpc.CodeBadRequest, "invalid Git history parameters")
	}
	var p rpc.GitHistoryParams
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	if dec.Decode(&p) != nil || dec.Decode(new(any)) != io.EOF || !rpc.ValidGitHistoryParams(p) {
		return nil, rpc.Errorf(rpc.CodeBadRequest, "invalid Git history parameters")
	}
	out := rpc.GitFetchResult{Target: p.GitFetchParams, Logs: []gitx.Run{}}
	result := rpc.GitHistoryResult{Target: p.Metadata()}
	finish := func(err error) rpc.GitHistoryResult {
		common := finishFetch(out, err)
		// Git 的成功摘要和钩子可能回显说明；结果只保留摘要标识，不保留正文。
		pairs := []string{}
		longPrefixes := []string{}
		seen := map[string]bool{}
		for _, line := range strings.Split(p.Message, "\n") {
			line = strings.TrimSpace(line)
			if line != "" && !seen[line] {
				seen[line] = true
				pairs = append(pairs, line, "[message omitted]")
				// 输出预算可能已截断长说明；只留下前缀也不能进入回执。
				if len(line) > 128 {
					longPrefixes = append(longPrefixes, line[:128])
				}
			}
		}
		replacer := strings.NewReplacer(pairs...)
		redact := func(text string) string {
			for _, prefix := range longPrefixes {
				if strings.Contains(text, prefix) {
					return "[message omitted]"
				}
			}
			text = replacer.Replace(text)
			clean := gitx.SanitizeFetchText(text)
			common.LogsTruncated = common.LogsTruncated || common.Started && clean != text
			return clean
		}
		for i := range common.Logs {
			common.Logs[i].Out = redact(common.Logs[i].Out)
			common.Logs[i].Err = redact(common.Logs[i].Err)
		}
		if common.Error != nil {
			common.Error.Message = redact(common.Error.Message)
		}
		result.Status, result.Started, result.Error = common.Status, common.Started, common.Error
		result.Logs, result.LogsTruncated = common.Logs, common.LogsTruncated
		if raw, _ := json.Marshal(result); len(raw) > rpc.GitHistoryResultLimit {
			result.Logs, result.LogsTruncated = []gitx.Run{}, true
		}
		return result
	}
	conflict := func() rpc.GitHistoryResult {
		return finish(rpc.Errorf(rpc.CodeConflict, "Git history inputs changed; refresh the log and changes"))
	}
	ctx = gitx.WithTrace(ctx, nil)
	release, err := d.lockFetchTarget(ctx, p.GitFetchParams)
	if err != nil {
		return finish(err), nil
	}
	defer release()
	readCtx := gitx.WithReadLimit(ctx, rpc.GitReadLimit)
	st, err := gitx.ReadStatusChecked(readCtx, p.Expected.Root)
	if err != nil {
		return finish(err), nil
	}
	if st.Commit != p.Head || st.Head != p.Current || st.Operation != "" {
		return conflict(), nil
	}
	oid, err := gitx.ResolveBranchCommit(readCtx, p.Expected.Root, p.OID)
	if err != nil || oid != p.OID {
		if ctx.Err() != nil {
			return finish(ctx.Err()), nil
		}
		return conflict(), nil
	}
	base, oldest, err := gitx.HistoryBase(readCtx, p.Expected.Root, p.OID, p.Action)
	if err != nil {
		if ctx.Err() != nil {
			return finish(ctx.Err()), nil
		}
		return finish(rpc.Errorf(rpc.CodeConflict, "%s", err)), nil
	}
	remotes, err := gitx.RemotesContainingChecked(readCtx, p.Expected.Root, oldest)
	if err != nil {
		return finish(err), nil
	}
	if !slices.Equal(remotes, p.Remotes) || len(remotes) > 0 && !p.Confirmed {
		return conflict(), nil
	}
	if err := gitx.CheckRepositoryIdentity(readCtx, p.Expected.Root, p.Expected); err != nil {
		return finish(err), nil
	}
	if err := ctx.Err(); err != nil {
		return finish(err), nil
	}
	truncated, err := gitx.HistoryBounded(gitWriteTrace(ctx, &out, "history"), p.Expected.Root, p.Action, p.OID, p.Head, p.Mode, base, p.Message, func() { out.Started = true })
	out.LogsTruncated = out.LogsTruncated || truncated
	var left *gitx.AutostashLeft
	if errors.As(err, &left) {
		result.Autostash, err = left, nil
	}
	return finish(err), nil
}

func (d *Daemon) gitRemotesContaining(ctx context.Context, raw json.RawMessage) (any, error) {
	var p rpc.GitRemotesContainingParams
	if err := gitReadParams(ctx, raw, &p); err != nil {
		return nil, err
	}
	if !rpc.ValidGitBranchOID(p.OID) {
		return nil, gitReadError(ctx, gitx.ErrLogQuery)
	}
	ctx = gitx.WithReadLimit(ctx, rpc.GitReadLimit)
	id, err := gitFileIdentity(ctx, p.Path, p.Expected)
	if err != nil {
		return nil, err
	}
	oid, err := gitx.ResolveBranchCommit(ctx, id.Root, p.OID)
	if err != nil || oid != p.OID {
		return nil, gitReadError(ctx, gitx.ErrLogQuery)
	}
	names, err := gitx.RemotesContainingChecked(ctx, id.Root, p.OID)
	if err != nil {
		return nil, gitReadError(ctx, err)
	}
	if err := gitx.CheckRepositoryIdentity(ctx, p.Path, id); err != nil {
		return nil, gitReadError(ctx, err)
	}
	return gitReadResult(ctx, rpc.GitRemotesContainingResult{Identity: id, OID: p.OID, Remotes: names})
}
