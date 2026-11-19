package rpc

import "posse/internal/gitx"

// 冲突预测要两遍全量列出三棵树和属性，按三份均分（两遍输入和合并各一份），一份能容纳路径上限内的仓库。
const GitDivergenceReadLimit = 48 << 20

type GitDivergenceParams struct {
	Path             string                `json:"path"`
	Expected         GitRepositoryIdentity `json:"expected"`
	BaseRef          string                `json:"baseRef"`
	PreviousBaseline string                `json:"previousBaseline,omitempty"`
}

type GitDivergenceResult struct {
	Identity GitRepositoryIdentity `json:"identity"`
	BaseRef  string                `json:"baseRef"`
	gitx.DivergenceRead
}
