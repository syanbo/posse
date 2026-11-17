package client

import (
	"context"
	"strings"

	"posse/internal/gitx"
	"posse/internal/rpc"
)

func (c *Client) GitPushPreview(ctx context.Context, p rpc.GitPushPreviewParams) (rpc.GitPushPreviewResult, error) {
	var out rpc.GitPushPreviewResult
	if c.Desktop == nil {
		return out, rpc.Errorf(rpc.CodeReadOnly, "desktop identity required for Git queries")
	}
	if !validGitIdentity(p.Expected) || !gitx.ValidPushPreviewQuery(p.Branch, p.Limit) {
		return out, gitx.ErrPushQuery
	}
	if err := c.gitRead(ctx, "git.pushPreview", p.Path, p, &out); err != nil {
		return rpc.GitPushPreviewResult{}, err
	}
	plan := out.Plan
	if out.Identity != p.Expected || plan.Branch != p.Branch || !gitx.ValidPushRemote(plan.Remote) || !gitx.ValidPushBranch(plan.RemoteBranch) || !gitx.ValidBlobOID(out.BranchOID) || strings.Trim(out.BranchOID, "0") == "" || out.TrackingRef != "refs/remotes/"+plan.Remote+"/"+plan.RemoteBranch || !gitx.ValidLogBaseline(out.Baseline) || plan.NewBranch != out.TrackingMissing || plan.Gone && !plan.SetUpstream || plan.Commits == nil || len(plan.Commits) > p.Limit || out.More && len(plan.Commits) != p.Limit {
		return rpc.GitPushPreviewResult{}, gitx.ErrPushOutput
	}
	if out.TrackingMissing {
		if out.TrackingOID != "" {
			return rpc.GitPushPreviewResult{}, gitx.ErrPushOutput
		}
	} else if !gitx.ValidBlobOID(out.TrackingOID) || len(out.TrackingOID) != len(out.BranchOID) || strings.Trim(out.TrackingOID, "0") == "" {
		return rpc.GitPushPreviewResult{}, gitx.ErrPushOutput
	}
	seen := map[string]bool{}
	for _, commit := range plan.Commits {
		if !gitx.ValidReadCommit(commit) || len(commit.Hash) != len(out.BranchOID) || strings.Trim(commit.Hash, "0") == "" || commit.Hash == out.TrackingOID || seen[commit.Hash] {
			return rpc.GitPushPreviewResult{}, gitx.ErrPushOutput
		}
		seen[commit.Hash] = true
	}
	if out.BranchOID == out.TrackingOID && (len(plan.Commits) > 0 || out.More) {
		return rpc.GitPushPreviewResult{}, gitx.ErrPushOutput
	}
	return out, ctx.Err()
}
