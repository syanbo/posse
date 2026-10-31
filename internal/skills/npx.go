package skills

import (
	"strings"
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

// RemoveArgs 是删除 npx skills 装的技能：连同它给各家建的链接。
func RemoveArgs(name string, global bool) []string {
	args := []string{name}
	if global {
		args = append(args, "-g")
	}
	return npx("remove", append(args, "-y")...)
}

// UpdateArgs 是更新 npx skills 装的技能，names 为空时全部更新。
func UpdateArgs(names []string, global bool) []string {
	args := append([]string{}, names...)
	if global {
		args = append(args, "-g")
	}
	return npx("update", append(args, "-y")...)
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
