package client

import (
	"context"
	"encoding/json"
	"errors"
	"path/filepath"
	"strings"

	"posse/internal/rpc"
)

func gitReadPath(path string) bool {
	return filepath.IsAbs(path) && !strings.ContainsRune(path, 0) && len(path) <= 4096
}

func (c *Client) gitRead(ctx context.Context, method, path string, params, out any) error {
	if c.Desktop == nil {
		return rpc.Errorf(rpc.CodeReadOnly, "desktop identity required for Git queries")
	}
	if !gitReadPath(path) {
		return rpc.Errorf(rpc.CodeBadRequest, "file path must be absolute and contain no NUL")
	}
	if err := c.Call(ctx, method, params, out); err != nil {
		return err
	}
	encoded, err := json.Marshal(out)
	if err != nil || len(encoded) > rpc.GitReadLimit {
		return errors.New("invalid Git query response")
	}
	return nil
}

func validGitIdentity(id rpc.GitRepositoryIdentity) bool {
	for _, path := range []string{id.Root, id.GitDir, id.CommonDir} {
		if !gitReadPath(path) || filepath.Clean(path) != path {
			return false
		}
	}
	return id.RootInode != 0 && id.GitInode != 0 && id.CommonInode != 0
}

func (c *Client) GitDiscover(ctx context.Context, p rpc.GitDiscoverParams) (rpc.GitDiscoverResult, error) {
	var out rpc.GitDiscoverResult
	if err := c.gitRead(ctx, "git.discover", p.Path, p, &out); err != nil {
		return rpc.GitDiscoverResult{}, err
	}
	if !gitReadPath(out.RealPath) || filepath.Clean(out.RealPath) != out.RealPath {
		return rpc.GitDiscoverResult{}, errors.New("invalid Git query response")
	}
	for _, repo := range out.Repos {
		if !gitReadPath(repo.Root) || !gitReadPath(repo.Real) || !validGitIdentity(repo.Identity) || repo.Real != repo.Identity.Root {
			return rpc.GitDiscoverResult{}, errors.New("invalid Git query response")
		}
		for _, alias := range repo.Aliases {
			if !gitReadPath(alias) {
				return rpc.GitDiscoverResult{}, errors.New("invalid Git query response")
			}
		}
	}
	return out, nil
}

func (c *Client) GitStatus(ctx context.Context, p rpc.GitStatusParams) (rpc.GitStatusResult, error) {
	var out rpc.GitStatusResult
	if err := c.gitRead(ctx, "git.status", p.Path, p, &out); err != nil {
		return rpc.GitStatusResult{}, err
	}
	if !validGitIdentity(out.Identity) || out.Status == nil {
		return rpc.GitStatusResult{}, errors.New("invalid Git query response")
	}
	return out, nil
}

func (c *Client) GitBranches(ctx context.Context, p rpc.GitBranchesParams) (rpc.GitBranchesResult, error) {
	var out rpc.GitBranchesResult
	if err := c.gitRead(ctx, "git.branches", p.Path, p, &out); err != nil {
		return rpc.GitBranchesResult{}, err
	}
	if !validGitIdentity(out.Identity) || len(out.Recent) > 6 {
		return rpc.GitBranchesResult{}, errors.New("invalid Git query response")
	}
	for _, b := range out.Branches {
		if b.Worktree != "" && !gitReadPath(b.Worktree) {
			return rpc.GitBranchesResult{}, errors.New("invalid Git query response")
		}
	}
	return out, nil
}
