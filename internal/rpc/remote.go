package rpc

const RemoteProtocol = 1

var RemoteCapabilities = []string{
	"rpc.v1",
	"terminal.stream.v1",
	"desktop.control.v1",
	"files.v1",
	"git.v1",
}

type RemoteInfo struct {
	Protocol     int      `json:"protocol"`
	InstanceID   string   `json:"instanceId"`
	Platform     string   `json:"platform"`
	Arch         string   `json:"arch"`
	Home         string   `json:"home"`
	Version      string   `json:"version"`
	Build        string   `json:"build"`
	Capabilities []string `json:"capabilities"`
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
	Running  bool   `json:"running"`
}
