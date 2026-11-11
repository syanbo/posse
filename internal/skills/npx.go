package skills

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"regexp"
	"strings"
	"syscall"
	"time"
)

// 装、删、更新交给 npx skills（vercel-labs/skills）：本机的技能本来就是它装的（~/.agents/.skill-lock.json），
// Posse 不另做一套。版本固定，免得它升级后参数变了；关掉它的匿名统计。

// NPXVersion 是调用的 skills 版本，参数按它的 --help 核对过。
const NPXVersion = "1.7.4"

// npxAgent 是 npx skills 里各家的名字（-a）。
var npxAgent = [NumAgents]string{"claude-code", "codex", "grok", "cursor"}

// Install 是一次安装。
type Install struct {
	// Source 是 owner/repo、网址或本地目录。
	Source string
	// Skills 是只装其中的这几个，空是全部。
	Skills []string
	// Claude 为真时也给 Claude 建链接。Codex、Grok、Cursor 都读 ~/.agents/skills（项目里是 .agents/skills），
	// 装给 codex 就都用得上；Claude 只读自己的目录，要单独链一份。
	Claude bool
	// Global 为真时装到用户目录（-g），否则装进当前目录的项目里。
	Global bool
}

func npx(sub string, args ...string) []string {
	return append([]string{"npx", "-y", "skills@" + NPXVersion, sub}, args...)
}

// Check 挡住会被 npx 当成自己参数的来源和名字（以 - 开头）。
func (in Install) Check() error {
	if strings.HasPrefix(strings.TrimSpace(in.Source), "-") {
		return errors.New("the source must not start with -")
	}
	for _, s := range in.Skills {
		if strings.HasPrefix(s, "-") {
			return fmt.Errorf("skill name %s must not start with -", s)
		}
	}
	return nil
}

// Args 是安装的命令行。
func (in Install) Args() []string {
	args := []string{strings.TrimSpace(in.Source)}
	if in.Global {
		args = append(args, "-g")
	}
	args = append(args, "-a", npxAgent[Codex])
	if in.Claude {
		args = append(args, "-a", npxAgent[Claude])
	}
	for _, s := range in.Skills {
		args = append(args, "-s", s)
	}
	return npx("add", append(args, "-y")...)
}

// Listed 是来源里的一个技能。
type Listed struct {
	Name        string
	Description string
	// Group 是来源自己给技能分的组（anthropics/skills 那样），没分组时为空。
	Group string
}

// ListArgs 是列出来源里有哪些技能、不安装的命令。
func ListArgs(source string) []string {
	return npx("add", strings.TrimSpace(source), "--list")
}

// listTimeout 是列技能最多等多久：要克隆仓库，网络不通时 git 会一直连。
const listTimeout = 60 * time.Second

// ListSource 在 dir 里起一个交互式登录 shell 跑 ListArgs（窗口进程的 PATH 里通常没有 npx，
// 要从用户的 shell 配置里拿），解析出技能列表。相对路径的来源按 dir 算。
// shell 把一行 POSIX sh 命令变成在用户 shell 里跑它的参数（agent.ShellCommand：fish、csh 用户交给 /bin/sh）。
func ListSource(ctx context.Context, shell func(cmd string) []string, dir, source string) ([]Listed, error) {
	ctx, cancel := context.WithTimeout(ctx, listTimeout)
	defer cancel()
	argv := shell(ShellLine(ListArgs(source)))
	cmd := exec.CommandContext(ctx, argv[0], argv[1:]...)
	cmd.Dir = dir
	// git 要账号密码时不问（没有终端可答，会一直等），直接报错
	cmd.Env = append(os.Environ(), "GIT_TERMINAL_PROMPT=0", "NO_COLOR=1")
	// 新开会话：没有控制终端，交互式 shell 不去抢前台；超时连同 npx、git 一起杀掉
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	cmd.Cancel = func() error { return syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL) }
	cmd.WaitDelay = time.Second
	var out bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &out
	err := cmd.Run()
	if ctx.Err() == context.DeadlineExceeded {
		return nil, fmt.Errorf("not done after %d seconds; check the network or the source", int(listTimeout/time.Second))
	}
	// shell 找不到命令时退出码是 127
	if ee := (*exec.ExitError)(nil); errors.As(err, &ee) && ee.ExitCode() == 127 {
		return nil, ErrNoNPX
	}
	return ParseList(out.String())
}

// ErrNoNPX 是用户的 shell 里没有 npx：没装 Node.js。
var ErrNoNPX = errors.New("npx not found")

// FindBrew 找 Homebrew：窗口进程从访达启动时 PATH 里没有它的目录。找不到返回空。
func FindBrew() string {
	if p, err := exec.LookPath("brew"); err == nil {
		return p
	}
	for _, p := range []string{"/opt/homebrew/bin/brew", "/usr/local/bin/brew"} {
		if fi, err := os.Stat(p); err == nil && !fi.IsDir() && fi.Mode()&0o111 != 0 {
			return p
		}
	}
	return ""
}

var ansiRe = regexp.MustCompile(`\x1b\[[0-9;?]*[A-Za-z]`)

// ParseList 解析 --list 的输出（NPXVersion 那一版，给人看的文字，没有 JSON）：
//
//	◇  Available Skills
//	组名（可选，不带竖线）
//	│
//	│    技能名
//	│
//	│      说明
//	└  Use --skill <name> to install specific skills
//
// 读不到列表时返回它报的错：■ 那行和下面第一行细节，或者 └ 那行。
func ParseList(out string) ([]Listed, error) {
	lines := strings.Split(ansiRe.ReplaceAllString(out, ""), "\n")
	start := -1
	for i, l := range lines {
		if strings.HasSuffix(strings.TrimSpace(l), "Available Skills") {
			start = i + 1
			break
		}
	}
	if start < 0 {
		return nil, listError(lines)
	}
	var list []Listed
	group := ""
	for _, l := range lines[start:] {
		l = strings.TrimRight(l, " \r")
		if strings.HasPrefix(l, "└") {
			break
		}
		body, bar := strings.CutPrefix(l, "│")
		if !bar {
			if t := strings.TrimSpace(l); t != "" {
				group = t
			}
			continue
		}
		t := strings.TrimLeft(body, " ")
		if t == "" {
			continue
		}
		switch indent := len(body) - len(t); {
		case indent <= 4:
			list = append(list, Listed{Name: t, Group: group})
		case len(list) > 0:
			d := &list[len(list)-1].Description
			*d = strings.TrimSpace(*d + " " + t)
		}
	}
	if len(list) == 0 {
		return nil, errors.New("the source has no skills")
	}
	return list, nil
}

func listError(lines []string) error {
	for i, l := range lines {
		if t, ok := strings.CutPrefix(strings.TrimSpace(l), "■"); ok {
			msg := strings.TrimSpace(t)
			for _, d := range lines[i+1:] {
				d = strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(d), "│"))
				if d != "" && !strings.HasPrefix(d, "└") {
					msg += ": " + d
					break
				}
			}
			return fmt.Errorf("%s", msg)
		}
	}
	for _, l := range lines {
		if t, ok := strings.CutPrefix(strings.TrimSpace(l), "└"); ok {
			return fmt.Errorf("%s", strings.TrimSpace(t))
		}
	}
	return errors.New("no skill list in the output")
}

// RemoveArgs 是删除 npx skills 装的技能：连同它给各家建的链接。技能名来自 SKILL.md，
// 以 - 开头的会被 npx 当成选项（name: --all），和 Install.Check 一样拒绝。
func RemoveArgs(name string, global bool) ([]string, error) {
	if strings.HasPrefix(name, "-") {
		return nil, fmt.Errorf("skill name %s must not start with -", name)
	}
	args := []string{name}
	if global {
		args = append(args, "-g")
	}
	return npx("remove", append(args, "-y")...), nil
}

// UpdateArgs 是更新 npx skills 装的技能，names 为空时全部更新。以 - 开头的名字同 RemoveArgs 拒绝。
func UpdateArgs(names []string, global bool) ([]string, error) {
	for _, s := range names {
		if strings.HasPrefix(s, "-") {
			return nil, fmt.Errorf("skill name %s must not start with -", s)
		}
	}
	args := append([]string{}, names...)
	if global {
		args = append(args, "-g")
	}
	return npx("update", append(args, "-y")...), nil
}

// ShellLine 把命令拼成一行 shell，前面带上关掉统计的环境变量，给确认框显示、交给终端运行。
func ShellLine(args []string) string {
	q := make([]string, len(args))
	for i, a := range args {
		q[i] = shellQuote(a)
	}
	return "DISABLE_TELEMETRY=1 " + strings.Join(q, " ")
}

func shellQuote(s string) string {
	if s != "" && strings.Trim(s, "abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789-_./:@=+,%") == "" {
		return s
	}
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}
