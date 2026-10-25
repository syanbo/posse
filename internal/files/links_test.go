package files

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestLinkAt(t *testing.T) {
	tests := []struct {
		line       string
		col        int
		kind       LinkKind
		text       string
		start, end int
	}{
		{`import { A } from '~/constants/scene-keys';`, 25, LinkPath, "~/constants/scene-keys", 19, 41},
		{`import { A } from '~/constants/scene-keys';`, 10, LinkWord, "A", 9, 10},
		{`const x = require("./utils/common.js")`, 25, LinkPath, "./utils/common.js", 19, 36},
		{`#include <sys/types.h>`, 14, LinkPath, "sys/types.h", 10, 21},
		{`// see https://example.com/docs. ok`, 12, LinkURL, "https://example.com/docs", 7, 31},
		{`// 见 https://example.com/a，然后`, 12, LinkURL, "https://example.com/a", 7, 28}, // 全角逗号结束网址
		{`// 见 https://example.com/a（说明）`, 12, LinkURL, "https://example.com/a", 7, 28},
		{`s := 'react'`, 8, LinkNone, "", 0, 0},                                     // 包名不算路径，字符串里也不认标识符
		{`s := "two words"`, 8, LinkNone, "", 0, 0},                                 // 带空格的不是路径
		{`export const E = 'kLYRNEventQuickOrderSuccess';`, 25, LinkNone, "", 0, 0}, // 普通字符串
		{`// 原生通知 RN，见 docs/events.md。`, 30, LinkPath, "docs/events.md", 25, 39},    // 注释里不带引号的路径
		{`// see [events](./docs/events.md).`, 20, LinkPath, "./docs/events.md", 16, 32},
		{`// 原生通知 RN 通用 key`, 10, LinkNone, "", 0, 0}, // 注释里的词不算
		{`x := foo() // call foo`, 20, LinkNone, "", 0, 0},
		{`#include "lib/x.h"`, 12, LinkPath, "lib/x.h", 10, 17},
		{`path := filepath.Join(a, b)`, 12, LinkWord, "filepath", 8, 16},
	}
	for _, tt := range tests {
		lk := LinkAt(tt.line, tt.col)
		if lk.Kind != tt.kind || lk.Text != tt.text || (tt.kind != LinkNone && (lk.Start != tt.start || lk.End != tt.end)) {
			t.Errorf("LinkAt(%q, %d) = %+v, want %v %q %d-%d", tt.line, tt.col, lk, tt.kind, tt.text, tt.start, tt.end)
		}
	}
}

func TestResolveImport(t *testing.T) {
	root := t.TempDir()
	put := func(rel, text string) {
		p := filepath.Join(root, rel)
		os.MkdirAll(filepath.Dir(p), 0o755)
		if err := os.WriteFile(p, []byte(text), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	put("src/constants/scene-keys.ts", "")
	put("src/utils/common/index.js", "")
	put("src/app.tsx", "")
	put("lib/x.go", "package lib")
	put("go.mod", "module example.com/m\n\ngo 1.22\n")
	put("tsconfig.json", "{\n  // 注释\n  \"compilerOptions\": {\n    \"baseUrl\": \".\",\n    \"paths\": { \"~/*\": [\"src/*\"], },\n  },\n}\n")
	from := filepath.Join(root, "src", "app.tsx")
	tests := []struct {
		spec, want string
		line       int
	}{
		{"~/constants/scene-keys", "src/constants/scene-keys.ts", 0},
		{"./utils/common", "src/utils/common/index.js", 0},
		{"./constants/scene-keys.ts:12", "src/constants/scene-keys.ts", 12},
		{"example.com/m/lib", "lib/x.go", 0},
		{"/src/app.tsx", "src/app.tsx", 0},
		{"./nope", "", 0},
	}
	for _, tt := range tests {
		got, line, ok := ResolveImport(root, from, tt.spec)
		if tt.want == "" {
			if ok {
				t.Errorf("ResolveImport(%q) = %q, want none", tt.spec, got)
			}
			continue
		}
		if !ok || got != filepath.Join(root, tt.want) || line != tt.line {
			t.Errorf("ResolveImport(%q) = %q %d %v, want %q %d", tt.spec, got, line, ok, tt.want, tt.line)
		}
	}
	if p := PreferFor(root, from, `import { SCENE } from '~/constants/scene-keys';`); p != "src/constants/scene-keys.ts" {
		t.Errorf("PreferFor = %q", p)
	}
	if p := PreferFor(root, filepath.Join(root, "main.go"), `	"example.com/m/lib"`); p != "lib" {
		t.Errorf("PreferFor go = %q", p)
	}
}

func TestImportPathOn(t *testing.T) {
	tests := map[string]string{
		`import x from "./x";`:          "./x",
		`const y = require('../y')`:     "../y",
		`	"posse/internal/files"`:       "posse/internal/files",
		`import "posse/internal/files"`: "posse/internal/files",
		`@import url("theme.css");`:     "theme.css",
		`#include <stdio.h>`:            "stdio.h",
		`export * from "./z"`:           "./z",
		`const s = "not an import"`:     "",
		`foo("./looks/like/path")`:      "",
	}
	for line, want := range tests {
		if got := ImportPathOn(line); got != want {
			t.Errorf("ImportPathOn(%q) = %q, want %q", line, got, want)
		}
	}
}

// 第三方包：node_modules 按 package.json 的入口，Go 的 replace、require 和标准库
func TestResolveThirdParty(t *testing.T) {
	root := t.TempDir()
	put := func(rel, text string) {
		p := filepath.Join(root, rel)
		os.MkdirAll(filepath.Dir(p), 0o755)
		if err := os.WriteFile(p, []byte(text), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	put("node_modules/lodash/package.json", `{"main": "lodash.js"}`)
	put("node_modules/lodash/lodash.js", "")
	put("node_modules/lodash/fp.js", "")
	put("node_modules/@scope/pkg/package.json", `{"types": "dist/index.d.ts", "main": "dist/index.js"}`)
	put("node_modules/@scope/pkg/dist/index.d.ts", "")
	put("node_modules/plain/index.js", "")
	put("src/app.js", "")
	put("third_party/mygo/ui/ui.go", "package ui")
	put("go.mod", "module example.com/m\n\ngo 1.22\n\nrequire (\n\tgithub.com/egoist/mygo v0.4.8\n)\n\nreplace github.com/egoist/mygo => ./third_party/mygo\n")
	from := filepath.Join(root, "src", "app.js")
	tests := []struct{ spec, want string }{
		{"lodash", "node_modules/lodash/lodash.js"},
		{"lodash/fp", "node_modules/lodash/fp.js"},
		{"@scope/pkg", "node_modules/@scope/pkg/dist/index.d.ts"},
		{"plain", "node_modules/plain/index.js"},
		{"github.com/egoist/mygo/ui", "third_party/mygo/ui/ui.go"},
		{"nope", ""},
	}
	for _, tt := range tests {
		got, _, ok := ResolveImport(root, from, tt.spec)
		if tt.want == "" {
			if ok {
				t.Errorf("ResolveImport(%q) = %q, want none", tt.spec, got)
			}
			continue
		}
		if !ok || got != filepath.Join(root, tt.want) {
			t.Errorf("ResolveImport(%q) = %q %v, want %q", tt.spec, got, ok, tt.want)
		}
	}
	if goEnv("GOROOT") != "" {
		if got, _, ok := ResolveImport(root, from, "fmt"); !ok || !strings.HasSuffix(got, filepath.Join("src", "fmt", "doc.go")) && !strings.HasSuffix(filepath.Dir(got), "fmt") {
			t.Errorf("fmt = %q %v", got, ok)
		}
	}
}
