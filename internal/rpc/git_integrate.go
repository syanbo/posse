package rpc

import (
	"encoding/json"
	"slices"
	"strings"

	"posse/internal/gitx"
)

const (
	GitIntegrateParamsLimit    = 64 << 10
	GitIntegrateResultLimit    = GitFetchResultLimit
	GitIntegrateMerge          = "merge"
	GitIntegrateRebase         = "rebase"
	GitIntegrateCheckoutRebase = "checkout-rebase"
	GitIntegrateCherryPick     = "cherry-pick"
	GitIntegrateRevert         = "revert"
	GitIntegrateContinue       = "continue"
	GitIntegrateSkip           = "skip"
	GitIntegrateAbort          = "abort"
)

type GitIntegrateParams struct {
	GitFetchParams
	Action    string   `json:"action"`
	Head      string   `json:"head"`
	Current   string   `json:"current,omitempty"`
	Branch    string   `json:"branch,omitempty"`
	BranchOID string   `json:"branchOID,omitempty"`
	Remote    bool     `json:"remote,omitempty"`
	OIDs      []string `json:"oids,omitempty"`
	Operation string   `json:"operation,omitempty"`
	IndexID   string   `json:"indexID,omitempty"`
}

type GitIntegrateResult struct {
	Target        GitIntegrateParams `json:"target"`
	Status        string             `json:"status"`
	Started       bool               `json:"started"`
	Error         *Error             `json:"error,omitempty"`
	Logs          []gitx.Run         `json:"logs"`
	LogsTruncated bool               `json:"logsTruncated"`
}

func ValidGitIntegrateParams(p GitIntegrateParams) bool {
	if !ValidGitFetchParams(p.GitFetchParams) || !ValidGitBranchOID(p.Head) || p.Current != "" && !gitx.ValidPushBranch(p.Current) {
		return false
	}
	branch := false
	commits := false
	progress := false
	switch p.Action {
	case GitIntegrateMerge, GitIntegrateRebase, GitIntegrateCheckoutRebase:
		branch = true
		if !gitx.ValidPushBranch(p.Branch) || !ValidGitBranchOID(p.BranchOID) || p.Action == GitIntegrateCheckoutRebase && p.Remote {
			return false
		}
		if p.Remote {
			remote, branch, ok := strings.Cut(p.Branch, "/")
			if !ok || !gitx.ValidPushRemote(remote) || !gitx.ValidPushBranch(branch) {
				return false
			}
		}
	case GitIntegrateCherryPick, GitIntegrateRevert:
		commits = true
		if len(p.OIDs) == 0 || len(p.OIDs) > 500 || p.Action == GitIntegrateRevert && len(p.OIDs) != 1 {
			return false
		}
		for _, oid := range p.OIDs {
			if !ValidGitBranchOID(oid) {
				return false
			}
		}
	case GitIntegrateContinue, GitIntegrateSkip, GitIntegrateAbort:
		progress = true
		if !slices.Contains([]string{"merge", "rebase", "cherry-pick", "revert"}, p.Operation) {
			return false
		}
		if p.Action == GitIntegrateContinue && !validCommitHash(p.IndexID) {
			return false
		}
	default:
		return false
	}
	if !branch && (p.Branch != "" || p.BranchOID != "" || p.Remote) || !commits && len(p.OIDs) != 0 || !progress && p.Operation != "" || p.Action != GitIntegrateContinue && p.IndexID != "" {
		return false
	}
	raw, err := json.Marshal(p)
	return err == nil && len(raw) <= GitIntegrateParamsLimit
}

// 参数在入队前校验，分支和提交还须在目标锁内复查；这里不引入额外 Git 动作。
func GitIntegrateArgs(p GitIntegrateParams) []string {
	switch p.Action {
	case GitIntegrateMerge:
		return []string{"merge", "--no-edit", "--", p.Branch}
	case GitIntegrateRebase:
		return []string{"rebase", p.Branch}
	case GitIntegrateCheckoutRebase:
		current := p.Current
		if current == "" {
			current = "HEAD"
		}
		return []string{"rebase", current, p.Branch}
	case GitIntegrateCherryPick:
		return append([]string{"cherry-pick"}, p.OIDs...)
	case GitIntegrateRevert:
		return []string{"revert", "--no-edit", p.OIDs[0]}
	case GitIntegrateContinue:
		if p.Operation == "merge" {
			return []string{"commit", "-F", "-"}
		}
		return []string{p.Operation, "--continue"}
	case GitIntegrateSkip:
		return []string{p.Operation, "--skip"}
	case GitIntegrateAbort:
		return []string{p.Operation, "--abort"}
	}
	return nil
}
