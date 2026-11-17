package rpc

// ChatLocation 的路径和工作区 ID 都属于提供它的目标主机。
type ChatLocation struct {
	Path        string `json:"path"`
	WorkspaceID string `json:"workspaceId,omitempty"`
}

type ChatEnsureParams struct {
	ExpectedPath string `json:"expectedPath"`
	Name         string `json:"name"`
}
