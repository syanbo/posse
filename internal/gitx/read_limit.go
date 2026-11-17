package gitx

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os/exec"
	"strings"
	"sync"
	"syscall"
	"time"
)

type readLimitKey struct{}
type readStrictKey struct{}
type readIsolatedKey struct{}

var ErrReadLimit = errors.New("Git output exceeds the read limit")

// WithReadLimit 只约束目标读取；原有本机调用的输出和记录方式不变。
func WithReadLimit(ctx context.Context, limit int) context.Context {
	return context.WithValue(ctx, readLimitKey{}, &readBudget{remaining: limit})
}

type readBudget struct {
	sync.Mutex
	remaining int
}

type readBuffer struct {
	buffer bytes.Buffer
	sink   io.Writer
	budget *readBudget
	over   bool
	cancel context.CancelFunc
}

func (b *readBuffer) Write(p []byte) (int, error) {
	b.budget.Lock()
	defer b.budget.Unlock()
	if len(p) > b.budget.remaining {
		b.over = true
		b.cancel()
		return 0, ErrReadLimit
	}
	b.budget.remaining -= len(p)
	if b.sink != nil {
		return b.sink.Write(p)
	}
	return b.buffer.Write(p)
}

func (b *readBuffer) Bytes() []byte  { return b.buffer.Bytes() }
func (b *readBuffer) String() string { return b.buffer.String() }

func runRead(ctx context.Context, dir string, stdin []byte, env []string, args ...string) ([]byte, error) {
	return runReadOutput(ctx, dir, stdin, env, nil, 0, args...)
}

// 流式正文单独计扫描预算，stderr 和元数据仍共用请求原有预算。
func runReadOutput(ctx context.Context, dir string, stdin []byte, env []string, sink io.Writer, streamLimit int, args ...string) ([]byte, error) {
	budget, _ := ctx.Value(readLimitKey{}).(*readBudget)
	if budget == nil {
		return nil, ErrReadLimit
	}
	if len(args) > 0 && (args[0] == "status" || args[0] == "diff") {
		if err := checkReadFilters(ctx, dir, 0, map[string]bool{}); err != nil {
			return nil, err
		}
	}
	ctx, cancel := context.WithTimeout(ctx, 2*time.Minute)
	defer cancel()
	cmd := exec.CommandContext(ctx, "git", append([]string{"-c", "core.fsmonitor=false"}, args...)...)
	cmd.Dir = dir
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	cmd.Cancel = func() error { return syscall.Kill(-cmd.Process.Pid, syscall.SIGTERM) }
	cmd.WaitDelay = 2 * time.Second
	// 目标路径必须自行决定仓库，不能被拉起 daemon 的终端环境重定向。
	isolated, _ := ctx.Value(readIsolatedKey{}).(bool)
	for _, e := range cmd.Environ() {
		key, _, _ := strings.Cut(e, "=")
		if isolated && strings.HasPrefix(key, "GIT_") {
			continue
		}
		switch key {
		case "GIT_DIR", "GIT_WORK_TREE", "GIT_COMMON_DIR", "GIT_INDEX_FILE", "GIT_OBJECT_DIRECTORY", "GIT_ALTERNATE_OBJECT_DIRECTORIES":
			continue
		}
		cmd.Env = append(cmd.Env, e)
	}
	cmd.Env = append(cmd.Env, "GIT_TERMINAL_PROMPT=0", "LC_ALL=C", "GIT_OPTIONAL_LOCKS=0", "GIT_TRACE2_EVENT=0", "GIT_NO_LAZY_FETCH=1")
	if isolated {
		cmd.Env = append(cmd.Env, "GIT_CONFIG_NOSYSTEM=1", "GIT_CONFIG_GLOBAL=/dev/null", "GIT_ATTR_NOSYSTEM=1")
	}
	cmd.Env = append(cmd.Env, env...)
	if stdin != nil {
		cmd.Stdin = bytes.NewReader(stdin)
	}
	out, errb := &readBuffer{budget: budget, cancel: cancel}, &readBuffer{budget: budget, cancel: cancel}
	if sink != nil {
		out.sink = sink
		out.budget = &readBudget{remaining: streamLimit}
	}
	cmd.Stdout, cmd.Stderr = out, errb
	err := cmd.Run()
	if (ctx.Err() != nil || errors.Is(err, exec.ErrWaitDelay)) && cmd.Process != nil {
		syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
	}
	if out.over || errb.over {
		return nil, ErrReadLimit
	}
	if ctx.Err() != nil {
		return nil, ctx.Err()
	}
	strict, _ := ctx.Value(readStrictKey{}).(bool)
	if err != nil || strict && len(errb.Bytes()) != 0 {
		pushPreview, _ := ctx.Value(pushPreviewReadKey{}).(bool)
		if pushPreview && strings.HasPrefix(errb.String(), "error: malformed value for push.default:") {
			return nil, ErrPushConfig
		}
		msg := withoutHints(errb.String())
		if msg == "" {
			msg = strings.TrimSpace(out.String())
		}
		if msg == "" && err != nil {
			msg = err.Error()
		}
		// 错误也要进 JSON；控制字符转义可能放大六倍，不把整段 stderr 当作错误回复。
		if len(msg) > 8<<10 {
			msg = msg[:8<<10]
		}
		return nil, &Error{Args: args, Msg: msg, cause: err}
	}
	return out.Bytes(), nil
}
