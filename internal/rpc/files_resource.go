package rpc

import "posse/internal/files"

// FileResourceVersionParams 与分段读取使用相同根限制；响应为 files.Version。
type FileResourceVersionParams struct {
	Root string `json:"root"`
	Path string `json:"path"`
}

// FileResourceRangeParams 的 Path 相对目标主机的 Root，解析软链后仍须位于根内。
type FileResourceRangeParams struct {
	Root     string       `json:"root"`
	Path     string       `json:"path"`
	Offset   int64        `json:"offset"`
	Limit    int          `json:"limit"`
	Expected *files.Stamp `json:"expected,omitempty"`
}
