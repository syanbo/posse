package rpc

import "posse/internal/gitx"

type GitFileParams struct {
	Path     string                `json:"path"`
	Expected GitRepositoryIdentity `json:"expected"`
	File     string                `json:"file"`
	Source   gitx.FileSource       `json:"source"`
	Revision string                `json:"revision"`
}
type GitFileResult struct {
	Identity GitRepositoryIdentity `json:"identity"`
	File     gitx.FileVersion      `json:"file"`
}
type GitBlobRangeParams struct {
	Path     string                `json:"path"`
	Expected GitRepositoryIdentity `json:"expected"`
	OID      string                `json:"oid"`
	Offset   int64                 `json:"offset"`
	Limit    int                   `json:"limit"`
}
type GitBlobRangeResult struct {
	Identity GitRepositoryIdentity `json:"identity"`
	OID      string                `json:"oid"`
	Size     int64                 `json:"size"`
	Offset   int64                 `json:"offset"`
	Data     []byte                `json:"data"`
	EOF      bool                  `json:"eof"`
}
