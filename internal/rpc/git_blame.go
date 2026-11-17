package rpc

import (
	"posse/internal/files"
	"posse/internal/gitx"
)

type GitBlameParams struct {
	Path     string                `json:"path"`
	Expected GitRepositoryIdentity `json:"expected"`
	File     string                `json:"file"`
}

type GitBlameResult struct {
	Identity GitRepositoryIdentity `json:"identity"`
	File     string                `json:"file"`
	Head     string                `json:"head"`
	Version  files.Version         `json:"version"`
	Lines    []gitx.BlameLine      `json:"lines"`
}
