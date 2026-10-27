package daemon

import (
	"context"
	"log"
	"os"
	"slices"
	"strings"
	"sync"
	"time"

	"posse/internal/agent"
	"posse/internal/term"
)

const (
	// agentStatEvery 是只 stat 的间隔：新装进 PATH 的 agent 这么快就能出现在菜单里
	agentStatEvery = 30 * time.Second
	// agentShellEvery 是重新起登录 shell 的间隔：要几百毫秒，alias 的增删和 PATH 的改动等它
	agentShellEvery = 10 * time.Minute
)

// agentScan 记着本机装了哪些 agent；installed 为 nil 是还没检测完。
type agentScan struct {
	mu        sync.Mutex
	installed []string
}

// installedAgents 是本机装了的 agent（按 agent.Known 的顺序），还没检测完时为 nil。
func (d *Daemon) installedAgents() []string {
	d.agents.mu.Lock()
	defer d.agents.mu.Unlock()
	return d.agents.installed
}

// agentInstalled 报告 name 有没有装；还没检测完时当作装了，不挡用户。
func (d *Daemon) agentInstalled(name string) bool {
	got := d.installedAgents()
	return got == nil || slices.Contains(got, name)
}

func (d *Daemon) detectLoop(ctx context.Context) {
	skip := agent.ShimDir(d.cfg.Home)
	var found []string
	var path string
	var shellAt time.Time
	tick := time.NewTicker(agentStatEvery)
	defer tick.Stop()
	for {
		if time.Since(shellAt) >= agentShellEvery {
			shellAt = time.Now()
			det, err := agent.DetectInShell(ctx, agent.UserShell(), d.detectEnv(skip))
			if err != nil {
				if ctx.Err() != nil {
					return
				}
				log.Printf("detect agents in login shell: %v", err)
				found, path = nil, agent.WithoutDir(os.Getenv("PATH"), skip)
			} else {
				found, path = det.Found, det.Path
			}
		}
		got := agent.Installed(found, path, skip)
		d.agents.mu.Lock()
		if !slices.Equal(got, d.agents.installed) {
			log.Printf("installed agents: %s", strings.Join(got, ", "))
		}
		d.agents.installed = got
		d.agents.mu.Unlock()
		select {
		case <-ctx.Done():
			return
		case <-tick.C:
		}
	}
}

// detectEnv 是检测用的 shell 的环境：和终端一样清理过，但不走 posse 的 zsh 集成，
// PATH 里也去掉转发脚本的目录，找到的才是真正的程序。
func (d *Daemon) detectEnv(skip string) []string {
	var env []string
	for _, kv := range term.Environ(nil) {
		k, v, _ := strings.Cut(kv, "=")
		switch k {
		case "ZDOTDIR", "POSSE_ZDOTDIR", "POSSE_USER_ZDOTDIR":
			continue
		case "PATH":
			kv = "PATH=" + agent.WithoutDir(v, skip)
		}
		env = append(env, kv)
	}
	if d.userZdotdir != "" {
		env = append(env, "ZDOTDIR="+d.userZdotdir)
	}
	return env
}
