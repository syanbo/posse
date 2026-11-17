package gitx

import (
	"bytes"
	"context"
	"errors"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"time"
)

const divergenceDiskLimit int64 = 64 << 20

func predictDivergence(ctx context.Context, id RepositoryIdentity, state divergenceInputs) ([]string, string, error) {
	empty := []string{}
	dir, err := os.MkdirTemp("", "posse-divergence-")
	if err != nil {
		return nil, "", err
	}
	defer os.RemoveAll(dir)
	gitDir := filepath.Join(dir, ".git")
	for _, name := range []string{"objects/info", "refs", "info"} {
		if err := os.MkdirAll(filepath.Join(gitDir, name), 0700); err != nil {
			return nil, "", err
		}
	}
	if err := os.Symlink(filepath.Join(id.CommonDir, "objects"), filepath.Join(gitDir, "source-objects")); err != nil {
		return nil, "", err
	}
	config := "[core]\nrepositoryformatversion = 0\nbare = false\n[gc]\nauto = 0\n"
	if len(state.baseline.Head) == 64 {
		config = "[core]\nrepositoryformatversion = 1\nbare = false\n[extensions]\nobjectformat = sha256\n[gc]\nauto = 0\n"
	}
	for name, data := range map[string]string{"config": config, "HEAD": state.baseline.Head + "\n", "objects/info/alternates": "../source-objects\n", "info/attributes": state.attributes} {
		if err := os.WriteFile(filepath.Join(gitDir, name), []byte(data), 0600); err != nil {
			return nil, "", err
		}
	}
	args := append([]string{}, state.args...)
	args = append(args, "merge-tree", "--write-tree", "--name-only", "--no-messages", "-z", state.baseline.Head, state.baseline.Base)
	raw, code, reason, err := runDivergenceMerge(ctx, dir, gitDir, args)
	if err != nil {
		return nil, "", err
	}
	if reason != "" {
		return empty, reason, nil
	}
	if len(raw) == 0 || raw[len(raw)-1] != 0 {
		return nil, "", ErrDivergenceOutput
	}
	parts := bytes.Split(raw[:len(raw)-1], []byte{0})
	if len(parts) == 0 || !validDivergenceOID(string(parts[0])) || len(parts[0]) != len(state.baseline.Head) {
		return nil, "", ErrDivergenceOutput
	}
	conflicts, seen := []string{}, map[string]bool{}
	for _, p := range parts[1:] {
		name := string(p)
		if !ValidBlameFile(name) || seen[name] {
			return nil, "", ErrDivergenceOutput
		}
		seen[name] = true
		conflicts = append(conflicts, name)
	}
	if code == 0 && len(conflicts) != 0 || code == 1 && len(conflicts) == 0 {
		return nil, "", ErrDivergenceOutput
	}
	return conflicts, "", nil
}

// 总量是运行中阈值，不是文件系统配额；100ms 复查及结束复查之外，子进程还受单文件限制。
// 不改 daemon 的 rlimit；固定 shell 脚本只接收位置参数，路径与修订均不拼进脚本。
func runDivergenceMerge(ctx context.Context, dir, gitDir string, args []string) ([]byte, int, string, error) {
	budget, _ := ctx.Value(readLimitKey{}).(*readBudget)
	if budget == nil {
		return nil, -1, "", ErrReadLimit
	}
	git, err := exec.LookPath("git")
	if err != nil {
		return nil, -1, DivergenceGit, nil
	}
	runCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	argv := append([]string{"-c", `ulimit -f 16384 || exit 125; exec "$@"`, "posse-merge", git}, args...)
	cmd := exec.CommandContext(runCtx, "/bin/sh", argv...)
	cmd.Dir = dir
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	cmd.Cancel = func() error { return syscall.Kill(-cmd.Process.Pid, syscall.SIGTERM) }
	cmd.WaitDelay = 2 * time.Second
	for _, e := range os.Environ() {
		k, _, _ := strings.Cut(e, "=")
		if strings.HasPrefix(k, "GIT_") || k == "ENV" || k == "BASH_ENV" {
			continue
		}
		cmd.Env = append(cmd.Env, e)
	}
	cmd.Env = append(cmd.Env, "GIT_DIR="+gitDir, "GIT_WORK_TREE="+dir, "GIT_INDEX_FILE="+filepath.Join(gitDir, "index"), "GIT_CONFIG_NOSYSTEM=1", "GIT_CONFIG_GLOBAL=/dev/null", "GIT_ATTR_NOSYSTEM=1", "GIT_NO_REPLACE_OBJECTS=1", "GIT_NO_LAZY_FETCH=1", "GIT_OPTIONAL_LOCKS=0", "GIT_TRACE2_EVENT=0", "GIT_TERMINAL_PROMPT=0", "LC_ALL=C")
	out, diagnostic := &readBuffer{budget: budget, cancel: cancel}, &readBuffer{budget: budget, cancel: cancel}
	cmd.Stdout, cmd.Stderr = out, diagnostic
	if err := cmd.Start(); err != nil {
		return nil, -1, "", err
	}
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	ticker := time.NewTicker(100 * time.Millisecond)
	defer ticker.Stop()
	var diskErr error
wait:
	for {
		select {
		case err = <-done:
			break wait
		case <-ticker.C:
			if diskErr == nil {
				diskErr = divergenceDiskCheck(dir)
				if diskErr != nil {
					cancel()
				}
			}
		}
	}
	if runCtx.Err() != nil || errors.Is(err, exec.ErrWaitDelay) {
		syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
	}
	if out.over || diagnostic.over {
		return nil, -1, "", ErrReadLimit
	}
	if ctx.Err() != nil {
		return nil, -1, "", ctx.Err()
	}
	if diskErr == nil {
		diskErr = divergenceDiskCheck(dir)
	}
	if diskErr != nil {
		if errors.Is(diskErr, errDivergenceDiskLimit) {
			return nil, -1, DivergenceLimit, nil
		}
		return nil, -1, "", diskErr
	}
	code := 0
	if err != nil {
		var exit *exec.ExitError
		if !errors.As(err, &exit) {
			return nil, -1, "", err
		}
		code = exit.ExitCode()
		if status, ok := exit.Sys().(syscall.WaitStatus); ok && status.Signaled() && status.Signal() == syscall.SIGXFSZ {
			return nil, -1, DivergenceLimit, nil
		}
		if code == 129 && (strings.Contains(diagnostic.String(), "unknown option") || strings.Contains(diagnostic.String(), "usage:")) {
			return nil, code, DivergenceGit, nil
		}
		if code != 1 {
			return nil, code, "", ErrDivergenceOutput
		}
	}
	if len(diagnostic.Bytes()) != 0 {
		return nil, code, "", ErrDivergenceOutput
	}
	return out.Bytes(), code, "", nil
}

var errDivergenceDiskLimit = errors.New("Git divergence temporary data exceeds the prediction limit")

func divergenceDiskCheck(dir string) error {
	var size int64
	count := 0
	return filepath.WalkDir(dir, func(name string, entry fs.DirEntry, err error) error {
		if os.IsNotExist(err) {
			return nil
		}
		if err != nil {
			return err
		}
		count++
		if count > 2*divergencePathLimit {
			return errDivergenceDiskLimit
		}
		if !entry.Type().IsRegular() {
			return nil
		}
		info, err := entry.Info()
		if os.IsNotExist(err) {
			return nil
		}
		if err != nil {
			return err
		}
		size += info.Size()
		if size > divergenceDiskLimit {
			return errDivergenceDiskLimit
		}
		return nil
	})
}
