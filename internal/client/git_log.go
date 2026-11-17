package client

import (
	"context"

	"posse/internal/gitx"
	"posse/internal/rpc"
)

func (c *Client) GitLog(ctx context.Context, p rpc.GitLogParams) (rpc.GitLogResult, error) {
	var out rpc.GitLogResult
	if c.Desktop == nil {
		return out, rpc.Errorf(rpc.CodeReadOnly, "desktop identity required for Git queries")
	}
	if !validGitIdentity(p.Expected) || !gitx.ValidLogQuery(p.Filter, p.Skip, p.Limit, p.Baseline) {
		return out, gitx.ErrLogQuery
	}
	if err := c.gitRead(ctx, "git.log", p.Path, p, &out); err != nil {
		return rpc.GitLogResult{}, err
	}
	if out.Identity != p.Expected || !gitx.ValidLogBaseline(out.Baseline) || p.Baseline != "" && out.Baseline != p.Baseline || out.Commits == nil || len(out.Commits) > p.Limit || out.HasMore && len(out.Commits) != p.Limit || out.NextSkip != p.Skip+len(out.Commits) {
		return rpc.GitLogResult{}, gitx.ErrLogOutput
	}
	seen := map[string]bool{}
	for _, commit := range out.Commits {
		if !gitx.ValidReadCommit(commit) || seen[commit.Hash] {
			return rpc.GitLogResult{}, gitx.ErrLogOutput
		}
		seen[commit.Hash] = true
	}
	return out, ctx.Err()
}

func (c *Client) GitCommitFiles(ctx context.Context, p rpc.GitCommitFilesParams) (rpc.GitCommitFilesResult, error) {
	var out rpc.GitCommitFilesResult
	if c.Desktop == nil {
		return out, rpc.Errorf(rpc.CodeReadOnly, "desktop identity required for Git queries")
	}
	if !validGitIdentity(p.Expected) || !gitx.ValidBlobOID(p.Hash) {
		return out, gitx.ErrLogQuery
	}
	if err := c.gitRead(ctx, "git.commitFiles", p.Path, p, &out); err != nil {
		return rpc.GitCommitFilesResult{}, err
	}
	if out.Identity != p.Expected || out.Commit.Hash != p.Hash || !gitx.ValidReadCommit(out.Commit) || out.Files == nil {
		return rpc.GitCommitFilesResult{}, gitx.ErrLogOutput
	}
	validBase := out.DiffBase == "" && !out.Shallow
	if len(out.Commit.Parents) > 0 {
		validBase = out.Shallow && out.DiffBase == "" || !out.Shallow && out.DiffBase == out.Commit.Parents[0]
	}
	if !validBase {
		return rpc.GitCommitFilesResult{}, gitx.ErrLogOutput
	}
	seen := map[string]bool{}
	for _, change := range out.Files {
		if !gitx.ValidReadFileChange(change) || seen[change.Path] {
			return rpc.GitCommitFilesResult{}, gitx.ErrLogOutput
		}
		seen[change.Path] = true
	}
	return out, ctx.Err()
}
