package client

import (
	"context"
	"strings"

	"posse/internal/gitx"
	"posse/internal/rpc"
)

func (c *Client) GitDivergence(ctx context.Context, p rpc.GitDivergenceParams) (rpc.GitDivergenceResult, error) {
	var out rpc.GitDivergenceResult
	if c.Desktop == nil {
		return out, rpc.Errorf(rpc.CodeReadOnly, "desktop identity required for Git queries")
	}
	if !validGitIdentity(p.Expected) || !gitx.ValidDivergenceQuery(p.BaseRef, p.PreviousBaseline) {
		return out, gitx.ErrDivergenceQuery
	}
	if err := c.gitRead(ctx, "git.divergence", p.Path, p, &out); err != nil {
		return rpc.GitDivergenceResult{}, err
	}
	bad := func() (rpc.GitDivergenceResult, error) { return rpc.GitDivergenceResult{}, gitx.ErrDivergenceOutput }
	if out.Identity != p.Expected || out.BaseRef != p.BaseRef || !gitx.ValidBlobOID(out.Head) || !gitx.ValidBlobOID(out.Base) || strings.Trim(out.Head, "0") == "" || strings.Trim(out.Base, "0") == "" || len(out.Head) != len(out.Base) || !gitx.ValidLogBaseline(out.Baseline) {
		return bad()
	}
	if out.Unchanged {
		if p.PreviousBaseline == "" || out.Baseline != p.PreviousBaseline || out.Ahead != 0 || out.Behind != 0 || out.Prediction != "" || out.Reason != "" || out.Conflicts != nil {
			return bad()
		}
		return out, ctx.Err()
	}
	if out.Ahead < 0 || out.Behind < 0 || out.Conflicts == nil || out.Head == out.Base && (out.Ahead != 0 || out.Behind != 0) || out.Head != out.Base && out.Ahead == 0 && out.Behind == 0 {
		return bad()
	}
	switch out.Prediction {
	case gitx.DivergenceNotNeeded:
		if out.Ahead != 0 && out.Behind != 0 || out.Reason != "" || len(out.Conflicts) != 0 {
			return bad()
		}
	case gitx.DivergenceCompleted:
		if out.Ahead == 0 || out.Behind == 0 || out.Reason != "" {
			return bad()
		}
	case gitx.DivergenceUnavailable:
		if out.Ahead == 0 || out.Behind == 0 || !gitx.ValidDivergenceReason(out.Reason) || len(out.Conflicts) != 0 {
			return bad()
		}
	default:
		return bad()
	}
	seen := map[string]bool{}
	for _, name := range out.Conflicts {
		if !gitx.ValidBlameFile(name) || seen[name] {
			return bad()
		}
		seen[name] = true
	}
	return out, ctx.Err()
}
