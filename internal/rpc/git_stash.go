package rpc

import (
	"encoding/json"
	"strings"
	"unicode/utf8"

	"posse/internal/gitx"
)

const (
	GitStashMessageLimit = 4096
	GitStashParamsLimit  = GitFetchParamsLimit + 6*GitStashMessageLimit
	GitStashResultLimit  = GitFetchResultLimit
	GitStashPush         = "push"
	GitStashApply        = "apply"
	GitStashPop          = "pop"
	GitStashDrop         = "drop"
)

type GitStashParams struct {
	GitFetchParams
	Action      string       `json:"action"`
	OID         string       `json:"oid,omitempty"`
	Head        string       `json:"head,omitempty"`
	IndexID     string       `json:"indexID,omitempty"`
	Entries     []gitx.Entry `json:"entries,omitempty"`
	Message     string       `json:"message,omitempty"`
	MessageHash string       `json:"messageHash,omitempty"`
}

type GitStashResult struct {
	Target        GitStashParams `json:"target"`
	Status        string         `json:"status"`
	Started       bool           `json:"started"`
	OID           string         `json:"oid,omitempty"`
	Error         *Error         `json:"error,omitempty"`
	Logs          []gitx.Run     `json:"logs"`
	LogsTruncated bool           `json:"logsTruncated"`
}

// 说明只保留摘要，原参数查询仍可核对，但操作记录不保存说明里的凭据。
func (p GitStashParams) Metadata() GitStashParams {
	p.Message = ""
	if len(p.Entries) == 0 {
		p.Entries = nil
	}
	return p
}

func ValidGitStashTarget(p GitStashParams) bool {
	if !ValidGitFetchParams(p.GitFetchParams) {
		return false
	}
	if p.Action != GitStashDrop && (p.Head != "" && !ValidGitBranchOID(p.Head) || !validCommitHash(p.IndexID)) {
		return false
	}
	switch p.Action {
	case GitStashPush:
		if p.OID != "" || !validCommitHash(p.MessageHash) || len(p.Message) > GitStashMessageLimit || !utf8.ValidString(p.Message) || strings.ContainsRune(p.Message, 0) || p.Message != "" && GitCommitHash([]byte(p.Message)) != p.MessageHash {
			return false
		}
		seen := map[string]bool{}
		for _, e := range p.Entries {
			if !ValidGitPath(strings.TrimSuffix(e.Path, "/")) || seen[e.Path] || e.Orig != "" && !ValidGitPath(e.Orig) {
				return false
			}
			seen[e.Path] = true
		}
	case GitStashApply, GitStashPop, GitStashDrop:
		if !ValidGitBranchOID(p.OID) || len(p.Entries) != 0 || p.Message != "" || p.MessageHash != "" {
			return false
		}
		if p.Action == GitStashDrop && (p.Head != "" || p.IndexID != "") {
			return false
		}
	default:
		return false
	}
	raw, err := json.Marshal(p.Metadata())
	return err == nil && len(raw) <= GitFetchParamsLimit
}

func ValidGitStashParams(p GitStashParams) bool {
	if !ValidGitStashTarget(p) || p.Action == GitStashPush && GitCommitHash([]byte(p.Message)) != p.MessageHash {
		return false
	}
	raw, err := json.Marshal(p)
	return err == nil && len(raw) <= GitStashParamsLimit
}

// reflog 只允许来自锁内的 stash list；客户端请求不携带这个名字。
func ValidGitStashRef(ref string) bool {
	index, ok := strings.CutPrefix(ref, "stash@{")
	if !ok || !strings.HasSuffix(index, "}") {
		return false
	}
	index = strings.TrimSuffix(index, "}")
	if index == "" || len(index) > 1 && index[0] == '0' {
		return false
	}
	for _, c := range index {
		if c < '0' || c > '9' {
			return false
		}
	}
	return true
}
