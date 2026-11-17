package client

import (
	"errors"
	"strings"
	"testing"

	"posse/internal/gitx"
	"posse/internal/rpc"
)

func clientPushResult(p rpc.GitPushPreviewParams) rpc.GitPushPreviewResult {
	commit := clientLogCommit()
	return rpc.GitPushPreviewResult{Identity: p.Expected, PushPreviewRead: gitx.PushPreviewRead{Plan: gitx.PushPlan{Branch: p.Branch, Remote: "origin", RemoteBranch: p.Branch, Commits: []gitx.CommitInfo{commit}}, BranchOID: commit.Hash, TrackingRef: "refs/remotes/origin/" + p.Branch, TrackingOID: strings.Repeat("b", 40), Baseline: strings.Repeat("c", 64)}}
}
func TestGitPushPreviewClient(t *testing.T) {
	p := rpc.GitPushPreviewParams{Path: "/target/repo", Expected: testGitIdentity(), Branch: "main", Limit: 500}
	calls := 0
	c := fileTestClient(t, func(call rpc.DesktopCall) (any, error) {
		calls++
		if call.Method != "git.pushPreview" || call.Sequence != 0 {
			t.Fatalf("call %+v", call)
		}
		var got rpc.GitPushPreviewParams
		if err := rpc.Decode(call.Params, &got); err != nil || got != p {
			t.Fatalf("params %+v %v", got, err)
		}
		return clientPushResult(p), nil
	})
	got, err := c.GitPushPreview(t.Context(), p)
	if err != nil || len(got.Plan.Commits) != 1 || calls != 1 {
		t.Fatalf("out %+v calls=%d err=%v", got, calls, err)
	}
	p.Branch = "main~1"
	if _, err := c.GitPushPreview(t.Context(), p); !errors.Is(err, gitx.ErrPushQuery) || calls != 1 {
		t.Fatalf("invalid calls=%d err=%v", calls, err)
	}
}
func TestGitPushPreviewClientRejects(t *testing.T) {
	p := rpc.GitPushPreviewParams{Path: "/target/repo", Expected: testGitIdentity(), Branch: "main", Limit: 500}
	for name, mutate := range map[string]func(*rpc.GitPushPreviewResult){
		"identity":      func(r *rpc.GitPushPreviewResult) { r.Identity.RootInode++ },
		"branch":        func(r *rpc.GitPushPreviewResult) { r.Plan.Branch = "other" },
		"remote":        func(r *rpc.GitPushPreviewResult) { r.Plan.Remote = "--upload-pack=bad" },
		"remote-branch": func(r *rpc.GitPushPreviewResult) { r.Plan.RemoteBranch = "main~1" },
		"oid":           func(r *rpc.GitPushPreviewResult) { r.BranchOID = strings.Repeat("0", 40) },
		"tracking-ref":  func(r *rpc.GitPushPreviewResult) { r.TrackingRef = "refs/remotes/other/main" },
		"tracking-oid":  func(r *rpc.GitPushPreviewResult) { r.TrackingOID = strings.Repeat("b", 64) },
		"new":           func(r *rpc.GitPushPreviewResult) { r.Plan.NewBranch = true },
		"missing":       func(r *rpc.GitPushPreviewResult) { r.Plan.NewBranch = true; r.TrackingMissing = true },
		"gone":          func(r *rpc.GitPushPreviewResult) { r.Plan.Gone = true },
		"baseline":      func(r *rpc.GitPushPreviewResult) { r.Baseline = "bad" },
		"more":          func(r *rpc.GitPushPreviewResult) { r.More = true },
		"nil":           func(r *rpc.GitPushPreviewResult) { r.Plan.Commits = nil },
		"duplicate":     func(r *rpc.GitPushPreviewResult) { r.Plan.Commits = append(r.Plan.Commits, r.Plan.Commits[0]) },
		"commit":        func(r *rpc.GitPushPreviewResult) { r.Plan.Commits[0].Hash = r.TrackingOID },
	} {
		t.Run(name, func(t *testing.T) {
			out := clientPushResult(p)
			mutate(&out)
			calls := 0
			c := fileTestClient(t, func(rpc.DesktopCall) (any, error) { calls++; return out, nil })
			_, err := c.GitPushPreview(t.Context(), p)
			if !errors.Is(err, gitx.ErrPushOutput) || calls != 1 {
				t.Fatalf("calls=%d err=%v", calls, err)
			}
		})
	}
	calls := 0
	c := fileTestClient(t, func(rpc.DesktopCall) (any, error) {
		calls++
		return nil, rpc.Errorf(rpc.CodeNotFound, "unknown method")
	})
	if _, err := c.GitPushPreview(t.Context(), p); err == nil || calls != 1 {
		t.Fatalf("calls=%d err=%v", calls, err)
	}
	c.Desktop = nil
	if _, err := c.GitPushPreview(t.Context(), p); err == nil || calls != 1 {
		t.Fatalf("identity calls=%d err=%v", calls, err)
	}
}
