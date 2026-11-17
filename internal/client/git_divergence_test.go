package client

import (
	"errors"
	"strings"
	"testing"

	"posse/internal/gitx"
	"posse/internal/rpc"
)

func clientDivergenceResult(p rpc.GitDivergenceParams) rpc.GitDivergenceResult {
	return rpc.GitDivergenceResult{Identity: p.Expected, BaseRef: p.BaseRef, DivergenceRead: gitx.DivergenceRead{Head: strings.Repeat("a", 40), Base: strings.Repeat("b", 40), Ahead: 1, Behind: 2, Prediction: gitx.DivergenceCompleted, Conflicts: []string{"file"}, Baseline: strings.Repeat("c", 64)}}
}

func TestGitDivergenceClient(t *testing.T) {
	p := rpc.GitDivergenceParams{Path: "/target/repo", Expected: testGitIdentity(), BaseRef: "main"}
	calls := 0
	c := fileTestClient(t, func(call rpc.DesktopCall) (any, error) {
		calls++
		var got rpc.GitDivergenceParams
		if call.Method != "git.divergence" || call.Sequence != 0 || rpc.Decode(call.Params, &got) != nil || got != p {
			t.Fatalf("call %+v", call)
		}
		out := clientDivergenceResult(p)
		if p.PreviousBaseline != "" {
			out.Unchanged = true
			out.Ahead, out.Behind, out.Prediction, out.Conflicts = 0, 0, "", nil
		}
		return out, nil
	})
	got, err := c.GitDivergence(t.Context(), p)
	if err != nil || got.Prediction != gitx.DivergenceCompleted || calls != 1 {
		t.Fatalf("result %+v calls=%d %v", got, calls, err)
	}
	p.PreviousBaseline = got.Baseline
	got, err = c.GitDivergence(t.Context(), p)
	if err != nil || !got.Unchanged || calls != 2 {
		t.Fatalf("unchanged %+v calls=%d %v", got, calls, err)
	}
	p.PreviousBaseline = "invalid"
	if _, err := c.GitDivergence(t.Context(), p); !errors.Is(err, gitx.ErrDivergenceQuery) || calls != 2 {
		t.Fatalf("invalid baseline %v", err)
	}
	p.PreviousBaseline = ""
	p.BaseRef = "--all"
	if _, err := c.GitDivergence(t.Context(), p); !errors.Is(err, gitx.ErrDivergenceQuery) || calls != 2 {
		t.Fatalf("invalid revision %v", err)
	}
}

func TestGitDivergenceClientRejects(t *testing.T) {
	p := rpc.GitDivergenceParams{Path: "/target/repo", Expected: testGitIdentity(), BaseRef: "main"}
	for name, mutate := range map[string]func(*rpc.GitDivergenceResult){
		"identity":  func(r *rpc.GitDivergenceResult) { r.Identity.RootInode++ },
		"base-ref":  func(r *rpc.GitDivergenceResult) { r.BaseRef = "other" },
		"oid":       func(r *rpc.GitDivergenceResult) { r.Head = strings.Repeat("0", 40) },
		"format":    func(r *rpc.GitDivergenceResult) { r.Base = strings.Repeat("b", 64) },
		"baseline":  func(r *rpc.GitDivergenceResult) { r.Baseline = "invalid" },
		"negative":  func(r *rpc.GitDivergenceResult) { r.Ahead = -1 },
		"same":      func(r *rpc.GitDivergenceResult) { r.Base = r.Head },
		"both-zero": func(r *rpc.GitDivergenceResult) { r.Ahead, r.Behind = 0, 0 },
		"nil":       func(r *rpc.GitDivergenceResult) { r.Conflicts = nil },
		"state":     func(r *rpc.GitDivergenceResult) { r.Prediction = "unknown" },
		"reason":    func(r *rpc.GitDivergenceResult) { r.Reason = gitx.DivergenceGit },
		"unavailable-conflicts": func(r *rpc.GitDivergenceResult) {
			r.Prediction, r.Reason = gitx.DivergenceUnavailable, gitx.DivergenceGit
		},
		"unavailable-reason": func(r *rpc.GitDivergenceResult) {
			r.Prediction, r.Conflicts, r.Reason = gitx.DivergenceUnavailable, []string{}, "unknown"
		},
		"not-needed": func(r *rpc.GitDivergenceResult) { r.Prediction, r.Conflicts = gitx.DivergenceNotNeeded, []string{} },
		"path":       func(r *rpc.GitDivergenceResult) { r.Conflicts = []string{"../outside"} },
		"duplicate":  func(r *rpc.GitDivergenceResult) { r.Conflicts = []string{"file", "file"} },
		"unsolicited-unchanged": func(r *rpc.GitDivergenceResult) {
			r.Unchanged = true
			r.Ahead, r.Behind, r.Prediction, r.Conflicts = 0, 0, "", nil
		},
	} {
		t.Run(name, func(t *testing.T) {
			out := clientDivergenceResult(p)
			mutate(&out)
			calls := 0
			c := fileTestClient(t, func(rpc.DesktopCall) (any, error) { calls++; return out, nil })
			if _, err := c.GitDivergence(t.Context(), p); !errors.Is(err, gitx.ErrDivergenceOutput) || calls != 1 {
				t.Fatalf("calls=%d %v", calls, err)
			}
		})
	}
	p.PreviousBaseline = strings.Repeat("d", 64)
	out := clientDivergenceResult(p)
	out.Unchanged = true
	out.Ahead, out.Behind, out.Prediction, out.Conflicts = 0, 0, "", nil
	c := fileTestClient(t, func(rpc.DesktopCall) (any, error) { return out, nil })
	if _, err := c.GitDivergence(t.Context(), p); !errors.Is(err, gitx.ErrDivergenceOutput) {
		t.Fatalf("mismatched unchanged %v", err)
	}
	calls := 0
	c = fileTestClient(t, func(rpc.DesktopCall) (any, error) {
		calls++
		return nil, rpc.Errorf(rpc.CodeNotFound, "unknown method")
	})
	if _, err := c.GitDivergence(t.Context(), p); err == nil || calls != 1 {
		t.Fatalf("unknown calls=%d %v", calls, err)
	}
	c.Desktop = nil
	if _, err := c.GitDivergence(t.Context(), p); err == nil || calls != 1 {
		t.Fatalf("identity calls=%d %v", calls, err)
	}
}
