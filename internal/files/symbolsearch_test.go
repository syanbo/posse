package files

import (
	"context"
	"os/exec"
	"path/filepath"
	"slices"
	"testing"
)

// 符号搜索：名字含查询（不分大小写）的声明，调用和注释里的不算；名字一样的排最前，只要类型时只留类型
func TestFindSymbols(t *testing.T) {
	root := t.TempDir()
	write(t, filepath.Join(root, "conf.go"), "package conf\n\n// loadConfig 读配置\nfunc loadConfig() *Config { return nil }\n\ntype Config struct{}\n\nfunc use() { loadConfig() }\n")
	write(t, filepath.Join(root, "web", "app.ts"), "export class ConfigStore {}\nexport function readConfigFile(p: string) {}\nconst x = readConfigFile('a')\n")
	write(t, filepath.Join(root, "notes.md"), "type Config struct{}\n")
	for _, git := range []bool{false, true} {
		if git {
			if out, err := exec.Command("git", "init", "-q", root).CombinedOutput(); err != nil {
				t.Skipf("git unavailable: %v %s", err, out)
			}
		}
		l, _ := ListFiles(context.Background(), root)
		syms, _, err := FindSymbols(context.Background(), l, nil, SymbolQuery{Text: "config"})
		if err != nil {
			t.Fatal(err)
		}
		var got []string
		for _, s := range syms {
			got = append(got, s.Name+":"+s.Kind)
		}
		want := []string{"Config:type", "ConfigStore:type", "loadConfig:func", "readConfigFile:func"}
		if !slices.Equal(got, want) {
			t.Fatalf("git=%v symbols\n got %q\nwant %q", git, got, want)
		}
		types, _, _ := FindSymbols(context.Background(), l, nil, SymbolQuery{Text: "config", TypesOnly: true})
		if len(types) != 2 {
			t.Fatalf("types only: %v", types)
		}
		dir, _, _ := FindSymbols(context.Background(), l, nil, SymbolQuery{Text: "config", Dir: "web"})
		if len(dir) != 2 || dir[0].Path != filepath.Join("web", "app.ts") {
			t.Fatalf("dir: %v", dir)
		}
		over, _, _ := FindSymbols(context.Background(), l, map[string]string{"conf.go": "package conf\nfunc ConfigX() {}\n"}, SymbolQuery{Text: "configx"})
		if len(over) != 1 || over[0].Name != "ConfigX" {
			t.Fatalf("overlay: %v", over)
		}
	}
}
