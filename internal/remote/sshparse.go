package remote

import (
	"net"
	"net/url"
	"strconv"
	"strings"
)

// ParseSSHCommand 解析粘贴进目标框的 "ssh ..." 命令或 "ssh://..." 地址。
// 支持常见短参数分组（如 -vvp 2222）与目标后的 -p/-l/-o；显式无效端口直接返回 false 保留原输入。
func ParseSSHCommand(raw string) (Host, []string, bool) {
	s := strings.TrimSpace(raw)
	if s == "" {
		return Host{}, nil, false
	}
	if strings.HasPrefix(strings.ToLower(s), "ssh://") {
		u, err := url.Parse(s)
		if err != nil || u.Hostname() == "" {
			return Host{}, nil, false
		}
		var h Host
		host := u.Hostname()
		if strings.Contains(host, ":") && !strings.HasPrefix(host, "[") {
			host = "[" + host + "]"
		}
		if u.User != nil && u.User.Username() != "" {
			h.Target = u.User.Username() + "@" + host
		} else {
			h.Target = host
		}
		if p := u.Port(); p != "" {
			n, err := strconv.Atoi(p)
			if err != nil || n < 1 || n > 65535 {
				return Host{}, nil, false
			}
			h.Port = n
		} else if strings.Contains(u.Host, ":") && !strings.HasSuffix(u.Host, "]") {
			// 形如 ssh://host:abc 的显式非法端口
			return Host{}, nil, false
		}
		return h, nil, true
	}

	tokens, ok := splitShellTokens(s)
	if !ok || len(tokens) < 2 {
		return Host{}, nil, false
	}
	cmd := strings.ToLower(tokens[0])
	if cmd != "ssh" && !strings.HasSuffix(cmd, "/ssh") {
		return Host{}, nil, false
	}

	valueFlags := map[byte]bool{
		'B': true, 'b': true, 'c': true, 'D': true, 'E': true, 'e': true,
		'F': true, 'I': true, 'i': true, 'J': true, 'L': true, 'l': true,
		'm': true, 'O': true, 'o': true, 'P': true, 'p': true, 'Q': true,
		'R': true, 'S': true, 'W': true, 'w': true,
	}

	var h Host
	var userFromFlag string
	var ignored []string
	addIgnored := func(f string) {
		for _, prev := range ignored {
			if prev == f {
				return
			}
		}
		ignored = append(ignored, f)
	}

	applyValueFlag := func(flagCh byte, val string) bool {
		switch flagCh {
		case 'p':
			n, err := strconv.Atoi(val)
			if err != nil || n < 1 || n > 65535 {
				return false
			}
			h.Port = n
		case 'l':
			if val == "" {
				return false
			}
			userFromFlag = val
		case 'o':
			k, v, hasEq := strings.Cut(val, "=")
			if !hasEq {
				k, v, _ = strings.Cut(val, " ")
			}
			switch strings.ToLower(strings.TrimSpace(k)) {
			case "port":
				n, err := strconv.Atoi(strings.TrimSpace(v))
				if err != nil || n < 1 || n > 65535 {
					return false
				}
				h.Port = n
			case "user":
				uv := strings.TrimSpace(v)
				if uv == "" {
					return false
				}
				userFromFlag = uv
			default:
				addIgnored("-o")
			}
		default:
			addIgnored("-" + string(flagCh))
		}
		return true
	}

	parseFlagToken := func(idx int) (next int, stop bool, valid bool) {
		tok := tokens[idx]
		if tok == "--" {
			return idx + 1, true, true
		}
		if !strings.HasPrefix(tok, "-") || tok == "-" {
			return idx, true, true
		}
		body := tok[1:]
		for j := 0; j < len(body); j++ {
			flagCh := body[j]
			if !valueFlags[flagCh] {
				continue
			}
			val := body[j+1:]
			if val == "" {
				if idx+1 >= len(tokens) {
					return idx, true, false
				}
				val = tokens[idx+1]
				if !applyValueFlag(flagCh, val) {
					return idx, true, false
				}
				return idx + 2, false, true
			}
			if !applyValueFlag(flagCh, val) {
				return idx, true, false
			}
			return idx + 1, false, true
		}
		return idx + 1, false, true
	}

	i := 1
	endOfFlags := false
	for i < len(tokens) {
		if tokens[i] == "--" {
			endOfFlags = true
			i++
			break
		}
		next, stop, valid := parseFlagToken(i)
		if !valid {
			return Host{}, nil, false
		}
		if stop {
			break
		}
		i = next
	}

	if i >= len(tokens) {
		return Host{}, nil, false
	}
	target := tokens[i]
	i++

	// 目标后若紧跟选项（OpenSSH 常见写法如 ssh host -p 2222），在出现远端命令前继续解析
	if !endOfFlags {
		for i < len(tokens) && strings.HasPrefix(tokens[i], "-") && tokens[i] != "-" {
			if tokens[i] == "--" {
				i++
				break
			}
			next, stop, valid := parseFlagToken(i)
			if !valid {
				return Host{}, nil, false
			}
			if stop {
				break
			}
			i = next
		}
	}
	if i < len(tokens) {
		addIgnored("command")
	}
	if userFromFlag != "" && !strings.Contains(target, "@") {
		target = userFromFlag + "@" + target
	}
	h.Target = target
	return h, ignored, true
}

// DeriveName 从 SSH 目标推导出简短可读的默认显示名称。
func DeriveName(target string) string {
	s := strings.TrimSpace(target)
	if s == "" {
		return ""
	}
	if _, host, ok := strings.Cut(s, "@"); ok {
		s = host
	}
	if s == "" {
		return ""
	}
	// IPv6 方括号地址或纯 IP 原样保留
	unbracket := strings.TrimSuffix(strings.TrimPrefix(s, "["), "]")
	if net.ParseIP(unbracket) != nil {
		return s
	}
	if head, _, ok := strings.Cut(s, "."); ok && head != "" {
		return head
	}
	return s
}

func splitShellTokens(s string) ([]string, bool) {
	var out []string
	var cur strings.Builder
	inTok := false
	var quote byte
	for i := 0; i < len(s); i++ {
		ch := s[i]
		if quote != 0 {
			if ch == quote {
				quote = 0
				continue
			}
			if quote == '"' && ch == '\\' && i+1 < len(s) {
				i++
				cur.WriteByte(s[i])
				continue
			}
			cur.WriteByte(ch)
			continue
		}
		switch ch {
		case ' ', '\t', '\r', '\n':
			if inTok {
				out = append(out, cur.String())
				cur.Reset()
				inTok = false
			}
		case '\'', '"':
			quote = ch
			inTok = true
		case '\\':
			if i+1 < len(s) {
				i++
				cur.WriteByte(s[i])
				inTok = true
			}
		default:
			cur.WriteByte(ch)
			inTok = true
		}
	}
	if quote != 0 {
		return nil, false
	}
	if inTok {
		out = append(out, cur.String())
	}
	return out, true
}
