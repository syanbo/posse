package app

import (
	"bytes"
	"io"
	"net/url"
	"strconv"
	"strings"
	"unicode/utf8"

	"golang.org/x/net/html"
)

func (s *htmlResource) rewriteHTML(path string, data []byte) []byte {
	z := html.NewTokenizer(bytes.NewReader(data))
	var out strings.Builder
	style := false
	for {
		kind := z.Next()
		if kind == html.ErrorToken {
			if z.Err() != io.EOF {
				return nil
			}
			break
		}
		if kind == html.TextToken && style {
			out.WriteString(s.rewriteCSS(path, string(z.Text())))
			continue
		}
		if kind != html.StartTagToken && kind != html.SelfClosingTagToken && kind != html.EndTagToken {
			out.Write(z.Raw())
			continue
		}
		t := z.Token()
		if t.Data == "base" {
			continue
		}
		if t.Data == "style" {
			style = kind == html.StartTagToken
		}
		for i := range t.Attr {
			a := &t.Attr[i]
			switch a.Key {
			case "style":
				a.Val = s.rewriteCSS(path, a.Val)
			case "srcset", "imagesrcset":
				a.Val = s.rewriteSrcset(path, a.Val)
			case "srcdoc":
				a.Val = string(s.rewriteHTML(path, []byte(a.Val)))
			case "src", "href", "xlink:href", "poster", "data", "action", "formaction", "background", "manifest":
				a.Val = s.rewriteURL(path, a.Val)
			case "ping":
				a.Val = ""
			}
		}
		// refresh 不经用户点击就导航，不能让它绕过预览的根目录检查。
		if t.Data == "meta" {
			blocked := false
			for _, a := range t.Attr {
				if a.Key == "http-equiv" && strings.EqualFold(a.Val, "refresh") {
					blocked = true
				}
			}
			if blocked {
				continue
			}
		}
		out.WriteString(t.String())
	}
	return []byte(out.String())
}

func (s *htmlResource) rewriteSrcset(path, raw string) string {
	var out []string
	for raw != "" {
		raw = strings.TrimLeft(raw, " \t\r\n\f,")
		end := strings.IndexAny(raw, " \t\r\n\f")
		if end < 0 {
			end = len(raw)
		}
		u := raw[:end]
		raw = raw[end:]
		desc := ""
		if !strings.HasSuffix(u, ",") {
			if i := strings.IndexByte(raw, ','); i >= 0 {
				desc, raw = raw[:i], raw[i+1:]
			} else {
				desc, raw = raw, ""
			}
		}
		u = strings.TrimRight(u, ",")
		if u != "" {
			out = append(out, s.rewriteURL(path, u)+desc)
		}
	}
	return strings.Join(out, ", ")
}

func cssSpace(c byte) bool { return strings.ContainsRune(" \t\r\n\f", rune(c)) }
func cssName(c byte) bool {
	return c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || c == '-' || c == '_' || c == '\\' || c >= 128
}
func cssHex(c byte) bool { return c >= '0' && c <= '9' || c >= 'a' && c <= 'f' || c >= 'A' && c <= 'F' }

// CSS 转义可能藏在函数名和主机名里，先按 CSS 语法解开，再做地址检查。
func cssEscape(raw string, i int) (string, int) {
	i++
	if i >= len(raw) {
		return "", i
	}
	if cssHex(raw[i]) {
		start := i
		for i < len(raw) && i-start < 6 && cssHex(raw[i]) {
			i++
		}
		n, _ := strconv.ParseInt(raw[start:i], 16, 32)
		if i < len(raw) && cssSpace(raw[i]) {
			if raw[i] == '\r' && i+1 < len(raw) && raw[i+1] == '\n' {
				i++
			}
			i++
		}
		if n == 0 || !utf8.ValidRune(rune(n)) {
			n = int64(utf8.RuneError)
		}
		return string(rune(n)), i
	}
	if raw[i] == '\r' || raw[i] == '\n' || raw[i] == '\f' {
		if raw[i] == '\r' && i+1 < len(raw) && raw[i+1] == '\n' {
			i++
		}
		return "", i + 1
	}
	_, n := utf8.DecodeRuneInString(raw[i:])
	return raw[i : i+n], i + n
}

func cssString(raw string, i int) (string, int) {
	quote := raw[i]
	i++
	var out strings.Builder
	for i < len(raw) && raw[i] != quote {
		if raw[i] == '\\' {
			s, end := cssEscape(raw, i)
			out.WriteString(s)
			i = end
		} else {
			out.WriteByte(raw[i])
			i++
		}
	}
	if i < len(raw) {
		i++
	}
	return out.String(), i
}

func cssQuote(raw string) string {
	return `"` + strings.NewReplacer(`\`, `\\`, `"`, `\"`, "\n", `\a `, "\r", `\d `, "<", `\3c `).Replace(raw) + `"`
}

func (s *htmlResource) rewriteCSS(path, raw string) string {
	var out strings.Builder
	importURL := false
	for i := 0; i < len(raw); {
		start := i
		if strings.HasPrefix(raw[i:], "/*") {
			end := strings.Index(raw[i+2:], "*/")
			if end < 0 {
				break
			}
			i += end + 4
			out.WriteString(raw[start:i])
			continue
		}
		if raw[i] == '\'' || raw[i] == '"' {
			value, end := cssString(raw, i)
			i = end
			if importURL || previewLocalURL(value) {
				out.WriteString(cssQuote(s.rewriteURL(path, value)))
			} else {
				out.WriteString(raw[start:i])
			}
			importURL = false
			continue
		}
		at := raw[i] == '@'
		if at {
			i++
		}
		if i < len(raw) && cssName(raw[i]) {
			var name strings.Builder
			for i < len(raw) && cssName(raw[i]) {
				if raw[i] == '\\' {
					v, end := cssEscape(raw, i)
					name.WriteString(v)
					i = end
				} else {
					name.WriteByte(raw[i])
					i++
				}
			}
			word := strings.ToLower(name.String())
			if at && word == "import" {
				importURL = true
			}
			if !at && word == "url" && i < len(raw) && raw[i] == '(' {
				i++
				for i < len(raw) && cssSpace(raw[i]) {
					i++
				}
				value := ""
				if i < len(raw) && (raw[i] == '\'' || raw[i] == '"') {
					value, i = cssString(raw, i)
				} else {
					var v strings.Builder
					for i < len(raw) && raw[i] != ')' {
						if raw[i] == '\\' {
							part, end := cssEscape(raw, i)
							v.WriteString(part)
							i = end
						} else {
							v.WriteByte(raw[i])
							i++
						}
					}
					value = strings.TrimSpace(v.String())
				}
				for i < len(raw) && raw[i] != ')' {
					i++
				}
				if i < len(raw) {
					i++
				}
				out.WriteString("url(" + cssQuote(s.rewriteURL(path, value)) + ")")
				importURL = false
				continue
			}
			out.WriteString(raw[start:i])
			continue
		}
		if i == start {
			i++
		}
		if !cssSpace(raw[start]) && !at {
			importURL = false
		}
		out.WriteString(raw[start:i])
	}
	return out.String()
}

// WebKit 也接受缩写、八进制和整数形式的 IPv4，net.ParseIP 不识别这些写法。
func previewLocalURL(raw string) bool {
	if localImageURL(raw) {
		return true
	}
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" {
		return false
	}
	parts := strings.Split(strings.TrimSuffix(u.Hostname(), "."), ".")
	if len(parts) > 4 {
		return false
	}
	var address uint64
	for i, part := range parts {
		base := 10
		if strings.HasPrefix(part, "0x") || strings.HasPrefix(part, "0X") {
			base, part = 16, part[2:]
		} else if len(part) > 1 && part[0] == '0' {
			base = 8
		}
		n, err := strconv.ParseUint(part, base, 32)
		bits := 8
		if i == len(parts)-1 {
			bits = 8 * (5 - len(parts))
		}
		if err != nil || n >= uint64(1)<<bits {
			return false
		}
		address = address<<bits | n
	}
	return address == 0 || address>>24 == 127
}
