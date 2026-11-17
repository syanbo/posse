package rpc

import "posse/internal/files"

type FileListParams struct {
	Root       string `json:"root"`
	SingleRepo bool   `json:"singleRepo,omitempty"`
	All        bool   `json:"all,omitempty"`
}

type FileSearchParams struct {
	Root       string            `json:"root"`
	SingleRepo bool              `json:"singleRepo,omitempty"`
	Query      files.Query       `json:"query"`
	Overlay    map[string]string `json:"overlay,omitempty"`
}

type FileSearchResult struct {
	Hits      []files.Hit `json:"hits"`
	Truncated bool        `json:"truncated"`
}
