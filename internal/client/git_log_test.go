package client

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"posse/internal/gitx"
	"posse/internal/rpc"
)

func clientLogParams() rpc.GitLogParams {
	return rpc.GitLogParams{Path: "/target/repo", Expected: testGitIdentity(), Limit: 300}
}
func clientLogCommit() gitx.CommitInfo {
	return gitx.CommitInfo{Hash: strings.Repeat("a", 40), Parents: []string{}, Author: "Author", Subject: "message", Time: time.Unix(1700000000, 0)}
}
func TestGitLogClient(t *testing.T) {
	p := clientLogParams()
	commit := clientLogCommit()
	calls := 0
	c := fileTestClient(t, func(call rpc.DesktopCall) (any, error) {
		calls++
		if call.Sequence != 0 {
			t.Fatal("read became write")
		}
		switch call.Method {
		case "git.log":
			var got rpc.GitLogParams
			if err := rpc.Decode(call.Params, &got); err != nil || got.Path != p.Path || got.Expected != p.Expected || got.Limit != 300 {
				t.Fatalf("params %+v %v", got, err)
			}
			return rpc.GitLogResult{Identity: p.Expected, Commits: []gitx.CommitInfo{commit}, Baseline: strings.Repeat("b", 64), NextSkip: 1}, nil
		case "git.commitFiles":
			var got rpc.GitCommitFilesParams
			if err := rpc.Decode(call.Params, &got); err != nil || got.Hash != commit.Hash || got.Expected != p.Expected {
				t.Fatalf("detail params %+v %v", got, err)
			}
			return rpc.GitCommitFilesResult{Identity: p.Expected, Commit: commit, Files: []gitx.FileChange{{Path: "new", Orig: "old", Status: 'R'}}}, nil
		}
		t.Fatalf("unexpected %s", call.Method)
		return nil, nil
	})
	page, err := c.GitLog(t.Context(), p)
	if err != nil || len(page.Commits) != 1 {
		t.Fatalf("page %+v %v", page, err)
	}
	detail, err := c.GitCommitFiles(t.Context(), rpc.GitCommitFilesParams{Path: p.Path, Expected: p.Expected, Hash: commit.Hash})
	if err != nil || len(detail.Files) != 1 {
		t.Fatalf("details %+v %v", detail, err)
	}
	if calls != 2 {
		t.Fatalf("calls=%d", calls)
	}
}

func TestGitCommitFilesDiffBase(t *testing.T) {
	parent := strings.Repeat("b", 40)
	for _, tc := range []struct {
		name           string
		parents        []string
		base           string
		shallow, valid bool
	}{
		{"root", nil, "", false, true},
		{"normal", []string{parent}, parent, false, true},
		{"shallow", []string{parent}, "", true, true},
		{"root-with-parent", nil, parent, false, false},
		{"root-marked-shallow", nil, "", true, false},
		{"missing-base", []string{parent}, "", false, false},
		{"wrong-base", []string{parent}, strings.Repeat("c", 40), false, false},
		{"shallow-with-base", []string{parent}, parent, true, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			p := clientLogParams()
			commit := clientLogCommit()
			commit.Parents = tc.parents
			c := fileTestClient(t, func(rpc.DesktopCall) (any, error) {
				return rpc.GitCommitFilesResult{Identity: p.Expected, Commit: commit, Files: []gitx.FileChange{}, DiffBase: tc.base, Shallow: tc.shallow}, nil
			})
			_, err := c.GitCommitFiles(t.Context(), rpc.GitCommitFilesParams{Path: p.Path, Expected: p.Expected, Hash: commit.Hash})
			if (err == nil) != tc.valid {
				t.Fatalf("valid=%v err=%v", tc.valid, err)
			}
		})
	}
}

func TestGitLogClientRejects(t *testing.T) {
	p := clientLogParams()
	q := rpc.GitCommitFilesParams{Path: p.Path, Expected: p.Expected, Hash: clientLogCommit().Hash}
	for _, remote := range []bool{false, true} {
		c := &Client{Remote: remote, Socket: "/missing"}
		_, e1 := c.GitLog(t.Context(), p)
		_, e2 := c.GitCommitFiles(t.Context(), q)
		for _, err := range []error{e1, e2} {
			var re *rpc.Error
			if !errors.As(err, &re) || re.Code != rpc.CodeReadOnly {
				t.Fatalf("identity %v", err)
			}
		}
	}
	calls := 0
	c := fileTestClient(t, func(rpc.DesktopCall) (any, error) {
		calls++
		return nil, rpc.Errorf(rpc.CodeUnknown, "history unsupported")
	})
	for _, change := range []func(*rpc.GitLogParams){func(p *rpc.GitLogParams) { p.Path = "relative" }, func(p *rpc.GitLogParams) { p.Expected.RootInode = 0 }, func(p *rpc.GitLogParams) { p.Skip = 1 }, func(p *rpc.GitLogParams) { p.Limit = 1001 }, func(p *rpc.GitLogParams) { p.Filter.Branches = []string{"--all"} }} {
		bad := p
		change(&bad)
		if _, err := c.GitLog(t.Context(), bad); err == nil {
			t.Fatal("invalid request")
		}
	}
	bad := q
	bad.Hash = "HEAD"
	if _, err := c.GitCommitFiles(t.Context(), bad); err == nil {
		t.Fatal("mutable hash accepted")
	}
	if calls != 0 {
		t.Fatal("invalid request dispatched")
	}
	if _, err := c.GitLog(t.Context(), p); err == nil {
		t.Fatal("unsupported hidden")
	}
	if _, err := c.GitCommitFiles(t.Context(), q); err == nil {
		t.Fatal("unsupported hidden")
	}
	if calls != 2 {
		t.Fatalf("fallback/replay %d", calls)
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if _, err := c.GitLog(ctx, p); err == nil {
		t.Fatal("cancel ignored")
	}
}

func TestGitLogClientInvalidResponses(t *testing.T) {
	p := clientLogParams()
	commit := clientLogCommit()
	for _, change := range []func(*rpc.GitLogResult){
		func(r *rpc.GitLogResult) { r.Identity.RootInode++ },
		func(r *rpc.GitLogResult) { r.Baseline = "invalid" },
		func(r *rpc.GitLogResult) { r.Commits = nil },
		func(r *rpc.GitLogResult) { r.Commits[0].Hash = "invalid" },
		func(r *rpc.GitLogResult) { r.Commits[0].Parents = []string{"invalid"} },
		func(r *rpc.GitLogResult) { r.Commits[0].Subject = "bad\x00" },
		func(r *rpc.GitLogResult) { r.Commits = append(r.Commits, r.Commits[0]); r.NextSkip = 2 },
		func(r *rpc.GitLogResult) { r.NextSkip = 100 },
		func(r *rpc.GitLogResult) { r.HasMore = true },
	} {
		r := rpc.GitLogResult{Identity: p.Expected, Commits: []gitx.CommitInfo{commit}, Baseline: strings.Repeat("b", 64), NextSkip: 1}
		change(&r)
		c := fileTestClient(t, func(rpc.DesktopCall) (any, error) { return r, nil })
		if _, err := c.GitLog(t.Context(), p); err == nil {
			t.Fatalf("bad page %+v", r)
		}
	}
	for _, change := range []func(*rpc.GitCommitFilesResult){
		func(r *rpc.GitCommitFilesResult) { r.Identity.RootInode++ },
		func(r *rpc.GitCommitFilesResult) { r.Commit.Hash = strings.Repeat("b", 40) },
		func(r *rpc.GitCommitFilesResult) { r.Files = nil },
		func(r *rpc.GitCommitFilesResult) { r.Files[0].Path = "../escape" },
		func(r *rpc.GitCommitFilesResult) { r.Files[0].Orig = "" },
		func(r *rpc.GitCommitFilesResult) { r.Files[0].Status = 'Q' },
		func(r *rpc.GitCommitFilesResult) { r.Files = append(r.Files, r.Files[0]) },
	} {
		r := rpc.GitCommitFilesResult{Identity: p.Expected, Commit: commit, Files: []gitx.FileChange{{Path: "new", Orig: "old", Status: 'R'}}}
		change(&r)
		c := fileTestClient(t, func(rpc.DesktopCall) (any, error) { return r, nil })
		if _, err := c.GitCommitFiles(t.Context(), rpc.GitCommitFilesParams{Path: p.Path, Expected: p.Expected, Hash: commit.Hash}); err == nil {
			t.Fatalf("bad detail %+v", r)
		}
	}
}
