package errtext

import (
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"unicode"
)

// hanAllowed 是 internal/app 以外允许写中文字面量的地方：都不是界面文字。
// sub 为空时整个文件都允许，否则只允许含 sub 的字面量。
var hanAllowed = []struct{ file, sub, why string }{
	{"internal/errtext/zh.go", "", "中文译文表本身"},
	{"internal/namer/title.go", "", "会话标题的格式（MMDD｜类型｜摘要、中文类型词）是写进 agent 会话存储的内容，不跟界面语言变"},
	{"internal/agent/manual.go", "", "生成的 zsh 集成脚本里的注释，给读脚本的人看"},
	{"internal/agent/skill.go", "", "session-namer 技能的说明，写给 agent，里面是会话标题的格式"},
	{"internal/cli/cli.go", "MMDD｜类型｜摘要", "posse session title 的用法里的标题格式"},
	{"internal/cli/session.go", "MMDD｜类型｜摘要", "posse session title 的用法里的标题格式"},
	{"internal/history/history.go", "", "SQL 里的注释"},
}

// 常驻进程、CLI 和各内部包不产出中文界面文字：报错和说明写英文源文本，界面一侧经本包或 locales 翻译。
// internal/app 由它自己的 TestNoChineseLiterals 管。
func TestNoChineseOutsideApp(t *testing.T) {
	fset := token.NewFileSet()
	root, err := filepath.Abs("../..")
	if err != nil {
		t.Fatal(err)
	}
	for _, top := range []string{"internal", "cmd"} {
		filepath.WalkDir(filepath.Join(root, top), func(p string, d fs.DirEntry, err error) error {
			if err != nil {
				return err
			}
			rel, _ := filepath.Rel(root, p)
			rel = filepath.ToSlash(rel)
			if d.IsDir() {
				if rel == "internal/app" {
					return filepath.SkipDir
				}
				return nil
			}
			if !strings.HasSuffix(p, ".go") || strings.HasSuffix(p, "_test.go") {
				return nil
			}
			f, err := parser.ParseFile(fset, p, nil, 0)
			if err != nil {
				t.Fatal(err)
			}
			ast.Inspect(f, func(n ast.Node) bool {
				lit, ok := n.(*ast.BasicLit)
				if !ok || lit.Kind != token.STRING {
					return true
				}
				s, err := strconv.Unquote(lit.Value)
				if err != nil || !hasHan(s) || hanOK(rel, s) {
					return true
				}
				pos := fset.Position(lit.Pos())
				t.Errorf("%s:%d: Chinese text outside internal/app; write the English source and translate it in errtext or locales: %.60s", rel, pos.Line, s)
				return true
			})
			return nil
		})
	}
}

func hanOK(file, s string) bool {
	for _, a := range hanAllowed {
		if a.file == file && (a.sub == "" || strings.Contains(s, a.sub)) {
			return true
		}
	}
	return false
}

func hasHan(s string) bool {
	for _, r := range s {
		if unicode.Is(unicode.Han, r) {
			return true
		}
	}
	return false
}

func TestLangOf(t *testing.T) {
	for tag, want := range map[string]Lang{"": ZH, "zh-Hans": ZH, "zh-Hans-CN": ZH, "zh_CN": ZH, "zh": ZH, "en": EN, "en-US": EN, "ja": EN} {
		if got := LangOf(tag); got != want {
			t.Errorf("LangOf(%q) = %q, want %q", tag, got, want)
		}
	}
}

func TestParseLanguages(t *testing.T) {
	for in, want := range map[string]string{
		"(\n    \"zh-Hans-CN\",\n    en\n)\n": "zh-Hans-CN",
		"(\n    en\n)\n":                      "en",
		"(\n)\n":                              "",
	} {
		if got := parseLanguages(in); got != want {
			t.Errorf("parseLanguages(%q) = %q, want %q", in, got, want)
		}
	}
}
