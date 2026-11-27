package remote

import (
	"bufio"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

const (
	maxSSHConfigFiles   = 32
	maxSSHConfigBytes   = 1 << 20
	maxSSHConfigAliases = 256
)

// Alias 描述从 ~/.ssh/config 静态解析出的单个主机别名，用于添加弹窗的快捷候选。
type Alias struct {
	Name     string
	HostName string
	User     string
	Port     int
}

// Hint 返回展示在候选行右侧的简短地址提示（如 user@hostname:2222）。
func (a Alias) Hint() string {
	host := a.HostName
	if host == "" {
		return ""
	}
	var b strings.Builder
	if a.User != "" {
		b.WriteString(a.User)
		b.WriteByte('@')
	}
	b.WriteString(host)
	if a.Port != 0 && a.Port != 22 {
		b.WriteByte(':')
		b.WriteString(strconv.Itoa(a.Port))
	}
	return b.String()
}

type sshConfigWalk struct {
	sshDir    string
	seenFiles []os.FileInfo
	seenAlias map[string]int
	out       []Alias
	files     int
	bytes     int64
}

// ConfigAliases 静态解析 <userHome>/.ssh/config 中的无条件顶层 Host 别名。
// 不调用 ssh -G，避免执行 Match exec；忽略条件 Include（Host/Match 块内）、通配模式、非常规文件并限制扫描预算。
func ConfigAliases(userHome string) []Alias {
	if userHome == "" {
		return nil
	}
	w := &sshConfigWalk{
		sshDir:    filepath.Join(userHome, ".ssh"),
		seenAlias: map[string]int{},
	}
	w.parseFile(filepath.Join(w.sshDir, "config"), 0)
	return w.out
}

func (w *sshConfigWalk) parseFile(path string, depth int) {
	if depth > 4 || w.files >= maxSSHConfigFiles || w.bytes >= maxSSHConfigBytes || len(w.out) >= maxSSHConfigAliases {
		return
	}
	info, err := os.Stat(path)
	if err != nil || !info.Mode().IsRegular() {
		return
	}
	for _, prev := range w.seenFiles {
		if os.SameFile(prev, info) {
			return
		}
	}
	w.seenFiles = append(w.seenFiles, info)
	w.files++

	f, err := os.Open(path)
	if err != nil {
		return
	}
	defer f.Close()

	var current []int // 当前 Host 块关联的 out 下标
	inBlock := false  // 进入过任意 Host/Match 块后，Include 为条件 Include，不再静态展开为全局别名
	inMatch := false
	sc := bufio.NewScanner(io.LimitReader(f, maxSSHConfigBytes-w.bytes))
	sc.Buffer(make([]byte, 0, 16<<10), 256<<10)
	for sc.Scan() {
		rawLine := sc.Text()
		w.bytes += int64(len(rawLine)) + 1
		if w.bytes >= maxSSHConfigBytes {
			break
		}
		key, args := parseSSHConfigLine(rawLine)
		if key == "" || len(args) == 0 {
			continue
		}
		switch strings.ToLower(key) {
		case "match":
			inBlock = true
			inMatch = true
			current = nil
		case "host":
			inMatch = false
			current = nil
			unconditionalAll := len(args) == 1 && args[0] == "*"
			if !unconditionalAll {
				inBlock = true
			}
			for _, pat := range args {
				if pat == "" || strings.ContainsAny(pat, "*?!") {
					continue
				}
				if idx, ok := w.seenAlias[pat]; ok {
					current = append(current, idx)
					continue
				}
				if len(w.out) >= maxSSHConfigAliases {
					continue
				}
				idx := len(w.out)
				w.out = append(w.out, Alias{Name: pat})
				w.seenAlias[pat] = idx
				current = append(current, idx)
			}
		case "include":
			// 仅展开顶层无条件 Include，避免把受 Host/Match 作用域限制的别名误判为全局可直接连接
			if inBlock || inMatch {
				continue
			}
			for _, inc := range args {
				if inc == "" || strings.ContainsAny(inc, "*?[") {
					continue
				}
				if strings.HasPrefix(inc, "~/") {
					inc = filepath.Join(filepath.Dir(w.sshDir), strings.TrimPrefix(inc, "~/"))
				} else if !filepath.IsAbs(inc) {
					inc = filepath.Join(w.sshDir, inc)
				}
				w.parseFile(inc, depth+1)
			}
		case "hostname":
			if inMatch || len(current) == 0 {
				continue
			}
			v := args[0]
			for _, idx := range current {
				if w.out[idx].HostName == "" {
					w.out[idx].HostName = v
				}
			}
		case "user":
			if inMatch || len(current) == 0 {
				continue
			}
			v := args[0]
			for _, idx := range current {
				if w.out[idx].User == "" {
					w.out[idx].User = v
				}
			}
		case "port":
			if inMatch || len(current) == 0 {
				continue
			}
			if n, err := strconv.Atoi(args[0]); err == nil && n >= 1 && n <= 65535 {
				for _, idx := range current {
					if w.out[idx].Port == 0 {
						w.out[idx].Port = n
					}
				}
			}
		}
	}
}

// parseSSHConfigLine 按 OpenSSH 配置词法切分指令和参数，正确处理双引号内的空格与 '#'。
func parseSSHConfigLine(line string) (string, []string) {
	line = strings.TrimSpace(line)
	if line == "" || strings.HasPrefix(line, "#") {
		return "", nil
	}
	var toks []string
	var cur strings.Builder
	inQuote := false
	inTok := false
	for i := 0; i < len(line); i++ {
		ch := line[i]
		if inQuote {
			if ch == '"' {
				inQuote = false
				continue
			}
			cur.WriteByte(ch)
			continue
		}
		if ch == '#' && !inTok {
			break
		}
		switch ch {
		case '"':
			inQuote = true
			inTok = true
		case ' ', '\t', '=':
			if ch == '=' && len(toks) > 0 {
				// 仅首指令后的 '=' 是键值分隔符，参数里的 '=' 原样保留
				cur.WriteByte(ch)
				inTok = true
				continue
			}
			if inTok {
				toks = append(toks, cur.String())
				cur.Reset()
				inTok = false
			}
		default:
			cur.WriteByte(ch)
			inTok = true
		}
	}
	if inQuote {
		return "", nil
	}
	if inTok {
		toks = append(toks, cur.String())
	}
	if len(toks) < 2 {
		return "", nil
	}
	return toks[0], toks[1:]
}
