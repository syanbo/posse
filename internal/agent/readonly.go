package agent

import (
	"os"
	"path/filepath"
	"strings"
)

// NormalizeShellLine 把 shell 的反斜杠续行（\\\n、\\\r\n，单引号内除外）规整为空格，
// 并保留转义反斜杠（\\）后的真实换行，避免借续行拼接隐藏额外命令。
func NormalizeShellLine(cmdLine string) string {
	var b strings.Builder
	b.Grow(len(cmdLine))
	var quote byte
	for i := 0; i < len(cmdLine); i++ {
		ch := cmdLine[i]
		if quote == '\'' {
			b.WriteByte(ch)
			if ch == '\'' {
				quote = 0
			}
			continue
		}
		if ch == '\\' {
			if i+2 < len(cmdLine) && cmdLine[i+1] == '\r' && cmdLine[i+2] == '\n' {
				b.WriteByte(' ')
				i += 2
				continue
			}
			if i+1 < len(cmdLine) && cmdLine[i+1] == '\n' {
				b.WriteByte(' ')
				i++
				continue
			}
			if i+1 < len(cmdLine) {
				b.WriteByte(ch)
				i++
				b.WriteByte(cmdLine[i])
				continue
			}
		}
		if quote == 0 && (ch == '\'' || ch == '"') {
			quote = ch
		} else if quote == '"' && ch == '"' {
			quote = 0
		}
		b.WriteByte(ch)
	}
	return strings.TrimSpace(b.String())
}

// HasUnsafeShellSyntax 检查规整后的命令行是否仍包含裸换行、NUL、反引号、命令替换或进程替换。
func HasUnsafeShellSyntax(normalized string) bool {
	if normalized == "" {
		return true
	}
	return strings.ContainsAny(normalized, "\n\r\x00`") ||
		strings.Contains(normalized, "$(") ||
		strings.Contains(normalized, "<(") ||
		strings.Contains(normalized, ">(")
}

// IsSafeReadOnlyCommandLine 判断 shell 命令行是否全部由允许的只读排查命令、Posse 协作/查询命令
// 以及指向 /tmp 或 $POSSE_NOTES/discussions 的安全临时写入构成。
func IsSafeReadOnlyCommandLine(cmdLine string) bool {
	normalized := NormalizeShellLine(cmdLine)
	if HasUnsafeShellSyntax(normalized) {
		return false
	}
	segs, ok := SplitShellSegments(normalized)
	if !ok || len(segs) == 0 {
		return false
	}
	for _, seg := range segs {
		if !IsSafeReadOnlySegment(seg) {
			return false
		}
	}
	return true
}

// SplitShellSegments 在引号外按 |、|&、;、&&、|| 切分命令段；
// 保留 2>&1 / &>/... 等重定向中的 &，遇到后台执行 & 或未闭合引号返回 false。
func SplitShellSegments(s string) ([]string, bool) {
	var segs []string
	var cur strings.Builder
	var quote byte
	for i := 0; i < len(s); i++ {
		ch := s[i]
		if quote != 0 {
			if ch == '\\' && quote == '"' && i+1 < len(s) {
				cur.WriteByte(ch)
				i++
				cur.WriteByte(s[i])
				continue
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

// SegmentArgs 解析单个命令段中的非重定向参数列表，供规则键提取复用。
func SegmentArgs(seg string) ([]string, bool) {
	toks, _, ok := parseShellSegment(seg)
	if !ok || len(toks) == 0 {
		return nil, false
	}
	out := make([]string, len(toks))
	for i, t := range toks {
		out[i] = t.val
	}
	return out, true
}

// IsSafeReadOnlySegment 判断单个命令段（含其重定向）是否安全只读。
func IsSafeReadOnlySegment(seg string) bool {
	toks, redirs, ok := parseShellSegment(seg)
	if !ok || len(toks) == 0 {
		return false
	}
	for _, r := range redirs {
		if !isSafeRedir(r) {
			return false
		}
	}
	return isSafeReadOnlyTokens(toks)
}

func parseShellSegment(s string) ([]shellToken, []shellRedir, bool) {
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
				tok, next, hasTok, ok := readShellToken(s, i)
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
					tok, next, hasTok, ok := readShellToken(s, i)
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
				tok, next, hasTok, ok := readShellToken(s, i)
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
				tok, next, hasTok, ok := readShellToken(s, i)
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
		tok, next, hasTok, ok := readShellToken(s, i)
		if !ok || !hasTok {
			return nil, nil, false
		}
		i = next
		args = append(args, tok)
	}
	return args, redirs, true
}

func readShellToken(s string, i int) (shellToken, int, bool, bool) {
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
			if ch == '\\' && quote == '"' && i+1 < len(s) {
				i++
				cur.WriteByte(s[i])
				i++
				continue
			}
			if ch == quote {
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
		if ch == '\\' && i+1 < len(s) {
			i++
			cur.WriteByte(s[i])
			hasToken = true
			i++
			continue
		}
		cur.WriteByte(ch)
		hasToken = true
		i++
	}
	if quote != 0 {
		return shellToken{}, i, false, false
	}
	return shellToken{val: cur.String(), quoted: quoted, singleQuoted: singleQuoted}, i, hasToken, true
}

func isSafeRedir(r shellRedir) bool {
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
		return isSafeScratchPath(r.target.val, !r.target.singleQuoted, false)
	default:
		return false
	}
}

func isSafeScratchPath(p string, allowNotesEnv, allowRoot bool) bool {
	p = strings.TrimSpace(p)
	if p == "" {
		return false
	}
	notes := strings.TrimSpace(os.Getenv("POSSE_NOTES"))
	if allowNotesEnv && notes != "" && filepath.IsAbs(notes) {
		switch {
		case strings.HasPrefix(p, "$POSSE_NOTES/"):
			p = filepath.Clean(notes) + p[len("$POSSE_NOTES"):]
		case strings.HasPrefix(p, "${POSSE_NOTES}/"):
			p = filepath.Clean(notes) + p[len("${POSSE_NOTES}"):]
		}
	}
	// 禁止路径中残留环境变量展开或波浪号（如 /tmp/$VAR 可能借 .. 逃逸）
	if strings.ContainsAny(p, "$~") || !filepath.IsAbs(p) {
		return false
	}
	clean := filepath.Clean(p)
	if allowRoot && clean == "/tmp" {
		return true
	}
	if strings.HasPrefix(clean, "/tmp/") {
		return true
	}
	if notes != "" && filepath.IsAbs(notes) {
		discDir := filepath.Join(filepath.Clean(notes), "discussions")
		if allowRoot && clean == discDir {
			return true
		}
		if strings.HasPrefix(clean, discDir+string(filepath.Separator)) {
			return true
		}
	}
	return false
}

func isSafeReadOnlyTokens(tokens []shellToken) bool {
	if len(tokens) == 0 {
		return false
	}
	cmd := filepath.Base(tokens[0].val)
	argToks := tokens[1:]
	args := make([]string, len(argToks))
	for i, t := range argToks {
		args[i] = t.val
	}
	switch cmd {
	case "posse":
		return isSafeReadOnlyPosse(args)
	case "ls", "pwd", "cat", "head", "tail", "wc", "stat", "file", "which",
		"basename", "dirname", "realpath", "readlink", "echo", "printf", "nl", "date", "uname", "printenv", "cd",
		"grep", "egrep", "fgrep", "rg", "jq", "uniq", "cut", "tr", "diff", "comm", "true", "false",
		"du", "df", "od", "strings", "shasum", "md5", "column":
		return true
	case "env":
		return len(args) == 0
	case "sort":
		for _, a := range args {
			if a == "-o" || strings.HasPrefix(a, "-o") || a == "--output" || strings.HasPrefix(a, "--output=") {
				return false
			}
		}
		return true
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
	case "sqlite3":
		return isSafeReadOnlySQLite(args)
	case "tee":
		return isSafeScratchTee(argToks)
	case "mkdir":
		return isSafeScratchMkdir(argToks)
	case "lark-cli":
		return isSafeReadOnlyLarkCLI(argToks)
	case "find":
		for _, a := range args {
			switch a {
			case "-exec", "-execdir", "-ok", "-okdir", "-delete", "-fprint", "-fprintf", "-fls":
				return false
			}
		}
		return true
	case "sed":
		hasN := false
		for _, a := range args {
			if a == "-i" || strings.HasPrefix(a, "-i") || a == "--in-place" || strings.HasPrefix(a, "--in-place=") {
				return false
			}
			if a == "-n" {
				hasN = true
			}
		}
		return hasN
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

func isSafeReadOnlyXXD(args []string) bool {
	// xxd 传 2 个位置参数时会把结果写进第二个文件；仅允许 0 或 1 个位置参数且禁止 -r 反向写。
	pos := 0
	endFlags := false
	for i := 0; i < len(args); i++ {
		a := args[i]
		if !endFlags && a == "--" {
			endFlags = true
			continue
		}
		if !endFlags && strings.HasPrefix(a, "-") && a != "-" {
			if a == "-r" || a == "-revert" || a == "--revert" {
				return false
			}
			switch a {
			case "-c", "-g", "-l", "-o", "-s", "-n", "-R", "-cols", "-groupsize", "-len", "-offset", "-seek", "-name":
				i++
			}
			continue
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
			case "-s":
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
	hasList := false
	endFlags := false
	for _, a := range args {
		if !endFlags && a == "--" {
			endFlags = true
			continue
		}
		if !endFlags && strings.HasPrefix(a, "-") && len(a) > 1 {
			if strings.HasPrefix(a, "--") {
				return false
			}
			for _, ch := range a[1:] {
				switch ch {
				case 'l':
					hasList = true
				case 'v', 'q':
				default:
					return false
				}
			}
			continue
		}
	}
	return hasList
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
				switch {
				case a == "--list":
					hasList = true
				case a == "--verbose", a == "--gzip", a == "--bzip2", a == "--xz":
				case a == "--file":
					i++
				case strings.HasPrefix(a, "--file="):
				default:
					return false
				}
				continue
			}
			hasF := false
			for j := 1; j < len(a); j++ {
				switch a[j] {
				case 't':
					hasList = true
				case 'v', 'z', 'j', 'J':
				case 'f':
					hasF = true
					// -f 后面如果紧跟文件名（如 -fa.tar），剩余字符视作文件名
					if j+1 < len(a) {
						hasF = false
						j = len(a)
					}
				default:
					return false
				}
			}
			if hasF {
				i++
			}
			continue
		}
	}
	return hasList
}

func isSafeReadOnlySQLite(args []string) bool {
	hasReadonly := false
	var pos []string
	endFlags := false
	for i := 0; i < len(args); i++ {
		a := args[i]
		if !endFlags && a == "--" {
			endFlags = true
			continue
		}
		if !endFlags && strings.HasPrefix(a, "-") && a != "-" {
			switch a {
			case "-readonly", "--readonly":
				hasReadonly = true
			case "-header", "-noheader", "-column", "-csv", "-json", "-line", "-list",
				"-box", "-table", "-markdown", "-tabs", "-quote", "-html", "-ascii",
				"-batch", "-bail":
			case "-separator", "-newline", "-nullvalue":
				i++
			default:
				return false
			}
			continue
		}
		pos = append(pos, a)
	}
	if !hasReadonly || len(pos) < 2 {
		return false
	}
	for _, sql := range pos[1:] {
		if !isSafeSQLiteReadQuery(sql) {
			return false
		}
	}
	return true
}

func isSafeSQLiteReadQuery(sql string) bool {
	sql = strings.TrimSpace(sql)
	if sql == "" || strings.ContainsAny(sql, "\n\r") {
		return false
	}
	lower := strings.ToLower(sql)
	// 明确禁止点命令写文件/执行 shell 以及写盘/加载扩展函数
	for _, bad := range []string{
		".output", ".once", ".shell", ".system", ".save", ".backup", ".restore",
		".import", ".load", ".read", ".open", ".clone", ".excel", ".log", ".trace",
		"load_extension", "writefile", "attach ", "attach\t",
	} {
		if strings.Contains(lower, bad) {
			return false
		}
	}
	if strings.HasPrefix(lower, ".") {
		return lower == ".schema" || strings.HasPrefix(lower, ".schema ") ||
			lower == ".tables" || strings.HasPrefix(lower, ".tables ")
	}
	stmts := strings.Split(lower, ";")
	hasStmt := false
	for _, st := range stmts {
		st = strings.TrimSpace(st)
		if st == "" {
			continue
		}
		hasStmt = true
		ok := strings.HasPrefix(st, "select ") || strings.HasPrefix(st, "select\t") ||
			strings.HasPrefix(st, "select(") || strings.HasPrefix(st, "select*") ||
			strings.HasPrefix(st, "with ") || strings.HasPrefix(st, "with\t") ||
			strings.HasPrefix(st, "pragma ") || strings.HasPrefix(st, "pragma\t") ||
			strings.HasPrefix(st, "explain ") || strings.HasPrefix(st, "explain\t")
		if !ok {
			return false
		}
	}
	return hasStmt
}

func isSafeScratchTee(args []shellToken) bool {
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
		if !isSafeScratchPath(a, !t.singleQuoted, false) {
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
		case a == "-C" || a == "--git-dir" || a == "--work-tree" || a == "-c":
			i += 2
		case strings.HasPrefix(a, "-C") || strings.HasPrefix(a, "--git-dir=") || strings.HasPrefix(a, "--work-tree=") ||
			a == "--no-pager" || a == "-P" || a == "--bare":
			i++
		case strings.HasPrefix(a, "-"):
			return false
		default:
			sub := a
			rest := args[i+1:]
			switch sub {
			case "status", "diff", "log", "show", "rev-parse", "ls-files", "ls-tree", "blame", "shortlog", "describe":
				return true
			case "branch":
				for _, r := range rest {
					switch r {
					case "-d", "-D", "-m", "-M", "-c", "-C", "-f", "--delete", "--move", "--copy", "--force":
						return false
					}
				}
				return true
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

func isSafeScratchMkdir(args []shellToken) bool {
	hasPath := false
	for _, t := range args {
		a := t.val
		if a == "-p" {
			continue
		}
		if strings.HasPrefix(a, "-") {
			return false
		}
		if !isSafeScratchPath(a, !t.singleQuoted, true) {
			return false
		}
		hasPath = true
	}
	return hasPath
}

var larkReadVerbs = map[string]bool{
	"list": true, "get": true, "mget": true, "search": true, "resolve": true,
	"fetch": true, "meta": true, "export": true, "query": true, "info": true,
	"status": true, "read": true, "show": true, "describe": true, "stat": true,
	"stats": true, "count": true, "lookup": true, "detail": true, "details": true,
	"find": true, "inspect": true, "diff": true, "preview": true, "download": true,
}

var larkWriteVerbs = map[string]bool{
	"create": true, "update": true, "delete": true, "del": true, "rm": true,
	"remove": true, "send": true, "upload": true, "patch": true, "set": true,
	"unset": true, "add": true, "move": true, "mv": true, "copy": true,
	"cp": true, "reply": true, "forward": true, "import": true, "recall": true,
	"edit": true, "write": true, "clear": true, "clean": true, "grant": true,
	"revoke": true, "transfer": true, "put": true, "post": true, "append": true,
	"insert": true, "replace": true, "rename": true, "archive": true, "unarchive": true,
	"restore": true, "close": true, "reopen": true, "complete": true, "uncomplete": true,
	"cancel": true, "approve": true, "reject": true, "submit": true, "start": true,
	"stop": true, "enable": true, "disable": true, "bind": true, "unbind": true,
	"pin": true, "unpin": true, "join": true, "leave": true, "kick": true,
	"invite": true, "subscribe": true, "unsubscribe": true, "register": true, "unregister": true,
	"sync": true, "merge": true, "reset": true, "rollback": true, "mark": true,
	"unmark": true, "react": true, "unreact": true, "urgent": true, "publish": true,
	"unpublish": true, "deploy": true, "trigger": true, "run": true, "exec": true,
	"modify": true, "change": true, "drop": true, "truncate": true, "save": true,
	"share": true, "unshare": true, "lock": true, "unlock": true, "clone": true,
	"fork": true, "book": true, "reserve": true, "checkin": true, "checkout": true,
	"punch": true, "login": true, "logout": true, "init": true, "config": true,
}

func isLarkContentValueFlag(flag string) bool {
	switch flag {
	case "--text", "--body", "--content", "--data", "--params", "--fields",
		"--title", "--keyword", "--filter-json", "--url", "--jq", "--message",
		"--msg", "--json", "--profile", "--as", "--format", "--output", "--output-dir", "-o":
		return true
	}
	return false
}

// isSafeReadOnlyLarkCLI 放行 lark-cli 的帮助、schema、--dry-run、显式白名单与按动词判定的只读子命令；
// 含 download 的命令必须显式通过 --output-dir/--output/-o 写入 /tmp 或 $POSSE_NOTES/discussions。
func isSafeReadOnlyLarkCLI(args []shellToken) bool {
	var pos []string
	var outputs []shellToken
	hasHelp := false
	hasDryRun := false
	hasInvalidOutput := false
	hasDownloadFlag := false

	for i := 0; i < len(args); i++ {
		a := args[i].val
		if a == "--" {
			break
		}
		prev := ""
		if i > 0 {
			prev = args[i-1].val
		}
		if (a == "--help" || a == "-h") && !isLarkContentValueFlag(prev) {
			hasHelp = true
			continue
		}
		if (a == "--dry-run" || a == "--dry-run=true" || a == "--dry-run=1") && !isLarkContentValueFlag(prev) {
			hasDryRun = true
			continue
		}
		if a == "--download-resources" || a == "--download" {
			hasDownloadFlag = true
			continue
		}
		switch {
		case a == "--output-dir" || a == "--output" || a == "-o":
			if i+1 >= len(args) {
				hasInvalidOutput = true
				continue
			}
			i++
			outputs = append(outputs, args[i])
			continue
		case strings.HasPrefix(a, "--output-dir="):
			outputs = append(outputs, shellToken{val: strings.TrimPrefix(a, "--output-dir="), singleQuoted: args[i].singleQuoted})
			continue
		case strings.HasPrefix(a, "--output="):
			outputs = append(outputs, shellToken{val: strings.TrimPrefix(a, "--output="), singleQuoted: args[i].singleQuoted})
			continue
		case strings.HasPrefix(a, "-o="):
			outputs = append(outputs, shellToken{val: strings.TrimPrefix(a, "-o="), singleQuoted: args[i].singleQuoted})
			continue
		case strings.HasPrefix(a, "-o") && len(a) > 2:
			outputs = append(outputs, shellToken{val: a[2:], singleQuoted: args[i].singleQuoted})
			continue
		case a == "--profile" || a == "--as" || a == "--format" || a == "--jq":
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

	if hasHelp {
		return true
	}
	if hasInvalidOutput {
		return false
	}
	for _, out := range outputs {
		if !isSafeScratchPath(out.val, !out.singleQuoted, true) {
			return false
		}
	}
	if hasDryRun {
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
	isDownload := strings.Contains(strings.ToLower(sub), "download") || hasDownloadFlag
	if isDownload && len(outputs) == 0 {
		return false
	}
	if isExplicitSafeLarkCLI(domain, sub) {
		return true
	}
	return isSafeReadOnlyLarkVerbSubcommand(sub)
}

func isExplicitSafeLarkCLI(domain, sub string) bool {
	switch domain {
	case "auth":
		return sub == "status"
	case "profile":
		return sub == "list"
	case "base":
		switch sub {
		case "+record-list", "+record-get", "+record-search", "+table-list", "+field-list", "+view-list", "+base-get", "+app-get":
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

func isSafeReadOnlyLarkVerbSubcommand(sub string) bool {
	if !strings.HasPrefix(sub, "+") || len(sub) < 2 {
		return false
	}
	parts := strings.FieldsFunc(strings.ToLower(sub[1:]), func(r rune) bool {
		return r == '-' || r == '_'
	})
	hasRead := false
	for _, p := range parts {
		if larkWriteVerbs[p] {
			return false
		}
		if larkReadVerbs[p] {
			hasRead = true
		}
	}
	return hasRead
}
