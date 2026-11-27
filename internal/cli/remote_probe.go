package cli

import (
	"encoding/json"
	"flag"
	"fmt"
	"runtime"

	"posse/internal/daemon"
	"posse/internal/rpc"
)

func cmdRemoteProbe(e *env, args []string) error {
	fs := flag.NewFlagSet("remote probe", flag.ContinueOnError)
	start := fs.Bool("start", false, "start the daemon if it is not running")
	pos, err := parse(fs, args)
	if err != nil {
		return err
	}
	if len(pos) != 0 {
		return fmt.Errorf("usage: posse remote probe [--start]")
	}
	if *start {
		if err := e.ensureDaemon(); err != nil {
			return err
		}
	}
	home, err := daemon.RemoteHome(e.home)
	if err != nil {
		return err
	}
	// 只检查 socket，旧 daemon 没有 daemon.info 也能发现；兼容性由隧道另一端判断。
	probe := rpc.RemoteProbe{
		Protocol: rpc.RemoteProtocol,
		Platform: runtime.GOOS,
		Arch:     runtime.GOARCH,
		Home:     home,
		Socket:   e.socket,
		Version:  daemon.Version,
		Build:    daemon.Build,
		Edition:  daemon.Edition,
		Running:  e.daemonUp(),
	}
	data, err := json.Marshal(probe)
	if err != nil {
		return err
	}
	_, err = fmt.Fprintf(e.out, "POSSE_REMOTE_V1:%s\n", data)
	return err
}
