package gitx

import (
	"regexp"
	"strings"
	"unicode/utf8"
)

var readAncestry = regexp.MustCompile(`^(?:[~^][0-9]*)*$`)

// ValidReadRevision 只允许引用名或对象名加祖先后缀；不接受选项、范围和路径表达式。
// 引用名按 Git 的字符规则检查，保留 +、引号、Unicode 等合法名字。
func ValidReadRevision(rev string) bool {
	if rev == "" || len(rev) > 1024 || !utf8.ValidString(rev) || strings.HasPrefix(rev, "-") {
		return false
	}
	base := rev
	if i := strings.IndexAny(rev, "~^"); i >= 0 {
		base = rev[:i]
		if !readAncestry.MatchString(rev[i:]) {
			return false
		}
	}
	if base == "" || base == "@" || strings.Contains(base, "..") || strings.Contains(base, "@{") || strings.HasSuffix(base, ".") {
		return false
	}
	for _, r := range base {
		if r <= 32 || r == 127 || strings.ContainsRune("~^:?*[\\", r) {
			return false
		}
	}
	for _, part := range strings.Split(base, "/") {
		if part == "" || strings.HasPrefix(part, ".") || strings.HasSuffix(part, ".lock") {
			return false
		}
	}
	return true
}
