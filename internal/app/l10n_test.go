package app

import (
	"bytes"
	"encoding/json"
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"
	"unicode"

	"github.com/egoist/mygo/ui"
)

// newTester 是测试用的 Tester，界面固定中文：Tester 默认没有语言（取英文），也不随跑测试那台机器的系统语言变。
func newTester(view func(c *ui.Context), width, height int) *ui.Tester {
	tt := ui.NewTester(view, width, height)
	tt.SetLocale("zh-Hans")
	return tt
}

// catalogKeys 读一张译文表里 Posse 自己的键（去掉 @ 开头的元数据和 mygo. 开头的覆盖）。
func catalogKeys(t *testing.T, name string) []string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("..", "..", "locales", name))
	if err != nil {
		t.Fatal(err)
	}
	var m map[string]json.RawMessage
	if err := json.Unmarshal(b, &m); err != nil {
		t.Fatalf("%s: %v", name, err)
	}
	var keys []string
	for k := range m {
		if !strings.HasPrefix(k, "@") && !strings.HasPrefix(k, "mygo.") {
			keys = append(keys, k)
		}
	}
	slices.Sort(keys)
	return keys
}

// 中文表和英文表的键完全一致：mygo i18n check 缺译只给警告，运行时缺了的键退回英文。
func TestCatalogsSameKeys(t *testing.T) {
	en, zh := catalogKeys(t, "en.json"), catalogKeys(t, "zh-Hans.json")
	for _, k := range en {
		if _, ok := slices.BinarySearch(zh, k); !ok {
			t.Errorf("zh-Hans.json 缺 %s", k)
		}
	}
	for _, k := range zh {
		if _, ok := slices.BinarySearch(en, k); !ok {
			t.Errorf("zh-Hans.json 多了 %s（en.json 里没有）", k)
		}
	}
}

// goSources 是仓库里除生成文件、第三方代码以外的 Go 源文件（含测试）。
func goSources(t *testing.T) map[string][]byte {
	t.Helper()
	root := filepath.Join("..", "..")
	out := map[string][]byte{}
	err := filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			switch d.Name() {
			case "third_party", "mobile", "build", "node_modules", ".git", ".posse":
				return filepath.SkipDir
			}
			return nil
		}
		if strings.HasSuffix(p, ".go") && d.Name() != "catalog_gen.go" {
			b, err := os.ReadFile(p)
			if err != nil {
				return err
			}
			out[p] = b
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return out
}

// en.json 里没有代码用不到的键（用到的键不存在由编译保证）。common 开头的通用词先备好、各界面直接用，不查。
func TestCatalogKeysUsed(t *testing.T) {
	var all bytes.Buffer
	for _, b := range goSources(t) {
		all.Write(b)
	}
	src := all.Bytes()
	for _, k := range catalogKeys(t, "en.json") {
		if strings.HasPrefix(k, "common") {
			continue
		}
		name := "." + strings.ToUpper(k[:1]) + k[1:]
		if !bytes.Contains(src, []byte(name+"(")) && !bytes.Contains(src, []byte(name+",")) &&
			!bytes.Contains(src, []byte(name+"}")) && !bytes.Contains(src, []byte(name+"\n")) {
			t.Errorf("en.json 的 %s 没有代码用到", k)
		}
	}
}

// locales/catalog_gen.go 和两张表对得上：改了 JSON 要跑 go tool mygo generate（go build 不经 mygo）。
// 在临时目录里从头生成一份比较，不动仓库里的文件。
func TestCatalogGenerated(t *testing.T) {
	if testing.Short() {
		t.Skip("要调 go tool mygo")
	}
	root, _ := filepath.Abs(filepath.Join("..", ".."))
	dir := t.TempDir()
	if err := os.Mkdir(filepath.Join(dir, "locales"), 0o755); err != nil {
		t.Fatal(err)
	}
	files, _ := filepath.Glob(filepath.Join(root, "locales", "*.json"))
	for _, f := range files {
		b, err := os.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, "locales", filepath.Base(f)), b, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	cfg := `{"name": "Posse", "identifier": "com.dengbo.posse", "i18n": {"dir": "locales", "source": "en"}}`
	if err := os.WriteFile(filepath.Join(dir, "mygo.json"), []byte(cfg), 0o644); err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command("go", "tool", "mygo", "generate", dir)
	cmd.Dir = root
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("go tool mygo generate: %v\n%s", err, out)
	}
	want, err := os.ReadFile(filepath.Join(dir, "locales", "catalog_gen.go"))
	if err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(filepath.Join(root, "locales", "catalog_gen.go"))
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, want) {
		t.Error("locales/catalog_gen.go 不是最新的：跑 go tool mygo generate")
	}
}

// l10nPending 是界面文字还没换成 locales 的键的文件，换完一个从这里去掉一个；不在表里的文件不许写中文字面量
// （注释和测试不算）。
var l10nPending = []string{}

func TestNoChineseLiterals(t *testing.T) {
	fset := token.NewFileSet()
	files, _ := filepath.Glob("*.go")
	for _, f := range files {
		if strings.HasSuffix(f, "_test.go") {
			continue
		}
		file, err := parser.ParseFile(fset, f, nil, 0)
		if err != nil {
			t.Fatal(err)
		}
		var found []string
		ast.Inspect(file, func(n ast.Node) bool {
			if lit, ok := n.(*ast.BasicLit); ok && lit.Kind == token.STRING {
				if s, err := strconv.Unquote(lit.Value); err == nil && hasHan(s) {
					found = append(found, fset.Position(lit.Pos()).String()+" "+lit.Value)
				}
			}
			return true
		})
		pending := slices.Contains(l10nPending, f)
		switch {
		case pending && len(found) == 0:
			t.Errorf("%s 已经没有中文字面量，从 l10nPending 里去掉", f)
		case !pending:
			for _, s := range found {
				t.Errorf("界面文字要经 locales 的键：%s", s)
			}
		}
	}
	for _, f := range l10nPending {
		if _, err := os.Stat(f); err != nil {
			t.Errorf("l10nPending 里的 %s 不存在了", f)
		}
	}
}

func hasHan(s string) bool {
	for _, r := range s {
		if unicode.Is(unicode.Han, r) {
			return true
		}
	}
	return false
}

// flatMessageFuncs 是可以调生成的平面函数（locales.SettingsTitle()，按 App 全局的语言）的函数：没有 Context 的菜单。
// 视图里一律用 L(c)，见 L 的注释。
var flatMessageFuncs = []string{"installMenu"}

func TestNoFlatMessages(t *testing.T) {
	fset := token.NewFileSet()
	files, _ := filepath.Glob("*.go")
	for _, f := range files {
		if strings.HasSuffix(f, "_test.go") {
			continue
		}
		file, err := parser.ParseFile(fset, f, nil, 0)
		if err != nil {
			t.Fatal(err)
		}
		for _, decl := range file.Decls {
			fn, ok := decl.(*ast.FuncDecl)
			allowed := ok && slices.Contains(flatMessageFuncs, fn.Name.Name)
			ast.Inspect(decl, func(n ast.Node) bool {
				sel, ok := n.(*ast.SelectorExpr)
				if !ok {
					return true
				}
				if id, ok := sel.X.(*ast.Ident); ok && id.Name == "locales" && !allowed {
					switch sel.Sel.Name {
					case "In", "Messages", "Catalog":
					default:
						t.Errorf("%s: locales.%s 按 App 全局的语言取文字，视图里用 L(c).%s", fset.Position(sel.Pos()), sel.Sel.Name, sel.Sel.Name)
					}
				}
				return true
			})
		}
	}
}
