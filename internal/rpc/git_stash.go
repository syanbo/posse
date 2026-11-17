package rpc

import (
 "encoding/json"
 "strings"
 "unicode/utf8"

 "posse/internal/gitx"
)

// GitWriteTarget 与 Fetch 保持相同的工作区登记和仓库身份约束。
type GitWriteTarget = GitFetchParams

type GitStashStateResult struct {
 Target GitWriteTarget `json:"target"`
 Baseline gitx.StashBaseline `json:"baseline"`
}
type GitStashPushParams struct {
 Target GitWriteTarget `json:"target"`
 Baseline gitx.StashBaseline `json:"baseline"`
 Message string `json:"message"`
}
type GitStashApplyParams struct {
 Target GitWriteTarget `json:"target"`
 Baseline gitx.StashBaseline `json:"baseline"`
 Hash string `json:"hash"`
}
type GitStashPopParams = GitStashApplyParams
type GitStashDropParams struct {
 Target GitWriteTarget `json:"target"`
 Hash string `json:"hash"`
}
type GitStashExpectation struct {
 Target GitWriteTarget `json:"target"`
 Action string `json:"action"`
 Hash string `json:"hash"`
}
type GitStashResult struct {
 GitFetchResult
 Action string `json:"action"`
 Hash string `json:"hash"`
 // Applied 只确认 apply 命令成功；false 不保证工作树没有部分变化。
 Applied bool `json:"applied"`
 DropState string `json:"dropState"`
}
const (
 GitStashParamsLimit = 16 << 10
 GitStashResultLimit = 128 << 10
 GitStashDropNotAttempted = "not_attempted"
 GitStashDropSucceeded = "succeeded"
 GitStashDropFailed = "failed"
 GitStashDropUnknown = "unknown"
)
func ValidGitStashExpectation(p GitStashExpectation) bool {
 if !ValidGitFetchParams(p.Target) {return false}
 switch p.Action {
 case "push": return p.Hash==""
 case "apply","pop","drop": return gitx.ValidStashHash(p.Hash)
 }
 return false
}
func ValidGitStashParams(action string, target GitWriteTarget, baseline gitx.StashBaseline, hash,message string) bool {
 if !ValidGitStashExpectation(GitStashExpectation{Target:target,Action:action,Hash:hash}) {return false}
 if action=="drop" {if baseline!=(gitx.StashBaseline{}) {return false}} else if !gitx.ValidStashBaseline(baseline) {return false}
 if action!="push" && message!="" || len(message)>4096 || !utf8.ValidString(message) || strings.ContainsRune(message,0) {return false}
 b,err:=json.Marshal(struct{Target GitWriteTarget; Baseline gitx.StashBaseline; Hash,Message string}{target,baseline,hash,message})
 return err==nil && len(b)<=GitStashParamsLimit
}
