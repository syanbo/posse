package agent

import (
	"bufio"
	"bytes"
	"context"
	"encoding/base64"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"syscall"
	"time"
)

// Command 是各 agent 启动时运行的命令，检测装没装也按它找。
var Command = map[string]string{"claude": "claude", "codex": "codex", "grok": "grok", "cursor": "cursor-agent", "antigravity": "agy", "zcode": "zcode"}

// FallbackAgents 是没有检测结果时能先显示的启动选项。ZCode 必须先通过 TUI 探测。
func FallbackAgents() []string {
	return slices.DeleteFunc(slices.Clone(Known), func(name string) bool { return name == "zcode" })
}

const (
	foundMark = "__posse_found__"
	pathMark  = "__posse_path__"
	zcodeMark = "__posse_zcode_storage__"
)

// detectScript 在用户的交互式登录 shell 里查各命令：.zshrc 里的 alias、函数（比如 Claude 本地安装写的
// alias claude=~/.claude/local/claude）只有这样才找得到。启动文件可能往 stdout 打字，结果加标记取。
func detectScript() string {
	var cmds []string
	for _, name := range Known {
		cmds = append(cmds, Command[name])
	}
	// ZCode 桌面包的内置 CLI 也能报版本，只有装了 TUI 模块的 CLI 会走到检查终端这一步。
	return "for c in " + strings.Join(cmds, " ") + "; do command -v \"$c\" >/dev/null 2>&1 || continue; " +
		`if [ "$c" = zcode ]; then case "$(zcode tui --locale en-US --no-color </dev/null 2>&1)" in ` +
		`*"TUI requires an interactive terminal."*) ;; *) continue ;; esac; fi; ` +
		"printf '\\n" + foundMark + "%s\\n' \"$c\"; done; printf '\\n" + pathMark + "%s\\n' \"$PATH\"; " +
		`for k in ZCODE_STORAGE_DIR ZCODE_SESSION_DB_PATH ZCODE_SESSION_DB; do ` +
		`v=$(/usr/bin/printenv "$k" | /usr/bin/base64 | /usr/bin/tr -d '\n'); printf '\n` + zcodeMark + `%s=%s\n' "$k" "$v"; done`
}

// Detection 是一次在登录 shell 里的检测结果：找得到的 agent，以及那个 shell 的 PATH（之后只 stat 用）。
type Detection struct {
	Found []string
	Path  string
	ZCode ZCodeStorage
}

// DetectInShell 起一个交互式登录 shell 检测装了哪些 agent。env 是 shell 的环境，里面的 PATH 不要带
// $POSSE_HOME/bin（那里有同名的转发脚本）。fish 等 shell 先加载自己的环境，再交给 sh 执行探测。
func DetectInShell(ctx context.Context, shell string, env []string) (Detection, error) {
	ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	argv := shellScriptArgv(shell, detectScript())
	cmd := exec.CommandContext(ctx, argv[0], argv[1:]...)
	cmd.Env = env
	// 新开会话：没有控制终端，交互式 shell 不会去抢前台、等终端输入；超时连同它拉起的子进程一起杀掉
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	cmd.Cancel = func() error { return syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL) }
	cmd.WaitDelay = time.Second
	out, err := cmd.Output()
	var d Detection
	gotPath := false
	sc := bufio.NewScanner(bytes.NewReader(out))
	sc.Buffer(nil, 1<<20)
	for sc.Scan() {
		line := sc.Text()
		if c, ok := strings.CutPrefix(line, foundMark); ok {
			for name, command := range Command {
				if command == c {
					d.Found = append(d.Found, name)
				}
			}
		} else if p, ok := strings.CutPrefix(line, pathMark); ok {
			d.Path, gotPath = p, true
		} else if value, ok := strings.CutPrefix(line, zcodeMark); ok {
			key, encoded, _ := strings.Cut(value, "=")
			raw, err := base64.StdEncoding.DecodeString(encoded)
			if err != nil {
				continue
			}
			decoded := strings.TrimSuffix(string(raw), "\n")
			switch key {
			case "ZCODE_STORAGE_DIR":
				d.ZCode.Dir = decoded
			case "ZCODE_SESSION_DB_PATH":
				d.ZCode.DB = decoded
			case "ZCODE_SESSION_DB":
				d.ZCode.LegacyDB = decoded
			}
		}
	}
	// 启动文件里最后一条命令失败也会让 shell 非零退出，拿到了 PATH 就算数
	if !gotPath {
		if err == nil {
			err = exec.ErrNotFound
		}
		return Detection{}, err
	}
	return d, nil
}

// ProbeZCode 用于登录 shell 探测失败时。保留同样的 TUI 检查，避免桌面包的内置命令被认成完整 CLI。
func ProbeZCode(ctx context.Context, path string) bool {
	if path == "" {
		return false
	}
	ctx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, path, "tui", "--locale", "en-US", "--no-color")
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	cmd.Cancel = func() error { return syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL) }
	cmd.WaitDelay = time.Second
	out, _ := cmd.CombinedOutput()
	return ctx.Err() == nil && bytes.Contains(out, []byte("TUI requires an interactive terminal."))
}

// Installed 合并登录 shell 找到的 found 和在 path 里 stat 到的，按 Known 的顺序返回；skip 是转发脚本的目录。
// shell 只是隔一阵查一次，新装进 PATH 的靠 stat 尽快认出来。
func Installed(found []string, path, skip string) []string {
	out := []string{}
	for _, name := range Known {
		if slices.Contains(found, name) || name != "zcode" && LookPathAfter(Command[name], path, skip) != "" {
			out = append(out, name)
		}
	}
	return out
}

// WithoutDir 从 PATH 里去掉目录 dir。
func WithoutDir(path, dir string) string {
	dir = filepath.Clean(dir)
	var keep []string
	for _, p := range filepath.SplitList(path) {
		if p != "" && filepath.Clean(p) != dir {
			keep = append(keep, p)
		}
	}
	return strings.Join(keep, string(filepath.ListSeparator))
}
