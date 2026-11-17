package daemon

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"runtime"

	"posse/internal/rpc"
)

func (d *Daemon) registerRemote(srv *rpc.Server) {
	if d.instanceID == "" {
		// 不进交接记录或磁盘：连接恢复后可据此辨认 daemon 是否已经换过。
		d.instanceID = rand.Text()
	}
	srv.Handle("daemon.info", d.remoteInfo)
}

// RemoteHome 在目标机器上解析身份路径，避免 /tmp 与 /private/tmp 被当作两个实例。
func RemoteHome(home string) (string, error) {
	path, err := filepath.EvalSymlinks(home)
	if errors.Is(err, os.ErrNotExist) {
		return filepath.Clean(home), nil
	}
	return path, err
}

func (d *Daemon) remoteInfo(context.Context, json.RawMessage) (any, error) {
	home, err := RemoteHome(d.cfg.Home)
	if err != nil {
		return nil, err
	}
	return rpc.RemoteInfo{
		Protocol:     rpc.RemoteProtocol,
		InstanceID:   d.instanceID,
		Platform:     runtime.GOOS,
		Arch:         runtime.GOARCH,
		Home:         home,
		Version:      Version,
		Build:        Build,
		Capabilities: []string{"rpc.v1", "terminal.stream.v1"},
	}, nil
}
