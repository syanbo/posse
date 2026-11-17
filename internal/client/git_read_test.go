package client

import (
	"errors"
	"strings"
	"testing"

	"posse/internal/gitx"
	"posse/internal/rpc"
)

func testGitIdentity() rpc.GitRepositoryIdentity {
	return rpc.GitRepositoryIdentity{Root: "/target/repo", GitDir: "/target/repo/.git", CommonDir: "/target/repo/.git", RootInode: 1, GitInode: 2, CommonInode: 2}
}

func TestGitReadClient(t *testing.T) {
	id := testGitIdentity()
	calls := 0
	c := fileTestClient(t, func(p rpc.DesktopCall) (any, error) {
		calls++
		if p.Sequence != 0 {
			t.Fatal("read became write")
		}
		switch p.Method {
		case "git.discover":
			var params rpc.GitDiscoverParams
			if err := rpc.Decode(p.Params, &params); err != nil || params.Path != "/target/repo" || !params.SingleRepo {
				t.Fatalf("discover params: %+v %v", params, err)
			}
			return rpc.GitDiscoverResult{RealPath: id.Root, Repos: []rpc.GitRepository{{Root: id.Root, Real: id.Root, Identity: id}}}, nil
		case "git.status":
			var params rpc.GitStatusParams
			if err := rpc.Decode(p.Params, &params); err != nil || !params.Full {
				t.Fatalf("status params: %+v %v", params, err)
			}
			return rpc.GitStatusResult{Identity: id, Status: &gitx.Status{Head: "main"}}, nil
		case "git.branches":
			return rpc.GitBranchesResult{Identity: id, Branches: []gitx.Branch{{Name: "main", Current: true}}, Recent: []string{"main"}}, nil
		}
		t.Fatalf("unexpected method: %s", p.Method)
		return nil, nil
	})
	if _, err := c.GitDiscover(t.Context(), rpc.GitDiscoverParams{Path: id.Root, SingleRepo: true}); err != nil {
		t.Fatal(err)
	}
	if _, err := c.GitStatus(t.Context(), rpc.GitStatusParams{Path: id.Root, Full: true}); err != nil {
		t.Fatal(err)
	}
	if _, err := c.GitBranches(t.Context(), rpc.GitBranchesParams{Path: id.Root}); err != nil {
		t.Fatal(err)
	}
	if calls != 3 {
		t.Fatalf("calls=%d", calls)
	}
}

func TestGitReadClientRejects(t *testing.T) {
	for _, remote := range []bool{false, true} {
		c := &Client{Remote: remote, Socket: "/missing"}
		_, e1 := c.GitDiscover(t.Context(), rpc.GitDiscoverParams{Path: "/repo"})
		_, e2 := c.GitStatus(t.Context(), rpc.GitStatusParams{Path: "/repo"})
		_, e3 := c.GitBranches(t.Context(), rpc.GitBranchesParams{Path: "/repo"})
		for _, err := range []error{e1, e2, e3} {
			var re *rpc.Error
			if !errors.As(err, &re) || re.Code != rpc.CodeReadOnly {
				t.Fatalf("missing identity: %v", err)
			}
		}
	}
	calls := 0
	c := fileTestClient(t, func(p rpc.DesktopCall) (any, error) {
		calls++
		return nil, rpc.Errorf(rpc.CodeUnknown, "target Git not supported")
	})
	for _, path := range []string{"relative", "/nul\x00", "/" + strings.Repeat("a", 4096)} {
		if _, err := c.GitStatus(t.Context(), rpc.GitStatusParams{Path: path}); err == nil {
			t.Fatal("invalid path accepted")
		}
	}
	if calls != 0 {
		t.Fatal("invalid path reached target")
	}
	for _, call := range []func() error{
		func() error { _, err := c.GitDiscover(t.Context(), rpc.GitDiscoverParams{Path: "/repo"}); return err },
		func() error { _, err := c.GitStatus(t.Context(), rpc.GitStatusParams{Path: "/repo"}); return err },
		func() error { _, err := c.GitBranches(t.Context(), rpc.GitBranchesParams{Path: "/repo"}); return err },
	} {
		err := call()
		var re *rpc.Error
		if !errors.As(err, &re) || re.Code != rpc.CodeUnknown || re.Message != "target Git not supported" {
			t.Fatalf("lost target error: %v", err)
		}
	}
	if calls != 3 {
		t.Fatalf("fallback or replay: %d calls", calls)
	}
}

func TestGitReadClientInvalidResponses(t *testing.T) {
	for _, path := range []string{"", "relative", "/nul\x00", "/a/../b"} {
		c := fileTestClient(t, func(rpc.DesktopCall) (any, error) {
			return rpc.GitDiscoverResult{RealPath: path}, nil
		})
		if _, err := c.GitDiscover(t.Context(), rpc.GitDiscoverParams{Path: "/repo"}); err == nil {
			t.Fatalf("invalid discovery real path accepted: %q", path)
		}
	}
	for _, path := range []string{"", "relative", "/nul\x00"} {
		id := testGitIdentity()
		id.GitDir = path
		c := fileTestClient(t, func(rpc.DesktopCall) (any, error) {
			return rpc.GitStatusResult{Identity: id, Status: &gitx.Status{}}, nil
		})
		if _, err := c.GitStatus(t.Context(), rpc.GitStatusParams{Path: "/repo"}); err == nil {
			t.Fatalf("bad gitdir accepted: %q", path)
		}
	}
	c := fileTestClient(t, func(rpc.DesktopCall) (any, error) {
		return rpc.GitStatusResult{Identity: testGitIdentity(), Status: &gitx.Status{Head: strings.Repeat("x", rpc.GitReadLimit)}}, nil
	})
	if _, err := c.GitStatus(t.Context(), rpc.GitStatusParams{Path: "/repo"}); err == nil {
		t.Fatal("oversized response accepted")
	}
	c = fileTestClient(t, func(rpc.DesktopCall) (any, error) {
		return rpc.GitBranchesResult{Identity: testGitIdentity(), Recent: make([]string, 7)}, nil
	})
	if _, err := c.GitBranches(t.Context(), rpc.GitBranchesParams{Path: "/repo"}); err == nil {
		t.Fatal("invalid recent list accepted")
	}
}
