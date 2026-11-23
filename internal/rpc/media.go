package rpc

import "posse/internal/media"

const MediaListLimit = 4 << 20

type MediaSource struct {
	Agent          string `json:"agent"`
	SessionID      string `json:"sessionId"`
	TranscriptPath string `json:"transcriptPath"`
}

type MediaListResult struct {
	Pasted  []media.Item `json:"pasted"`
	Made    []media.Item `json:"made"`
	MadeAll int          `json:"madeAll"`
	Found   bool         `json:"found"`
}

type MediaPathParams struct {
	Item     media.Item `json:"item"`
	Original bool       `json:"original,omitempty"`
}

type MediaPathResult struct {
	Path string `json:"path"`
}
