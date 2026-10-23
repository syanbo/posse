package files

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func TestWordAt(t *testing.T) {
	tests := []struct {
		line       string
		col        int
		word, qual string
		start, end int
	}{
		{"MediaPickerHelper.openAlbum(this, 3);", 22, "openAlbum", "MediaPickerHelper", 18, 27},
		{"MediaPickerHelper.openAlbum(this, 3);", 27, "openAlbum", "MediaPickerHelper", 18, 27}, // 光标紧跟在词后面
		{"MediaPickerHelper.openAlbum(this, 3);", 5, "MediaPickerHelper", "", 0, 17},
		{"foo::bar()", 6, "bar", "foo", 5, 8},
		{"p->next", 4, "next", "p", 3, 7},
		{"this.value = 1", 6, "value", "", 5, 10},
		{"x = 中文名(1)", 7, "中文名", "", 4, 13},
		{"a + b", 2, "", "", 2, 2},
		{"$el.focus()", 1, "$el", "", 0, 3},
	}
	for _, tt := range tests {
		word, qual, start, end := WordAt(tt.line, tt.col)
		if word != tt.word || qual != tt.qual || start != tt.start || end != tt.end {
			t.Errorf("WordAt(%q, %d) = %q %q %d %d, want %q %q %d %d", tt.line, tt.col, word, qual, start, end, tt.word, tt.qual, tt.start, tt.end)
		}
	}
}

func writeFiles(t *testing.T, root string, fs map[string]string) *List {
	t.Helper()
	l := &List{Root: root}
	for rel, text := range fs {
		p := filepath.Join(root, rel)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(text), 0o644); err != nil {
			t.Fatal(err)
		}
		l.Files = append(l.Files, rel)
	}
	return l
}

func TestFindDefinitionsJava(t *testing.T) {
	root := t.TempDir()
	l := writeFiles(t, root, map[string]string{
		"app/ChatActivity.java":      "class ChatActivity {\n  void init() {\n    MediaPickerHelper.openAlbum(this, PickOptions.single(), 3);\n  }\n}\n",
		"lib/MediaPickerHelper.java": "public class MediaPickerHelper {\n  public static void openAlbum(Activity a, PickOptions o, int code) {\n  }\n}\n",
		"lib/Other.java":             "class Other {\n  void openAlbum() {}\n  int x = openAlbum();\n}\n",
		"notes/openAlbum.md":         "openAlbum here\n",
	})
	defs, err := FindDefinitions(context.Background(), l, nil, DefinitionQuery{Word: "openAlbum", Qual: "MediaPickerHelper", From: "app/ChatActivity.java"})
	if err != nil {
		t.Fatal(err)
	}
	if len(defs) != 2 {
		t.Fatalf("defs = %+v", defs)
	}
	if d := defs[0]; d.Path != "lib/MediaPickerHelper.java" || d.Line != 1 || d.Kind != "method" || !d.Decl || d.Text[d.Span[0]:d.Span[1]] != "openAlbum" {
		t.Errorf("first = %+v", d)
	}
	if defs[1].Path != "lib/Other.java" || defs[1].Line != 1 {
		t.Errorf("second = %+v", defs[1])
	}
	// 限定符指向 MediaPickerHelper.java，领先够多，直接跳
	if u, ok := Unique(defs); !ok || u.Path != "lib/MediaPickerHelper.java" {
		t.Errorf("unique = %+v %v", u, ok)
	}
	defs, _ = FindDefinitions(context.Background(), l, nil, DefinitionQuery{Word: "openAlbum", From: "app/ChatActivity.java"})
	if _, ok := Unique(defs); ok {
		t.Error("two declarations without a qualifier should not be unique")
	}
}

func TestFindDefinitionsGo(t *testing.T) {
	root := t.TempDir()
	l := writeFiles(t, root, map[string]string{
		"a/a.go":      "package a\n\nfunc Run() {\n\tx := helper()\n\t_ = x\n}\n\nfunc helper() int { return 1 }\n",
		"a/a_test.go": "package a\n\nfunc helper() int { return 2 }\n",
		"b/b.go":      "package b\n\ntype Thing struct {\n\thelper int\n}\n\nvar helper = 3\n",
	})
	defs, err := FindDefinitions(context.Background(), l, nil, DefinitionQuery{Word: "helper", From: "a/a.go"})
	if err != nil {
		t.Fatal(err)
	}
	// b.go 里的字段 helper 是别的文件里的弱匹配，有真声明时不列
	if len(defs) != 3 {
		t.Fatalf("defs = %+v", defs)
	}
	if defs[0].Path != "a/a.go" || defs[0].Line != 7 || defs[0].Kind != "func" {
		t.Errorf("first = %+v", defs[0])
	}
	if defs[1].Path != "a/a_test.go" {
		t.Errorf("second = %+v", defs[1])
	}
	// 未保存的内容优先于磁盘
	overlay := map[string]string{"b/b.go": "package b\n\nfunc helper() {}\n"}
	defs, _ = FindDefinitions(context.Background(), l, overlay, DefinitionQuery{Word: "helper", Qual: "b", From: "a/a.go"})
	if len(defs) == 0 || defs[0].Path != "b/b.go" || defs[0].Line != 2 {
		t.Errorf("qualified = %+v", defs)
	}
}

func TestFindDefinitionsUnique(t *testing.T) {
	root := t.TempDir()
	l := writeFiles(t, root, map[string]string{
		"x.ts": "export function load(id: string) {}\nconst r = load('1');\n",
		"y.py": "def load(path):\n    pass\n",
	})
	defs, _ := FindDefinitions(context.Background(), l, nil, DefinitionQuery{Word: "load", From: "x.ts"})
	if len(defs) != 2 {
		t.Fatalf("defs = %+v", defs)
	}
	if defs[0].Path != "x.ts" || defs[0].Line != 0 || !defs[0].Decl {
		t.Errorf("first = %+v", defs[0])
	}
	if _, ok := Unique(defs); ok {
		t.Error("ts and py declarations should both count")
	}
	defs, _ = FindDefinitions(context.Background(), l, nil, DefinitionQuery{Word: "load", From: "y.py"})
	if u, ok := Unique(defs[:1]); !ok || u.Path != "y.py" {
		t.Errorf("unique = %+v %v", u, ok)
	}
}

// git 仓库里走 git grep 粗筛，结果和逐个读文件一样；注释和字符串里的假定义不算
func TestFindDefinitionsGitGrep(t *testing.T) {
	root := t.TempDir()
	l := writeFiles(t, root, map[string]string{
		"a.js":    "export function load(id) {}\n// const load = 1\nconst s = 'function load(';\n",
		"b.js":    "const r = load(1);\n",
		"c.js":    "function load() {} // another\n",
		"note.md": "function load() {}\n",
	})
	if out, err := exec.Command("git", "init", "-q", root).CombinedOutput(); err != nil {
		t.Skipf("git unavailable: %v %s", err, out)
	}
	q := DefinitionQuery{Word: "load", From: "b.js"}
	plain, err := FindDefinitions(context.Background(), l, nil, q)
	if err != nil {
		t.Fatal(err)
	}
	l.Git = true
	viaGit, err := FindDefinitions(context.Background(), l, nil, q)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(plain, viaGit) {
		t.Fatalf("git grep = %+v\nscan = %+v", viaGit, plain)
	}
	if len(plain) != 2 || plain[0].Path != "a.js" || plain[0].Line != 0 || plain[1].Path != "c.js" {
		t.Fatalf("defs = %+v", plain)
	}
	// 未保存的内容替代磁盘上的
	overlay := map[string]string{"c.js": "// nothing\n"}
	viaGit, _ = FindDefinitions(context.Background(), l, overlay, q)
	if len(viaGit) != 1 || viaGit[0].Path != "a.js" {
		t.Fatalf("overlay defs = %+v", viaGit)
	}
}

func TestImports(t *testing.T) {
	root := t.TempDir()
	l := writeFiles(t, root, map[string]string{
		"src/app.ts":             "",
		"src/lib/util.ts":        "",
		"src/lib/store/index.ts": "",
		"src/styles.css":         "",
		"pkg/mod.py":             "",
		"pkg/sub/deep.py":        "",
		"internal/files/list.go": "package files",
		"app/src/main/java/com/x/MediaPickerHelper.java": "",
		"lib/widgets/bar.dart":                           "",
		"go.mod":                                         "module example.com/m\n",
	})
	from := filepath.Join(root, "src", "app.ts")
	named, all := Imports(l, root, from, strings.Join([]string{
		`import React, { useState as useS, type Props } from './lib/util';`,
		`import * as store from './lib/store';`,
		`const { a, b: c } = require('./lib/util');`,
		`import './styles.css';`,
		`export { x } from './lib/store';`,
	}, "\n"))
	want := map[string]string{"React": "src/lib/util.ts", "useS": "src/lib/util.ts", "Props": "src/lib/util.ts", "store": "src/lib/store/index.ts",
		"a": "src/lib/util.ts", "c": "src/lib/util.ts", "x": "src/lib/store/index.ts"}
	if !reflect.DeepEqual(named, want) {
		t.Errorf("js named = %v", named)
	}
	if !reflect.DeepEqual(all, []string{"src/lib/util.ts", "src/lib/store/index.ts", "src/styles.css"}) {
		t.Errorf("js all = %v", all)
	}

	named, _ = Imports(l, root, filepath.Join(root, "pkg", "main.py"), "from .mod import a, b as bb\nfrom pkg.sub.deep import d\nimport pkg.mod as pm\n")
	want = map[string]string{"a": "pkg/mod.py", "bb": "pkg/mod.py", "d": "pkg/sub/deep.py", "pm": "pkg/mod.py"}
	if !reflect.DeepEqual(named, want) {
		t.Errorf("py named = %v", named)
	}

	named, _ = Imports(l, root, filepath.Join(root, "main.go"), "import (\n\tf \"example.com/m/internal/files\"\n\t\"example.com/m/internal/files\"\n)\n")
	want = map[string]string{"f": "internal/files", "files": "internal/files"}
	if !reflect.DeepEqual(named, want) {
		t.Errorf("go named = %v", named)
	}

	named, _ = Imports(l, root, filepath.Join(root, "app/src/main/java/com/x/Chat.java"), "import com.x.MediaPickerHelper;\nimport static com.x.MediaPickerHelper.openAlbum;\nimport java.util.*;\n")
	want = map[string]string{"MediaPickerHelper": "app/src/main/java/com/x/MediaPickerHelper.java", "openAlbum": "app/src/main/java/com/x/MediaPickerHelper.java"}
	if !reflect.DeepEqual(named, want) {
		t.Errorf("java named = %v", named)
	}

	named, all = Imports(l, root, filepath.Join(root, "lib", "main.dart"), "import 'package:app/widgets/bar.dart' as bar;\nimport 'dart:io';\n")
	if named["bar"] != "lib/widgets/bar.dart" || !reflect.DeepEqual(all, []string{"lib/widgets/bar.dart"}) {
		t.Errorf("dart = %v %v", named, all)
	}
}
