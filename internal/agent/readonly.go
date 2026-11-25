package agent

import (
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"syscall"
)

// ScratchRoots 定义自动放行允许写入的临时目录、讨论目录根及机器人级只读策略（测试与 daemon 可直接注入）。
type ScratchRoots struct {
	TmpDir         string   // 默认 "/tmp"
	NotesDir       string   // 默认 os.Getenv("POSSE_NOTES")
	DiscussionsDir string   // 可选直接覆盖；为空时取 filepath.Join(NotesDir, "discussions")
	ReadOnlyHosts  []string // 允许 curl /api/0/ 只读查询的域名白名单（默认空 = 不放行任何 curl）
	TrustedScripts []string // 主人显式信任的脚本绝对路径或目录（目录以 / 结尾）
}

// DefaultScratchRoots 返回当前进程环境下的默认临时目录、讨论目录根及飞书 worker 注入的只读白名单策略。
func DefaultScratchRoots() ScratchRoots {
	return ScratchRoots{
		TmpDir:         "/tmp",
		NotesDir:       strings.TrimSpace(os.Getenv("POSSE_NOTES")),
		ReadOnlyHosts:  splitEnvList(os.Getenv("POSSE_LARK_READONLY_HOSTS"), ","),
		TrustedScripts: splitEnvList(os.Getenv("POSSE_LARK_TRUSTED_SCRIPTS"), ":"),
	}
}

func splitEnvList(raw, sep string) []string {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil
	}
	var out []string
	for _, part := range strings.Split(raw, sep) {
		part = strings.TrimSpace(part)
		if part != "" {
			out = append(out, part)
		}
	}
	return out
}

// NormalizeShellLine 仅把「未转义空白 + 反斜杠 + LF」形式的续行按 shell 语义规整为单个前导空白；
// 反斜杠紧贴前一个字符的续行、单引号内续行、转义反斜杠后的换行以及任何 \r（含 CRLF）均保留原字符以便后续拒绝。
func NormalizeShellLine(cmdLine string) string {
	var b strings.Builder
	b.Grow(len(cmdLine))
	var quote byte
	prevWasSpace := false
	for i := 0; i < len(cmdLine); i++ {
		ch := cmdLine[i]
		if quote == '\'' {
			b.WriteByte(ch)
			if ch == '\'' {
				quote = 0
			}
			prevWasSpace = false
			continue
		}
		if ch == '\\' {
			if prevWasSpace && i+1 < len(cmdLine) && cmdLine[i+1] == '\n' {
				i++
				prevWasSpace = true
				continue
			}
			if i+1 < len(cmdLine) {
				b.WriteByte(ch)
				i++
				b.WriteByte(cmdLine[i])
				prevWasSpace = false
				continue
			}
		}
		if quote == 0 && (ch == '\'' || ch == '"') {
			quote = ch
			prevWasSpace = false
		} else if quote == '"' && ch == '"' {
			quote = 0
			prevWasSpace = false
		} else {
			prevWasSpace = ch == ' ' || ch == '\t'
		}
		b.WriteByte(ch)
	}
	return strings.TrimSpace(b.String())
}

// HasUnsafeShellSyntax 检查规整后的命令行是否仍包含裸换行、回车（含 CRLF）、NUL、反引号、命令替换或进程替换。
func HasUnsafeShellSyntax(normalized string) bool {
	if normalized == "" {
		return true
	}
	return strings.ContainsAny(normalized, "\n\r\x00`") ||
		strings.Contains(normalized, "$(") ||
		strings.Contains(normalized, "<(") ||
		strings.Contains(normalized, ">(")
}

// IsSafeReadOnlyCommandLine 判断 shell 命令行在默认根目录下是否全部由安全只读排查段构成。
func IsSafeReadOnlyCommandLine(cmdLine string) bool {
	return IsSafeReadOnlyCommandLineWithRoots(cmdLine, DefaultScratchRoots())
}

// IsSafeReadOnlyCommandLineWithRoots 使用指定 ScratchRoots 判断 shell 命令行是否安全只读。
func IsSafeReadOnlyCommandLineWithRoots(cmdLine string, roots ScratchRoots) bool {
	normalized := NormalizeShellLine(cmdLine)
	if HasUnsafeShellSyntax(normalized) {
		return false
	}
	segs, ok := SplitShellSegments(normalized)
	if !ok || len(segs) == 0 {
		return false
	}
	for _, seg := range segs {
		if !IsSafeReadOnlySegmentWithRoots(seg, roots) {
			return false
		}
	}
	return true
}

// SplitShellSegments 在引号外按 |、|&、;、&&、|| 切分命令段；
// 保留 2>&1 / &>/... 等重定向中的 &；遇到未加引号的 # 注释、后台执行 & 或未闭合引号返回 false。
func SplitShellSegments(s string) ([]string, bool) {
	var segs []string
	var cur strings.Builder
	var quote byte
	for i := 0; i < len(s); i++ {
		ch := s[i]
		if quote != 0 {
			if ch == '\\' && quote == '"' && i+1 < len(s) {
				next := s[i+1]
				if next == '"' || next == '\\' || next == '$' || next == '`' {
					cur.WriteByte(ch)
					i++
					cur.WriteByte(next)
					continue
				}
			}
			if ch == quote {
				quote = 0
			}
			cur.WriteByte(ch)
			continue
		}
		if ch == '\\' && i+1 < len(s) {
			cur.WriteByte(ch)
			i++
			cur.WriteByte(s[i])
			continue
		}
		if ch == '#' {
			// 未加引号的 # 注释直接拒绝
			return nil, false
		}
		if ch == '\'' || ch == '"' {
			quote = ch
			cur.WriteByte(ch)
			continue
		}
		if ch == ';' {
			if t := strings.TrimSpace(cur.String()); t != "" {
				segs = append(segs, t)
			}
			cur.Reset()
			continue
		}
		if ch == '|' {
			if i+1 < len(s) && (s[i+1] == '|' || s[i+1] == '&') {
				i++
			}
			if t := strings.TrimSpace(cur.String()); t != "" {
				segs = append(segs, t)
			}
			cur.Reset()
			continue
		}
		if ch == '>' {
			cur.WriteByte(ch)
			if i+1 < len(s) && (s[i+1] == '&' || s[i+1] == '|') {
				i++
				cur.WriteByte(s[i])
			}
			continue
		}
		if ch == '<' {
			cur.WriteByte(ch)
			if i+1 < len(s) && s[i+1] == '&' {
				i++
				cur.WriteByte(s[i])
			}
			continue
		}
		if ch == '&' {
			if i+1 < len(s) && s[i+1] == '&' {
				i++
				if t := strings.TrimSpace(cur.String()); t != "" {
					segs = append(segs, t)
				}
				cur.Reset()
				continue
			}
			if i+1 < len(s) && s[i+1] == '>' {
				cur.WriteByte('&')
				i++
				cur.WriteByte('>')
				continue
			}
			// 后台执行 & 一律交人工审批
			return nil, false
		}
		cur.WriteByte(ch)
	}
	if quote != 0 {
		return nil, false
	}
	if t := strings.TrimSpace(cur.String()); t != "" {
		segs = append(segs, t)
	}
	return segs, true
}

type shellToken struct {
	val               string
	quoted            bool
	singleQuoted      bool
	hasSentryTokenVar bool
}

type shellRedir struct {
	op     string
	target shellToken
	isDup  bool
}

// ParseSegmentForRuleKey 使用与 CLI 相同的严格展开规则解析需审批命令段的参数及是否存在重定向；
// 无法可靠解析（含未加引号 glob/花括号/未允许变量等）时返回 ok=false。
func ParseSegmentForRuleKey(seg string) (args []string, hasRedir bool, ok bool) {
	toks, redirs, parsed := parseShellSegment(seg, true)
	if !parsed || len(toks) == 0 {
		return nil, false, false
	}
	for _, t := range toks {
		if t.hasSentryTokenVar {
			return nil, false, false
		}
	}
	for _, r := range redirs {
		if r.target.hasSentryTokenVar {
			return nil, false, false
		}
	}
	out := make([]string, len(toks))
	for i, t := range toks {
		out[i] = t.val
	}
	return out, len(redirs) > 0, true
}

// HasUnsafeRuleOutputFlag 检查 lark-cli / tee 等命令是否可能向安全临时目录之外写文件（含显式越界与默认写当前目录）。
func HasUnsafeRuleOutputFlag(args []string) bool {
	if len(args) == 0 {
		return false
	}
	roots := DefaultScratchRoots()
	cmd := filepath.Base(args[0])
	switch cmd {
	case "tee":
		return true
	case "lark-cli":
		var pos []string
		hasOutput := false
		hasDownloadResources := false
		for i := 1; i < len(args); i++ {
			a := args[i]
			if a == "--" {
				break
			}
			if a == "--download-resources" || a == "--download" {
				hasDownloadResources = true
				continue
			}
			if strings.HasPrefix(a, "--download-resources=") && isTruthyBoolFlagValue(strings.TrimPrefix(a, "--download-resources=")) {
				hasDownloadResources = true
				continue
			}
			if strings.HasPrefix(a, "--download=") && isTruthyBoolFlagValue(strings.TrimPrefix(a, "--download=")) {
				hasDownloadResources = true
				continue
			}
			var target string
			isOutFlag := false
			switch {
			case a == "--output" || a == "--output-dir" || a == "-o":
				if i+1 >= len(args) {
					return true
				}
				i++
				target = args[i]
				isOutFlag = true
			case strings.HasPrefix(a, "--output="):
				target = strings.TrimPrefix(a, "--output=")
				isOutFlag = true
			case strings.HasPrefix(a, "--output-dir="):
				target = strings.TrimPrefix(a, "--output-dir=")
				isOutFlag = true
			case strings.HasPrefix(a, "-o="):
				target = strings.TrimPrefix(a, "-o=")
				isOutFlag = true
			case strings.HasPrefix(a, "-o") && len(a) > 2:
				target = a[2:]
				isOutFlag = true
			case a == "--profile" || a == "--as" || a == "--format" || a == "--jq" || a == "-q":
				i++
				continue
			}
			if isOutFlag {
				hasOutput = true
				if !isSafePathInRoots(target, true, true, true, true, false, roots) {
					return true
				}
				continue
			}
			if !strings.HasPrefix(a, "-") && len(pos) < 2 {
				pos = append(pos, a)
			}
		}
		if hasDownloadResources {
			return true
		}
		if len(pos) >= 2 {
			lowerSub := strings.ToLower(pos[1])
			if (strings.Contains(lowerSub, "download") || strings.Contains(lowerSub, "export") ||
				(pos[0] == "drive" && (pos[1] == "+version-get" || pos[1] == "+preview"))) && !hasOutput {
				return true
			}
		}
	}
	return false
}

// IsSafeReadOnlySegment 使用默认根目录判断单个命令段（含其重定向）是否安全只读。
func IsSafeReadOnlySegment(seg string) bool {
	return IsSafeReadOnlySegmentWithRoots(seg, DefaultScratchRoots())
}

// IsSafeReadOnlySegmentWithRoots 使用指定根目录判断单个命令段（含其重定向）是否安全只读。
func IsSafeReadOnlySegmentWithRoots(seg string, roots ScratchRoots) bool {
	toks, redirs, ok := parseShellSegment(seg, true)
	if !ok || len(toks) == 0 {
		return false
	}
	for _, r := range redirs {
		if r.target.hasSentryTokenVar || !isSafeRedir(r, roots) {
			return false
		}
	}
	return isSafeReadOnlyTokens(toks, roots)
}

// IsSafeDiscussionWritePath 判断 write_file 等工具写入路径是否严格位于讨论笔记目录内（含软链接、硬链接与普通文件校验）。
func IsSafeDiscussionWritePath(target string, roots ScratchRoots) bool {
	return isSafePathInRoots(target, false, false, false, false, false, roots)
}

func parseShellSegment(s string, strictExpansions bool) ([]shellToken, []shellRedir, bool) {
	var args []shellToken
	var redirs []shellRedir
	i := 0
	for i < len(s) {
		for i < len(s) && (s[i] == ' ' || s[i] == '\t') {
			i++
		}
		if i >= len(s) {
			break
		}
		if s[i] == '#' {
			return nil, nil, false
		}
		// 检查是否以重定向开始：>、<、&> 或纯数字文件描述符前缀（如 2>/dev/null、1>/tmp/a）
		fd := ""
		redirStart := false
		if s[i] == '>' || s[i] == '<' || (s[i] == '&' && i+1 < len(s) && s[i+1] == '>') {
			redirStart = true
		} else if s[i] >= '0' && s[i] <= '9' {
			j := i
			for j < len(s) && s[j] >= '0' && s[j] <= '9' {
				j++
			}
			if j < len(s) && (s[j] == '>' || s[j] == '<') {
				fd = s[i:j]
				i = j
				redirStart = true
			}
		}

		if redirStart {
			if fd == "" && s[i] == '&' {
				i += 2 // 跳过 &>
				op := "&>"
				if i < len(s) && s[i] == '>' {
					op = "&>>"
					i++
				}
				if i < len(s) && (s[i] == '>' || s[i] == '&' || s[i] == '|') {
					return nil, nil, false
				}
				tok, next, hasTok, ok := readShellToken(s, i, strictExpansions)
				if !ok || !hasTok {
					return nil, nil, false
				}
				i = next
				redirs = append(redirs, shellRedir{op: op, target: tok})
				continue
			}
			if s[i] == '>' {
				i++
				if i < len(s) && s[i] == '&' {
					i++
					tok, next, hasTok, ok := readShellToken(s, i, strictExpansions)
					if !ok || !hasTok {
						return nil, nil, false
					}
					i = next
					redirs = append(redirs, shellRedir{op: fd + ">&", target: tok, isDup: true})
					continue
				}
				if i < len(s) && s[i] == '|' {
					return nil, nil, false
				}
				op := fd + ">"
				if i < len(s) && s[i] == '>' {
					op += ">"
					i++
				}
				if i < len(s) && (s[i] == '>' || s[i] == '&' || s[i] == '|') {
					return nil, nil, false
				}
				tok, next, hasTok, ok := readShellToken(s, i, strictExpansions)
				if !ok || !hasTok {
					return nil, nil, false
				}
				i = next
				redirs = append(redirs, shellRedir{op: op, target: tok})
				continue
			}
			if s[i] == '<' {
				i++
				if i < len(s) && (s[i] == '<' || s[i] == '>' || s[i] == '&') {
					return nil, nil, false
				}
				tok, next, hasTok, ok := readShellToken(s, i, strictExpansions)
				if !ok || !hasTok {
					return nil, nil, false
				}
				i = next
				redirs = append(redirs, shellRedir{op: fd + "<", target: tok})
				continue
			}
		}

		if s[i] == '&' {
			return nil, nil, false
		}
		tok, next, hasTok, ok := readShellToken(s, i, strictExpansions)
		if !ok || !hasTok {
			return nil, nil, false
		}
		i = next
		args = append(args, tok)
	}
	return args, redirs, true
}

func isIdentByte(b byte) bool {
	return (b >= 'a' && b <= 'z') || (b >= 'A' && b <= 'Z') || (b >= '0' && b <= '9') || b == '_'
}

// matchAllowedDollar 检查 s[i:] 是否以双引号内允许的精确变量展开开头：
// 通用允许 $POSSE_NOTES、${POSSE_NOTES}、$HOME、${HOME}；
// 额外标记 $SENTRY_AUTH_TOKEN、${SENTRY_AUTH_TOKEN}，仅允许出现在 curl 的 -H/--header 值中。
func matchAllowedDollar(s string, i int) (int, bool, bool) {
	for _, braced := range []string{"${POSSE_NOTES}", "${HOME}"} {
		if strings.HasPrefix(s[i:], braced) {
			return len(braced), true, false
		}
	}
	for _, bare := range []string{"$POSSE_NOTES", "$HOME"} {
		if strings.HasPrefix(s[i:], bare) {
			end := i + len(bare)
			if end >= len(s) || !isIdentByte(s[end]) {
				return len(bare), true, false
			}
		}
	}
	if strings.HasPrefix(s[i:], "${SENTRY_AUTH_TOKEN}") {
		return len("${SENTRY_AUTH_TOKEN}"), true, true
	}
	if strings.HasPrefix(s[i:], "$SENTRY_AUTH_TOKEN") {
		end := i + len("$SENTRY_AUTH_TOKEN")
		if end >= len(s) || !isIdentByte(s[end]) {
			return len("$SENTRY_AUTH_TOKEN"), true, true
		}
	}
	return 0, false, false
}

func readShellToken(s string, i int, strictExpansions bool) (shellToken, int, bool, bool) {
	for i < len(s) && (s[i] == ' ' || s[i] == '\t') {
		i++
	}
	var cur strings.Builder
	var quote byte
	hasToken := false
	quoted := false
	singleQuoted := false
	hasSentryTokenVar := false
	for i < len(s) {
		ch := s[i]
		if quote != 0 {
			if quote == '"' {
				if ch == '\\' && i+1 < len(s) {
					next := s[i+1]
					if next == '"' || next == '\\' || next == '$' || next == '`' {
						cur.WriteByte(next)
						i += 2
						continue
					}
					// POSIX 双引号内其他反斜杠保持字面值
					cur.WriteByte(ch)
					i++
					continue
				}
				if ch == '"' {
					quote = 0
					i++
					continue
				}
				if strictExpansions && ch == '$' {
					n, ok, isSentry := matchAllowedDollar(s, i)
					if !ok {
						return shellToken{}, i, false, false
					}
					if isSentry {
						hasSentryTokenVar = true
					}
					cur.WriteString(s[i : i+n])
					i += n
					continue
				}
				cur.WriteByte(ch)
				i++
				continue
			}
			// 单引号内全部字面
			if ch == '\'' {
				quote = 0
				i++
				continue
			}
			cur.WriteByte(ch)
			i++
			continue
		}
		if ch == '\'' || ch == '"' {
			quote = ch
			hasToken = true
			quoted = true
			if ch == '\'' {
				singleQuoted = true
			}
			i++
			continue
		}
		if ch == ' ' || ch == '\t' || ch == '>' || ch == '<' || ch == '&' {
			break
		}
		if ch == '#' {
			return shellToken{}, i, false, false
		}
		if ch == '\\' {
			if i+1 >= len(s) {
				return shellToken{}, i, false, false
			}
			i++
			cur.WriteByte(s[i])
			hasToken = true
			i++
			continue
		}
		if strictExpansions {
			if ch == '(' || ch == ')' || ch == '*' || ch == '?' || ch == '[' || ch == '{' || ch == '}' {
				// 拒绝任何未加引号的 glob 元字符、花括号展开及括号（防文件名展开成选项、子 shell 及 zsh glob 限定符）
				return shellToken{}, i, false, false
			}
			if ch == '$' {
				// 引号外变量展开可能触发字段分割，一律拒绝（仅双引号内允许 $HOME / $POSSE_NOTES）
				return shellToken{}, i, false, false
			}
			if ch == '~' {
				// ~ 只允许出现在未加引号的 token 开头作为 HOME（~ 或 ~/...）
				if hasToken || cur.Len() > 0 {
					return shellToken{}, i, false, false
				}
				if i+1 < len(s) {
					next := s[i+1]
					if next != '/' && next != ' ' && next != '\t' && next != '>' && next != '<' && next != '&' {
						return shellToken{}, i, false, false
					}
				}
			}
		}
		cur.WriteByte(ch)
		hasToken = true
		i++
	}
	if quote != 0 {
		return shellToken{}, i, false, false
	}
	return shellToken{
		val:               cur.String(),
		quoted:            quoted,
		singleQuoted:      singleQuoted,
		hasSentryTokenVar: hasSentryTokenVar,
	}, i, hasToken, true
}

func isSafeRedir(r shellRedir, roots ScratchRoots) bool {
	if r.isDup {
		if r.target.quoted {
			return false
		}
		switch r.op {
		case "2>&":
			return r.target.val == "1"
		case "1>&", ">&":
			return r.target.val == "1" || r.target.val == "2"
		default:
			return false
		}
	}
	switch r.op {
	case "<", "0<":
		return r.target.val != ""
	case ">", ">>", "1>", "1>>", "2>", "2>>", "&>", "&>>":
		if r.target.val == "/dev/null" {
			return true
		}
		return isSafeScratchToken(r.target, false, false, roots)
	default:
		return false
	}
}

func hasDotDotSegment(p string) bool {
	for _, part := range strings.Split(filepath.ToSlash(p), "/") {
		if part == ".." {
			return true
		}
	}
	return false
}

func pathWithin(clean, root string, allowRoot bool) bool {
	if root == "" {
		return false
	}
	if allowRoot && clean == root {
		return true
	}
	if root == string(filepath.Separator) {
		return strings.HasPrefix(clean, root)
	}
	return strings.HasPrefix(clean, root+string(filepath.Separator))
}

// evalWithMissingSuffix 对 p 的最深已存在祖先执行 EvalSymlinks，并拼回尚未创建的后缀路径。
func evalWithMissingSuffix(clean string) (string, bool) {
	cur := clean
	for {
		_, err := os.Lstat(cur)
		if err == nil {
			resolved, err := filepath.EvalSymlinks(cur)
			if err != nil {
				return "", false
			}
			rel, err := filepath.Rel(cur, clean)
			if err != nil {
				return "", false
			}
			if rel == "." {
				return filepath.Clean(resolved), true
			}
			return filepath.Clean(filepath.Join(resolved, rel)), true
		}
		if !os.IsNotExist(err) {
			return "", false
		}
		parent := filepath.Dir(cur)
		if parent == cur {
			return "", false
		}
		cur = parent
	}
}

func isSafeScratchToken(tok shellToken, allowRoot, allowExistingDir bool, roots ScratchRoots) bool {
	return isSafePathInRoots(tok.val, !tok.singleQuoted, allowRoot, true, allowExistingDir, false, roots)
}

func isSafeNewScratchToken(tok shellToken, roots ScratchRoots) bool {
	return isSafePathInRoots(tok.val, !tok.singleQuoted, false, true, false, true, roots)
}

func isRootNotSymlink(path string) bool {
	fi, err := os.Lstat(path)
	if err != nil {
		return os.IsNotExist(err)
	}
	if fi.Mode()&os.ModeSymlink != 0 {
		return false
	}
	return fi.IsDir()
}

func isSafeExistingTarget(fi os.FileInfo, allowExistingDir bool) bool {
	if fi.Mode()&os.ModeSymlink != 0 {
		return false
	}
	if fi.IsDir() {
		return allowExistingDir
	}
	if !fi.Mode().IsRegular() {
		return false
	}
	st, ok := fi.Sys().(*syscall.Stat_t)
	if !ok || st.Nlink != 1 {
		return false
	}
	return true
}

func isSafePathInRoots(p string, allowNotesEnv, allowRoot, allowTmp, allowExistingDir, mustNotExist bool, roots ScratchRoots) bool {
	if p == "" || strings.TrimSpace(p) != p {
		return false
	}
	notes := strings.TrimSpace(roots.NotesDir)
	if allowNotesEnv && notes != "" && filepath.IsAbs(notes) {
		switch {
		case strings.HasPrefix(p, "$POSSE_NOTES/"):
			p = filepath.Clean(notes) + p[len("$POSSE_NOTES"):]
		case strings.HasPrefix(p, "${POSSE_NOTES}/"):
			p = filepath.Clean(notes) + p[len("${POSSE_NOTES}"):]
		}
	}
	// 禁止路径中残留环境变量展开、波浪号或 .. 路径段（避免 symlink/.. 语义漂移）
	if strings.ContainsAny(p, "$~") || !filepath.IsAbs(p) || hasDotDotSegment(p) {
		return false
	}
	clean := filepath.Clean(p)

	var candidates []string
	if allowTmp {
		tmpRoot := roots.TmpDir
		if tmpRoot == "" {
			tmpRoot = "/tmp"
		}
		if filepath.IsAbs(tmpRoot) {
			cleanTmp := filepath.Clean(tmpRoot)
			// 保留 macOS /tmp -> /private/tmp 系统别名；其他自定义根本身若为软链接则不作为安全根
			if cleanTmp == "/tmp" || isRootNotSymlink(cleanTmp) {
				candidates = append(candidates, cleanTmp)
			}
		}
	}
	discRoot := strings.TrimSpace(roots.DiscussionsDir)
	notesValid := true
	if notes != "" && filepath.IsAbs(notes) {
		cleanNotes := filepath.Clean(notes)
		if !isRootNotSymlink(cleanNotes) {
			notesValid = false
		}
		if discRoot == "" {
			discRoot = filepath.Join(cleanNotes, "discussions")
		}
	}
	if notesValid && discRoot != "" && filepath.IsAbs(discRoot) {
		cleanDisc := filepath.Clean(discRoot)
		if isRootNotSymlink(cleanDisc) {
			candidates = append(candidates, cleanDisc)
		}
	}

	for _, root := range candidates {
		resolvedRoot, ok := evalWithMissingSuffix(root)
		if !ok {
			continue
		}
		lexMatched := pathWithin(clean, root, allowRoot) || pathWithin(clean, resolvedRoot, allowRoot)
		if !lexMatched {
			continue
		}
		// 非配置根本身的目标若已存在：mustNotExist 时必须尚不存在；否则必须非软链接，文件写操作要求普通文件且 nlink==1
		if clean != root && clean != resolvedRoot {
			if fi, err := os.Lstat(clean); err == nil {
				if mustNotExist || !isSafeExistingTarget(fi, allowExistingDir) {
					return false
				}
			} else if !os.IsNotExist(err) {
				return false
			}
		} else if mustNotExist {
			return false
		}
		resolvedTarget, ok := evalWithMissingSuffix(clean)
		if !ok {
			return false
		}
		if mustNotExist {
			if _, err := os.Lstat(resolvedTarget); !os.IsNotExist(err) {
				return false
			}
		}
		if pathWithin(resolvedTarget, resolvedRoot, allowRoot) {
			return true
		}
	}
	return false
}

func resolveTrustedCommand(tok shellToken) (string, bool) {
	raw := tok.val
	if raw == "" || strings.Contains(raw, "=") {
		return "", false
	}
	if !strings.Contains(raw, "/") {
		if strings.ContainsAny(raw, "$~") {
			return "", false
		}
		return raw, true
	}
	p := raw
	homeDir := strings.TrimSpace(os.Getenv("HOME"))
	if homeDir == "" {
		if h, err := os.UserHomeDir(); err == nil {
			homeDir = h
		}
	}
	if !tok.singleQuoted && homeDir != "" && filepath.IsAbs(homeDir) {
		switch {
		case strings.HasPrefix(p, "$HOME/"):
			p = filepath.Clean(homeDir) + p[len("$HOME"):]
		case strings.HasPrefix(p, "${HOME}/"):
			p = filepath.Clean(homeDir) + p[len("${HOME}"):]
		case !tok.quoted && strings.HasPrefix(p, "~/"):
			p = filepath.Clean(homeDir) + p[1:]
		}
	}
	if strings.ContainsAny(p, "$~") || !filepath.IsAbs(p) || hasDotDotSegment(p) {
		return "", false
	}
	clean := filepath.Clean(p)
	dir := filepath.Dir(clean)
	allowedDirs := []string{
		"/bin",
		"/usr/bin",
		"/usr/sbin",
		"/opt/homebrew/bin",
		"/usr/local/bin",
	}
	if homeDir != "" && filepath.IsAbs(homeDir) {
		allowedDirs = append(allowedDirs, filepath.Join(filepath.Clean(homeDir), ".local", "bin"))
	}
	if posseHome := strings.TrimSpace(os.Getenv("POSSE_HOME")); posseHome != "" && filepath.IsAbs(posseHome) {
		allowedDirs = append(allowedDirs, filepath.Join(filepath.Clean(posseHome), "bin"))
	}
	for _, allowed := range allowedDirs {
		if dir == allowed {
			return filepath.Base(clean), true
		}
	}
	return "", false
}

func anySentryToken(tokens []shellToken) bool {
	for _, t := range tokens {
		if t.hasSentryTokenVar {
			return true
		}
	}
	return false
}

func isValidHostname(host string) bool {
	if host == "" || strings.HasPrefix(host, ".") || strings.HasSuffix(host, ".") ||
		strings.HasPrefix(host, "-") || strings.HasSuffix(host, "-") || strings.Contains(host, "..") {
		return false
	}
	for i := 0; i < len(host); i++ {
		c := host[i]
		if !((c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') || (c >= '0' && c <= '9') || c == '.' || c == '-') {
			return false
		}
	}
	return true
}

func isValidPort(portStr string) bool {
	if portStr == "" || len(portStr) > 5 || portStr[0] == '0' {
		return false
	}
	for i := 0; i < len(portStr); i++ {
		if portStr[i] < '0' || portStr[i] > '9' {
			return false
		}
	}
	n, err := strconv.Atoi(portStr)
	return err == nil && n >= 1 && n <= 65535
}

func isAllowedReadOnlyHostPort(hostname, port string, hosts []string) bool {
	hostname = strings.ToLower(strings.TrimSpace(hostname))
	if !isValidHostname(hostname) {
		return false
	}
	if port != "" && !isValidPort(port) {
		return false
	}
	for _, h := range hosts {
		h = strings.ToLower(strings.TrimSpace(h))
		if h == "" {
			continue
		}
		if strings.Contains(h, ":") {
			if port != "" && hostname+":"+port == h {
				return true
			}
		} else {
			if port == "" && hostname == h {
				return true
			}
		}
	}
	return false
}

func isAllowedReadOnlyHostToken(raw string, hosts []string) bool {
	if raw == "" || strings.TrimSpace(raw) != raw || strings.ContainsAny(raw, "/\\ \t\r\n") {
		return false
	}
	if strings.Contains(raw, ":") {
		host, port, ok := strings.Cut(raw, ":")
		if !ok || strings.Contains(port, ":") || port == "" {
			return false
		}
		return isAllowedReadOnlyHostPort(host, port, hosts)
	}
	return isAllowedReadOnlyHostPort(raw, "", hosts)
}

func isSafeReadOnlyTokens(tokens []shellToken, roots ScratchRoots) bool {
	if len(tokens) == 0 {
		return false
	}
	// glab 专属前置环境变量：仅允许 GITLAB_HOST=<命中 ReadOnlyHosts 的主机> glab ...
	if len(tokens) >= 2 && strings.HasPrefix(tokens[0].val, "GITLAB_HOST=") {
		if anySentryToken(tokens) {
			return false
		}
		host := strings.TrimPrefix(tokens[0].val, "GITLAB_HOST=")
		if !isAllowedReadOnlyHostToken(host, roots.ReadOnlyHosts) {
			return false
		}
		cmd, ok := resolveTrustedCommand(tokens[1])
		if !ok || cmd != "glab" {
			return false
		}
		return isSafeReadOnlyGlab(tokens[2:], roots)
	}
	// 机器人级显式信任脚本（直接执行绝对路径或 bash/sh <绝对路径>）
	if len(roots.TrustedScripts) > 0 && !anySentryToken(tokens) && isTrustedScriptCall(tokens, roots) {
		return true
	}
	cmd, ok := resolveTrustedCommand(tokens[0])
	if !ok {
		return false
	}
	// $SENTRY_AUTH_TOKEN 仅允许出现在 curl 的 -H/--header 参数中，其他命令一律拒绝
	if cmd != "curl" && anySentryToken(tokens) {
		return false
	}
	argToks := tokens[1:]
	args := make([]string, len(argToks))
	for i, t := range argToks {
		args[i] = t.val
	}
	switch cmd {
	case "posse":
		return isSafeReadOnlyPosse(args)
	case "ls", "pwd", "cat", "head", "tail", "wc", "stat", "which",
		"basename", "dirname", "realpath", "readlink", "echo", "printf", "nl", "date", "uname", "cd",
		"grep", "egrep", "fgrep", "jq", "uniq", "cut", "tr", "diff", "comm", "true", "false",
		"du", "df", "od", "strings", "shasum", "md5", "column":
		return true
	case "file":
		return isSafeReadOnlyFile(args)
	case "rg":
		return isSafeReadOnlyRG(args)
	case "sort":
		return isSafeReadOnlySort(args)
	case "tree":
		return isSafeReadOnlyTree(args)
	case "xxd":
		return isSafeReadOnlyXXD(args)
	case "plutil":
		return isSafeReadOnlyPlutil(args)
	case "unzip":
		return isSafeReadOnlyUnzip(args)
	case "tar":
		return isSafeReadOnlyTar(args)
	case "tee":
		return isSafeScratchTee(argToks, roots)
	case "mkdir":
		return isSafeScratchMkdir(argToks, roots)
	case "lark-cli":
		return isSafeReadOnlyLarkCLI(argToks, roots)
	case "glab":
		return isSafeReadOnlyGlab(argToks, roots)
	case "aliyun":
		return isSafeReadOnlyAliyun(argToks, roots)
	case "ossutil":
		return isSafeReadOnlyOSS(argToks, roots)
	case "tccli":
		return isSafeReadOnlyTCCLI(args)
	case "curl":
		return isSafeReadOnlyCurl(argToks, roots)
	case "find":
		return isSafeReadOnlyFind(args)
	case "sed":
		return isSafeReadOnlySed(args)
	case "git":
		return isSafeReadOnlyGit(args)
	default:
		return false
	}
}

func isSafeReadOnlyPosse(args []string) bool {
	// posse CLI 在 -- 之前任意位置的 -h/--help 都只打印帮助不执行写操作
	var rest []string
	for _, a := range args {
		if a == "--" {
			break
		}
		if a == "-h" || a == "--help" {
			return true
		}
		if a == "--json" {
			continue
		}
		rest = append(rest, a)
	}
	if len(rest) == 0 {
		return false
	}
	if rest[0] == "version" || rest[0] == "help" {
		return true
	}
	if len(rest) < 2 {
		return false
	}
	switch rest[0] + " " + rest[1] {
	case "discussion read", "discussion summarize", "discussion mention",
		"discussion list", "discussion show", "discussion pending", "discussion search",
		"task check", "task done", "task ask",
		"task list", "task show", "task inbox", "task guide",
		"history show", "history search", "history list", "history brief",
		"terminal list",
		"workspace list", "workspace show", "workspace current", "workspace diff",
		"project list",
		"lark list", "lark profiles", "lark status",
		"daemon status":
		return true
	}
	return false
}

func isSafeReadOnlyFile(args []string) bool {
	endFlags := false
	for _, a := range args {
		if !endFlags && a == "--" {
			endFlags = true
			continue
		}
		if !endFlags && strings.HasPrefix(a, "-") && a != "-" {
			if strings.HasPrefix(a, "--") {
				return false
			}
			for _, ch := range a[1:] {
				switch ch {
				case 'b', 'i', 'L', 'z':
				default:
					return false
				}
			}
		}
	}
	return true
}

func isSafeReadOnlyRG(args []string) bool {
	for i := 0; i < len(args); i++ {
		a := args[i]
		if a == "--" {
			break
		}
		if a == "--pre" || strings.HasPrefix(a, "--pre=") ||
			a == "--pre-glob" || strings.HasPrefix(a, "--pre-glob=") ||
			a == "--search-zip" || strings.HasPrefix(a, "--search-zip=") {
			return false
		}
		// 跳过明确带值的短选项参数，避免把搜索词误当选项
		switch a {
		case "-e", "-f", "-g", "-t", "-T", "-A", "-B", "-C", "-m", "-M", "-j", "-E", "-r", "--regexp", "--file", "--glob", "--iglob", "--type", "--type-not":
			i++
			continue
		}
		if strings.HasPrefix(a, "-") && !strings.HasPrefix(a, "--") && len(a) > 1 {
			// -e/-f/-g/-t/-T/-r/-E 等紧贴值形式（如 -efooz）跳过
			first := a[1]
			if first == 'e' || first == 'f' || first == 'g' || first == 't' || first == 'T' || first == 'r' || first == 'E' {
				continue
			}
			if strings.ContainsRune(a[1:], 'z') {
				return false
			}
		}
	}
	return true
}

func isSafeReadOnlySort(args []string) bool {
	for _, a := range args {
		if a == "--" {
			break
		}
		if a == "-o" || strings.HasPrefix(a, "-o") ||
			a == "--output" || strings.HasPrefix(a, "--output=") ||
			a == "--compress-program" || strings.HasPrefix(a, "--compress-program=") ||
			a == "-T" || strings.HasPrefix(a, "-T") ||
			a == "--temporary-directory" || strings.HasPrefix(a, "--temporary-directory=") {
			return false
		}
		if strings.HasPrefix(a, "-") && !strings.HasPrefix(a, "--") && len(a) > 1 {
			if strings.ContainsAny(a[1:], "oT") {
				return false
			}
		}
	}
	return true
}

func isSafeReadOnlyFind(args []string) bool {
	for _, a := range args {
		switch a {
		case "-exec", "-execdir", "-ok", "-okdir", "-delete", "-fprint", "-fprint0", "-fprintf", "-fls":
			return false
		}
	}
	return true
}

var (
	sedPrintAddrRe = regexp.MustCompile(`^(([0-9]+|\$)(,([0-9]+|\$))?)?p$`)
	sedPrintPatRe  = regexp.MustCompile(`^/[^/]*/p$`)
)

func isSafeSedPrintScript(script string) bool {
	return sedPrintAddrRe.MatchString(script) || sedPrintPatRe.MatchString(script)
}

func isSafeReadOnlySed(args []string) bool {
	hasN := false
	var scripts []string
	endFlags := false
	for i := 0; i < len(args); i++ {
		a := args[i]
		if !endFlags && a == "--" {
			endFlags = true
			continue
		}
		if !endFlags && strings.HasPrefix(a, "-") && a != "-" {
			switch a {
			case "-n":
				hasN = true
			case "-e":
				if i+1 >= len(args) {
					return false
				}
				i++
				scripts = append(scripts, args[i])
			default:
				return false
			}
			continue
		}
		if len(scripts) == 0 {
			scripts = append(scripts, a)
		}
	}
	if !hasN || len(scripts) == 0 {
		return false
	}
	for _, sc := range scripts {
		if !isSafeSedPrintScript(sc) {
			return false
		}
	}
	return true
}

func isSafeReadOnlyTree(args []string) bool {
	for _, a := range args {
		if a == "--" {
			break
		}
		if a == "-o" || strings.HasPrefix(a, "-o") || a == "--output" || strings.HasPrefix(a, "--output=") {
			return false
		}
	}
	return true
}

func isXXDNumber(s string) bool {
	if s == "" {
		return false
	}
	if s[0] == '+' || s[0] == '-' {
		s = s[1:]
		if s == "" {
			return false
		}
	}
	if strings.HasPrefix(s, "0x") || strings.HasPrefix(s, "0X") {
		s = s[2:]
		if s == "" {
			return false
		}
		for i := 0; i < len(s); i++ {
			c := s[i]
			if !((c >= '0' && c <= '9') || (c >= 'a' && c <= 'f') || (c >= 'A' && c <= 'F')) {
				return false
			}
		}
		return true
	}
	for i := 0; i < len(s); i++ {
		if s[i] < '0' || s[i] > '9' {
			return false
		}
	}
	return true
}

func isSafeReadOnlyXXD(args []string) bool {
	// 仅允许无 flag 或 -l N / -s N / -c N / -g N / -p，拒绝 -r 与任何未知 flag，且最多 1 个位置参数（避免写出第二个文件）。
	pos := 0
	endFlags := false
	for i := 0; i < len(args); i++ {
		a := args[i]
		if !endFlags && a == "--" {
			endFlags = true
			continue
		}
		if !endFlags && strings.HasPrefix(a, "-") && a != "-" {
			if a == "-p" {
				continue
			}
			if a == "-l" || a == "-s" || a == "-c" || a == "-g" {
				if i+1 >= len(args) || !isXXDNumber(args[i+1]) {
					return false
				}
				i++
				continue
			}
			if len(a) > 2 && (a[1] == 'l' || a[1] == 's' || a[1] == 'c' || a[1] == 'g') && isXXDNumber(a[2:]) {
				continue
			}
			return false
		}
		pos++
		if pos > 1 {
			return false
		}
	}
	return true
}

func isSafeReadOnlyPlutil(args []string) bool {
	hasReadMode := false
	hasFile := false
	endFlags := false
	for _, a := range args {
		if !endFlags && a == "--" {
			endFlags = true
			continue
		}
		if !endFlags && strings.HasPrefix(a, "-") && a != "-" {
			switch a {
			case "-p", "-lint":
				hasReadMode = true
			default:
				return false
			}
			continue
		}
		hasFile = true
	}
	return hasReadMode && hasFile
}

func isSafeReadOnlyUnzip(args []string) bool {
	hasL := false
	hasZ := false
	has1 := false
	endFlags := false
	for _, a := range args {
		if !endFlags && a == "--" {
			endFlags = true
			continue
		}
		if !endFlags && strings.HasPrefix(a, "-") && len(a) > 1 {
			switch a {
			case "-l":
				hasL = true
			case "-Z":
				hasZ = true
			case "-1":
				has1 = true
			case "-Z1":
				hasZ = true
				has1 = true
			default:
				return false
			}
			continue
		}
	}
	if hasL && (hasZ || has1) {
		return false
	}
	if has1 && !hasZ {
		return false
	}
	return hasL || hasZ
}

func isSafeReadOnlyTar(args []string) bool {
	hasList := false
	endFlags := false
	for i := 0; i < len(args); i++ {
		a := args[i]
		if !endFlags && a == "--" {
			endFlags = true
			continue
		}
		if !endFlags && strings.HasPrefix(a, "-") && len(a) > 1 {
			if strings.HasPrefix(a, "--") {
				return false
			}
			for j := 1; j < len(a); j++ {
				switch a[j] {
				case 't':
					hasList = true
				case 'v', 'z':
				case 'f':
					var archive string
					if j+1 < len(a) {
						archive = a[j+1:]
						j = len(a)
					} else {
						if i+1 >= len(args) {
							return false
						}
						i++
						archive = args[i]
					}
					if archive == "" || strings.Contains(archive, ":") {
						return false
					}
				default:
					return false
				}
			}
			continue
		}
	}
	return hasList
}

func isSafeScratchTee(args []shellToken, roots ScratchRoots) bool {
	hasPath := false
	endFlags := false
	for _, t := range args {
		a := t.val
		if !endFlags && a == "--" {
			endFlags = true
			continue
		}
		if !endFlags && strings.HasPrefix(a, "-") {
			switch a {
			case "-a", "-i", "--append", "--ignore-interrupts":
				continue
			default:
				return false
			}
		}
		if !isSafeScratchToken(t, false, false, roots) {
			return false
		}
		hasPath = true
	}
	return hasPath
}

func isSafeReadOnlyGit(args []string) bool {
	i := 0
	for i < len(args) {
		a := args[i]
		switch {
		case a == "-C" || a == "--git-dir" || a == "--work-tree":
			if i+1 >= len(args) {
				return false
			}
			i += 2
		case strings.HasPrefix(a, "-C") || strings.HasPrefix(a, "--git-dir=") || strings.HasPrefix(a, "--work-tree=") ||
			a == "--no-pager" || a == "-P" || a == "--bare":
			i++
		case strings.HasPrefix(a, "-"):
			// 拒绝全局 -c 及其他未在白名单内的全局选项
			return false
		default:
			sub := a
			rest := args[i+1:]
			switch sub {
			case "diff", "log", "show":
				for _, r := range rest {
					if r == "--" {
						break
					}
					if strings.HasPrefix(r, "--output") || r == "--ext-diff" || r == "--textconv" {
						return false
					}
				}
				return true
			case "status", "rev-parse", "ls-files", "ls-tree", "blame", "shortlog", "describe":
				return true
			case "branch":
				return isSafeReadOnlyGitBranch(rest)
			case "stash", "worktree":
				return len(rest) >= 1 && rest[0] == "list"
			case "remote":
				return len(rest) == 0 || (len(rest) == 1 && (rest[0] == "-v" || rest[0] == "--verbose"))
			default:
				return false
			}
		}
	}
	return false
}

func isSafeReadOnlyGitBranch(rest []string) bool {
	hasList := false
	endFlags := false
	for i := 0; i < len(rest); i++ {
		r := rest[i]
		if !endFlags && r == "--" {
			endFlags = true
			continue
		}
		if !endFlags && strings.HasPrefix(r, "-") {
			switch {
			case r == "--list":
				hasList = true
			case r == "-a" || r == "--all" || r == "-r" || r == "--remotes" ||
				r == "-v" || r == "-vv" || r == "--verbose" || r == "--show-current":
			case r == "--contains" || r == "--merged" || r == "--no-merged":
				if i+1 < len(rest) && !strings.HasPrefix(rest[i+1], "-") {
					i++
				}
			case strings.HasPrefix(r, "--contains=") || strings.HasPrefix(r, "--merged=") || strings.HasPrefix(r, "--no-merged="):
			default:
				return false
			}
			continue
		}
		// 未带 --list 时不允许任何位置参数，避免误建分支（git branch <new-name>）
		if !hasList {
			return false
		}
	}
	return true
}

func isSafeScratchMkdir(args []shellToken, roots ScratchRoots) bool {
	hasPath := false
	for _, t := range args {
		a := t.val
		if a == "-p" {
			continue
		}
		if strings.HasPrefix(a, "-") {
			return false
		}
		if !isSafeScratchToken(t, true, true, roots) {
			return false
		}
		hasPath = true
	}
	return hasPath
}

type larkOutputArg struct {
	flag string // "--output", "--output-dir", "-o"
	tok  shellToken
}

func prevMayConsumeValue(args []shellToken, i int) bool {
	if i <= 0 {
		return false
	}
	prev := args[i-1].val
	return strings.HasPrefix(prev, "-") && !strings.Contains(prev, "=")
}

func isTruthyBoolFlagValue(v string) bool {
	v = strings.ToLower(strings.TrimSpace(v))
	return v != "false" && v != "0"
}

// isSafeReadOnlyLarkCLI 仅放行 lark-cli 的单次无歧义帮助/dry-run、schema 与显式白名单子命令；
// 取消按动词推断，未列入显式白名单的快捷命令一律交人工审批。
func isSafeReadOnlyLarkCLI(args []shellToken, roots ScratchRoots) bool {
	var pos []string
	var outputs []larkOutputArg
	helpCount := 0
	helpInvalid := false
	dryRunCount := 0
	dryRunInvalid := false
	hasUnknownShortFlag := false
	hasInvalidOutput := false
	hasDownloadResources := false

	for i := 0; i < len(args); i++ {
		a := args[i].val
		if a == "--" {
			break
		}
		if a == "--help" || a == "-h" {
			if prevMayConsumeValue(args, i) {
				helpInvalid = true
				continue
			}
			helpCount++
			continue
		}
		if strings.HasPrefix(a, "--help=") || strings.HasPrefix(a, "-h=") {
			helpInvalid = true
			continue
		}
		if a == "--dry-run" {
			if prevMayConsumeValue(args, i) {
				dryRunInvalid = true
				continue
			}
			dryRunCount++
			continue
		}
		if strings.HasPrefix(a, "--dry-run=") {
			dryRunInvalid = true
			continue
		}
		if a == "--download-resources" || a == "--download" {
			hasDownloadResources = true
			continue
		}
		if strings.HasPrefix(a, "--download-resources=") {
			if isTruthyBoolFlagValue(strings.TrimPrefix(a, "--download-resources=")) {
				hasDownloadResources = true
			}
			continue
		}
		if strings.HasPrefix(a, "--download=") {
			if isTruthyBoolFlagValue(strings.TrimPrefix(a, "--download=")) {
				hasDownloadResources = true
			}
			continue
		}
		switch {
		case a == "--output-dir" || a == "--output" || a == "-o":
			if i+1 >= len(args) {
				hasInvalidOutput = true
				continue
			}
			i++
			outputs = append(outputs, larkOutputArg{flag: a, tok: args[i]})
			continue
		case strings.HasPrefix(a, "--output-dir="):
			outputs = append(outputs, larkOutputArg{
				flag: "--output-dir",
				tok: shellToken{
					val:          strings.TrimPrefix(a, "--output-dir="),
					quoted:       args[i].quoted,
					singleQuoted: args[i].singleQuoted,
				},
			})
			continue
		case strings.HasPrefix(a, "--output="):
			outputs = append(outputs, larkOutputArg{
				flag: "--output",
				tok: shellToken{
					val:          strings.TrimPrefix(a, "--output="),
					quoted:       args[i].quoted,
					singleQuoted: args[i].singleQuoted,
				},
			})
			continue
		case strings.HasPrefix(a, "-o="):
			outputs = append(outputs, larkOutputArg{
				flag: "-o",
				tok: shellToken{
					val:          strings.TrimPrefix(a, "-o="),
					quoted:       args[i].quoted,
					singleQuoted: args[i].singleQuoted,
				},
			})
			continue
		case strings.HasPrefix(a, "-o") && len(a) > 2:
			outputs = append(outputs, larkOutputArg{
				flag: "-o",
				tok: shellToken{
					val:          a[2:],
					quoted:       args[i].quoted,
					singleQuoted: args[i].singleQuoted,
				},
			})
			continue
		case a == "--profile" || a == "--as" || a == "--format" || a == "--jq" || a == "-q":
			i++
			continue
		}
		if strings.HasPrefix(a, "-") {
			// 短选项组（如 -hh、-hv、-hh=false）或未识别的短选项使 help/dry-run 特殊放行失效
			if !strings.HasPrefix(a, "--") && len(a) > 1 {
				hasUnknownShortFlag = true
			}
			continue
		}
		if len(pos) < 2 {
			pos = append(pos, a)
		}
	}

	// --help/-h/--dry-run 出现一次以上、带 = 赋值、与可能消费值的前一 flag 相邻，
	// 或同时出现组合/未知短选项时，整条命令一律走审批
	if helpInvalid || helpCount > 1 || dryRunInvalid || dryRunCount > 1 {
		return false
	}
	if (helpCount > 0 || dryRunCount > 0) && hasUnknownShortFlag {
		return false
	}
	if helpCount == 1 {
		return true
	}
	if hasInvalidOutput {
		return false
	}
	for _, out := range outputs {
		if !isSafeScratchToken(out.tok, true, true, roots) {
			return false
		}
	}
	if dryRunCount == 1 {
		return true
	}
	if len(pos) == 0 {
		return false
	}
	if pos[0] == "schema" {
		return true
	}
	if len(pos) < 2 {
		return false
	}
	domain, sub := pos[0], pos[1]
	lowerSub := strings.ToLower(sub)

	// im 消息拉取带 --download-resources 固定写 ./lark-im-resources/，无法证明落盘位置，一律走审批
	if hasDownloadResources {
		if domain == "im" && (sub == "+messages-mget" || sub == "+chat-messages-list" || sub == "+threads-messages-list") {
			return false
		}
		if len(outputs) == 0 {
			return false
		}
	}

	// base +record-download-attachment 在 lark-cli 1.0.100 仅支持 --output（不支持 --output-dir/-o）
	if domain == "base" && sub == "+record-download-attachment" {
		hasOutput := false
		for _, out := range outputs {
			if out.flag != "--output" {
				return false
			}
			hasOutput = true
		}
		if !hasOutput {
			return false
		}
	}

	// drive +export 默认写当前目录，仅支持 --output-dir 指定安全落盘目录
	if domain == "drive" && sub == "+export" {
		hasOutputDir := false
		for _, out := range outputs {
			if out.flag != "--output-dir" {
				return false
			}
			hasOutputDir = true
		}
		if !hasOutputDir {
			return false
		}
	}

	// 其余含 download 或 export 的显式白名单命令必须显式给出安全输出目标
	if (strings.Contains(lowerSub, "download") || strings.Contains(lowerSub, "export")) && len(outputs) == 0 {
		return false
	}

	return isExplicitSafeLarkCLI(domain, sub)
}

func isExplicitSafeLarkCLI(domain, sub string) bool {
	switch domain {
	case "auth":
		return sub == "status"
	case "profile":
		return sub == "list"
	case "base":
		switch sub {
		case "+record-list", "+record-get", "+record-search", "+table-list", "+field-list", "+view-list", "+base-get", "+app-get", "+url-resolve", "+record-download-attachment":
			return true
		}
	case "im":
		switch sub {
		case "+chat-messages-list", "+messages-mget", "+messages-search", "+chat-search", "+chat-list", "+chat-members-list", "+threads-messages-list", "+messages-resources-download", "+flag-list":
			return true
		}
	case "drive":
		switch sub {
		case "+search", "+download", "+meta", "+list", "+export":
			return true
		}
	case "docs":
		switch sub {
		case "+fetch", "+search":
			return true
		}
	case "wiki":
		switch sub {
		case "+search", "+node-list", "+space-list", "+node-get":
			return true
		}
	case "contact":
		switch sub {
		case "+search-user", "+get-user":
			return true
		}
	case "vc", "minutes", "meeting":
		switch sub {
		case "+search", "+get", "+list", "+fetch":
			return true
		}
	}
	return false
}

// --- Round 2: glab / aliyun / ossutil / tccli / curl / trustedScripts ---

var glabRepoPathRe = regexp.MustCompile(`^[A-Za-z0-9_.-]+(/[A-Za-z0-9_.-]+)+$`)

func hasSafeRepoSegments(p string) bool {
	if !glabRepoPathRe.MatchString(p) {
		return false
	}
	for _, seg := range strings.Split(p, "/") {
		if seg == "." || seg == ".." {
			return false
		}
	}
	return true
}

func isSafeGlabRepoArg(repo string, roots ScratchRoots) bool {
	if repo == "" || strings.TrimSpace(repo) != repo || strings.ContainsAny(repo, "\\ \t\r\n") {
		return false
	}
	if strings.HasPrefix(repo, "https://") {
		if strings.Contains(repo, "%") {
			return false
		}
		u, err := url.Parse(repo)
		if err != nil || u.Scheme != "https" || u.Opaque != "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" {
			return false
		}
		if strings.Contains(u.Host, ":") && u.Port() == "" {
			return false
		}
		if !isAllowedReadOnlyHostPort(u.Hostname(), u.Port(), roots.ReadOnlyHosts) {
			return false
		}
		p := strings.TrimPrefix(u.Path, "/")
		return hasSafeRepoSegments(p)
	}
	if strings.Contains(repo, ":") {
		return false
	}
	return hasSafeRepoSegments(repo)
}

func isSafeGlabIssueTarget(arg string, roots ScratchRoots) bool {
	if arg == "" || strings.TrimSpace(arg) != arg || strings.ContainsAny(arg, "\\ \t\r\n") {
		return false
	}
	if strings.HasPrefix(arg, "https://") {
		if strings.Contains(arg, "%") || strings.Contains(arg, "..") {
			return false
		}
		u, err := url.Parse(arg)
		if err != nil || u.Scheme != "https" || u.Opaque != "" || u.User != nil {
			return false
		}
		if strings.Contains(u.Host, ":") && u.Port() == "" {
			return false
		}
		return isAllowedReadOnlyHostPort(u.Hostname(), u.Port(), roots.ReadOnlyHosts)
	}
	if strings.Contains(arg, "://") || strings.Contains(arg, ":") {
		return false
	}
	id := strings.TrimPrefix(arg, "#")
	return isDigits(id)
}

type glabSubSpec struct {
	boolFlags  map[string]bool
	valueFlags map[string]bool
	minPos     int
	maxPos     int
	repoPos    bool
	issuePos   bool
}

var glabQuerySpecs = map[string]glabSubSpec{
	"repo view": {
		boolFlags: map[string]bool{"-h": true, "--help": true},
		valueFlags: map[string]bool{
			"-b": true, "--branch": true,
			"--jq": true,
			"-F":   true, "--output": true,
		},
		minPos:  0,
		maxPos:  1,
		repoPos: true,
	},
	"mr view": {
		boolFlags: map[string]bool{
			"-c": true, "--comments": true,
			"-h": true, "--help": true,
			"--resolved": true,
			"-s":         true, "--system-logs": true,
			"--unresolved": true,
		},
		valueFlags: map[string]bool{
			"--jq": true,
			"-F":   true, "--output": true,
			"-p": true, "--page": true,
			"-P": true, "--per-page": true,
			"-R": true, "--repo": true,
		},
		minPos: 0,
		maxPos: 1,
	},
	"mr list": {
		boolFlags: map[string]bool{
			"-A": true, "--all": true,
			"-c": true, "--closed": true,
			"-d": true, "--draft": true,
			"-h": true, "--help": true,
			"-M": true, "--merged": true,
			"--not-draft": true,
		},
		valueFlags: map[string]bool{
			"-a": true, "--assignee": true,
			"--author":        true,
			"--created-after": true, "--created-before": true,
			"--deployed-after": true, "--deployed-before": true,
			"--environment": true,
			"-g":            true, "--group": true,
			"--jq": true,
			"-l":   true, "--label": true,
			"-m": true, "--milestone": true,
			"--not-label": true,
			"-o":          true, "--order": true,
			"-F": true, "--output": true,
			"-p": true, "--page": true,
			"-P": true, "--per-page": true,
			"-R": true, "--repo": true,
			"-r": true, "--reviewer": true,
			"--search": true,
			"-S":       true, "--sort": true,
			"-s": true, "--source-branch": true,
			"-t": true, "--target-branch": true,
		},
		minPos: 0,
		maxPos: 0,
	},
	"mr diff": {
		boolFlags: map[string]bool{
			"-h": true, "--help": true,
			"--raw": true,
		},
		valueFlags: map[string]bool{
			"--color": true,
			"-R":      true, "--repo": true,
		},
		minPos: 0,
		maxPos: 1,
	},
	"issue view": {
		boolFlags: map[string]bool{
			"-c": true, "--comments": true,
			"-h": true, "--help": true,
			"-s": true, "--system-logs": true,
		},
		valueFlags: map[string]bool{
			"--jq": true,
			"-F":   true, "--output": true,
			"-p": true, "--page": true,
			"-P": true, "--per-page": true,
			"-R": true, "--repo": true,
		},
		minPos:   1,
		maxPos:   1,
		issuePos: true,
	},
	"issue list": {
		boolFlags: map[string]bool{
			"-A": true, "--all": true,
			"-c": true, "--closed": true,
			"-C": true, "--confidential": true,
			"-h": true, "--help": true,
		},
		valueFlags: map[string]bool{
			"-a": true, "--assignee": true,
			"--author": true,
			"-e":       true, "--epic": true,
			"-g": true, "--group": true,
			"--in": true,
			"-t":   true, "--issue-type": true,
			"-i": true, "--iteration": true,
			"--jq": true,
			"-l":   true, "--label": true,
			"-m": true, "--milestone": true,
			"--not-assignee": true, "--not-author": true, "--not-label": true,
			"--order": true,
			"-O":      true, "--output": true,
			"-F": true, "--output-format": true,
			"-p": true, "--page": true,
			"-P": true, "--per-page": true,
			"-R": true, "--repo": true,
			"--search": true,
			"-s":       true, "--sort": true,
		},
		minPos: 0,
		maxPos: 0,
	},
	"release view": {
		boolFlags: map[string]bool{"-h": true, "--help": true},
		valueFlags: map[string]bool{
			"--jq": true,
			"-F":   true, "--output": true,
			"-R": true, "--repo": true,
		},
		minPos: 0,
		maxPos: 1,
	},
	"release list": {
		boolFlags: map[string]bool{"-h": true, "--help": true},
		valueFlags: map[string]bool{
			"--jq": true,
			"-F":   true, "--output": true,
			"-p": true, "--page": true,
			"-P": true, "--per-page": true,
			"-R": true, "--repo": true,
		},
		minPos: 0,
		maxPos: 0,
	},
	"ci list": {
		boolFlags: map[string]bool{
			"-h": true, "--help": true,
			"-y": true, "--yaml-errors": true,
		},
		valueFlags: map[string]bool{
			"--jq": true,
			"-n":   true, "--name": true,
			"-o": true, "--order": true,
			"-F": true, "--output": true,
			"-p": true, "--page": true,
			"-P": true, "--per-page": true,
			"-r": true, "--ref": true,
			"-R": true, "--repo": true,
			"--scope":  true,
			"--sha":    true,
			"--sort":   true,
			"--source": true,
			"-s":       true, "--status": true,
			"-a": true, "--updated-after": true,
			"-b": true, "--updated-before": true,
			"-u": true, "--username": true,
		},
		minPos: 0,
		maxPos: 0,
	},
	"ci status": {
		boolFlags: map[string]bool{
			"-c": true, "--compact": true,
			"-h": true, "--help": true,
		},
		valueFlags: map[string]bool{
			"-b": true, "--branch": true,
			"--jq": true,
			"-F":   true, "--output": true,
			"-R": true, "--repo": true,
		},
		minPos: 0,
		maxPos: 0,
	},
}

func isSafeReadOnlyGlab(argToks []shellToken, roots ScratchRoots) bool {
	if len(argToks) == 0 {
		return false
	}
	sub := argToks[0].val
	if sub == "api" {
		return isSafeReadOnlyGlabAPI(argToks[1:])
	}
	if len(argToks) < 2 {
		return false
	}
	act := argToks[1].val
	rest := argToks[2:]
	if sub == "repo" && act == "clone" {
		return isSafeReadOnlyGlabClone(rest, roots)
	}
	spec, ok := glabQuerySpecs[sub+" "+act]
	if !ok {
		return false
	}
	var pos []string
	endFlags := false
	for i := 0; i < len(rest); i++ {
		a := rest[i].val
		if !endFlags && a == "--" {
			endFlags = true
			continue
		}
		if !endFlags && strings.HasPrefix(a, "-") && a != "-" {
			if strings.HasPrefix(a, "--") {
				name, val, hasEq := strings.Cut(a, "=")
				if hasEq {
					if !spec.valueFlags[name] {
						return false
					}
					if name == "--repo" && !isSafeGlabRepoArg(val, roots) {
						return false
					}
					continue
				}
				if spec.boolFlags[a] {
					continue
				}
				if spec.valueFlags[a] {
					if i+1 >= len(rest) {
						return false
					}
					val := rest[i+1].val
					if a == "--repo" && !isSafeGlabRepoArg(val, roots) {
						return false
					}
					i++
					continue
				}
				return false
			}
			// 拒绝任何组合或粘连短选项（如 -cw、-Ap、-Rfoo）
			if len(a) != 2 {
				return false
			}
			if spec.boolFlags[a] {
				continue
			}
			if spec.valueFlags[a] {
				if i+1 >= len(rest) {
					return false
				}
				val := rest[i+1].val
				if a == "-R" && !isSafeGlabRepoArg(val, roots) {
					return false
				}
				i++
				continue
			}
			return false
		}
		pos = append(pos, a)
	}
	if len(pos) < spec.minPos || len(pos) > spec.maxPos {
		return false
	}
	if spec.repoPos && len(pos) == 1 && !isSafeGlabRepoArg(pos[0], roots) {
		return false
	}
	if spec.issuePos && len(pos) == 1 && !isSafeGlabIssueTarget(pos[0], roots) {
		return false
	}
	return true
}

func isSafeGlabAPIHeader(hv string) bool {
	if hv == "" || strings.ContainsAny(hv, "\r\n@") {
		return false
	}
	idx := strings.IndexByte(hv, ':')
	if idx <= 0 {
		return false
	}
	name := strings.ToLower(strings.TrimSpace(hv[:idx]))
	if name != "accept" && name != "content-type" {
		return false
	}
	return strings.TrimSpace(hv[idx+1:]) != ""
}

func isSafeReadOnlyGlabAPI(argToks []shellToken) bool {
	var pos []string
	endFlags := false
	for i := 0; i < len(argToks); i++ {
		a := argToks[i].val
		if !endFlags && a == "--" {
			endFlags = true
			continue
		}
		if !endFlags && strings.HasPrefix(a, "-") {
			switch {
			case a == "--paginate":
				continue
			case a == "-X" || a == "--method":
				if i+1 >= len(argToks) || !strings.EqualFold(argToks[i+1].val, "GET") {
					return false
				}
				i++
				continue
			case strings.HasPrefix(a, "--method="):
				if !strings.EqualFold(strings.TrimPrefix(a, "--method="), "GET") {
					return false
				}
				continue
			case strings.HasPrefix(a, "-X") && len(a) > 2:
				if !strings.EqualFold(a[2:], "GET") {
					return false
				}
				continue
			case a == "-H" || a == "--header":
				if i+1 >= len(argToks) || !isSafeGlabAPIHeader(argToks[i+1].val) {
					return false
				}
				i++
				continue
			case strings.HasPrefix(a, "--header="):
				if !isSafeGlabAPIHeader(strings.TrimPrefix(a, "--header=")) {
					return false
				}
				continue
			default:
				// 拒绝 --hostname、-f/-F/--field/--raw-field/--input/--form/--placeholder 及任何未在白名单内的 flag
				return false
			}
		}
		pos = append(pos, a)
	}
	if len(pos) != 1 {
		return false
	}
	raw := pos[0]
	if raw == "" || strings.Contains(raw, "://") || strings.HasPrefix(raw, "//") ||
		strings.Contains(raw, "..") || strings.Contains(raw, "\\") || strings.Contains(raw, "%") ||
		strings.ContainsAny(raw, " \t\r\n") {
		return false
	}
	for i := 0; i < len(raw); i++ {
		if raw[i] < 0x20 || raw[i] == 0x7f {
			return false
		}
	}
	p := raw
	if idx := strings.IndexAny(p, "?#"); idx >= 0 {
		p = p[:idx]
	}
	p = strings.ToLower(strings.Trim(p, "/"))
	if p == "" {
		return false
	}
	for _, seg := range strings.Split(p, "/") {
		if seg == "" || seg == "graphql" {
			return false
		}
	}
	return true
}

func isSafeReadOnlyGlabClone(argToks []shellToken, roots ScratchRoots) bool {
	var before []shellToken
	var gitFlags []shellToken
	dashIdx := -1
	for i, t := range argToks {
		if t.val == "--" {
			dashIdx = i
			break
		}
	}
	if dashIdx >= 0 {
		before = argToks[:dashIdx]
		gitFlags = argToks[dashIdx+1:]
	} else {
		before = argToks
	}
	if len(before) != 2 {
		return false
	}
	if strings.HasPrefix(before[0].val, "-") || strings.HasPrefix(before[1].val, "-") {
		return false
	}
	if !isSafeGlabRepoArg(before[0].val, roots) {
		return false
	}
	if !isSafeNewScratchToken(before[1], roots) {
		return false
	}
	for i := 0; i < len(gitFlags); i++ {
		a := gitFlags[i].val
		switch {
		case a == "--depth":
			if i+1 >= len(gitFlags) || !isDigits(gitFlags[i+1].val) {
				return false
			}
			i++
		case strings.HasPrefix(a, "--depth="):
			if !isDigits(strings.TrimPrefix(a, "--depth=")) {
				return false
			}
		case a == "-b" || a == "--branch" || a == "--filter" || a == "--shallow-since":
			if i+1 >= len(gitFlags) || strings.HasPrefix(gitFlags[i+1].val, "-") {
				return false
			}
			i++
		case strings.HasPrefix(a, "--branch=") || strings.HasPrefix(a, "--filter=") || strings.HasPrefix(a, "--shallow-since="):
		case a == "--single-branch" || a == "--no-single-branch" || a == "--no-tags" || a == "--quiet" || a == "-q" || a == "--bare":
		default:
			return false
		}
	}
	return true
}

func isDigits(s string) bool {
	if s == "" {
		return false
	}
	for i := 0; i < len(s); i++ {
		if s[i] < '0' || s[i] > '9' {
			return false
		}
	}
	return true
}

func isPositiveNumber(s string) bool {
	if s == "" {
		return false
	}
	dot := false
	hasDigit := false
	for i := 0; i < len(s); i++ {
		c := s[i]
		if c >= '0' && c <= '9' {
			hasDigit = true
			continue
		}
		if c == '.' && !dot {
			dot = true
			continue
		}
		return false
	}
	return hasDigit
}

func hasPascalActionPrefix(action string, prefixes ...string) bool {
	for _, p := range prefixes {
		if strings.HasPrefix(action, p) && len(action) > len(p) {
			return true
		}
	}
	return false
}

func hasSensitiveActionWord(action string, words []string) bool {
	low := strings.ToLower(action)
	for _, w := range words {
		if strings.Contains(low, w) {
			return true
		}
	}
	return false
}

func isForbiddenFileRefValue(v string) bool {
	if strings.HasPrefix(v, "@") {
		return true
	}
	return strings.HasPrefix(strings.ToLower(v), "file://")
}

var aliyunSensitiveActionWords = []string{
	"secret", "token", "password", "credential", "key", "policy", "role",
}

var aliyunSLSValueFlags = map[string]bool{
	"--profile": true, "-p": true,
	"--region": true, "--language": true,
	"--cli-query": true, "--version": true,
	"--project": true, "--logstore": true,
	"--from": true, "--to": true,
	"--query": true, "--topic": true,
	"--line": true, "--offset": true,
	"--body": true, "--type": true,
	"--shard": true, "--cursor": true,
	"--count": true, "--page": true, "--size": true,
}

var aliyunSLSOptBoolFlags = map[string]bool{
	"--reverse":   true,
	"--power-sql": true,
}

var aliyunSLSBoolFlags = map[string]bool{
	"--quiet":  true,
	"--secure": true,
}

func isBoolLiteral(s string) bool {
	return strings.EqualFold(s, "true") || strings.EqualFold(s, "false")
}

func isSafeAliyunSLSFlags(argToks []shellToken) bool {
	for i := 0; i < len(argToks); i++ {
		a := argToks[i].val
		if !strings.HasPrefix(a, "-") || a == "-" || a == "--" {
			return false
		}
		name, val, hasEq := strings.Cut(a, "=")
		if hasEq {
			if aliyunSLSValueFlags[name] {
				if isForbiddenFileRefValue(val) {
					return false
				}
				continue
			}
			if aliyunSLSOptBoolFlags[name] {
				if !isBoolLiteral(val) {
					return false
				}
				continue
			}
			return false
		}
		if aliyunSLSBoolFlags[a] {
			continue
		}
		if aliyunSLSOptBoolFlags[a] {
			if i+1 < len(argToks) && isBoolLiteral(argToks[i+1].val) {
				i++
			}
			continue
		}
		if aliyunSLSValueFlags[a] {
			if i+1 >= len(argToks) || isForbiddenFileRefValue(argToks[i+1].val) {
				return false
			}
			i++
			continue
		}
		return false
	}
	return true
}

func isSafeReadOnlyAliyun(argToks []shellToken, roots ScratchRoots) bool {
	i := 0
	hasCLIQuery := false
	for i < len(argToks) {
		a := argToks[i].val
		if !strings.HasPrefix(a, "-") {
			break
		}
		name, val, hasEq := strings.Cut(a, "=")
		if hasEq {
			switch name {
			case "--profile", "--region", "--language", "--version":
				if isForbiddenFileRefValue(val) {
					return false
				}
				i++
			case "--cli-query":
				if isForbiddenFileRefValue(val) {
					return false
				}
				hasCLIQuery = true
				i++
			default:
				return false
			}
			continue
		}
		switch a {
		case "--profile", "-p", "--region", "--language", "--version":
			if i+1 >= len(argToks) || isForbiddenFileRefValue(argToks[i+1].val) {
				return false
			}
			i += 2
		case "--cli-query":
			if i+1 >= len(argToks) || isForbiddenFileRefValue(argToks[i+1].val) {
				return false
			}
			hasCLIQuery = true
			i += 2
		case "--secure", "--quiet":
			i++
		default:
			return false
		}
	}
	if i >= len(argToks) {
		return false
	}
	service := strings.ToLower(argToks[i].val)
	rest := argToks[i+1:]
	switch service {
	case "sls":
		if len(rest) == 0 || strings.HasPrefix(rest[0].val, "-") {
			return false
		}
		action := rest[0].val
		if !hasPascalActionPrefix(action, "Get", "List", "Describe") || hasSensitiveActionWord(action, aliyunSensitiveActionWords) {
			return false
		}
		return isSafeAliyunSLSFlags(rest[1:])
	case "oss", "ossutil":
		if hasCLIQuery {
			return false
		}
		return isSafeReadOnlyOSS(rest, roots)
	default:
		// configure、ram、sts 等其余服务一律审批
		return false
	}
}

var ossLSBoolFlags = map[string]bool{
	"-s": true, "--short-format": true,
	"-d": true, "--directory": true,
	"-m": true, "--multipart": true,
	"-a": true, "--all-type": true,
	"--all-versions": true,
	"--quiet":        true, "--secure": true,
	"--cli-ai-mode": true, "--no-cli-ai-mode": true,
	"--cli-non-interactive": true,
}

var ossLSValueFlags = map[string]bool{
	"--profile": true, "-p": true,
	"--region": true, "--language": true,
	"--limited-num": true, "--marker": true,
	"--upload-id-marker": true, "--version-id-marker": true,
	"--encoding-type": true, "--include": true, "--exclude": true,
	"--payer": true, "--retry-times": true,
	"--cli-output": true, "--cli-cursor": true,
}

var ossStatBoolFlags = map[string]bool{
	"--quiet": true, "--secure": true,
	"--cli-ai-mode": true, "--no-cli-ai-mode": true,
	"--cli-non-interactive": true,
}

var ossStatValueFlags = map[string]bool{
	"--profile": true, "-p": true,
	"--region": true, "--language": true,
	"--encoding-type": true, "--version-id": true,
	"--payer": true, "--retry-times": true,
	"--cli-output": true,
}

var ossCPBoolFlags = map[string]bool{
	"-r": true, "--recursive": true,
	"-f": true, "--force": true,
	"-u": true, "--update": true,
	"--only-current-dir":    true,
	"--disable-all-symlink": true, "--disable-dir-object": true,
	"--quiet": true, "--secure": true,
	"--cli-non-interactive": true,
}

var ossCPValueFlags = map[string]bool{
	"--profile": true, "-p": true,
	"--region": true, "--language": true,
	"-j": true, "--jobs": true, "--parallel": true,
	"--part-size": true, "--bigfile-threshold": true,
	"--maxupspeed": true, "--maxdownspeed": true,
	"--retry-times": true, "--timeout": true,
	"--include": true, "--exclude": true,
	"--payer": true, "--encoding-type": true, "--version-id": true,
}

func isSafeReadOnlyOSS(argToks []shellToken, roots ScratchRoots) bool {
	i := 0
	for i < len(argToks) {
		a := argToks[i].val
		if !strings.HasPrefix(a, "-") {
			break
		}
		name, val, hasEq := strings.Cut(a, "=")
		if hasEq {
			switch name {
			case "--profile", "--region", "--language":
				if isForbiddenFileRefValue(val) {
					return false
				}
				i++
			default:
				return false
			}
			continue
		}
		switch a {
		case "--profile", "-p", "--region", "--language":
			if i+1 >= len(argToks) || isForbiddenFileRefValue(argToks[i+1].val) {
				return false
			}
			i += 2
		case "--quiet", "--secure":
			i++
		default:
			return false
		}
	}
	if i >= len(argToks) {
		return false
	}
	sub := argToks[i].val
	rest := argToks[i+1:]
	switch sub {
	case "ls", "stat":
		boolFlags, valFlags := ossLSBoolFlags, ossLSValueFlags
		if sub == "stat" {
			boolFlags, valFlags = ossStatBoolFlags, ossStatValueFlags
		}
		var pos []shellToken
		endFlags := false
		for j := 0; j < len(rest); j++ {
			a := rest[j].val
			if !endFlags && a == "--" {
				endFlags = true
				continue
			}
			if !endFlags && strings.HasPrefix(a, "-") && a != "-" {
				name, val, hasEq := strings.Cut(a, "=")
				if hasEq {
					if !strings.HasPrefix(name, "--") || !valFlags[name] || isForbiddenFileRefValue(val) {
						return false
					}
					continue
				}
				if boolFlags[a] {
					continue
				}
				if valFlags[a] {
					if j+1 >= len(rest) || isForbiddenFileRefValue(rest[j+1].val) {
						return false
					}
					j++
					continue
				}
				return false
			}
			pos = append(pos, rest[j])
		}
		if len(pos) > 1 {
			return false
		}
		for _, p := range pos {
			if !strings.HasPrefix(p.val, "oss://") {
				return false
			}
		}
		return true
	case "cp":
		var pos []shellToken
		endFlags := false
		recursive := false
		outputDirCount := 0
		checkpointDirCount := 0
		for j := 0; j < len(rest); j++ {
			t := rest[j]
			a := t.val
			if !endFlags && a == "--" {
				endFlags = true
				continue
			}
			if !endFlags && strings.HasPrefix(a, "-") && a != "-" {
				name, val, hasEq := strings.Cut(a, "=")
				if hasEq {
					if !strings.HasPrefix(name, "--") {
						return false
					}
					if name == "--output-dir" {
						outputDirCount++
						dirTok := shellToken{val: val, quoted: t.quoted, singleQuoted: t.singleQuoted}
						if outputDirCount > 1 || !isSafeScratchToken(dirTok, false, false, roots) {
							return false
						}
						continue
					}
					if name == "--checkpoint-dir" {
						checkpointDirCount++
						dirTok := shellToken{val: val, quoted: t.quoted, singleQuoted: t.singleQuoted}
						if checkpointDirCount > 1 || !isSafeScratchToken(dirTok, false, false, roots) {
							return false
						}
						continue
					}
					if !ossCPValueFlags[name] || isForbiddenFileRefValue(val) {
						return false
					}
					continue
				}
				if a == "--output-dir" {
					if j+1 >= len(rest) {
						return false
					}
					outputDirCount++
					if outputDirCount > 1 || !isSafeScratchToken(rest[j+1], false, false, roots) {
						return false
					}
					j++
					continue
				}
				if a == "--checkpoint-dir" {
					if j+1 >= len(rest) {
						return false
					}
					checkpointDirCount++
					if checkpointDirCount > 1 || !isSafeScratchToken(rest[j+1], false, false, roots) {
						return false
					}
					j++
					continue
				}
				if ossCPBoolFlags[a] {
					if a == "-r" || a == "--recursive" {
						recursive = true
					}
					continue
				}
				if ossCPValueFlags[a] {
					if j+1 >= len(rest) || isForbiddenFileRefValue(rest[j+1].val) {
						return false
					}
					j++
					continue
				}
				return false
			}
			pos = append(pos, t)
		}
		if outputDirCount != 1 || checkpointDirCount != 1 || len(pos) != 2 {
			return false
		}
		src, dst := pos[0], pos[1]
		if !strings.HasPrefix(src.val, "oss://") || strings.HasPrefix(dst.val, "oss://") {
			return false
		}
		if recursive {
			return isSafeNewScratchToken(dst, roots)
		}
		return isSafeScratchToken(dst, false, false, roots)
	default:
		return false
	}
}

var tccliAllowedServices = map[string]bool{
	"cls": true, "cvm": true, "lighthouse": true, "vpc": true, "clb": true,
	"cdb": true, "cynosdb": true, "redis": true, "mongodb": true, "postgres": true,
	"mariadb": true, "es": true, "ckafka": true, "tdmq": true, "monitor": true,
	"apm": true, "rum": true, "tke": true, "scf": true, "cdn": true,
	"teo": true, "waf": true, "dnspod": true, "apigateway": true, "billing": true, "tag": true,
}

var tccliSensitiveActionWords = []string{
	"secret", "token", "password", "credential", "key",
	"auth", "login", "assume", "sign", "url", "policy", "role",
}

var tccliForbiddenFlagPrefixes = []string{
	"endpoint", "proxy", "http-proxy", "https-proxy",
	"secret", "token", "role",
	"cli-input-json", "cli-unfold-argument", "generate-cli-skeleton",
	"waiter", "sso",
}

func isForbiddenTCCLIFlag(flagName string) bool {
	low := strings.ToLower(strings.TrimPrefix(flagName, "--"))
	for _, p := range tccliForbiddenFlagPrefixes {
		if strings.HasPrefix(low, p) {
			return true
		}
	}
	return false
}

func isSafeReadOnlyTCCLI(args []string) bool {
	i := 0
	for i < len(args) {
		a := args[i]
		if !strings.HasPrefix(a, "-") {
			break
		}
		name, val, hasEq := strings.Cut(a, "=")
		if hasEq {
			switch name {
			case "--profile", "--region", "--output", "--version", "--timeout":
				if isForbiddenFileRefValue(val) {
					return false
				}
				i++
			default:
				return false
			}
			continue
		}
		switch a {
		case "--profile", "--region", "--output", "--version", "--timeout":
			if i+1 >= len(args) || isForbiddenFileRefValue(args[i+1]) {
				return false
			}
			i += 2
		default:
			return false
		}
	}
	if i+1 >= len(args) {
		return false
	}
	service := strings.ToLower(args[i])
	action := args[i+1]
	if !tccliAllowedServices[service] || strings.HasPrefix(action, "-") {
		return false
	}
	if !hasPascalActionPrefix(action, "Describe", "Search", "Get", "List") || hasSensitiveActionWord(action, tccliSensitiveActionWords) {
		return false
	}
	expectVal := false
	for _, a := range args[i+2:] {
		if strings.HasPrefix(a, "-") {
			if !strings.HasPrefix(a, "--") || len(a) <= 2 {
				return false
			}
			name, val, hasEq := strings.Cut(a, "=")
			if isForbiddenTCCLIFlag(name) {
				return false
			}
			if hasEq {
				if isForbiddenFileRefValue(val) {
					return false
				}
				expectVal = false
			} else {
				expectVal = true
			}
			continue
		}
		if !expectVal || isForbiddenFileRefValue(a) {
			return false
		}
		expectVal = false
	}
	return true
}

var curlAllowedWriteOutVars = []string{
	"%{http_code}",
	"%{response_code}",
	"%{content_type}",
	"%{size_download}",
	"%{time_total}",
}

func isSafeCurlWriteOut(s string) bool {
	if s == "" || strings.Contains(s, "@") {
		return false
	}
	for i := 0; i < len(s); {
		ch := s[i]
		if ch == '%' {
			matched := false
			for _, v := range curlAllowedWriteOutVars {
				if strings.HasPrefix(s[i:], v) {
					i += len(v)
					matched = true
					break
				}
			}
			if !matched {
				return false
			}
			continue
		}
		if ch == '\\' {
			if i+1 >= len(s) || (s[i+1] != 'n' && s[i+1] != 't') {
				return false
			}
			i += 2
			continue
		}
		if ch == '\t' || (ch >= 0x20 && ch <= 0x7e) {
			i++
			continue
		}
		return false
	}
	return true
}

func isExactSentryAuthHeader(hv string) bool {
	const prefix = "authorization: bearer "
	if len(hv) < len(prefix) || !strings.EqualFold(hv[:len(prefix)], prefix) {
		return false
	}
	rest := hv[len(prefix):]
	return rest == "$SENTRY_AUTH_TOKEN" || rest == "${SENTRY_AUTH_TOKEN}"
}

func isSafeCurlHeader(tok shellToken) bool {
	hv := tok.val
	if hv == "" || strings.HasPrefix(strings.TrimSpace(hv), "@") || strings.ContainsAny(hv, "\r\n") {
		return false
	}
	var name string
	if idx := strings.IndexByte(hv, ':'); idx >= 0 {
		name = strings.ToLower(strings.TrimSpace(hv[:idx]))
	} else {
		name = strings.ToLower(strings.TrimSpace(strings.TrimSuffix(hv, ";")))
	}
	switch name {
	case "host", "cookie", "x-http-method-override":
		return false
	}
	if strings.Contains(strings.ToLower(hv), "x-http-method-override") {
		return false
	}
	if tok.hasSentryTokenVar {
		return isExactSentryAuthHeader(hv)
	}
	return true
}

func isSafeReadOnlyCurl(argToks []shellToken, roots ScratchRoots) bool {
	if len(roots.ReadOnlyHosts) == 0 {
		return false
	}
	var urls []shellToken
	endFlags := false
	outputCount := 0
	sentryHeaderCount := 0
	for i := 0; i < len(argToks); i++ {
		t := argToks[i]
		a := t.val
		if !endFlags && a == "--" {
			endFlags = true
			continue
		}
		if !endFlags && strings.HasPrefix(a, "-") && a != "-" {
			switch a {
			case "--silent", "--show-error", "--get", "--compressed", "--fail", "--globoff":
				if t.hasSentryTokenVar {
					return false
				}
				continue
			case "-X", "--request":
				if t.hasSentryTokenVar || i+1 >= len(argToks) || argToks[i+1].hasSentryTokenVar || !strings.EqualFold(argToks[i+1].val, "GET") {
					return false
				}
				i++
				continue
			case "-H", "--header":
				if t.hasSentryTokenVar || i+1 >= len(argToks) {
					return false
				}
				ht := argToks[i+1]
				if !isSafeCurlHeader(ht) {
					return false
				}
				if ht.hasSentryTokenVar {
					sentryHeaderCount++
					if sentryHeaderCount > 1 {
						return false
					}
				}
				i++
				continue
			case "-m", "--max-time":
				if t.hasSentryTokenVar || i+1 >= len(argToks) || argToks[i+1].hasSentryTokenVar || !isPositiveNumber(argToks[i+1].val) {
					return false
				}
				i++
				continue
			case "-w", "--write-out":
				if t.hasSentryTokenVar || i+1 >= len(argToks) || argToks[i+1].hasSentryTokenVar || !isSafeCurlWriteOut(argToks[i+1].val) {
					return false
				}
				i++
				continue
			case "-o", "--output":
				if t.hasSentryTokenVar || i+1 >= len(argToks) || argToks[i+1].hasSentryTokenVar {
					return false
				}
				outputCount++
				if outputCount > 1 || strings.Contains(argToks[i+1].val, "#") {
					return false
				}
				if !isSafeScratchToken(argToks[i+1], false, false, roots) {
					return false
				}
				i++
				continue
			default:
				if strings.HasPrefix(a, "--") || t.hasSentryTokenVar {
					return false
				}
				// 仅允许纯短布尔组合：s / S / G / f / g（移除 L 拒绝重定向）
				for _, ch := range a[1:] {
					switch ch {
					case 's', 'S', 'G', 'f', 'g':
					default:
						return false
					}
				}
				continue
			}
		}
		if t.hasSentryTokenVar {
			return false
		}
		urls = append(urls, t)
	}
	if len(urls) != 1 {
		return false
	}
	return isAllowedReadOnlyURL(urls[0].val, roots.ReadOnlyHosts)
}

func isAllowedReadOnlyURL(rawURL string, hosts []string) bool {
	if rawURL == "" || strings.ContainsAny(rawURL, "{}[]\\ \t\r\n") {
		return false
	}
	for i := 0; i < len(rawURL); i++ {
		if rawURL[i] < 0x20 || rawURL[i] == 0x7f {
			return false
		}
	}
	if !strings.HasPrefix(strings.ToLower(rawURL), "https://") {
		return false
	}
	u, err := url.Parse(rawURL)
	if err != nil || u.Scheme != "https" || u.Opaque != "" || u.User != nil {
		return false
	}
	// 从原始 URL 提取 authority 与 path 部分做额外核对，防止空 userinfo、歧义路径与百分号编码绕过
	afterScheme := rawURL[len("https://"):]
	slashIdx := strings.IndexByte(afterScheme, '/')
	if slashIdx <= 0 {
		return false
	}
	authority := afterScheme[:slashIdx]
	if strings.Contains(authority, "@") || strings.ContainsAny(authority, "?#") {
		return false
	}
	rawPath := afterScheme[slashIdx:]
	if qIdx := strings.IndexAny(rawPath, "?#"); qIdx >= 0 {
		rawPath = rawPath[:qIdx]
	}
	if !strings.HasPrefix(u.Path, "/api/0/") || !strings.HasPrefix(rawPath, "/api/0/") {
		return false
	}
	if hasDotDotSegment(u.Path) || hasDotDotSegment(rawPath) {
		return false
	}
	lowerRawPath := strings.ToLower(rawPath)
	lowerEscaped := strings.ToLower(u.EscapedPath())
	for _, bad := range []string{"%2e", "%2f", "%5c", `\`} {
		if strings.Contains(lowerRawPath, bad) || strings.Contains(lowerEscaped, bad) {
			return false
		}
	}
	if strings.Contains(u.Host, ":") && u.Port() == "" {
		return false
	}
	return isAllowedReadOnlyHostPort(u.Hostname(), u.Port(), hosts)
}

func autoWritableRoots(roots ScratchRoots) []string {
	var raw []string
	raw = append(raw, "/tmp", "/private/tmp")
	if tmp := strings.TrimSpace(roots.TmpDir); tmp != "" && filepath.IsAbs(tmp) {
		raw = append(raw, tmp)
	}
	notes := strings.TrimSpace(roots.NotesDir)
	if notes != "" && filepath.IsAbs(notes) {
		raw = append(raw, notes)
		if strings.TrimSpace(roots.DiscussionsDir) == "" {
			raw = append(raw, filepath.Join(notes, "discussions"))
		}
	}
	if disc := strings.TrimSpace(roots.DiscussionsDir); disc != "" && filepath.IsAbs(disc) {
		raw = append(raw, disc)
	}
	seen := map[string]bool{}
	var out []string
	add := func(p string) {
		c := filepath.Clean(p)
		if c == "" || seen[c] {
			return
		}
		seen[c] = true
		out = append(out, c)
	}
	for _, r := range raw {
		add(r)
		if resolved, ok := evalWithMissingSuffix(filepath.Clean(r)); ok {
			add(resolved)
		}
	}
	return out
}

func pathsOverlap(a, b string) bool {
	a = filepath.Clean(a)
	b = filepath.Clean(b)
	if a == "/" || b == "/" {
		return true
	}
	return pathWithin(a, b, true) || pathWithin(b, a, true)
}

func overlapsWritableRoots(p string, writableRoots []string) bool {
	for _, wr := range writableRoots {
		if pathsOverlap(p, wr) {
			return true
		}
	}
	return false
}

func isTrustedScriptCall(tokens []shellToken, roots ScratchRoots) bool {
	if len(tokens) == 0 || len(roots.TrustedScripts) == 0 {
		return false
	}
	var scriptPath string
	if filepath.IsAbs(tokens[0].val) && !strings.Contains(tokens[0].val, "=") {
		scriptPath = tokens[0].val
	} else {
		cmd, ok := resolveTrustedCommand(tokens[0])
		if !ok || (cmd != "bash" && cmd != "sh") {
			return false
		}
		if len(tokens) < 2 || strings.HasPrefix(tokens[1].val, "-") {
			return false
		}
		scriptPath = tokens[1].val
	}
	return isPathTrustedScript(scriptPath, roots)
}

func isPathTrustedScript(targetPath string, roots ScratchRoots) bool {
	if targetPath == "" || strings.TrimSpace(targetPath) != targetPath ||
		!filepath.IsAbs(targetPath) || hasDotDotSegment(targetPath) || strings.ContainsAny(targetPath, "$~") {
		return false
	}
	resolvedTarget, err := filepath.EvalSymlinks(targetPath)
	if err != nil {
		return false
	}
	resolvedTarget = filepath.Clean(resolvedTarget)
	fi, err := os.Stat(resolvedTarget)
	if err != nil || !fi.Mode().IsRegular() {
		return false
	}
	writableRoots := autoWritableRoots(roots)
	if overlapsWritableRoots(resolvedTarget, writableRoots) {
		return false
	}
	for _, entry := range roots.TrustedScripts {
		entry = strings.TrimSpace(entry)
		if entry == "" || !filepath.IsAbs(entry) || hasDotDotSegment(entry) {
			continue
		}
		if strings.HasSuffix(entry, "/") {
			dirPath := filepath.Clean(entry)
			resolvedDir, err := filepath.EvalSymlinks(dirPath)
			if err != nil {
				continue
			}
			resolvedDir = filepath.Clean(resolvedDir)
			if overlapsWritableRoots(resolvedDir, writableRoots) {
				continue
			}
			dfi, err := os.Stat(resolvedDir)
			if err != nil || !dfi.IsDir() {
				continue
			}
			if pathWithin(resolvedTarget, resolvedDir, false) {
				return true
			}
		} else {
			resolvedEntry, err := filepath.EvalSymlinks(filepath.Clean(entry))
			if err != nil {
				continue
			}
			resolvedEntry = filepath.Clean(resolvedEntry)
			if overlapsWritableRoots(resolvedEntry, writableRoots) {
				continue
			}
			if resolvedTarget == resolvedEntry {
				return true
			}
		}
	}
	return false
}
