package agent

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"syscall"
)

// ScratchRoots 定义自动放行允许写入的临时目录与讨论目录根（测试可注入临时目录）。
type ScratchRoots struct {
	TmpDir         string // 默认 "/tmp"
	NotesDir       string // 默认 os.Getenv("POSSE_NOTES")
	DiscussionsDir string // 可选直接覆盖；为空时取 filepath.Join(NotesDir, "discussions")
}

// DefaultScratchRoots 返回当前进程环境下的默认临时目录与讨论目录根。
func DefaultScratchRoots() ScratchRoots {
	return ScratchRoots{
		TmpDir:   "/tmp",
		NotesDir: strings.TrimSpace(os.Getenv("POSSE_NOTES")),
	}
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
	val          string
	quoted       bool
	singleQuoted bool
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
				if !isSafePathInRoots(target, true, true, true, true, roots) {
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
		if !isSafeRedir(r, roots) {
			return false
		}
	}
	return isSafeReadOnlyTokens(toks, roots)
}

// IsSafeDiscussionWritePath 判断 write_file 等工具写入路径是否严格位于讨论笔记目录内（含软链接、硬链接与普通文件校验）。
func IsSafeDiscussionWritePath(target string, roots ScratchRoots) bool {
	return isSafePathInRoots(target, false, false, false, false, roots)
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

// matchAllowedDollar 检查 s[i:] 是否以允许的精确变量展开开头（$POSSE_NOTES、${POSSE_NOTES}、$HOME、${HOME}）。
func matchAllowedDollar(s string, i int) (int, bool) {
	for _, braced := range []string{"${POSSE_NOTES}", "${HOME}"} {
		if strings.HasPrefix(s[i:], braced) {
			return len(braced), true
		}
	}
	for _, bare := range []string{"$POSSE_NOTES", "$HOME"} {
		if strings.HasPrefix(s[i:], bare) {
			end := i + len(bare)
			if end >= len(s) || !isIdentByte(s[end]) {
				return len(bare), true
			}
		}
	}
	return 0, false
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
					n, ok := matchAllowedDollar(s, i)
					if !ok {
						return shellToken{}, i, false, false
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
				n, ok := matchAllowedDollar(s, i)
				if !ok {
					return shellToken{}, i, false, false
				}
				cur.WriteString(s[i : i+n])
				hasToken = true
				i += n
				continue
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
		val:          cur.String(),
		quoted:       quoted,
		singleQuoted: singleQuoted,
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
	return isSafePathInRoots(tok.val, !tok.singleQuoted, allowRoot, true, allowExistingDir, roots)
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

func isSafePathInRoots(p string, allowNotesEnv, allowRoot, allowTmp, allowExistingDir bool, roots ScratchRoots) bool {
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
		// 非配置根本身的目标若已存在：必须非软链接，文件写操作要求普通文件且 nlink==1（目录写操作允许真实目录）
		if clean != root && clean != resolvedRoot {
			if fi, err := os.Lstat(clean); err == nil {
				if !isSafeExistingTarget(fi, allowExistingDir) {
					return false
				}
			} else if !os.IsNotExist(err) {
				return false
			}
		}
		resolvedTarget, ok := evalWithMissingSuffix(clean)
		if !ok {
			return false
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

func isSafeReadOnlyTokens(tokens []shellToken, roots ScratchRoots) bool {
	if len(tokens) == 0 {
		return false
	}
	cmd, ok := resolveTrustedCommand(tokens[0])
	if !ok {
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
		"basename", "dirname", "realpath", "readlink", "echo", "printf", "nl", "date", "uname", "printenv", "cd",
		"grep", "egrep", "fgrep", "jq", "uniq", "cut", "tr", "diff", "comm", "true", "false",
		"du", "df", "od", "strings", "shasum", "md5", "column":
		return true
	case "file":
		return isSafeReadOnlyFile(args)
	case "rg":
		return isSafeReadOnlyRG(args)
	case "env":
		return len(args) == 0
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
	for i := 0; i < len(args); i++ {
		a := args[i]
		if a == "--" {
			break
		}
		if a == "-C" || a == "--compile" || strings.HasPrefix(a, "--compile=") ||
			a == "-m" || strings.HasPrefix(a, "-m") ||
			a == "--magic-file" || strings.HasPrefix(a, "--magic-file=") {
			return false
		}
		switch a {
		case "-e", "-f", "-F", "-P", "--exclude", "--exclude-quiet", "--files-from", "--separator", "--parameter":
			i++
			continue
		}
		if strings.HasPrefix(a, "-") && !strings.HasPrefix(a, "--") && len(a) > 1 {
			first := a[1]
			if first == 'e' || first == 'f' || first == 'F' || first == 'P' {
				continue
			}
			if strings.ContainsAny(a[1:], "Cm") {
				return false
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
			continue
		}
		if len(pos) < 2 {
			pos = append(pos, a)
		}
	}

	// --help/-h/--dry-run 出现一次以上、带 = 赋值或与可能消费值的前一 flag 相邻时，整条命令一律走审批
	if helpInvalid || helpCount > 1 || dryRunInvalid || dryRunCount > 1 {
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
