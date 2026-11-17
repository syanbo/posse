package client

import (
	"context"
	"strings"

	"posse/internal/gitx"
	"posse/internal/rpc"
)

func (c *Client) GitCompare(ctx context.Context, p rpc.GitCompareParams) (rpc.GitCompareResult, error) {
	var out rpc.GitCompareResult
	if c.Desktop == nil {
		return out, rpc.Errorf(rpc.CodeReadOnly, "desktop identity required for Git queries")
	}
	if !validGitIdentity(p.Expected) || !gitx.ValidCompareQuery(p.Current, p.Other, p.Limit) {
		return out, gitx.ErrCompareQuery
	}
	if err := c.gitRead(ctx, "git.compare", p.Path, p, &out); err != nil {
		return rpc.GitCompareResult{}, err
	}
	if out.Identity != p.Expected || out.Current != p.Current || out.Other != p.Other || !gitx.ValidLogBaseline(out.Baseline) || !gitx.ValidBlobOID(out.CurrentOID) || !gitx.ValidBlobOID(out.OtherOID) || len(out.CurrentOID) != len(out.OtherOID) || strings.Trim(out.CurrentOID, "0") == "" || strings.Trim(out.OtherOID, "0") == "" {
		return rpc.GitCompareResult{}, gitx.ErrCompareOutput
	}
	seen := map[string]bool{}
	for _, side := range []struct {
		commits []gitx.CommitInfo
		more    bool
		exclude string
	}{{out.OnlyCurrent, out.MoreCurrent, out.OtherOID}, {out.OnlyOther, out.MoreOther, out.CurrentOID}} {
		if side.commits == nil || len(side.commits) > p.Limit || side.more && len(side.commits) != p.Limit {
			return rpc.GitCompareResult{}, gitx.ErrCompareOutput
		}
		for _, commit := range side.commits {
			if !gitx.ValidReadCommit(commit) || len(commit.Hash) != len(out.CurrentOID) || strings.Trim(commit.Hash, "0") == "" || seen[commit.Hash] || commit.Hash == side.exclude {
				return rpc.GitCompareResult{}, gitx.ErrCompareOutput
			}
			seen[commit.Hash] = true
		}
	}
	if (p.Current == p.Other && out.CurrentOID != out.OtherOID) || (out.CurrentOID == out.OtherOID && (len(out.OnlyCurrent) != 0 || len(out.OnlyOther) != 0 || out.MoreCurrent || out.MoreOther)) {
		return rpc.GitCompareResult{}, gitx.ErrCompareOutput
	}
	return out, ctx.Err()
}
