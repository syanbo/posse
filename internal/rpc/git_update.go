package rpc

import (
	"encoding/json"
	"strings"

	"posse/internal/gitx"
)

const (
	GitUpdateParamsLimit = GitFetchParamsLimit
	GitUpdateResultLimit = GitFetchResultLimit
)

// Current 固定点击时的分支身份，排队期间不能转换成另一种更新动作。
type GitUpdateParams struct {
	GitFetchParams
	Branch    string `json:"branch"`
	BranchOID string `json:"branchOID"`
	Upstream  string `json:"upstream"`
	Current   bool   `json:"current"`
}

type GitUpdateResult struct {
	Target        GitUpdateParams     `json:"target"`
	Status        string              `json:"status"`
	Started       bool                `json:"started"`
	Incoming      int                 `json:"incoming"`
	Autostash     *gitx.AutostashLeft `json:"autostash,omitempty"`
	Error         *Error              `json:"error,omitempty"`
	Logs          []gitx.Run          `json:"logs"`
	LogsTruncated bool                `json:"logsTruncated"`
}

func ValidGitUpdateParams(p GitUpdateParams) bool {
	if !ValidGitFetchParams(p.GitFetchParams) || !gitx.ValidPushBranch(p.Branch) || !gitx.ValidBlobOID(p.BranchOID) || strings.Trim(p.BranchOID, "0") == "" || p.Upstream != "" && !gitx.ValidPushBranch(p.Upstream) {
		return false
	}
	if !p.Current && p.Upstream != "" {
		remote, branch, ok := strings.Cut(p.Upstream, "/")
		if !ok || !gitx.ValidPushRemote(remote) || !gitx.ValidPushBranch(branch) {
			return false
		}
	}
	b, err := json.Marshal(p)
	return err == nil && len(b) <= GitUpdateParamsLimit
}
