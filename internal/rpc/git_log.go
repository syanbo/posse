package rpc

import "posse/internal/gitx"

type GitLogParams struct {
	Path     string                `json:"path"`
	Expected GitRepositoryIdentity `json:"expected"`
	Filter   gitx.LogFilter        `json:"filter"`
	Skip     int                   `json:"skip"`
	Limit    int                   `json:"limit"`
	Baseline string                `json:"baseline"`
}
type GitLogResult struct {
	Identity GitRepositoryIdentity `json:"identity"`
	Commits  []gitx.CommitInfo     `json:"commits"`
	Baseline string                `json:"baseline"`
	HasMore  bool                  `json:"hasMore"`
	NextSkip int                   `json:"nextSkip"`
}
type GitCommitFilesParams struct {
	FullMessage bool                  `json:"fullMessage,omitempty"`
	Path        string                `json:"path"`
	Expected    GitRepositoryIdentity `json:"expected"`
	Hash        string                `json:"hash"`
}
type GitCommitFilesResult struct {
	FullMessage string                `json:"fullMessage,omitempty"`
	Identity    GitRepositoryIdentity `json:"identity"`
	Commit      gitx.CommitInfo       `json:"commit"`
	Files       []gitx.FileChange     `json:"files"`
	DiffBase    string                `json:"diffBase"`
	Shallow     bool                  `json:"shallow"`
}
