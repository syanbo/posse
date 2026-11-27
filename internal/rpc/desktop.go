package rpc

import "encoding/json"

// DesktopIdentity 只在当前 daemon 的当前控制代次有效，不随交接保存。
type DesktopIdentity struct {
	ClientID   string `json:"clientId"`
	InstanceID string `json:"instanceId"`
	Epoch      uint64 `json:"epoch"`
}

type DesktopRequest struct {
	InstanceID string `json:"instanceId"`
	ClientID   string `json:"clientId"`
	Label      string `json:"label,omitempty"`
	Epoch      uint64 `json:"epoch"`
	// Automatic 只在新建本机窗口时使用；已有控制者时不抢占。
	Automatic bool `json:"automatic,omitempty"`
}

type DesktopState struct {
	InstanceID string `json:"instanceId"`
	Epoch      uint64 `json:"epoch"`
	Owner      string `json:"owner,omitempty"`
	Label      string `json:"label,omitempty"`
	ExpiresAt  int64  `json:"expiresAt,omitempty"`
}

func (s DesktopState) Identity(clientID string) DesktopIdentity {
	return DesktopIdentity{ClientID: clientID, InstanceID: s.InstanceID, Epoch: s.Epoch}
}

const (
	CodeReadOnly       = "read_only"
	CodeBusy           = "busy"
	CodeOutcomeUnknown = "outcome_unknown"
)

// DesktopMethod 只开放已核对的桌面入口；worker/hook 和终端输入仍走各自协议。
func DesktopMethod(method string) (write, allowed bool) {
	switch method {
	case "daemon.info", "daemon.status", "daemon.memory", "server.autostart.status", "project.list", "workspace.list", "workspace.show", "workspace.current", "workspace.diff", "usage.get",
		"terminal.list", "terminal.show", "terminal.read", "terminal.wait", "terminal.cheatsheet", "task.list", "task.show", "git.trace", "git.discover", "git.status", "git.branches", "git.file", "git.blobRange", "git.log", "git.commitFiles", "git.blame", "git.compare", "git.worktreeCompare", "git.pushPreview", "git.divergence", "git.remotesContaining",
		"history.list", "history.search", "history.show", "session.tools", "media.list", "media.path", "files.entry", "files.version", "files.stat", "files.readDirs", "files.readRange", "files.resourceRange", "files.resourceVersion", "files.list", "files.search", "files.symbols", "files.definition":
		return false, true
	case "git.history", "git.integrate", "git.stash", "git.commit", "git.paths", "git.branch", "git.update", "git.push", "git.fetch", "chat.ensure", "project.add", "project.set", "project.rm", "workspace.create", "workspace.rm", "workspace.relocate", "workspace.merge",
		"terminal.create", "terminal.clear", "terminal.rename", "terminal.seen", "terminal.reclaimSize", "terminal.close",
		"task.start", "task.reply", "task.send", "task.close", "task.clear", "history.resume", "history.trash", "files.save", "files.mkdir", "files.trash", "files.pasteImage", "files.pasteFile",
		"awake.set", "usage.refresh":
		return true, true
	default:
		return false, false
	}
}

// DesktopCall 的序号在同一控制代次内不可复用；参数仅在执行期间保留。
type DesktopCall struct {
	Desktop  DesktopIdentity `json:"desktop"`
	Sequence uint64          `json:"sequence"`
	Method   string          `json:"method"`
	Params   json.RawMessage `json:"params,omitempty"`
}

type DesktopOperationRef struct {
	Desktop  DesktopIdentity `json:"desktop"`
	Sequence uint64          `json:"sequence"`
}

// State 为 running、done 或 unknown；unknown 不能解释为从未执行。
type DesktopOperation struct {
	DesktopOperationRef
	Method     string          `json:"method,omitempty"`
	State      string          `json:"state"`
	StartedAt  int64           `json:"startedAt,omitempty"`
	FinishedAt int64           `json:"finishedAt,omitempty"`
	Result     json.RawMessage `json:"result,omitempty"`
	Error      *Error          `json:"error,omitempty"`
}
