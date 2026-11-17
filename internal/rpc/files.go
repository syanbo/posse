package rpc

import "posse/internal/files"

const (
	FileBatchLimit       = 128
	CodePermissionDenied = "permission_denied"
)

// FilePathsParams 的路径属于目标主机；不按客户端工作目录或用户 home 展开。
type FilePathsParams struct {
	Paths []string `json:"paths"`
}

type FileStatResult struct {
	Path  string      `json:"path"`
	Stamp files.Stamp `json:"stamp"`
	Error *Error      `json:"error,omitempty"`
}

type FileDirectoryResult struct {
	Path    string        `json:"path"`
	Entries []files.Entry `json:"entries"`
	Stamp   files.Stamp   `json:"stamp"`
	Error   *Error        `json:"error,omitempty"`
}

type FileRangeParams struct {
	Path     string       `json:"path"`
	Offset   int64        `json:"offset"`
	Limit    int          `json:"limit"`
	Expected *files.Stamp `json:"expected,omitempty"`
}

type FileRangeResult struct {
	Data  []byte      `json:"data"`
	Stamp files.Stamp `json:"stamp"`
	EOF   bool        `json:"eof"`
}
