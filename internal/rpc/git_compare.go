package rpc

import "posse/internal/gitx"

type GitCompareParams struct {
	Path     string                `json:"path"`
	Expected GitRepositoryIdentity `json:"expected"`
	Current  string                `json:"current"`
	Other    string                `json:"other"`
	Limit    int                   `json:"limit"`
}

type GitCompareResult struct {
	Identity GitRepositoryIdentity `json:"identity"`
	Current  string                `json:"current"`
	Other    string                `json:"other"`
	gitx.CompareRead
}
