package rpc

import (
	"encoding/json"
	"path/filepath"
	"strings"
	"unicode/utf8"

	"posse/internal/gitx"
)

const (
	GitFetchParamsLimit = 16 << 10
	GitFetchResultLimit = 128 << 10
	GitFetchLogLimit    = 4
	GitFetchSucceeded   = "succeeded"
	GitFetchFailed      = "failed"
	GitFetchCanceled    = "canceled"
	GitFetchTimedOut    = "timed_out"
	GitFetchConflict    = "conflict"
)

type GitFetchParams struct {
	WorkspaceID   string                `json:"workspaceID"`
	WorkspacePath string                `json:"workspacePath"`
	Expected      GitRepositoryIdentity `json:"expected"`
}

type GitFetchResult struct {
	Target GitFetchParams `json:"target"`
	Status string         `json:"status"`
	// Started 表示已进入 fetch，失败时也可能更新了部分引用，不表示回滚。
	Started       bool       `json:"started"`
	Error         *Error     `json:"error,omitempty"`
	Logs          []gitx.Run `json:"logs"`
	LogsTruncated bool       `json:"logsTruncated"`
}

func ValidGitFetchParams(p GitFetchParams) bool {
	if p.WorkspaceID == "" || len(p.WorkspaceID) > 128 || !utf8.ValidString(p.WorkspaceID) || strings.ContainsRune(p.WorkspaceID, 0) {
		return false
	}
	for _, path := range []string{p.WorkspacePath, p.Expected.Root, p.Expected.GitDir, p.Expected.CommonDir} {
		if !filepath.IsAbs(path) || len(path) > 4096 || !utf8.ValidString(path) || strings.ContainsRune(path, 0) {
			return false
		}
	}
	if p.Expected.RootInode == 0 || p.Expected.GitInode == 0 || p.Expected.CommonInode == 0 {
		return false
	}
	b, err := json.Marshal(p)
	return err == nil && len(b) <= GitFetchParamsLimit
}
