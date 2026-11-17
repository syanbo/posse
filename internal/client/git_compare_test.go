package client

import (
	"errors"
	"strings"
	"testing"

	"posse/internal/gitx"
	"posse/internal/rpc"
)

func clientCompareResult(p rpc.GitCompareParams) rpc.GitCompareResult {
	commit := clientLogCommit()
	return rpc.GitCompareResult{Identity: p.Expected, Current: p.Current, Other: p.Other, CompareRead: gitx.CompareRead{CurrentOID: commit.Hash, OtherOID: strings.Repeat("b", 40), OnlyCurrent: []gitx.CommitInfo{commit}, OnlyOther: []gitx.CommitInfo{}, Baseline: strings.Repeat("c", 64)}}
}
func TestGitCompareClient(t *testing.T) {
	p := rpc.GitCompareParams{Path: "/target/repo", Expected: testGitIdentity(), Current: "main", Other: "other", Limit: 1000}
	calls := 0
	c := fileTestClient(t, func(call rpc.DesktopCall) (any, error) {
		calls++
		if call.Method != "git.compare" || call.Sequence != 0 {
			t.Fatalf("call %+v", call)
		}
		var got rpc.GitCompareParams
		if err := rpc.Decode(call.Params, &got); err != nil || got != p {
			t.Fatalf("params %+v %v", got, err)
		}
		return clientCompareResult(p), nil
	})
	got, err := c.GitCompare(t.Context(), p)
	if err != nil || len(got.OnlyCurrent) != 1 || calls != 1 {
		t.Fatalf("result %+v calls=%d err=%v", got, calls, err)
	}
	p.Current = "--all"
	if _, err := c.GitCompare(t.Context(), p); !errors.Is(err, gitx.ErrCompareQuery) || calls != 1 {
		t.Fatalf("invalid calls=%d err=%v", calls, err)
	}
}
func TestGitCompareClientRejects(t *testing.T) {
	p := rpc.GitCompareParams{Path: "/target/repo", Expected: testGitIdentity(), Current: "main", Other: "other", Limit: 1}
	for name, mutate := range map[string]func(*rpc.GitCompareResult){
		"identity":  func(r *rpc.GitCompareResult) { r.Identity.RootInode++ },
		"current":   func(r *rpc.GitCompareResult) { r.Current = "different" },
		"other":     func(r *rpc.GitCompareResult) { r.Other = "different" },
		"oid":       func(r *rpc.GitCompareResult) { r.CurrentOID = strings.Repeat("0", 40) },
		"format":    func(r *rpc.GitCompareResult) { r.OtherOID = strings.Repeat("b", 64) },
		"nil":       func(r *rpc.GitCompareResult) { r.OnlyOther = nil },
		"more":      func(r *rpc.GitCompareResult) { r.MoreOther = true },
		"overlap":   func(r *rpc.GitCompareResult) { r.OnlyOther = r.OnlyCurrent },
		"duplicate": func(r *rpc.GitCompareResult) { r.OnlyCurrent = append(r.OnlyCurrent, r.OnlyCurrent[0]) },
		"same-oid":  func(r *rpc.GitCompareResult) { r.OtherOID = r.CurrentOID },
		"baseline":  func(r *rpc.GitCompareResult) { r.Baseline = "bad" },
		"commit":    func(r *rpc.GitCompareResult) { r.OnlyCurrent[0].Subject = "bad\x00" },
	} {
		t.Run(name, func(t *testing.T) {
			calls := 0
			out := clientCompareResult(p)
			mutate(&out)
			c := fileTestClient(t, func(rpc.DesktopCall) (any, error) { calls++; return out, nil })
			_, err := c.GitCompare(t.Context(), p)
			if !errors.Is(err, gitx.ErrCompareOutput) || calls != 1 {
				t.Fatalf("calls=%d err=%v", calls, err)
			}
		})
	}
	calls := 0
	c := fileTestClient(t, func(rpc.DesktopCall) (any, error) {
		calls++
		return nil, rpc.Errorf(rpc.CodeNotFound, "unknown method")
	})
	if _, err := c.GitCompare(t.Context(), p); err == nil || calls != 1 {
		t.Fatalf("unknown calls=%d err=%v", calls, err)
	}
	c.Desktop = nil
	if _, err := c.GitCompare(t.Context(), p); err == nil || calls != 1 {
		t.Fatalf("identity calls=%d err=%v", calls, err)
	}
}
