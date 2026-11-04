package term

import (
	"bytes"
	"encoding/base64"
	"strings"
	"unicode"
	"unicode/utf8"
)

// 程序状态协议（OSC 7501，Superlogical 的 Rex 定义）：程序自己报告空闲、工作中、等人处理、完成、出错。
// libghostty-vt 不认识它，常驻进程在输出交给内核之前自己挑出来。
// 规范见 https://www.superlogical.com/rex/docs/build/program-status.md

// ProgramStatus 是程序报告的一条状态（只取根记录，带 id 的子记录不管；kind、progress、title 用不上，不解析）。
type ProgramStatus struct {
	State string // idle | working | done | blocked | error | clear
	App   string // 报告者，如 claude-code、codex、grok-build
	Msg   string // 一行说明，已去掉控制字符
}

const (
	progPrefix = "\x1b]7501;"
	// 规范的整条上限；超过的直接丢掉
	progMaxLen = 4096
	progMaxMsg = 2048
)

// progReply 是对 OSC 7501 ; ? 的应答：Claude、Grok 只在收到它（且先于 CSI c 的应答）后才报告状态。
var progReply = []byte("\x1b]7501;?\x1b\\")

// progScanner 从 PTY 输出里挑出 OSC 7501 序列，序列被两次读取切开时留着前半截。
type progScanner struct {
	pend []byte
}

// feed 扫一段输出，每条完整的序列把 ; 之后、终止符之前的内容交给 emit。p 在调用后会被复用，不留引用。
func (s *progScanner) feed(p []byte, emit func(body []byte)) {
	data := p
	if len(s.pend) > 0 {
		data = append(s.pend, p...)
		s.pend = nil
	}
	for len(data) > 0 {
		i := bytes.Index(data, []byte(progPrefix))
		if i < 0 {
			s.keepPrefix(data)
			return
		}
		rest := data[i+len(progPrefix):]
		end, next := progTerminator(rest)
		switch {
		case end >= 0:
			if end+len(progPrefix) <= progMaxLen {
				emit(rest[:end])
			}
			data = rest[next:]
		case next < 0:
			// 中途换了别的转义序列：这一条作废，从 ESC 处接着找
			data = rest[-next-1:]
		default:
			if len(data)-i <= progMaxLen {
				s.pend = append([]byte(nil), data[i:]...)
			}
			return
		}
	}
}

// keepPrefix 留下末尾可能是下一条序列开头的几个字节。
func (s *progScanner) keepPrefix(data []byte) {
	for n := min(len(progPrefix)-1, len(data)); n > 0; n-- {
		if bytes.HasPrefix([]byte(progPrefix), data[len(data)-n:]) {
			s.pend = append([]byte(nil), data[len(data)-n:]...)
			return
		}
	}
}

// progTerminator 找序列的终止符（BEL 或 ESC \）。找到时返回内容长度和终止符之后的位置；
// 没写完返回 -1, 0；遇到别的 ESC 返回 -1, -(ESC 位置)-1。
func progTerminator(b []byte) (end, next int) {
	for k := 0; k < len(b); k++ {
		switch b[k] {
		case 0x07:
			return k, k + 1
		case 0x1b:
			if k+1 == len(b) {
				return -1, 0
			}
			if b[k+1] == '\\' {
				return k, k + 2
			}
			return -1, -k - 1
		}
	}
	return -1, 0
}

// parseProgramStatus 解析一条报告。probe 为 true 是功能探测（OSC 7501 ; ?）；ok 为 false 是格式不对，
// 或是带 id 的子记录（含只清子记录的 clear）：agent 都只报根记录。
func parseProgramStatus(body []byte) (st ProgramStatus, probe, ok bool) {
	if strings.TrimSpace(string(body)) == "?" {
		return st, true, false
	}
	id := ""
	for _, pair := range strings.Split(string(body), ":") {
		k, v, found := strings.Cut(pair, "=")
		if !found {
			continue
		}
		// 规范：键和值两边的空白去掉
		switch v = strings.TrimSpace(v); strings.TrimSpace(k) {
		case "state":
			st.State = v
		case "id":
			id = v
		case "app":
			st.App = v
		case "msg":
			msg, good := decodeStatusText(v, progMaxMsg)
			if !good {
				return ProgramStatus{}, false, false
			}
			st.Msg = msg
		}
	}
	switch st.State {
	case "idle", "working", "done", "blocked", "error", "clear":
	default:
		return ProgramStatus{}, false, false
	}
	if id != "" {
		return ProgramStatus{}, false, false
	}
	if len(st.App) > 32 {
		st.App = st.App[:32]
	}
	return st, false, true
}

// decodeStatusText 解 base64 的说明文字。内容不可信：解不开、超长或带控制字符的整条作废（规范如此），
// 隐形的方向控制字符去掉。
func decodeStatusText(v string, limit int) (string, bool) {
	raw, err := base64.StdEncoding.DecodeString(v)
	if err != nil {
		if raw, err = base64.RawStdEncoding.DecodeString(v); err != nil {
			return "", false
		}
	}
	if len(raw) > limit || !utf8.Valid(raw) {
		return "", false
	}
	var b strings.Builder
	for _, r := range string(raw) {
		switch {
		case unicode.IsControl(r):
			return "", false
		case unicode.Is(unicode.Bidi_Control, r):
		default:
			b.WriteRune(r)
		}
	}
	return strings.TrimSpace(b.String()), true
}
