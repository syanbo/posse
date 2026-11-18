package rpc

import (
	"encoding/json"
	"strings"

	"posse/internal/gitx"
)

const (
	GitBranchParamsLimit = GitFetchParamsLimit
	GitBranchResultLimit = GitFetchResultLimit
	GitBranchCheckout    = "checkout"
	GitBranchRevision    = "revision"
	GitBranchCreate      = "create"
	GitBranchDelete      = "delete"
	GitBranchRename      = "rename"
)

// LocalOID 为空明确表示点击远端分支时没有同名本地分支，不能在排队后改换检出方式。
type GitBranchParams struct {
	GitFetchParams
	Action      string `json:"action"`
	Branch      string `json:"branch,omitempty"`
	NewBranch   string `json:"newBranch,omitempty"`
	BranchOID   string `json:"branchOID,omitempty"`
	Remote      bool   `json:"remote,omitempty"`
	LocalOID    string `json:"localOID,omitempty"`
	Revision    string `json:"revision,omitempty"`
	RevisionOID string `json:"revisionOID,omitempty"`
	FromOID     string `json:"fromOID,omitempty"`
	Checkout    bool   `json:"checkout,omitempty"`
	Force       bool   `json:"force,omitempty"`
}

type GitBranchResult struct {
	Target        GitBranchParams `json:"target"`
	Status        string          `json:"status"`
	Started       bool            `json:"started"`
	OID           string          `json:"oid,omitempty"`
	Unmerged      bool            `json:"unmerged,omitempty"`
	Error         *Error          `json:"error,omitempty"`
	Logs          []gitx.Run      `json:"logs"`
	LogsTruncated bool            `json:"logsTruncated"`
}

func ValidGitBranchOID(oid string) bool {
	return gitx.ValidBlobOID(oid) && strings.Trim(oid, "0") != ""
}

func ValidGitBranchParams(p GitBranchParams) bool {
	if !ValidGitFetchParams(p.GitFetchParams) {
		return false
	}
	switch p.Action {
	case GitBranchCheckout, GitBranchDelete:
		if !gitx.ValidPushBranch(p.Branch) || !ValidGitBranchOID(p.BranchOID) || p.NewBranch != "" || p.Revision != "" || p.RevisionOID != "" || p.FromOID != "" || p.Checkout {
			return false
		}
		if p.Remote {
			remote, branch, ok := strings.Cut(p.Branch, "/")
			if !ok || !gitx.ValidPushRemote(remote) || !gitx.ValidPushBranch(branch) {
				return false
			}
		}
		if p.LocalOID != "" && (p.Action != GitBranchCheckout || !p.Remote || !ValidGitBranchOID(p.LocalOID) || len(p.LocalOID) != len(p.BranchOID)) {
			return false
		}
		if p.Force && (p.Action != GitBranchDelete || p.Remote) {
			return false
		}
	case GitBranchRevision:
		if !gitx.ValidReadRevision(p.Revision) || p.RevisionOID != "" && !ValidGitBranchOID(p.RevisionOID) || p.Branch != "" || p.NewBranch != "" || p.BranchOID != "" || p.Remote || p.LocalOID != "" || p.FromOID != "" || p.Checkout || p.Force {
			return false
		}
	case GitBranchCreate:
		if !gitx.ValidPushBranch(p.Branch) || !ValidGitBranchOID(p.FromOID) || p.NewBranch != "" || p.BranchOID != "" || p.Remote || p.LocalOID != "" || p.Revision != "" || p.RevisionOID != "" || p.Force {
			return false
		}
	case GitBranchRename:
		if !gitx.ValidPushBranch(p.Branch) || !gitx.ValidPushBranch(p.NewBranch) || p.Branch == p.NewBranch || !ValidGitBranchOID(p.BranchOID) || p.Remote || p.LocalOID != "" || p.Revision != "" || p.RevisionOID != "" || p.FromOID != "" || p.Checkout || p.Force {
			return false
		}
	default:
		return false
	}
	raw, err := json.Marshal(p)
	return err == nil && len(raw) <= GitBranchParamsLimit
}

// 命令只含已校验的名字和固定提交；解析出的修订 OID 由目标端填写。
func GitBranchArgs(p GitBranchParams, oid string) []string {
	switch p.Action {
	case GitBranchCheckout:
		if !p.Remote {
			return []string{"switch", p.Branch}
		}
		if p.LocalOID != "" {
			return []string{"switch", gitx.LocalOf(p.Branch)}
		}
		return []string{"switch", "--track", p.Branch}
	case GitBranchRevision:
		return []string{"switch", "--detach", oid}
	case GitBranchCreate:
		if p.Checkout {
			return []string{"switch", "-c", p.Branch, p.FromOID}
		}
		return []string{"branch", p.Branch, p.FromOID}
	case GitBranchDelete:
		if p.Remote {
			remote, branch, _ := strings.Cut(p.Branch, "/")
			return []string{"push", "--force-with-lease=refs/heads/" + branch + ":" + p.BranchOID, remote, "--delete", branch}
		}
		flag := "-d"
		if p.Force {
			flag = "-D"
		}
		return []string{"branch", flag, p.Branch}
	case GitBranchRename:
		return []string{"branch", "-m", p.Branch, p.NewBranch}
	}
	return nil
}
