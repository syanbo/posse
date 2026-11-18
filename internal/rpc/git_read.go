package rpc

import "posse/internal/gitx"

const GitReadLimit = 4 << 20

type GitRepositoryIdentity = gitx.RepositoryIdentity

type GitRepository struct {
	Root     string                `json:"root"`
	Real     string                `json:"real"`
	Aliases  []string              `json:"aliases,omitempty"`
	Identity GitRepositoryIdentity `json:"identity"`
}

type GitDiscoverParams struct {
	Path       string `json:"path"`
	SingleRepo bool   `json:"singleRepo"`
}

type GitDiscoverResult struct {
	RealPath string          `json:"realPath"`
	Repos    []GitRepository `json:"repos"`
	Partial  bool            `json:"partial"`
}

type GitStatusParams struct {
	Path        string `json:"path"`
	Full        bool   `json:"full"`
	LastMessage bool   `json:"lastMessage,omitempty"`
}

type GitStatusResult struct {
	LastMessage string                    `json:"lastMessage,omitempty"`
	Identity    GitRepositoryIdentity     `json:"identity"`
	Status      *gitx.Status              `json:"status"`
	Head        map[string]gitx.LineStats `json:"head,omitempty"`
	Stashes     []gitx.Stash              `json:"stashes,omitempty"`
}

type GitBranchesParams struct {
	Path string `json:"path"`
}

type GitBranchesResult struct {
	Identity GitRepositoryIdentity `json:"identity"`
	Branches []gitx.Branch         `json:"branches"`
	Tags     []string              `json:"tags"`
	Recent   []string              `json:"recent"`
}
