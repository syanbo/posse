package gitx

import (
	"context"
	"slices"
	"strconv"
	"strings"
	"sync/atomic"
	"time"
)

// Run 是界面执行的一条 git 命令，给"操作记录"看（照 WebStorm Git 工具窗口的 Console）。
// 开始时报一次（Done 为假），结束时用同一个 ID 再报一次。
type Run struct {
	ID    int64
	Op    string // 所属的操作（界面上的"推送""储藏"等，WithOp 带进来）；空的是后台读取
	OpID  int64  // 同一次操作的命令 OpID 相同
	Dir   string
	Args  []string
	Start time.Time
	Took  time.Duration
	Done  bool
	Out   string // 标准输出和标准错误（fetch、push 的进度和结果在标准错误里），太长截掉中间
	Err   string // 失败时 git 的报错，成功为空
	// Canceled 报告是被取消的（Err 是 MsgCanceled）：界面按它判断，不比较 Err 的文字
	Canceled bool `json:",omitempty"`
}

// MsgCanceled 是取消了的命令和操作的报错，英文源文本，界面经 errtext 翻译。
const MsgCanceled = "canceled"

// Trace 收到操作里的每条命令（包括它顺带做的查询），以及操作之外会改动仓库的命令和失败的命令；
// 操作之外成功的查询（每 1.5 秒的 status、diff、log 等）不报，不然全是轮询。在执行 git 的协程里调用，不能阻塞。
var Trace func(Run)

var traceSeq, opSeq atomic.Int64

type opKey struct{}

type opInfo struct {
	name string
	id   int64
}

// Seed 让之后的命令和操作编号接在 id、op 后面：窗口读回上次留下的记录时用，新旧编号不撞。
func Seed(id, op int64) {
	for cur := traceSeq.Load(); cur < id && !traceSeq.CompareAndSwap(cur, id); cur = traceSeq.Load() {
	}
	for cur := opSeq.Load(); cur < op && !opSeq.CompareAndSwap(cur, op); cur = opSeq.Load() {
	}
}

// WithOp 标出 ctx 下执行的命令属于哪个操作。
func WithOp(ctx context.Context, name string) context.Context {
	return context.WithValue(ctx, opKey{}, opInfo{name, opSeq.Add(1)})
}

// OpResult 报出操作的结果：失败时 msg 是界面上显示的报错（不一定来自 git，比如"范围里有合并提交"），
// 记在这个操作下面。成功时不报，命令都已经记过了。
func OpResult(ctx context.Context, dir, msg string) {
	op, _ := ctx.Value(opKey{}).(opInfo)
	if Trace == nil || op.id == 0 || msg == "" {
		return
	}
	now := time.Now()
	Trace(Run{ID: traceSeq.Add(1), Op: op.name, OpID: op.id, Dir: dir, Start: now, Done: true, Err: msg})
}

// OpCanceled 报出操作被取消了。
func OpCanceled(ctx context.Context, dir string) {
	op, _ := ctx.Value(opKey{}).(opInfo)
	if Trace == nil || op.id == 0 {
		return
	}
	Trace(Run{ID: traceSeq.Add(1), Op: op.name, OpID: op.id, Dir: dir, Start: time.Now(), Done: true, Err: MsgCanceled, Canceled: true})
}

func traceStart(ctx context.Context, dir string, args []string) *Run {
	if Trace == nil {
		return nil
	}
	op, _ := ctx.Value(opKey{}).(opInfo)
	r := &Run{ID: traceSeq.Add(1), Op: op.name, OpID: op.id, Dir: dir, Args: slices.Clone(args), Start: time.Now()}
	if r.Op != "" || !readOnly(args) {
		Trace(*r)
	}
	return r
}

func traceEnd(r *Run, stdout, stderr []byte, errMsg string, canceled bool) {
	if r == nil || Trace == nil || (errMsg == "" && r.Op == "" && readOnly(r.Args)) {
		return
	}
	r.Took, r.Done, r.Err, r.Canceled = time.Since(r.Start), true, errMsg, canceled
	// 查询的标准输出是界面要解析的数据，失败时才值得看；改动的命令两样都留
	text := strings.TrimRight(string(stderr), "\n")
	if !readOnly(r.Args) {
		if o := strings.TrimRight(string(stdout), "\n"); o != "" {
			text = strings.TrimLeft(o+"\n"+text, "\n")
		}
	}
	r.Out = clip(text, 8<<10)
	Trace(*r)
}

// clip 超过 n 字节时留头尾、中间写省略了多少。
func clip(s string, n int) string {
	if len(s) <= n {
		return s
	}
	head, tail := s[:n/2], s[len(s)-n/2:]
	return strings.ToValidUTF8(head, "") + "\n…（省略 " + strconv.Itoa(len(s)-n) + " 字节）…\n" + strings.ToValidUTF8(tail, "")
}

// readOnly 判断命令只是查询、不改仓库。
func readOnly(args []string) bool {
	if len(args) == 0 {
		return true
	}
	// 跳过 -c key=value 这类全局选项
	i := 0
	for i < len(args) && strings.HasPrefix(args[i], "-") {
		if args[i] == "-c" || args[i] == "-C" {
			i++
		}
		i++
	}
	if i >= len(args) {
		return true
	}
	cmd, rest := args[i], args[i+1:]
	has := func(flags ...string) bool {
		return slices.ContainsFunc(rest, func(a string) bool { return slices.Contains(flags, a) })
	}
	switch cmd {
	case "status", "log", "show", "diff", "for-each-ref", "merge-base", "ls-tree", "ls-files", "diff-tree", "blame",
		"reflog", "rev-parse", "rev-list", "cat-file", "symbolic-ref", "check-ignore", "cherry", "describe", "name-rev":
		return true
	case "hash-object":
		return !has("-w")
	case "stash":
		return len(rest) > 0 && (rest[0] == "list" || rest[0] == "show")
	case "worktree":
		return len(rest) > 0 && rest[0] == "list"
	case "config":
		return has("--get", "--get-all", "--get-regexp", "-l", "--list")
	case "branch":
		if has("-d", "-D", "--delete", "-m", "-M", "--move", "-c", "-C", "--copy", "-f", "--force", "-u", "--unset-upstream") ||
			slices.ContainsFunc(rest, func(a string) bool { return strings.HasPrefix(a, "--set-upstream-to") }) {
			return false
		}
		return has("--list", "-l", "--contains", "--merged", "--no-merged", "-r", "-a", "--remotes", "--all", "--show-current") ||
			slices.ContainsFunc(rest, func(a string) bool { return strings.HasPrefix(a, "--format") })
	case "tag":
		return len(rest) == 0 || has("-l", "--list")
	}
	return false
}

// ChangesRepo 判断终端里的一条 git 命令会不会改动仓库。和 readOnly 反过来只认确定会改的：
// 提示符、编辑器、agent 跑的查询五花八门，不认识的都当查询，免得操作记录被刷屏。
func ChangesRepo(args []string) bool {
	i := 0
	for i < len(args) && strings.HasPrefix(args[i], "-") {
		switch args[i] {
		case "-c", "-C", "--git-dir", "--work-tree", "--namespace", "--exec-path":
			i++
		}
		i++
	}
	if i >= len(args) {
		return false
	}
	cmd, rest := args[i], args[i+1:]
	has := func(flags ...string) bool {
		return slices.ContainsFunc(rest, func(a string) bool { return slices.Contains(flags, a) })
	}
	switch cmd {
	case "commit", "push", "pull", "fetch", "merge", "rebase", "cherry-pick", "revert", "reset", "checkout", "switch",
		"restore", "am", "rm", "mv", "init", "clone":
		return true
	case "add", "clean":
		return !has("-n", "--dry-run")
	case "apply":
		return !has("--check", "--stat", "--numstat", "--summary")
	case "stash", "branch", "tag":
		return !readOnly(args[i:])
	case "worktree":
		return len(rest) > 0 && slices.Contains([]string{"add", "remove", "move", "prune", "repair", "lock", "unlock"}, rest[0])
	case "remote":
		return len(rest) > 0 && slices.Contains([]string{"add", "remove", "rm", "rename", "set-url", "set-head", "set-branches", "prune", "update"}, rest[0])
	}
	return false
}
