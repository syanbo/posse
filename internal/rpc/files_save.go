package rpc

import "posse/internal/files"

type FileVersionParams struct {
	Path string `json:"path"`
}

// 文件正文以 base64 传输，额外空间容纳路径、版本和编辑 revision。
const FileSaveRequestLimit = (files.MaxSize+2)/3*4 + (64 << 10)

type FileSaveParams struct {
	Path     string        `json:"path"`
	Expected files.Version `json:"expected"`
	Data     []byte        `json:"data"`
	Hash     string        `json:"hash"`
	Revision uint64        `json:"revision"`
}

type FileSaveResult struct {
	Version  files.Version `json:"version"`
	Revision uint64        `json:"revision"`
}
