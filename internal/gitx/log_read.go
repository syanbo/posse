package gitx

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"syscall"
	"time"
	"unicode/utf8"
)

const (
	LogReadPageLimit = 1000
	LogReadSkipLimit = 100000
)

var (
	ErrLogQuery     = errors.New("invalid Git log query")
	ErrLogOutput    = errors.New("invalid Git history output")
	ErrLogChanged   = errors.New("Git history references changed; refresh the log")
	ErrLogAmbiguous = errors.New("Git commit hash is ambiguous")
	ErrLogNotCommit = errors.New("Git history query does not refer to a commit")
)

func ValidLogBaseline(s string) bool { return len(s) == 64 && ValidBlobOID(s) }
func ValidLogQuery(f LogFilter, skip, limit int, baseline string) bool {
	if skip < 0 || skip > LogReadSkipLimit || limit < 1 || limit > LogReadPageLimit || len(f.Branches) > 64 || skip > 0 && baseline == "" || baseline != "" && !ValidLogBaseline(baseline) {
		return false
	}
	for _, v := range []string{f.Author, f.Text} {
		if len(v) > 2048 || strings.ContainsRune(v, 0) || !utf8.ValidString(v) {
			return false
		}
	}
	for _, ref := range f.Branches {
		if !ValidReadRevision(ref) {
			return false
		}
	}
	return f.Path == "" || f.Path == "." || ValidFileQuery(FileIndex, "", f.Path)
}

func historyRead(ctx context.Context, dir string, stdin []byte, args ...string) ([]byte, error) {
	ctx = context.WithValue(ctx, readStrictKey{}, true)
	args = append([]string{"-c", "i18n.logOutputEncoding=UTF-8"}, args...)
	return runRead(ctx, dir, stdin, []string{"GIT_NO_REPLACE_OBJECTS=1"}, args...)
}

// 引用摘要不持有会话缓存；分页和查询末尾都复查，变化时由窗口刷新整份列表。
func historyRefs(ctx context.Context, dir string) ([]byte, bool, error) {
	out, err := historyRead(ctx, dir, nil, "for-each-ref", "--format=%(refname)%00%(objectname)%00%(objecttype)")
	if err != nil {
		return nil, false, err
	}
	for _, line := range bytes.Split(bytes.TrimSuffix(out, []byte("\n")), []byte("\n")) {
		if len(line) == 0 {
			continue
		}
		f := bytes.Split(line, []byte{0})
		if len(f) != 3 || !ValidReadRevision(string(f[0])) || !ValidBlobOID(string(f[1])) {
			return nil, false, ErrLogOutput
		}
	}
	exists, err := HeadExistsChecked(context.WithValue(ctx, readStrictKey{}, true), dir)
	if err != nil {
		return nil, false, err
	}
	head := []byte{}
	if exists {
		head, err = historyRead(ctx, dir, nil, "rev-parse", "--verify", "HEAD^{commit}")
		if err != nil {
			return nil, false, err
		}
		if !ValidBlobOID(strings.TrimSuffix(string(head), "\n")) {
			return nil, false, ErrLogOutput
		}
	}
	sym, err := historyRead(ctx, dir, nil, "symbolic-ref", "-q", "HEAD")
	if err != nil {
		var exit *exec.ExitError
		if !errors.As(err, &exit) || exit.ExitCode() != 1 {
			return nil, false, err
		}
		sym = []byte("HEAD\n")
	}
	out = append(out, 0)
	out = append(out, head...)
	out = append(out, 0)
	out = append(out, sym...)
	shallow, err := historyShallow(ctx, dir)
	if err != nil {
		return nil, false, err
	}
	out = append(out, 0)
	out = append(out, shallow...)
	return out, exists, nil
}

// deepen 可以只移动浅边界而不移动 refs，也必须使旧分页失效。
func historyShallow(ctx context.Context, dir string) ([]byte, error) {
	name, err := historyRead(ctx, dir, nil, "rev-parse", "--path-format=absolute", "--git-path", "shallow")
	if err != nil {
		return nil, err
	}
	f, err := os.OpenFile(strings.TrimSuffix(string(name), "\n"), os.O_RDONLY|syscall.O_NONBLOCK, 0)
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() {
		return nil, ErrLogOutput
	}
	budget, _ := ctx.Value(readLimitKey{}).(*readBudget)
	if budget == nil {
		return nil, ErrReadLimit
	}
	out := &readBuffer{budget: budget, cancel: func() {}}
	if _, err := io.Copy(out, f); err != nil {
		return nil, err
	}
	return out.Bytes(), ctx.Err()
}

func historyStarts(ctx context.Context, dir string, f LogFilter, head bool, hash string) ([]string, error) {
	if hash != "" {
		return []string{hash}, nil
	}
	selected := f.Branches
	if len(selected) == 0 && head {
		selected = []string{"HEAD"}
	}
	revs := []string{}
	for _, ref := range selected {
		if ref == "HEAD" && !head {
			continue
		}
		b, err := historyRead(ctx, dir, nil, "rev-parse", "--verify", "--end-of-options", ref+"^{commit}")
		if err != nil {
			return nil, err
		}
		oid := strings.TrimSuffix(string(b), "\n")
		if !ValidBlobOID(oid) {
			return nil, ErrLogOutput
		}
		revs = append(revs, oid)
	}
	return revs, nil
}

func historyHash(ctx context.Context, dir, text string) (string, error) {
	if len(text) < 7 || len(text) > 64 {
		return "", nil
	}
	for _, c := range text {
		if !strings.ContainsRune("0123456789abcdefABCDEF", c) {
			return "", nil
		}
	}
	out, err := historyRead(ctx, dir, nil, "rev-parse", "--disambiguate="+strings.ToLower(text))
	if err != nil {
		return "", err
	}
	candidates := strings.Fields(string(out))
	if len(candidates) == 0 {
		return "", nil
	}
	if len(candidates) > 1 {
		return "", ErrLogAmbiguous
	}
	oid := candidates[0]
	if !ValidBlobOID(oid) {
		return "", ErrLogOutput
	}
	kind, err := historyRead(ctx, dir, nil, "cat-file", "-t", oid)
	if err != nil {
		return "", err
	}
	if string(kind) != "commit\n" {
		return "", ErrLogNotCommit
	}
	return oid, nil
}

type LogPage struct {
	Commits  []CommitInfo
	Baseline string
	HasMore  bool
	NextSkip int
}

func ReadLogChecked(ctx context.Context, dir string, f LogFilter, skip, limit int, baseline string) (LogPage, error) {
	var page LogPage
	if !ValidLogQuery(f, skip, limit, baseline) {
		return page, ErrLogQuery
	}
	refs, head, err := historyRefs(ctx, dir)
	if err != nil {
		return page, err
	}
	text := strings.TrimSpace(f.Text)
	hash, err := historyHash(ctx, dir, text)
	if err != nil {
		return page, err
	}
	revs, err := historyStarts(ctx, dir, f, head, hash)
	if err != nil {
		return page, err
	}
	starts, _ := json.Marshal(revs)
	query, _ := json.Marshal(f)
	h := sha256.New()
	h.Write(refs)
	h.Write([]byte{0})
	h.Write(query)
	h.Write([]byte{0})
	h.Write([]byte(hash))
	h.Write([]byte{0})
	h.Write(starts)
	page.Baseline = hex.EncodeToString(h.Sum(nil))
	if baseline != "" && baseline != page.Baseline {
		return LogPage{}, ErrLogChanged
	}
	args := []string{"--literal-pathspecs", "log", "--no-ext-diff", "--no-textconv", "--no-show-signature", "--no-color", "--format=%H%x00%D", "-z", "--date-order", "-n", strconv.Itoa(limit + 1), "--skip", strconv.Itoa(skip)}
	if f.Author != "" {
		args = append(args, "--author="+f.Author, "--regexp-ignore-case", "--fixed-strings")
	}
	if hash == "" && text != "" {
		args = append(args, "--grep="+text, "--regexp-ignore-case", "--fixed-strings")
	}
	if hash != "" {
		args = append(args, "--no-walk")
	} else if len(f.Branches) == 0 {
		args = append(args, "--branches", "--remotes", "--tags")
	}
	page.Commits = []CommitInfo{}
	// 没有匹配的 HEAD 时不让 git log 隐式退回默认 HEAD；hash 命中也只有一页。
	if !(hash != "" && skip > 0) && !(hash == "" && len(f.Branches) > 0 && len(revs) == 0) {
		args = append(args, "--end-of-options")
		args = append(args, revs...)
		args = append(args, "--")
		if f.Path != "" {
			args = append(args, f.Path)
		}
		out, err := historyRead(ctx, dir, nil, args...)
		if err != nil {
			return LogPage{}, err
		}
		ids, decorations, err := parseHistoryIDs(out)
		if err != nil {
			return LogPage{}, err
		}
		if len(ids) > limit+1 {
			return LogPage{}, ErrLogOutput
		}
		page.HasMore = len(ids) > limit
		if page.HasMore {
			ids = ids[:limit]
		}
		page.Commits, err = readCommitObjects(ctx, dir, ids, decorations)
		if err != nil {
			return LogPage{}, err
		}
	}
	page.NextSkip = skip + len(page.Commits)
	after, afterHead, err := historyRefs(ctx, dir)
	if err != nil {
		return LogPage{}, err
	}
	afterHash, err := historyHash(ctx, dir, text)
	if err != nil {
		return LogPage{}, err
	}
	afterRevs, err := historyStarts(ctx, dir, f, afterHead, afterHash)
	if err != nil {
		return LogPage{}, err
	}
	afterStarts, _ := json.Marshal(afterRevs)
	if !bytes.Equal(refs, after) || hash != afterHash || !bytes.Equal(starts, afterStarts) {
		return LogPage{}, ErrLogChanged
	}
	return page, ctx.Err()
}

func parseHistoryIDs(out []byte) ([]string, map[string]string, error) {
	ids := []string{}
	decorations := map[string]string{}
	if len(out) == 0 {
		return ids, decorations, nil
	}
	if out[len(out)-1] != 0 {
		return nil, nil, ErrLogOutput
	}
	fields := bytes.Split(out[:len(out)-1], []byte{0})
	if len(fields)%2 != 0 {
		return nil, nil, ErrLogOutput
	}
	for i := 0; i < len(fields); i += 2 {
		oid, refs := string(fields[i]), string(fields[i+1])
		if !ValidBlobOID(oid) || !utf8.ValidString(refs) || strings.ContainsAny(refs, "\n\r") {
			return nil, nil, ErrLogOutput
		}
		if _, ok := decorations[oid]; ok {
			return nil, nil, ErrLogOutput
		}
		ids = append(ids, oid)
		decorations[oid] = refs
	}
	return ids, decorations, nil
}

// cat-file 的长度帧让提交信息中的 0x1e/0x1f 不会被误当作记录边界。
func readCommitObjects(ctx context.Context, dir string, ids []string, decorations map[string]string) ([]CommitInfo, error) {
	list := make([]CommitInfo, 0, len(ids))
	if len(ids) == 0 {
		return list, nil
	}
	out, err := historyRead(ctx, dir, []byte(strings.Join(ids, "\n")+"\n"), "cat-file", "--batch")
	if err != nil {
		return nil, err
	}
	for _, oid := range ids {
		i := bytes.IndexByte(out, '\n')
		if i < 0 {
			return nil, ErrLogOutput
		}
		f := strings.Fields(string(out[:i]))
		out = out[i+1:]
		if len(f) != 3 || f[0] != oid || f[1] != "commit" {
			return nil, ErrLogOutput
		}
		n, err := strconv.ParseInt(f[2], 10, 64)
		if err != nil || n < 0 || n >= int64(len(out)) || out[n] != '\n' {
			return nil, ErrLogOutput
		}
		decoded, err := decodeCommitObject(ctx, out[:n])
		if err != nil {
			return nil, err
		}
		c, err := parseCommitObject(oid, decoded)
		if err != nil {
			return nil, err
		}
		out = out[n+1:]
		c.Refs = decorations[oid]
		list = append(list, c)
	}
	if len(out) != 0 {
		return nil, ErrLogOutput
	}
	return list, nil
}

func parseCommitObject(oid string, raw []byte) (CommitInfo, error) {
	c := CommitInfo{Hash: oid, Parents: []string{}}
	if !utf8.Valid(raw) || bytes.IndexByte(raw, 0) >= 0 {
		return c, ErrLogOutput
	}
	headers, message, ok := strings.Cut(string(raw), "\n\n")
	if !ok {
		return c, ErrLogOutput
	}
	author, tree, committer := false, false, false
	for _, line := range strings.Split(headers, "\n") {
		if strings.HasPrefix(line, " ") {
			continue
		}
		key, value, ok := strings.Cut(line, " ")
		if !ok {
			return c, ErrLogOutput
		}
		switch key {
		case "tree":
			if tree || !ValidBlobOID(value) || len(value) != len(oid) {
				return c, ErrLogOutput
			}
			tree = true
		case "parent":
			if !ValidBlobOID(value) {
				return c, ErrLogOutput
			}
			c.Parents = append(c.Parents, value)
		case "author":
			if author {
				return c, ErrLogOutput
			}
			author = true
			start, end := strings.LastIndex(value, " <"), strings.LastIndex(value, "> ")
			if start < 0 || end < start+2 {
				return c, ErrLogOutput
			}
			c.Author = value[:start]
			c.Email = value[start+2 : end]
			stamp := strings.Fields(value[end+2:])
			if len(stamp) != 2 {
				return c, ErrLogOutput
			}
			sec, err := strconv.ParseInt(stamp[0], 10, 64)
			if err != nil {
				return c, ErrLogOutput
			}
			c.Time = time.Unix(sec, 0)
		case "committer":
			if committer {
				return c, ErrLogOutput
			}
			committer = true
		}
	}
	if !tree || !author || !committer {
		return c, ErrLogOutput
	}
	lines := []string{}
	for _, line := range strings.Split(message, "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			if len(lines) > 0 {
				break
			}
			continue
		}
		lines = append(lines, line)
	}
	c.Subject = strings.Join(lines, " ")
	if !ValidReadCommit(c) {
		return c, ErrLogOutput
	}
	return c, nil
}

func ValidReadCommit(c CommitInfo) bool {
	if !ValidBlobOID(c.Hash) || c.Time.Year() < 0 || c.Time.Year() > 9999 {
		return false
	}
	for _, parent := range c.Parents {
		if !ValidBlobOID(parent) || len(parent) != len(c.Hash) {
			return false
		}
	}
	for _, s := range []string{c.Author, c.Email, c.Subject, c.Refs} {
		if !utf8.ValidString(s) || strings.ContainsRune(s, 0) {
			return false
		}
	}
	return true
}

type CommitDetails struct {
	Commit   CommitInfo
	Files    []FileChange
	DiffBase string
	Shallow  bool
}

func ReadCommitFilesChecked(ctx context.Context, dir, hash string) (CommitInfo, []FileChange, error) {
	detail, err := ReadCommitDetailsChecked(ctx, dir, hash)
	return detail.Commit, detail.Files, err
}

func ReadCommitDetailsChecked(ctx context.Context, dir, hash string) (CommitDetails, error) {
	if !ValidBlobOID(hash) {
		return CommitDetails{}, ErrLogQuery
	}
	out, err := historyRead(ctx, dir, nil, "log", "--no-ext-diff", "--no-textconv", "--no-show-signature", "--no-color", "--format=%H%x00%D", "-z", "--no-walk", hash, "--")
	if err != nil {
		return CommitDetails{}, err
	}
	ids, decorations, err := parseHistoryIDs(out)
	if err != nil {
		return CommitDetails{}, err
	}
	if len(ids) != 1 || ids[0] != hash {
		return CommitDetails{}, ErrLogNotCommit
	}
	list, err := readCommitObjects(ctx, dir, ids, decorations)
	if err != nil {
		return CommitDetails{}, err
	}
	detail := CommitDetails{Commit: list[0]}
	detail.DiffBase, detail.Shallow, err = commitDiffBase(ctx, dir, detail.Commit)
	if err != nil {
		return CommitDetails{}, err
	}
	args := []string{"diff-tree", "--no-ext-diff", "--no-textconv", "-r", "--root", "--no-commit-id", "--name-status", "-z", "--find-renames"}
	if detail.DiffBase != "" {
		args = append(args, detail.DiffBase)
	}
	args = append(args, hash, "--")
	out, err = historyRead(ctx, dir, nil, args...)
	if err != nil {
		return CommitDetails{}, err
	}
	detail.Files, err = parseHistoryChanges(out)
	if err != nil {
		return CommitDetails{}, err
	}
	base, shallow, err := commitDiffBase(ctx, dir, detail.Commit)
	if err != nil {
		return CommitDetails{}, err
	}
	if base != detail.DiffBase || shallow != detail.Shallow {
		return CommitDetails{}, ErrLogChanged
	}
	return detail, ctx.Err()
}

// 原对象的 Parents 不随浅克隆改变；diff 的旧侧则遵循 Git 当前可见的父关系。
func commitDiffBase(ctx context.Context, dir string, c CommitInfo) (string, bool, error) {
	out, err := historyRead(ctx, dir, nil, "rev-list", "--parents", "--no-walk", c.Hash, "--")
	if err != nil {
		return "", false, err
	}
	fields := strings.Fields(string(out))
	if len(fields) == 0 || fields[0] != c.Hash {
		return "", false, ErrLogOutput
	}
	if len(fields) == 1 {
		return "", len(c.Parents) > 0, nil
	}
	if len(fields) != len(c.Parents)+1 {
		return "", false, ErrLogOutput
	}
	for i, parent := range c.Parents {
		if fields[i+1] != parent {
			return "", false, ErrLogOutput
		}
	}
	return c.Parents[0], false, nil
}

func parseHistoryChanges(out []byte) ([]FileChange, error) {
	list := []FileChange{}
	if len(out) == 0 {
		return list, nil
	}
	if out[len(out)-1] != 0 {
		return nil, ErrLogOutput
	}
	f := strings.Split(string(out[:len(out)-1]), "\x00")
	seen := map[string]bool{}
	for i := 0; i < len(f); {
		status := f[i]
		i++
		if status == "" || !strings.ContainsRune("AMDRCT", rune(status[0])) {
			return nil, ErrLogOutput
		}
		c := FileChange{Status: status[0]}
		if c.Status == 'R' || c.Status == 'C' {
			score, err := strconv.Atoi(status[1:])
			if err != nil || score < 0 || score > 100 || i >= len(f) {
				return nil, ErrLogOutput
			}
			c.Orig = f[i]
			i++
		} else if len(status) != 1 {
			return nil, ErrLogOutput
		}
		if i >= len(f) {
			return nil, ErrLogOutput
		}
		c.Path = f[i]
		i++
		if !ValidReadFileChange(c) || seen[c.Path] {
			return nil, ErrLogOutput
		}
		seen[c.Path] = true
		list = append(list, c)
	}
	return list, nil
}
func ValidReadFileChange(c FileChange) bool {
	if !strings.ContainsRune("AMDRCT", rune(c.Status)) || !utf8.ValidString(c.Path) || !ValidFileQuery(FileIndex, "", c.Path) {
		return false
	}
	if c.Status == 'R' || c.Status == 'C' {
		return utf8.ValidString(c.Orig) && ValidFileQuery(FileIndex, "", c.Orig)
	}
	return c.Orig == ""
}
