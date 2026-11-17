package rpc

import "posse/internal/files"

type FileSymbolsParams struct {
	Root       string            `json:"root"`
	SingleRepo bool              `json:"singleRepo,omitempty"`
	Query      files.SymbolQuery `json:"query"`
	Overlay    map[string]string `json:"overlay,omitempty"`
}

type FileSymbolsResult struct {
	Symbols   []files.Symbol `json:"symbols"`
	Truncated bool           `json:"truncated"`
}

type FileDefinitionParams struct {
	Root       string            `json:"root"`
	SingleRepo bool              `json:"singleRepo,omitempty"`
	Path       string            `json:"path"`
	Text       string            `json:"text"`
	Offset     int               `json:"offset"`
	Overlay    map[string]string `json:"overlay,omitempty"`
}

type FileDefinitionResult struct {
	Path        string             `json:"path,omitempty"`
	Line        int                `json:"line,omitempty"`
	Definitions []files.Definition `json:"definitions,omitempty"`
}
