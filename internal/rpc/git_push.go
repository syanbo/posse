package rpc

import "posse/internal/gitx"

type GitPushPreviewParams struct {
	Path     string                `json:"path"`
	Expected GitRepositoryIdentity `json:"expected"`
	Branch   string                `json:"branch"`
	Limit    int                   `json:"limit"`
}

type GitPushPreviewResult struct {
	Identity GitRepositoryIdentity `json:"identity"`
	gitx.PushPreviewRead
}
