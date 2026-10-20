package gitx

import (
	"bytes"
	"context"
	"errors"
	"os/exec"
	"slices"
	"strconv"
	"strings"
	"time"
)

// Divergence 是 worktree 当前分支相对基准分支的情况。
type Divergence struct {
	Head, Base string   // 算的时候两边的提交，没变就不用重算
	Ahead      int      // 分支有、基准分支没有的提交（要合回去的）
	Behind     int      // 基准分支有、分支还没有的提交
	Conflicts  []string // 现在合并会冲突的文件；两边都有新提交时才预测
}

// BaseDivergence 比较 dir 的 HEAD 和 base。界面每 10 秒调一次：不经 run、runRaw，
// 基准分支被删、合并会冲突时 git 的非零退出不该当成失败记进操作记录。
// 两边的提交和 prev 一样时直接返回 prev：大仓库里 merge-tree 不便宜。
func BaseDivergence(ctx context.Context, dir, base string, prev *Divergence) (Divergence, error) {
	var d Divergence
	out, _, err := quiet(ctx, dir, "rev-parse", "HEAD^{commit}", base+"^{commit}")
	if err != nil {
		return d, err
	}
	oids := strings.Fields(string(out))
	if len(oids) != 2 {
		return d, errors.New("unexpected rev-parse output: " + string(out))
	}
	d.Head, d.Base = oids[0], oids[1]
	if prev != nil && prev.Head == d.Head && prev.Base == d.Base {
		return *prev, nil
	}
	out, _, err = quiet(ctx, dir, "rev-list", "--left-right", "--count", d.Head+"..."+d.Base, "--")
	if err != nil {
		return d, err
	}
	f := strings.Fields(string(out))
	if len(f) != 2 {
		return d, errors.New("unexpected rev-list output: " + string(out))
	}
	d.Ahead, _ = strconv.Atoi(f[0])
	d.Behind, _ = strconv.Atoi(f[1])
	if d.Ahead == 0 || d.Behind == 0 {
		return d, nil
	}
	// merge-tree 在对象库里算合并结果，不碰工作区和暂存区；退出码 1 是有冲突。git 2.38 起才有 --write-tree，旧的预测不了就不报
	out, code, err := quiet(ctx, dir, "merge-tree", "--write-tree", "--name-only", "--no-messages", "-z", d.Head, d.Base)
	if err != nil && code != 1 {
		return d, nil
	}
	// 输出是树的 ID，后面每个冲突文件一项，都以 NUL 结尾
	parts := strings.Split(string(out), "\x00")
	for _, p := range parts[1:] {
		if p != "" && !slices.Contains(d.Conflicts, p) {
			d.Conflicts = append(d.Conflicts, p)
		}
	}
	return d, nil
}

// quiet 跑一条只读的 git 命令，不报给操作记录；返回标准输出和退出码。
func quiet(ctx context.Context, dir string, args ...string) ([]byte, int, error) {
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, "git", args...)
	cmd.Dir = dir
	cmd.Env = append(cmd.Environ(), "GIT_TERMINAL_PROMPT=0", "LC_ALL=C", "GIT_OPTIONAL_LOCKS=0", "GIT_TRACE2_EVENT=0")
	var out, errb bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &errb
	if err := cmd.Run(); err != nil {
		code := -1
		if ee, ok := err.(*exec.ExitError); ok {
			code = ee.ExitCode()
		}
		msg := strings.TrimSpace(errb.String())
		if msg == "" {
			msg = err.Error()
		}
		return out.Bytes(), code, &Error{Args: args, Msg: msg}
	}
	return out.Bytes(), 0, nil
}
