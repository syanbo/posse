package rpc

const RemoteProtocol = 1

var RemoteCapabilities = []string{
	"rpc.v1",
	"terminal.stream.v1",
	"desktop.control.v1",
	"files.v1",
	"git.v1",
}

// RemoteFeatures 按目标平台与构建形态宣告节点可用特性（可选字段，客户端据此隐藏不可用入口）。
func RemoteFeatures(goos, edition string) []string {
	switch goos {
	case "darwin":
		if edition == "headless" {
			return []string{"awake.v1", "media.thumbs.v1", "mobile.v1", "procmem.v1", "tailscale.v1", "trash.v1"}
		}
		return []string{"awake.v1", "desk.v1", "media.thumbs.v1", "mobile.v1", "procmem.v1", "tailscale.v1", "trash.v1"}
	case "linux":
		return []string{"mobile.v1", "tailscale.v1"}
	default:
		return nil
	}
}

type RemoteInfo struct {
	Protocol     int      `json:"protocol"`
	InstanceID   string   `json:"instanceId"`
	Platform     string   `json:"platform"`
	Arch         string   `json:"arch"`
	Home         string   `json:"home"`
	Version      string   `json:"version"`
	Build        string   `json:"build"`
	Edition      string   `json:"edition,omitempty"`
	Capabilities []string `json:"capabilities"`
	Features     []string `json:"features,omitempty"`
}

// RemoteProbe 描述 helper 所在的安装；运行中 daemon 的版本和能力另经 daemon.info 查询。
type RemoteProbe struct {
	Protocol int    `json:"protocol"`
	Platform string `json:"platform"`
	Arch     string `json:"arch"`
	Home     string `json:"home"`
	Socket   string `json:"socket"`
	Version  string `json:"version"`
	Build    string `json:"build"`
	Edition  string `json:"edition,omitempty"`
	Running  bool   `json:"running"`
}
