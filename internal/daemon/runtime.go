package daemon

import (
	"fmt"
	"log"
	"os"
	"path/filepath"

	"posse/internal/agent"
	"posse/internal/spawn"
)

// 常驻进程从 Posse.app 的副本运行（internal/spawn 的 Runtime），副本只在它拉起的终端全部退出后回收。
// 终端里会长期留着的 posse 路径（hooks 命令、statusLine、$POSSE_HOME/bin 的同名脚本）不写副本的路径，
// 写 PosseLink：每个常驻进程启动时把它指向自己。旧终端里后来手动启动的 agent 用的是当时的常驻进程，
// 那份副本可能先被回收，经这个链接调 posse hook 不会断。PATH 最前面仍是常驻进程自己的目录，
// 终端活着它就不会被回收。

// PosseLink 是 $POSSE_HOME/bin/posse，指向当前常驻进程的可执行文件。
func PosseLink(home string) string { return filepath.Join(agent.ShimDir(home), "posse") }

// linkPosse 把 PosseLink 原子地换成指向 exe。
func linkPosse(home, exe string) (string, error) {
	link := PosseLink(home)
	if cur, err := os.Readlink(link); err == nil && cur == exe {
		return link, nil
	}
	if err := os.MkdirAll(filepath.Dir(link), 0o700); err != nil {
		return "", err
	}
	tmp := fmt.Sprintf("%s.%d.tmp", link, os.Getpid())
	os.Remove(tmp)
	if err := os.Symlink(exe, tmp); err != nil {
		return "", err
	}
	if err := os.Rename(tmp, link); err != nil {
		os.Remove(tmp)
		return "", err
	}
	return link, nil
}

// liveRuntimes 是还活着的终端由哪些副本拉起，skip 是刚退出、还没摘掉的终端。
func (d *Daemon) liveRuntimes(skip string) []string {
	d.mu.Lock()
	defer d.mu.Unlock()
	var dirs []string
	for id, s := range d.terms {
		if id == skip || s.runtime == "" {
			continue
		}
		select {
		case <-s.Exited():
			continue
		default:
		}
		dirs = append(dirs, s.runtime)
	}
	return dirs
}

// collectRuntimes 回收这个 home 不再用的副本：常驻进程启动时、终端退出后调用。交接中和退出中不做：
// 终端正在交给新进程，或者留着等下次恢复。
func (d *Daemon) collectRuntimes(exited string) {
	if d.runtime == "" || d.handoff.active() || d.exiting.Load() {
		return
	}
	removed, err := spawn.Collect(d.runtime, d.cfg.Home, d.liveRuntimes(exited))
	if err != nil {
		log.Printf("runtime copies: %v", err)
	}
	for _, dir := range removed {
		log.Printf("removed runtime copy %s", dir)
	}
}
