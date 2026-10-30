package history

import (
	"encoding/json"
	"net"
	"net/url"
	"regexp"
	"strings"
)

// ViaUser 是 UsageEvent.Via 的取值之一：网址是用户自己发的。
const ViaUser = "user"

// WebUsage 是一个网站（按域名）被访问了几次。
type WebUsage struct {
	Host  string `json:"host"`
	Count int    `json:"count"`
}

// webURLRe 找文字里的网址：只认 ASCII 字符，中文标点和汉字之前就断开。
var webURLRe = regexp.MustCompile(`https?://[A-Za-z0-9\-._~:/?#\[\]@!$&'()*+,;=%]+`)

// webPrograms 是访问网址的命令行程序：只从这些程序的命令里取网址，echo、git clone 里出现的不算。
var webPrograms = setOf(`curl wget http https xh open bsk aria2c lynx w3m`)

// posseWebRe 认出 posse web open/goto：posse 自己还有许多和网页无关的子命令。
var posseWebRe = regexp.MustCompile(`\bposse\s+web\s+(?:open|goto)\b`)

// webURLs 从一段文字里取出网址，去掉末尾粘上的标点，按出现顺序去重；本机地址不算。
func webURLs(text string) []string {
	var out []string
	seen := map[string]bool{}
	for _, u := range webURLRe.FindAllString(text, -1) {
		u = trimURL(u)
		if webHost(u) == "" || seen[u] {
			continue
		}
		seen[u] = true
		out = append(out, u)
	}
	return out
}

// trimURL 去掉网址末尾的句读和引号；右括号只在网址里没有左括号时去掉（Markdown 链接 [x](url)）。
func trimURL(u string) string {
	for u != "" {
		c := u[len(u)-1]
		switch {
		case strings.IndexByte(".,;:!?'\"]>*", c) >= 0:
			u = u[:len(u)-1]
		case c == ')' && strings.Count(u, "(") < strings.Count(u, ")"):
			u = u[:len(u)-1]
		default:
			return u
		}
	}
	return u
}

// webHost 是网址的域名（去掉 www.），解析不了或是本机地址时返回 ""。
func webHost(raw string) string {
	u, err := url.Parse(raw)
	if err != nil {
		return ""
	}
	h := strings.ToLower(u.Hostname())
	if h == "" || h == "localhost" || strings.HasSuffix(h, ".localhost") {
		return ""
	}
	if ip := net.ParseIP(h); ip != nil && (ip.IsLoopback() || ip.IsUnspecified()) {
		return ""
	}
	return strings.TrimPrefix(h, "www.")
}

// addURL 记一次访问网址，via 是经什么访问的（工具名、程序名，用户发的是 ViaUser）。
func (s *UsageScanner) addURL(raw, via string) {
	h := webHost(raw)
	if h == "" {
		return
	}
	s.web[h]++
	s.event(UsageEvent{URL: clipRunes(raw, maxEventCommand), Via: via})
}

// addSearch 记一次网页搜索。
func (s *UsageScanner) addSearch(query, via string) {
	query = strings.TrimSpace(query)
	if query == "" {
		return
	}
	s.searches++
	s.event(UsageEvent{Query: clipRunes(query, maxEventCommand), Via: via})
}

// webCommand 从一条访问网页的命令里取网址，via 是第一个访问网页的程序。
func (s *UsageScanner) webCommand(cmd string, programs []cliProgram) {
	via := ""
	for _, p := range programs {
		if webPrograms[p.Name] {
			via = p.Name
			break
		}
	}
	if via == "" && posseWebRe.MatchString(cmd) {
		via = "posse web"
	}
	if via == "" {
		return
	}
	for _, u := range webURLs(cmd) {
		s.addURL(u, via)
	}
}

// toolURL 记下工具参数里的 url（WebFetch、浏览器类 MCP 的打开网页）。
func (s *UsageScanner) toolURL(input []byte, via string) {
	var a struct {
		URL string `json:"url"`
	}
	if json.Unmarshal(input, &a) == nil && a.URL != "" {
		s.addURL(a.URL, via)
	}
}

// userURLs 记下用户自己发的消息里的网址。借用历史索引的解析认出哪些是用户说的话：
// 各家以用户身份注入的环境、规则、提醒都已去掉。
func (s *UsageScanner) userURLs(b []byte) {
	if s.parser == nil {
		return
	}
	for _, m := range s.parser.line(b, &s.st) {
		if m.Role != "user" {
			continue
		}
		// 这一行可能没经过 line，时间取解析出的消息时间（cursor 的在消息开头的 <timestamp> 里）
		s.at = m.At
		for _, u := range webURLs(m.Text) {
			s.addURL(u, ViaUser)
		}
	}
}

func clipRunes(s string, n int) string {
	if r := []rune(s); len(r) > n {
		return string(r[:n]) + "…"
	}
	return s
}
