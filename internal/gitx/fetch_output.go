package gitx

import (
	"bytes"
	"context"
	"errors"
	"io"
	"regexp"
	"strconv"
	"strings"
)

const fetchStreamLimit = 4 << 10
const fetchLineLimit = 4 << 10

type fetchOutputKey struct{}
type fetchOutputPolicy struct{ truncated bool }

// StashBounded 只允许固定储藏动作，查询仍使用单独的只读预算。
func StashBounded(ctx context.Context, dir, action, value string) (bool, error) {
	if ctx.Value(readLimitKey{}) != nil { return false, errors.New("Git stash requires a write context") }
	policy := &fetchOutputPolicy{}
	ctx = context.WithValue(ctx, fetchOutputKey{}, policy)
	var err error
	switch action {
	case "push": err = StashPush(ctx, dir, value)
	case "apply": err = StashApply(ctx, dir, value)
	case "drop": err = StashDrop(ctx, dir, value)
	default: err = errors.New("invalid Git stash action")
	}
	return policy.truncated, err
}
type commandOutput interface {
	io.Writer
	Bytes() []byte
	String() string
}

// FetchBounded 沿用 Fetch 的参数与网络配置，只为目标执行限制诊断内存并去掉常见凭据表示。
// 任意第三方 helper 的输出无法判定是否含秘密；这里只保护 URL、认证字段及远端消息。
func FetchBounded(ctx context.Context, dir string) (bool, error) {
	if ctx.Value(readLimitKey{}) != nil {
		return false, errors.New("Git fetch requires a write context")
	}
	policy := &fetchOutputPolicy{}
	err := Fetch(context.WithValue(ctx, fetchOutputKey{}, policy), dir)
	return policy.truncated, err
}

var fetchURL = regexp.MustCompile(`(?i)[a-z][a-z0-9+.-]*://`)
var fetchSCP = regexp.MustCompile(`[^\s<>"'/]+@[^\s<>"':/]+:[^\s<>"']+`)
var fetchSecret = regexp.MustCompile(`(?i)(authorization|password|passwd|secret|token|credential)[\s:=]+`)

func sanitizeFetchLine(line []byte) []byte {
	s := strings.ToValidUTF8(string(line), "")
	if strings.HasPrefix(strings.TrimSpace(s), "remote:") || fetchSecret.MatchString(s) {
		return []byte("[authentication or remote message omitted]")
	}
	// URL 的 userinfo 可以含引号，不能把诊断的引号当作 URL 边界；整行去掉以免留下凭据尾部。
	if fetchURL.MatchString(s) || fetchSCP.MatchString(s) {
		return []byte("[remote URL omitted]")
	}
	return []byte(s)
}

// SanitizeFetchText 用于前置查询错误；与网络命令诊断使用相同的边界，原始配置值不进结果。
func SanitizeFetchText(text string) string {
	out := &fetchOutput{}
	out.Write([]byte(text))
	out.finish()
	return out.String()
}

type fetchOutput struct {
	line       []byte
	dropping   bool
	head, tail []byte
	total      int64
	truncated  bool
}

func (b *fetchOutput) Write(p []byte) (int, error) {
	n := len(p)
	for len(p) > 0 {
		end := bytes.IndexAny(p, "\r\n")
		part := p
		if end >= 0 {
			part = p[:end]
		}
		if !b.dropping {
			if len(b.line)+len(part) > fetchLineLimit {
				// 不保留超长行的片段，避免恰好截断 URL 后把凭据尾部当普通文本。
				b.line, b.dropping, b.truncated = nil, true, true
			} else {
				b.line = append(b.line, part...)
			}
		}
		if end < 0 {
			break
		}
		b.flushLine(true)
		p = p[end+1:]
	}
	return n, nil
}

func (b *fetchOutput) flushLine(newline bool) {
	if b.dropping {
		b.append([]byte("[output line omitted]"))
	} else {
		b.append(sanitizeFetchLine(b.line))
	}
	if newline {
		b.append([]byte{'\n'})
	}
	b.line, b.dropping = nil, false
}
func (b *fetchOutput) finish() {
	if len(b.line) != 0 || b.dropping {
		b.flushLine(false)
	}
}
func (b *fetchOutput) append(p []byte) {
	b.total += int64(len(p))
	if len(b.head) < fetchStreamLimit/2 {
		n := min(len(p), fetchStreamLimit/2-len(b.head))
		b.head = append(b.head, p[:n]...)
		p = p[n:]
	}
	b.tail = append(b.tail, p...)
	if len(b.tail) > fetchStreamLimit/2 {
		b.tail = append([]byte(nil), b.tail[len(b.tail)-fetchStreamLimit/2:]...)
		b.truncated = true
	}
}
func (b *fetchOutput) Bytes() []byte { return []byte(b.String()) }
func (b *fetchOutput) String() string {
	if b.total <= fetchStreamLimit {
		return string(b.head) + string(b.tail)
	}
	return strings.ToValidUTF8(string(b.head), "") + "\n… (" + strconv.FormatInt(b.total-fetchStreamLimit, 10) + " bytes omitted) …\n" + strings.ToValidUTF8(string(b.tail), "")
}

func fetchEnviron(env []string) []string {
	out := make([]string, 0, len(env))
	for _, e := range env {
		key, _, _ := strings.Cut(e, "=")
		if strings.HasPrefix(key, "GIT_TRACE") {
			continue
		}
		switch key {
		case "GIT_DIR", "GIT_WORK_TREE", "GIT_COMMON_DIR", "GIT_INDEX_FILE", "GIT_OBJECT_DIRECTORY", "GIT_ALTERNATE_OBJECT_DIRECTORIES", "GIT_NAMESPACE":
			continue
		}
		out = append(out, e)
	}
	return append(out, "GIT_TRACE2_EVENT=0")
}
