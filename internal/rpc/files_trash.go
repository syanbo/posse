package rpc

import "posse/internal/files"

type FileTrashParams struct {
	Path     string             `json:"path"`
	Expected files.EntryVersion `json:"expected"`
}

type FileTrashResult struct {
	Destination string `json:"destination"`
}
