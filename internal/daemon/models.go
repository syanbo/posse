package daemon

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"sync"
	"syscall"
	"time"

	"posse/internal/agent"
	"posse/internal/rpc"
	"posse/internal/term"
)

const (
	agentModelsCacheTTL    = 10 * time.Minute
	agentModelsErrCacheTTL = 30 * time.Second
	agentModelsFetchWait   = 15 * time.Second
)

type cachedModelList struct {
	list    agent.ModelList
	expires time.Time
}

type inflightModelFetch struct {
	done    chan struct{}
	cancel  context.CancelFunc
	waiters int
	gen     uint64
	refresh bool
	res     agent.ModelList
}

type agentModelsState struct {
	mu              sync.Mutex
	cache           map[string]cachedModelList
	lastGood        map[string]agent.ModelList
	cacheGen        map[string]uint64
	nextGen         map[string]uint64
	inflight        map[string]*inflightModelFetch
	inflightRefresh map[string]*inflightModelFetch
}

// cachedAgentModels 返回内存缓存中的模型目录（优先未过期缓存，过期时回退最近一次成功目录，不触发子进程）。
func (d *Daemon) cachedAgentModels(agentName string) (agent.ModelList, bool) {
	name := agent.NormalizeName(agentName)
	s := &d.models
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.cache != nil {
		if c, ok := s.cache[name]; ok && time.Now().Before(c.expires) && c.list.Error == "" && len(c.list.Models) > 0 {
			return c.list, true
		}
	}
	if s.lastGood != nil {
		if lg, ok := s.lastGood[name]; ok && len(lg.Models) > 0 {
			return lg, true
		}
	}
	return agent.ModelList{}, false
}

func (d *Daemon) agentModelsRPC(ctx context.Context, raw json.RawMessage) (any, error) {
	var p struct {
		Agent   string `json:"agent"`
		Refresh bool   `json:"refresh,omitempty"`
	}
	if len(raw) > 0 {
		if err := rpc.Decode(raw, &p); err != nil {
			return nil, err
		}
	}
	name := agent.NormalizeName(p.Agent)
	if name == "" {
		return nil, rpc.Errorf(rpc.CodeBadRequest, "agent is required")
	}
	return d.fetchAgentModels(ctx, name, p.Refresh), nil
}

func (d *Daemon) fetchAgentModels(ctx context.Context, name string, refresh bool) agent.ModelList {
	if !agent.CanJoinDiscussion(name) || d.checkEnabled(name) != nil || !d.agentInstalled(name) {
		return agent.ModelList{Agent: name}
	}

	s := &d.models
	s.mu.Lock()
	if !refresh && s.cache != nil {
		if c, ok := s.cache[name]; ok && time.Now().Before(c.expires) {
			s.mu.Unlock()
			return c.list
		}
	}
	if s.inflight == nil {
		s.inflight = map[string]*inflightModelFetch{}
	}
	if s.inflightRefresh == nil {
		s.inflightRefresh = map[string]*inflightModelFetch{}
	}
	if s.nextGen == nil {
		s.nextGen = map[string]uint64{}
	}
	if s.cacheGen == nil {
		s.cacheGen = map[string]uint64{}
	}

	var call *inflightModelFetch
	if refresh {
		call = s.inflightRefresh[name]
	} else {
		// 若已有更新的刷新在进行中，普通请求直接合流到该刷新请求
		if rf := s.inflightRefresh[name]; rf != nil {
			call = rf
		} else {
			call = s.inflight[name]
		}
	}
	if call == nil {
		fetchCtx, cancel := context.WithTimeout(context.Background(), agentModelsFetchWait)
		s.nextGen[name]++
		myGen := s.nextGen[name]
		call = &inflightModelFetch{
			done:    make(chan struct{}),
			cancel:  cancel,
			waiters: 1,
			gen:     myGen,
			refresh: refresh,
		}
		if refresh {
			s.inflightRefresh[name] = call
		} else {
			s.inflight[name] = call
		}
		go func(c *inflightModelFetch, fctx context.Context) {
			defer c.cancel()
			res := d.queryAgentModels(fctx, name)
			s.mu.Lock()
			c.res = res
			if c.refresh {
				if s.inflightRefresh[name] == c {
					delete(s.inflightRefresh, name)
				}
			} else {
				if s.inflight[name] == c {
					delete(s.inflight, name)
				}
			}
			// 仅在未被调用方主动取消且代次不落后于当前缓存时写入，避免旧查询覆盖刷新结果
			if (fctx.Err() == nil || fctx.Err() == context.DeadlineExceeded) && c.gen >= s.cacheGen[name] {
				if s.cache == nil {
					s.cache = map[string]cachedModelList{}
				}
				ttl := agentModelsCacheTTL
				if res.Error != "" {
					ttl = agentModelsErrCacheTTL
				}
				s.cache[name] = cachedModelList{list: res, expires: time.Now().Add(ttl)}
				s.cacheGen[name] = c.gen
				if res.Error == "" && len(res.Models) > 0 {
					if s.lastGood == nil {
						s.lastGood = map[string]agent.ModelList{}
					}
					s.lastGood[name] = res
				}
			}
			close(c.done)
			s.mu.Unlock()
		}(call, fetchCtx)
	} else {
		call.waiters++
	}
	s.mu.Unlock()

	select {
	case <-call.done:
		return call.res
	case <-ctx.Done():
		s.mu.Lock()
		call.waiters--
		if call.waiters <= 0 {
			// 最后一位等待者取消时立刻摘除该查询，防止新请求误接到已取消的查询
			if call.refresh {
				if s.inflightRefresh[name] == call {
					delete(s.inflightRefresh, name)
				}
			} else {
				if s.inflight[name] == call {
					delete(s.inflight, name)
				}
			}
			call.cancel()
		}
		s.mu.Unlock()
		return agent.ModelList{Agent: name, Error: ctx.Err().Error()}
	}
}

func (d *Daemon) queryAgentModels(ctx context.Context, name string) agent.ModelList {
	home := d.agentDataHome()
	switch name {
	case "claude":
		return agent.ClaudeModels(home)
	case "codex":
		return agent.CodexModels(home)
	case "antigravity", "cursor", "grok":
		return d.queryCLIModels(ctx, name)
	default:
		return agent.ModelList{Agent: name}
	}
}

func (d *Daemon) queryCLIModels(ctx context.Context, name string) agent.ModelList {
	list := agent.ModelList{
		Agent:   name,
		Efforts: agent.DefaultEfforts(name),
		Source:  "cli",
	}
	d.agents.mu.Lock()
	path := d.agents.path
	d.agents.mu.Unlock()
	if path == "" {
		path = os.Getenv("PATH")
	}
	cmdName := agent.Command[name]
	if cmdName == "" {
		list.Error = fmt.Sprintf("unknown agent %q", name)
		return list
	}
	exe := agent.LookPathAfter(cmdName, path, agent.ShimDir(d.cfg.Home))
	if exe == "" {
		list.Error = fmt.Sprintf("%s not found in PATH", cmdName)
		return list
	}

	out, stderr, err := runModelListCmd(ctx, exe, []string{"models"}, path)
	if err != nil {
		if ctx.Err() != nil {
			list.Error = ctx.Err().Error()
			return list
		}
		msg := compactCLIError(stderr, out, err)
		list.Error = msg
		return list
	}

	switch name {
	case "antigravity":
		list.Models = agent.ParseAntigravityModels(out)
	case "cursor":
		list.Models = agent.ParseCursorModels(out)
	case "grok":
		list.Models = agent.ParseGrokModels(out)
	}
	if len(list.Models) == 0 {
		list.Error = "no models returned by CLI"
	}
	return list
}

func compactCLIError(stderr, stdout string, err error) string {
	raw := strings.TrimSpace(stderr)
	if raw == "" {
		raw = strings.TrimSpace(stdout)
	}
	if raw == "" {
		return err.Error()
	}
	var lines []string
	for _, l := range strings.Split(raw, "\n") {
		l = strings.TrimSpace(l)
		if l != "" {
			lines = append(lines, l)
		}
	}
	msg := strings.Join(lines, " | ")
	if len(msg) > 240 {
		msg = msg[:237] + "..."
	}
	return msg
}

func runModelListCmd(ctx context.Context, exe string, args []string, path string) (stdout, stderr string, err error) {
	cmd := exec.CommandContext(ctx, exe, args...)
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Cancel = func() error {
		if cmd.Process != nil {
			return syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
		}
		return nil
	}
	// 子进程另开进程组或持有管道时，最多等 500ms 即收尾返回，避免永久卡在 stdout/stderr 拷贝
	cmd.WaitDelay = 500 * time.Millisecond
	env := term.Environ(nil)
	if path != "" {
		env = setEnvVar(env, "PATH", path)
	}
	cmd.Env = env
	var outBuf, errBuf bytes.Buffer
	cmd.Stdout = &outBuf
	cmd.Stderr = &errBuf
	err = cmd.Run()
	// 主进程先退出但同组子进程仍持有输出管道时，WaitDelay 触发后显式清掉同组残留进程
	if cmd.Process != nil && (ctx.Err() != nil || err != nil) {
		_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
	}
	if ctx.Err() != nil {
		return outBuf.String(), errBuf.String(), ctx.Err()
	}
	return outBuf.String(), errBuf.String(), err
}

func setEnvVar(env []string, key, val string) []string {
	prefix := key + "="
	for i, e := range env {
		if strings.HasPrefix(e, prefix) {
			env[i] = prefix + val
			return env
		}
	}
	return append(env, prefix+val)
}
