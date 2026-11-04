package errtext

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"

	"posse/internal/rpc"
)

// 译文表里每条原文都要还在代码里：改了报错的英文而没改译文表，这条就静默失效了。
func TestFormatsStillInSource(t *testing.T) {
	var src strings.Builder
	filepath.WalkDir("..", func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() || !strings.HasSuffix(p, ".go") || strings.HasSuffix(p, "_test.go") {
			return nil
		}
		// 本包只有 errtext.go 算源文本，译文表本身不算
		if filepath.Base(filepath.Dir(p)) == "errtext" && filepath.Base(p) != "errtext.go" {
			return nil
		}
		b, _ := os.ReadFile(p)
		src.Write(b)
		return nil
	})
	all := src.String()
	for lang, c := range catalogs {
		for _, e := range c.entries {
			if !strings.Contains(all, strconv.Quote(e.format)) {
				t.Errorf("%s: %q is no longer in the source", lang, e.format)
			}
		}
	}
}

// 译文里的 {n} 不能超过原文的参数个数（对人没意义的参数，如媒体 ID，可以不用）。
func TestPlaceholders(t *testing.T) {
	ph := regexp.MustCompile(`\{(\d+)\}`)
	for lang, c := range catalogs {
		for _, e := range c.entries {
			for _, m := range ph.FindAllStringSubmatch(e.text, -1) {
				n, _ := strconv.Atoi(m[1])
				if n < 1 || n > len(e.verbs) {
					t.Errorf("%s: %q uses {%d}, the source has %d arguments", lang, e.text, n, len(e.verbs))
				}
			}
		}
	}
}

func TestMessage(t *testing.T) {
	cases := []struct{ in, want string }{
		{`workspace "foo" not found`, "找不到工作区 foo"},
		{`workspace "a" has 2 running terminals; pass force to close them`, "工作区 a 还有 2 个终端在运行"},
		{"workspace posse/x already exists", "工作区 posse/x 已经有了"},
		{"merge failed and was rolled back: terminal has exited", "合并失败，已经回滚：终端已经退出了"},
		{"binary_file", "这是二进制文件，没法当文本打开"},
		{"git worktree add: fatal: invalid reference", "git worktree add: fatal: invalid reference"},
		// 几个仓库各一行：逐行翻译，单行的格式串不把几行吞成一个参数
		{"nothing merged:\nworktree b has uncommitted changes; commit them first\nrepository a is on branch dev, not main; switch it before merging",
			"一个仓库都没有合并：\nworktree b 有未提交的改动，先提交\n仓库 a 在分支 dev 上，不是 main，先切过去再合并"},
		{"a: worktree removed but branch not found\nb: git failed", "a：worktree 删掉了，但是branch not found\nb: git failed"},
	}
	for _, c := range cases {
		if got := MessageIn(ZH, c.in); got != c.want {
			t.Errorf("MessageIn(zh, %q) = %q, want %q", c.in, got, c.want)
		}
		if got := MessageIn(EN, c.in); got != capitalize(c.in) {
			t.Errorf("MessageIn(en, %q) = %q, want the source with a capital", c.in, got)
		}
	}
}

func TestText(t *testing.T) {
	cases := []struct {
		err    error
		zh, en string
	}{
		{rpc.Errorf(rpc.CodeNotFound, "project %q not found", "demo"), "找不到项目 demo", `Project "demo" not found`},
		{rpc.Errorf(rpc.CodeConflict, "something new"), "something new", "Something new"},
		{rpc.Errorf(rpc.CodeUnknown, "unknown method x"), "常驻进程是旧版，不认识这个操作：到设置 › 关于 更新常驻进程，终端不会断开", capitalize(srcOldDaemon)},
		{fmt.Errorf("call: %w", context.DeadlineExceeded), "常驻进程没有及时回应，稍后再试", capitalize(srcTimeout)},
		{&fs.PathError{Op: "open", Path: "/x/y", Err: fs.ErrPermission}, "没有权限访问 /x/y", "No permission to access /x/y"},
		{&fs.PathError{Op: "open", Path: "/x/y", Err: errors.New("odd")}, "/x/y：odd", "/x/y: odd"},
		{errors.New("not an image"), "这不是图片", "Not an image"},
	}
	for _, c := range cases {
		if got := TextIn(ZH, c.err); got != c.zh {
			t.Errorf("TextIn(zh, %v) = %q, want %q", c.err, got, c.zh)
		}
		if got := TextIn(EN, c.err); got != c.en {
			t.Errorf("TextIn(en, %v) = %q, want %q", c.err, got, c.en)
		}
	}
	if got := TextIn(EN, errors.New("this repository has no remote")); got != "This repository has no remote" {
		t.Errorf("TextIn(en, no remote) = %q", got)
	}
	if _, err := os.Open("/nonexistent/posse"); TextIn(ZH, err) != "找不到 /nonexistent/posse" {
		t.Errorf("TextIn(zh, ENOENT) = %q", TextIn(ZH, err))
	}
}

// 英文显示时首字母大写，只动开头那个全小写的单词。
func TestCapitalize(t *testing.T) {
	cases := []struct{ in, want string }{
		{"this repository has no remote", "This repository has no remote"},
		{"fatal: no upstream", "Fatal: no upstream"},
		{"macOS is too old", "macOS is too old"},
		{"iPhone disconnected", "iPhone disconnected"},
		{"binary_file", "binary_file"},
		{"/x/y: odd", "/x/y: odd"},
		{"2 terminals", "2 terminals"},
		{"Already capital", "Already capital"},
		{"canceled", "Canceled"},
		{"", ""},
	}
	for _, c := range cases {
		if got := capitalize(c.in); got != c.want {
			t.Errorf("capitalize(%q) = %q, want %q", c.in, got, c.want)
		}
	}
	// 中文的路径不经 display：译文原样，没有译文的英文原文也不改
	if got := MessageIn(ZH, "something new"); got != "something new" {
		t.Errorf("MessageIn(zh, unknown) = %q", got)
	}
}
