// Package cheatsheet 是各家 agent 内置命令、按键和输入前缀的速查表，以及认出本机 agent 的版本。
//
// 表是手工整理的（cheatsheet.json，对照本机装的版本和官方文档），随 Posse 发布更新；
// 本机版本比表新时界面提示可能有新命令，不自动抓取：命令的说明和取舍要人来定。
// 给人看的字段（组名、参数提示、说明）写中文和英文两份，For 按界面语言取。
package cheatsheet

import (
	"context"
	_ "embed"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"

	"posse/internal/agent"
	"posse/internal/procmem"
)

// Kind 是一组条目的类别，决定点了能不能贴进输入框。
type Kind string

const (
	KindCommand Kind = "command" // 斜杠命令
	KindKey     Kind = "key"     // 按键：只看不贴
	KindPrefix  Kind = "prefix"  // 输入前缀（@、! 等）
	KindSkill   Kind = "skill"   // agent 自带、编在程序里的技能（Claude 的 /code-review 等），扫目录扫不到
)

// Item 是一条命令、按键或前缀。
type Item struct {
	Name string `json:"name"`
	Args string `json:"args,omitempty"` // 参数提示，如 [指示]
	Desc string `json:"desc"`
	// ArgsEn、DescEn 是英文：说法照各家官方英文文档。参数提示本来就是英文的不写
	ArgsEn string `json:"argsEn,omitempty"`
	DescEn string `json:"descEn,omitempty"`
	Warn   bool   `json:"warn,omitempty"` // 会丢掉当前对话或做不可逆的操作
}

// Section 是一组条目。
type Section struct {
	Title   string `json:"title"`
	TitleEn string `json:"titleEn,omitempty"`
	Kind    Kind   `json:"kind"`
	Items   []Item `json:"items"`
}

// Sheet 是一家 agent 的速查表。
type Sheet struct {
	Agent string `json:"agent"`
	// Version 是整理时对照的版本。
	Version   string `json:"version"`
	Docs      string `json:"docs,omitempty"`
	Changelog string `json:"changelog,omitempty"`
	// SkillPrefix 是用户技能的调用前缀（Claude 是 /，Codex 是 $）。
	SkillPrefix string    `json:"skillPrefix"`
	Sections    []Section `json:"sections"`
}

//go:embed cheatsheet.json
var raw []byte

var sheets = sync.OnceValue(func() map[string]Sheet {
	var list []Sheet
	if err := json.Unmarshal(raw, &list); err != nil {
		panic("cheatsheet.json: " + err.Error()) // 由测试守着
	}
	m := map[string]Sheet{}
	for _, s := range list {
		m[s.Agent] = s
	}
	return m
})

// For 返回 agent 的速查表，给人看的字段按语言 lang（BCP 47，如 zh-Hans、en）取：
// 中文取中文，其余语言取英文，英文没写的退回中文。
func For(agentName, lang string) (Sheet, bool) {
	s, ok := sheets()[agentName]
	if !ok || isChinese(lang) {
		return s, ok
	}
	secs := make([]Section, len(s.Sections))
	for i, sec := range s.Sections {
		sec.Title = or(sec.TitleEn, sec.Title)
		items := make([]Item, len(sec.Items))
		for j, it := range sec.Items {
			it.Args = or(it.ArgsEn, it.Args)
			it.Desc = or(it.DescEn, it.Desc)
			items[j] = it
		}
		sec.Items = items
		secs[i] = sec
	}
	s.Sections = secs
	return s, true
}

func isChinese(lang string) bool {
	return lang == "zh" || strings.HasPrefix(lang, "zh-") || strings.HasPrefix(lang, "zh_")
}

func or(a, b string) string {
	if a != "" {
		return a
	}
	return b
}

// Insert 是点了一条后贴进输入框的文字：命令和技能带参数时后面留个空格接着写，前缀原样。按键返回空。
func Insert(k Kind, it Item) string {
	switch k {
	case KindCommand, KindSkill:
		if it.Args != "" {
			return it.Name + " "
		}
		return it.Name
	case KindPrefix:
		return it.Name
	}
	return ""
}

// Compare 比较两个点分版本号（2.1.316、2026.10.22），各段按数字比；认不出数字的段按 0 算。
func Compare(a, b string) int {
	as, bs := strings.Split(a, "."), strings.Split(b, ".")
	for i := range max(len(as), len(bs)) {
		var x, y int
		if i < len(as) {
			x, _ = strconv.Atoi(as[i])
		}
		if i < len(bs) {
			y, _ = strconv.Atoi(bs[i])
		}
		if x != y {
			if x < y {
				return -1
			}
			return 1
		}
	}
	return 0
}

// versionRe 认路径或 --version 输出里的版本号：至少三段数字（2.1.316、0.166.0、2026.10.22）。
var versionRe = regexp.MustCompile(`(?:^|[^0-9.])(\d+\.\d+\.\d+)`)

// VersionIn 取 s 里第一个版本号，没有返回空。
func VersionIn(s string) string {
	if m := versionRe.FindStringSubmatch(s); m != nil {
		return m[1]
	}
	return ""
}

// Running 找 shell 进程 shell 下面正在运行的 agentName 进程，返回它的版本，找不到返回空。
// 各家的安装目录都带版本号（~/.local/share/claude/versions/2.1.316、Caskroom/codex/0.166.0、
// grok-1.0.73-macos-aarch64、cursor-agent/versions/2026.10.22-…），先从解开链接的程序路径和参数里取，
// 取不到时程序本身就是这个 agent（不是 node 这类启动器）才跑一次 --version。
func Running(shell int, agentName string) string {
	pid := find(shell, agentName, 3)
	if pid == 0 {
		return ""
	}
	exe, args := procmem.ExeArgs(pid)
	if real, err := filepath.EvalSymlinks(exe); err == nil {
		exe = real
	}
	cmd := agent.Command[agentName]
	for _, p := range append([]string{exe}, args...) {
		if v := pathVersion(p, cmd); v != "" {
			return v
		}
	}
	if exe == "" || procmem.Name([]string{exe}) != cmd {
		return ""
	}
	return askVersion(exe)
}

// pathVersion 取绝对路径 p 里 agent 名字第一次出现之后的版本号：安装目录都是先有名字再有版本
// （claude/versions/2.1.316、codex/0.166.0）；名字之前的是别的程序的版本（nvm 的 node/v22.3.0/bin/codex）。
// 不是路径的参数（提示词、-c 配置）不看。
func pathVersion(p, cmd string) string {
	if !strings.HasPrefix(p, "/") || strings.ContainsAny(p, " \n") {
		return ""
	}
	i := strings.Index(p, cmd)
	if i < 0 {
		return ""
	}
	return VersionIn(p[i:])
}

// find 在 pid 往下 depth 层里找第一个是 agentName 的进程（shell 直接 exec、或经 npx 这类启动器拉起都有）。
func find(pid int, agentName string, depth int) int {
	if depth == 0 {
		return 0
	}
	for _, ch := range procmem.Children(pid) {
		if name, _ := agent.OfProcess(ch); name == agentName {
			return ch
		}
		if p := find(ch, agentName, depth-1); p != 0 {
			return p
		}
	}
	return 0
}

type askKey struct {
	path string
	mod  time.Time
}

var (
	askMu    sync.Mutex
	askCache = map[askKey]string{}
)

// askVersion 跑 exe --version 取版本，按路径和修改时间缓存：升级后文件变了才重跑。
func askVersion(exe string) string {
	fi, err := os.Stat(exe)
	if err != nil {
		return ""
	}
	k := askKey{exe, fi.ModTime()}
	askMu.Lock()
	v, ok := askCache[k]
	askMu.Unlock()
	if ok {
		return v
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, exe, "--version")
	cmd.WaitDelay = time.Second // 孙进程拿着输出管道时，超时后也别一直等
	out, err := cmd.Output()
	v = VersionIn(string(out))
	if err != nil && v == "" {
		return "" // 超时、出错不缓存，下次再试
	}
	askMu.Lock()
	askCache[k] = v
	askMu.Unlock()
	return v
}
