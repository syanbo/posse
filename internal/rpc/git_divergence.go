package rpc

import "posse/internal/gitx"

type GitDivergenceParams struct {
	Path             string                `json:"path"`
	Expected         GitRepositoryIdentity `json:"expected"`
	BaseRef          string                `json:"baseRef"`
	PreviousBaseline string                `json:"previousBaseline,omitempty"`
}

type GitDivergenceResult struct {
	Identity GitRepositoryIdentity `json:"identity"`
	BaseRef  string                `json:"baseRef"`
	gitx.DivergenceRead
}
