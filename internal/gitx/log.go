package gitx

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

// LogFilter 是日志的筛选。Branch 为空时看所有分支、远端分支和标签。
type LogFilter struct {
	Branch string
	Author string
	Text   string // 提交信息里的文字，或提交哈希的开头
	Path   string // 只看改过这个文件的提交（文件历史），相对仓库根目录
}

// Log 读日志，按拓扑顺序（画提交图要的顺序），跳过 skip 个，最多 limit 个。
func Log(ctx context.Context, dir string, f LogFilter, skip, limit int) ([]CommitInfo, error) {
	args := []string{"log", commitFormat, "--date-order", "-n", strconv.Itoa(limit), "--skip", strconv.Itoa(skip)}
	if f.Author != "" {
		// 按字面匹配：作者框里敲到一半的 "a[" 当正则会报错
		args = append(args, "--author="+f.Author, "--regexp-ignore-case", "--fixed-strings")
	}
	text := strings.TrimSpace(f.Text)
	switch {
	case text != "" && looksLikeHashPrefix(text):
		// 哈希的开头：只看这一个提交
		args = append(args, "--no-walk", text)
	default:
		if text != "" {
			args = append(args, "--grep="+text, "--regexp-ignore-case", "--fixed-strings")
		}
		if f.Branch != "" {
			args = append(args, f.Branch)
		} else {
			args = append(args, "--branches", "--remotes", "--tags", "HEAD")
		}
	}
	args = append(args, "--")
	if f.Path != "" {
		args = append(args, literal(f.Path)...)
	}
	out, err := runRaw(ctx, dir, nil, nil, args...)
	if err != nil {
		// 哈希开头对不上任何提交时 git 会报 unknown revision，当成没有结果
		var ge *Error
		if text != "" && looksLikeHashPrefix(text) && errors.As(err, &ge) {
			return nil, nil
		}
		return nil, err
	}
	return parseCommits(out), nil
}

// looksLikeHashPrefix 至少 7 位才当哈希：短的像 "feed"、"cafe" 更可能是要搜的词。
func looksLikeHashPrefix(s string) bool {
	if len(s) < 7 || len(s) > 40 {
		return false
	}
	for _, r := range s {
		if !strings.ContainsRune("0123456789abcdef", r) {
			return false
		}
	}
	return true
}

// CherryPick 把提交应用到当前分支；冲突时停在进行中的状态。
func CherryPick(ctx context.Context, dir, hash string) error {
	_, err := runRaw(ctx, dir, nil, nil, "cherry-pick", hash)
	return err
}

// RevertCommit 新建一个撤销 hash 的提交。
func RevertCommit(ctx context.Context, dir, hash string) error {
	_, err := runRaw(ctx, dir, nil, nil, "revert", "--no-edit", hash)
	return err
}

// Reset 把当前分支移到 hash；mode 是 soft、mixed、hard。
func Reset(ctx context.Context, dir, hash, mode string) error {
	switch mode {
	case "soft", "mixed", "hard":
	default:
		return errors.New("unknown reset mode " + mode)
	}
	_, err := runRaw(ctx, dir, nil, nil, "reset", "--"+mode, hash)
	return err
}

// ErrRewriteMerges 表示要改写的历史里有合并提交：交互式 rebase 会把它们拉平，不做。
var ErrRewriteMerges = errors.New("这段历史里有合并提交，改写会把它们拉平，hive 不做这种改写")

// ErrNotOnBranch 表示提交不在当前分支上。
var ErrNotOnBranch = errors.New("这个提交不在当前分支上")

// rewriteBase 检查从 hash 到 HEAD 能不能改写，返回 rebase 的起点（hash 的父提交，根提交时为空）。
func rewriteBase(ctx context.Context, dir, hash string) (base string, err error) {
	if _, err := runRaw(ctx, dir, nil, nil, "merge-base", "--is-ancestor", hash, "HEAD"); err != nil {
		return "", ErrNotOnBranch
	}
	out, err := run(ctx, dir, "rev-list", "--merges", hash+"^@..HEAD")
	if err != nil {
		// 根提交没有父提交，^@ 为空，范围就是到 HEAD 为止的全部
		out, err = run(ctx, dir, "rev-list", "--merges", "HEAD")
		if err != nil {
			return "", err
		}
	}
	if strings.TrimSpace(out) != "" {
		return "", ErrRewriteMerges
	}
	parent, err := run(ctx, dir, "rev-parse", "--verify", "--quiet", hash+"^")
	if err != nil {
		return "", nil
	}
	return parent, nil
}

// rebaseTodo 用交互式 rebase 改写当前分支：actions 把提交（完整哈希）映射到 rebase 的动作（reword、squash、drop），
// message 非空时作为 reword 和 squash 要写的提交信息。由 hive 生成的脚本改写待办列表和提交信息，不打开编辑器。
func rebaseTodo(ctx context.Context, dir, from string, actions map[string]string, message string) error {
	base, err := rewriteBase(ctx, dir, from)
	if err != nil {
		return err
	}
	tmp, err := os.MkdirTemp("", "hive-rebase-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(tmp)
	// 待办列表里是短哈希：完整哈希以它开头的就是要改的那个
	var rules strings.Builder
	for h, act := range actions {
		rules.WriteString(`if ($1 == "pick" && index("` + h + `", $2) == 1) $1 = "` + act + `"` + "\n")
	}
	seq := filepath.Join(tmp, "seq.sh")
	script := "#!/bin/sh\nawk '{\n" + rules.String() + "print }' \"$1\" > \"$1.hive\" && mv \"$1.hive\" \"$1\"\n"
	if err := os.WriteFile(seq, []byte(script), 0o755); err != nil {
		return err
	}
	msgFile := filepath.Join(tmp, "message")
	if err := os.WriteFile(msgFile, []byte(message+"\n"), 0o644); err != nil {
		return err
	}
	edit := filepath.Join(tmp, "edit.sh")
	if err := os.WriteFile(edit, []byte("#!/bin/sh\ncat '"+msgFile+"' > \"$1\"\n"), 0o755); err != nil {
		return err
	}
	// 不受用户配置影响：commit.cleanup 默认会删掉 # 开头的行（#123 这类引用），
	// abbreviateCommands 让待办写成 "p <哈希>"、脚本认不出，autoSquash 会顺手并掉范围里无关的 fixup! 提交
	args := []string{"-c", "commit.cleanup=whitespace", "-c", "rebase.abbreviateCommands=false",
		"rebase", "-i", "--autostash", "--no-autosquash"}
	if base == "" {
		args = append(args, "--root")
	} else {
		args = append(args, base)
	}
	env := []string{"GIT_SEQUENCE_EDITOR=" + seq, "GIT_EDITOR=" + edit}
	if _, err := runRaw(ctx, dir, nil, env, args...); err != nil {
		return err
	}
	return autostashConflict(ctx, dir)
}

// Reword 改提交信息；是 HEAD 时直接 amend。
func Reword(ctx context.Context, dir, hash, message string) error {
	head, _ := run(ctx, dir, "rev-parse", "HEAD")
	if head == hash {
		_, err := runRaw(ctx, dir, []byte(message), nil, "commit", "--amend", "--only", "-F", "-")
		return err
	}
	return rebaseTodo(ctx, dir, hash, map[string]string{hash: "reword"}, message)
}

// SquashIntoParent 把 hash 和它的父提交合成一个提交，提交信息用 message。
func SquashIntoParent(ctx context.Context, dir, hash, message string) error {
	parent, err := run(ctx, dir, "rev-parse", "--verify", "--quiet", hash+"^")
	if err != nil {
		return errors.New("这是第一个提交，前面没有可以合并的提交")
	}
	return rebaseTodo(ctx, dir, parent, map[string]string{hash: "squash"}, message)
}

// Drop 从当前分支的历史里删掉一个提交。
func Drop(ctx context.Context, dir, hash string) error {
	return rebaseTodo(ctx, dir, hash, map[string]string{hash: "drop"}, "")
}

// FullMessage 是提交的完整信息。
func FullMessage(ctx context.Context, dir, hash string) string {
	out, err := runRaw(ctx, dir, nil, nil, "log", "-1", "--format=%B", hash)
	if err != nil {
		return ""
	}
	return strings.TrimRight(string(out), "\n")
}

// BlameLine 是 Annotate 里的一行：最后改它的提交。
type BlameLine struct {
	Hash    string
	Author  string
	Time    time.Time
	Summary string
	Text    string
}

// Blame 逐行列出工作区里这个文件最后改它的提交；没提交的改动的哈希全是 0。
func Blame(ctx context.Context, dir, path string) ([]BlameLine, error) {
	out, err := runRaw(ctx, dir, nil, nil, "blame", "--porcelain", "--", path)
	if err != nil {
		return nil, err
	}
	type meta struct {
		author, summary string
		time            time.Time
	}
	metas := map[string]*meta{}
	var lines []BlameLine
	var cur string
	for _, l := range strings.Split(string(out), "\n") {
		switch {
		case strings.HasPrefix(l, "\t"):
			m := metas[cur]
			bl := BlameLine{Hash: cur, Text: l[1:]}
			if m != nil {
				bl.Author, bl.Summary, bl.Time = m.author, m.summary, m.time
			}
			lines = append(lines, bl)
		case len(l) >= 40 && looksLikeHash(l[:40]) && (len(l) == 40 || l[40] == ' '):
			cur = l[:40]
			if metas[cur] == nil {
				metas[cur] = &meta{}
			}
		case strings.HasPrefix(l, "author "):
			metas[cur].author = l[len("author "):]
		case strings.HasPrefix(l, "author-time "):
			if sec, err := strconv.ParseInt(l[len("author-time "):], 10, 64); err == nil {
				metas[cur].time = time.Unix(sec, 0)
			}
		case strings.HasPrefix(l, "summary "):
			metas[cur].summary = l[len("summary "):]
		}
	}
	return lines, nil
}
