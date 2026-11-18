package rpc

type FilePasteImageParams struct {
	Data []byte `json:"data"`
	Hash string `json:"hash"`
}

type FilePasteFileParams struct {
	Name string `json:"name"`
	Data []byte `json:"data"`
	Hash string `json:"hash"`
}

type FilePasteImageResult struct {
	Path string `json:"path"`
}
