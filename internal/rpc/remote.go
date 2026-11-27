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
			return []string{"autostart.v1", "autostart.write.v1", "awake.v1", "media.thumbs.v1", "memory.v1", "mobile.v1", "procmem.v1", "tailscale.v1", "trash.v1"}
		}
		return []string{"autostart.v1", "autostart.write.v1", "awake.v1", "desk.v1", "media.thumbs.v1", "memory.v1", "mobile.v1", "procmem.v1", "tailscale.v1", "trash.v1"}
	case "linux":
		return []string{"autostart.v1", "autostart.write.v1", "memory.v1", "mobile.v1", "procmem.v1", "tailscale.v1"}
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
	Managed      bool     `json:"managed,omitempty"`
	ServerTag    string   `json:"serverTag,omitempty"`
	UserHome     string   `json:"userHome,omitempty"`
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

const (
	AutostartNoteAquaInactive          = "aqua_inactive"
	AutostartNoteSystemdBusInactive    = "systemd_bus_inactive"
	AutostartNoteSystemdLingerDisabled = "systemd_linger_disabled"
)

// AutostartStatus 描述目标主机的开机自启服务状态（macOS LaunchAgent 或 Linux systemd --user）。
type AutostartStatus struct {
	Supported          bool   `json:"supported"`
	Kind               string `json:"kind,omitempty"`
	Managed            bool   `json:"managed,omitempty"`
	Writable           bool   `json:"writable,omitempty"`
	Installed          bool   `json:"installed"`
	Enabled            bool   `json:"enabled"`
	Active             bool   `json:"active"`
	Unit               string `json:"unit,omitempty"`
	Path               string `json:"path,omitempty"`
	TimerPath          string `json:"timerPath,omitempty"`
	ExpectedExecutable string `json:"expectedExecutable,omitempty"`
	Linger             string `json:"linger,omitempty"`
	NoteCode           string `json:"noteCode,omitempty"`
}

type AutostartSetParams struct {
	Enabled *bool `json:"enabled"`
}

type DaemonMemoryParams struct {
	Expanded []string `json:"expanded,omitempty"`
}

type DaemonMemoryResult struct {
	TotalKB    int64                   `json:"totalKB"`
	Workspaces []DaemonMemoryWorkspace `json:"workspaces"`
	Truncated  bool                    `json:"truncated,omitempty"`
}

type DaemonMemoryWorkspace struct {
	ID        string                 `json:"id"`
	Label     string                 `json:"label"`
	KB        int64                  `json:"kb"`
	Terminals []DaemonMemoryTerminal `json:"terminals"`
}

type DaemonMemoryTerminal struct {
	ID    string             `json:"id"`
	Title string             `json:"title"`
	Agent string             `json:"agent,omitempty"`
	KB    int64              `json:"kb"`
	Procs []DaemonMemoryProc `json:"procs,omitempty"`
}

type DaemonMemoryProc struct {
	Name  string `json:"name"`
	KB    int64  `json:"kb"`
	Count int    `json:"count"`
}
