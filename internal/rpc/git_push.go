package rpc

import (
	"encoding/json"
	"strings"

	"posse/internal/gitx"
)

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

const (
	GitPushParamsLimit = GitFetchParamsLimit
	GitPushResultLimit = GitFetchResultLimit
)

// 目标只能来自已显示的预览，不接受 URL 或任意 refspec。
type GitPushParams struct {
	GitFetchParams
	Branch          string `json:"branch"`
	Remote          string `json:"remote"`
	RemoteBranch    string `json:"remoteBranch"`
	Baseline        string `json:"baseline"`
	BranchOID       string `json:"branchOID"`
	TrackingRef     string `json:"trackingRef"`
	TrackingOID     string `json:"trackingOID"`
	TrackingMissing bool   `json:"trackingMissing"`
	Force           bool   `json:"force"`
	Tags            bool   `json:"tags"`
}

type GitPushResult struct {
	Target GitPushParams `json:"target"`
	Status string        `json:"status"`
	// Started 为真后，失败、超时或取消都不保证远端引用没有更新。
	Started       bool       `json:"started"`
	Rejected      bool       `json:"rejected"`
	Error         *Error     `json:"error,omitempty"`
	Logs          []gitx.Run `json:"logs"`
	LogsTruncated bool       `json:"logsTruncated"`
}

func ValidGitPushParams(p GitPushParams) bool {
	if !ValidGitFetchParams(p.GitFetchParams) || !gitx.ValidPushBranch(p.Branch) || !gitx.ValidPushRemote(p.Remote) || !gitx.ValidPushBranch(p.RemoteBranch) || !gitx.ValidLogBaseline(p.Baseline) || !gitx.ValidBlobOID(p.BranchOID) || strings.Trim(p.BranchOID, "0") == "" || p.TrackingRef != "refs/remotes/"+p.Remote+"/"+p.RemoteBranch {
		return false
	}
	if p.TrackingMissing {
		if p.TrackingOID != "" {
			return false
		}
	} else if !gitx.ValidBlobOID(p.TrackingOID) || len(p.TrackingOID) != len(p.BranchOID) || strings.Trim(p.TrackingOID, "0") == "" {
		return false
	}
	b, err := json.Marshal(p)
	return err == nil && len(b) <= GitPushParamsLimit
}

func (p GitPushParams) Matches(preview gitx.PushPreviewRead) bool {
	return p.Branch == preview.Plan.Branch && p.Remote == preview.Plan.Remote && p.RemoteBranch == preview.Plan.RemoteBranch && p.Baseline == preview.Baseline && p.BranchOID == preview.BranchOID && p.TrackingRef == preview.TrackingRef && p.TrackingOID == preview.TrackingOID && p.TrackingMissing == preview.TrackingMissing
}
