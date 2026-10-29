package app

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/egoist/mygo/ui"
	"github.com/egoist/mygo/ui/editor"
)

// 存进 ui.json 的写法读回来一样；每一项都有默认键，ID 不重复。
func TestKeyComboEncode(t *testing.T) {
	seen := map[string]bool{}
	for _, ka := range allKeyActions {
		if seen[ka.id] {
			t.Fatalf("duplicate key action %s", ka.id)
		}
		seen[ka.id] = true
		if len(ka.def) == 0 {
			t.Fatalf("%s has no default keys", ka.id)
		}
		for _, kc := range ka.def {
			got, ok := parseCombo(kc.encode())
			if !ok || got != kc {
				t.Fatalf("%s: %q read back as %v (%v)", ka.id, kc.encode(), got, ok)
			}
		}
	}
	if got := (keyCombo{mods: ui.Super, key: ui.KeyK, thenMods: ui.Super, thenKey: ui.KeySlash}).String(); got != "⌘K ⌘/" {
		t.Fatalf("chord text = %q", got)
	}
}

// 默认的键在同一处不重复（除了固定的几项）。
func TestDefaultKeysNoConflict(t *testing.T) {
	a := previewApp(t)
	for _, ka := range allKeyActions {
		if ka.fixed || ka.scope == scopeEditor {
			continue
		}
		for _, kc := range ka.def {
			if other := a.keyConflict(ka.id, kc); other != nil {
				t.Errorf("%s and %s both use %s", ka.id, other.id, kc)
			}
		}
	}
}

// 改了快捷键后旧键不再管用、新键管用，提示文字跟着变。
func TestRebindShortcut(t *testing.T) {
	a := previewApp(t)
	tt := ui.NewTester(a.build, 1200, 760)
	tt.Frame()
	a.setKeys("toggleSidebar", []keyCombo{{mods: ui.Cmd, key: ui.KeyJ}})
	hidden := a.state.SidebarHidden
	tt.Key(ui.Cmd, ui.KeyB)
	tt.Frame()
	if a.state.SidebarHidden != hidden {
		t.Fatal("the old key should no longer toggle the sidebar")
	}
	tt.Key(ui.Cmd, ui.KeyJ)
	tt.Frame()
	if a.state.SidebarHidden == hidden {
		t.Fatal("the new key should toggle the sidebar")
	}
	if a.withKey("收起侧栏", "toggleSidebar") != "收起侧栏 ⌘J" {
		t.Fatalf("tooltip = %q", a.withKey("收起侧栏", "toggleSidebar"))
	}
	// 改回默认就删掉记录
	a.setKeys("toggleSidebar", keyActionByID("toggleSidebar").def)
	if _, ok := a.state.Keys["toggleSidebar"]; ok {
		t.Fatal("default keys should not be stored")
	}
}

// 设置页里点按键、按新组合录进去；录了别人在用的键，从那一项上去掉；Esc 取消。
func TestRecordShortcut(t *testing.T) {
	a := previewApp(t)
	a.selected, a.settingsTab = settingsBoardID, "keys"
	tt := ui.NewTester(a.build, 1200, 900)
	tt.Frame()
	if err := tt.Click("修改快捷键 ⌘B"); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	if a.keyRec == nil || a.keyRec.id != "toggleSidebar" {
		t.Fatalf("clicking the keys should start recording: %+v", a.keyRec)
	}
	// 不带修饰键的字母不收，接着等
	tt.Key(0, ui.KeyJ)
	tt.Frame()
	if a.keyRec == nil || !slices.Equal(a.keysFor("toggleSidebar"), []keyCombo{{mods: ui.Cmd, key: ui.KeyB}}) {
		t.Fatal("a plain letter should not be taken")
	}
	tt.Key(ui.Cmd, ui.KeyT)
	tt.Frame()
	if a.keyRec != nil || !slices.Equal(a.keysFor("toggleSidebar"), []keyCombo{{mods: ui.Cmd, key: ui.KeyT}}) {
		t.Fatalf("recorded %v", a.keysFor("toggleSidebar"))
	}
	if len(a.keysFor("newTerminal")) != 0 {
		t.Fatal("the key should be taken away from 新建终端")
	}
	// 录的时候窗口快捷键不认：按 ⌘T 不会新建终端，也不会开关侧栏
	if err := tt.Click("修改快捷键 ⌘T"); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	hidden := a.state.SidebarHidden
	tt.Key(0, ui.KeyEscape)
	tt.Frame()
	if a.keyRec != nil || a.state.SidebarHidden != hidden || !slices.Equal(a.keysFor("toggleSidebar"), []keyCombo{{mods: ui.Cmd, key: ui.KeyT}}) {
		t.Fatal("Esc should cancel recording and keep the keys")
	}
	if !tt.HasText("恢复默认") {
		t.Fatal("changed rows should offer 恢复默认")
	}
	if err := tt.Click("全部恢复默认"); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	if len(a.state.Keys) != 0 || a.keyText("newTerminal") != "⌘T" {
		t.Fatal("reset all should restore the defaults")
	}
}

// 编辑器的命令改了键：新键绑上，旧键让出来。
func TestEditorBindings(t *testing.T) {
	a := previewApp(t)
	if a.editorBindings() != nil {
		t.Fatal("no changes, no bindings")
	}
	a.setKeys("editor.find", []keyCombo{{mods: ui.Cmd, key: ui.KeyJ}})
	b := a.editorBindings()
	if !slices.Contains(b, editor.Binding{Mods: ui.Cmd, Key: ui.KeyJ, Command: editor.Find}) ||
		!slices.Contains(b, editor.Binding{Mods: ui.Cmd, Key: ui.KeyF, Command: editor.NoCommand}) {
		t.Fatalf("bindings = %v", b)
	}
}

func TestFormatJSON(t *testing.T) {
	for _, c := range []struct{ in, want string }{
		{`{"b":1,"a":[1,2]}`, "{\n  \"b\": 1,\n  \"a\": [\n    1,\n    2\n  ]\n}"},
		{"{\n\t\"a\": {\"b\": true}}\n", "{\n\t\"a\": {\n\t\t\"b\": true\n\t}\n}\n"},
		{"{\n    \"a\": 1,  \"b\": 2\n}\n\n", "{\n    \"a\": 1,\n    \"b\": 2\n}\n"},
	} {
		got, err := formatJSONText(c.in)
		if err != nil || got != c.want {
			t.Fatalf("formatJSONText(%q) = %q, %v; want %q", c.in, got, err, c.want)
		}
	}
	_, err := formatJSONText("{\n  \"a\": 1,\n  \"b\": \n}")
	if err == nil || !strings.Contains(err.Error(), "第 4 行") {
		t.Fatalf("syntax error = %v", err)
	}
}

// JSON 文件顶栏的"格式化"和快捷键都把内容整理好，能撤销；不是 JSON 的文件没有这个按钮。
func TestFormatJSONDoc(t *testing.T) {
	a := previewApp(t)
	root := filesWorkspace(t, a)
	w := &a.workspaces[0]
	path := filepath.Join(root, "package.json")
	if err := os.WriteFile(path, []byte(`{"name":"posse","private":true}`), 0o644); err != nil {
		t.Fatal(err)
	}
	tt := ui.NewTester(a.build, 1200, 760)
	a.openFile(w, path, 0, [2]int{})
	d := a.files[w.ID].doc(path)
	until(t, tt, "loaded", func() bool { return d.kind == docText && tt.HasText("格式化") })
	if err := tt.Click("格式化"); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	want := "{\n  \"name\": \"posse\",\n  \"private\": true\n}"
	if got := d.ed.Buffer.String(); got != want || !d.isModified() {
		t.Fatalf("formatted = %q", got)
	}
	d.ed.Run(editor.Undo)
	if d.ed.Buffer.String() != `{"name":"posse","private":true}` {
		t.Fatal("formatting should undo in one step")
	}
	tt.Key(ui.Alt|ui.Shift, ui.KeyF)
	tt.Frame()
	if d.ed.Buffer.String() != want {
		t.Fatal("⇧⌥F should format the JSON file")
	}
	a.openFile(w, filepath.Join(root, "main.go"), 0, [2]int{})
	until(t, tt, "go file", func() bool { return a.files[w.ID].cur.kind == docText })
	if tt.HasText("格式化") {
		t.Fatal("only JSON files have 格式化")
	}
}
