package rpc

import "posse/internal/gitx"

type GitWorktreeCompareParams struct {
	Path     string                `json:"path"`
	Expected GitRepositoryIdentity `json:"expected"`
	Revision string                `json:"revision"`
	Baseline string                `json:"baseline,omitempty"`
}

type GitWorktreeCompareResult struct {
	Identity GitRepositoryIdentity `json:"identity"`
	Revision string                `json:"revision"`
	gitx.WorktreeComparison
}
