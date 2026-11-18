package rpc

import (
	"encoding/json"
	"posse/internal/gitx"
	"slices"
	"strings"
	"unicode/utf8"
)

const (
	GitHistoryParamsLimit = GitFetchParamsLimit + 6*GitCommitMessageLimit
	GitHistoryResultLimit = GitFetchResultLimit
	GitHistoryReset       = "reset"
	GitHistoryReword      = "reword"
	GitHistorySquash      = "squash"
	GitHistoryDrop        = "drop"
)

type GitHistoryParams struct {
	GitFetchParams
	Action      string   `json:"action"`
	OID         string   `json:"oid"`
	Head        string   `json:"head"`
	Current     string   `json:"current,omitempty"`
	Mode        string   `json:"mode,omitempty"`
	Message     string   `json:"message,omitempty"`
	MessageHash string   `json:"messageHash,omitempty"`
	Remotes     []string `json:"remotes,omitempty"`
	Confirmed   bool     `json:"confirmed,omitempty"`
}

type GitHistoryResult struct {
	Target        GitHistoryParams    `json:"target"`
	Status        string              `json:"status"`
	Started       bool                `json:"started"`
	Autostash     *gitx.AutostashLeft `json:"autostash,omitempty"`
	Error         *Error              `json:"error,omitempty"`
	Logs          []gitx.Run          `json:"logs"`
	LogsTruncated bool                `json:"logsTruncated"`
}

// 操作回执只保留说明摘要；查询不会重传正文。
func (p GitHistoryParams) Metadata() GitHistoryParams {
	p.Message = ""
	if len(p.Remotes) == 0 {
		p.Remotes = nil
	}
	return p
}

func ValidGitHistoryTarget(p GitHistoryParams) bool {
	if !ValidGitFetchParams(p.GitFetchParams) || !ValidGitBranchOID(p.Head) || !ValidGitBranchOID(p.OID) || p.Current != "" && !gitx.ValidPushBranch(p.Current) {
		return false
	}
	if !slices.IsSorted(p.Remotes) {
		return false
	}
	for i, name := range p.Remotes {
		if !gitx.ValidPushBranch(name) || strings.HasSuffix(name, "/HEAD") || i > 0 && p.Remotes[i-1] == name {
			return false
		}
	}
	switch p.Action {
	case GitHistoryReset:
		if p.Mode != "soft" && p.Mode != "mixed" && p.Mode != "hard" {
			return false
		}
	case GitHistoryReword, GitHistorySquash, GitHistoryDrop:
		if p.Mode != "" {
			return false
		}
	default:
		return false
	}
	if p.Action == GitHistoryReword || p.Action == GitHistorySquash {
		if !validCommitHash(p.MessageHash) || len(p.Message) > GitCommitMessageLimit || !utf8.ValidString(p.Message) || strings.ContainsRune(p.Message, 0) || p.Message != "" && GitCommitHash([]byte(p.Message)) != p.MessageHash {
			return false
		}
	} else if p.Message != "" || p.MessageHash != "" {
		return false
	}
	raw, err := json.Marshal(p.Metadata())
	return err == nil && len(raw) <= GitFetchParamsLimit
}
func ValidGitHistoryParams(p GitHistoryParams) bool {
	if !ValidGitHistoryTarget(p) {
		return false
	}
	if p.Action == GitHistoryReword || p.Action == GitHistorySquash {
		if strings.TrimSpace(p.Message) == "" || GitCommitHash([]byte(p.Message)) != p.MessageHash {
			return false
		}
	}
	raw, err := json.Marshal(p)
	return err == nil && len(raw) <= GitHistoryParamsLimit
}

type GitRemotesContainingParams struct {
	Path     string                `json:"path"`
	Expected GitRepositoryIdentity `json:"expected"`
	OID      string                `json:"oid"`
}
type GitRemotesContainingResult struct {
	Identity GitRepositoryIdentity `json:"identity"`
	OID      string                `json:"oid"`
	Remotes  []string              `json:"remotes"`
}
