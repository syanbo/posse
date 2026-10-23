package files

import (
	"path"
	"path/filepath"
	"regexp"
	"strings"
)

// 当前文件的 import 表：找定义时 import 进来的名字优先去它来自的文件里找，这个文件 import 的别的文件也靠前。
// 只看单行的写法，不解析语法。

var (
	jsImportRe      = regexp.MustCompile(`^\s*import\s+(?:type\s+)?(.+?)\s+from\s+['"]([^'"]+)['"]`)
	jsExportFromRe  = regexp.MustCompile(`^\s*export\s+(?:type\s+)?(\{[^}]*\}|\*(?:\s+as\s+[\pL\pN_$]+)?)\s+from\s+['"]([^'"]+)['"]`)
	jsRequireRe     = regexp.MustCompile(`^\s*(?:const|let|var)\s+(\{[^}]*\}|[\pL\pN_$]+)\s*=\s*(?:await\s+)?(?:require|import)\(\s*['"]([^'"]+)['"]\s*\)`)
	jsSideEffectRe  = regexp.MustCompile(`^\s*import\s+['"]([^'"]+)['"]`)
	pyFromRe        = regexp.MustCompile(`^\s*from\s+([\w.]+)\s+import\s+\(?\s*([^)#]+)`)
	pyImportRe      = regexp.MustCompile(`^\s*import\s+([\w.]+)(?:\s+as\s+(\w+))?`)
	javaImportRe    = regexp.MustCompile(`^\s*import\s+(static\s+)?([\w.]+)(\.\*)?\s*;?`)
	dartImportRe    = regexp.MustCompile(`^\s*(?:import|export|part)\s+['"]([^'"]+)['"](?:\s+(?:deferred\s+)?as\s+(\w+))?`)
	cIncludeRe      = regexp.MustCompile(`^\s*#\s*(?:include|import)\s*["<]([^">]+)[">]`)
	rubyRequireRe   = regexp.MustCompile(`^\s*(?:require_relative|require|load)\s*\(?\s*['"]([^'"]+)['"]`)
	goImportStartRe = regexp.MustCompile(`^\s*import\s*\(`)
)

// Imports 解析 text（from 这个文件的内容）里的 import：named 是名字（含 `as` 别名）→ 来源文件相对 root 的斜杠路径
// （Go 是目录），all 是 import 的所有文件或目录。认不出来源的跳过。
func Imports(l *List, root, from, text string) (named map[string]string, all []string) {
	named = map[string]string{}
	seen := map[string]bool{}
	addAll := func(rel string) {
		if rel != "" && !seen[rel] {
			seen[rel] = true
			all = append(all, rel)
		}
	}
	resolve := func(spec string) string {
		p, _, ok := ResolveImport(root, from, spec)
		if !ok {
			return ""
		}
		rel := relIn(root, p)
		if path.Ext(rel) == ".go" {
			rel = path.Dir(rel)
		}
		return rel
	}
	name := func(n, rel string) {
		if n = strings.TrimSpace(n); n != "" && rel != "" {
			named[n] = rel
			addAll(rel)
		}
	}
	fam := familyOf(from)
	inGoBlock := false
	for _, line := range strings.Split(text, "\n") {
		switch fam {
		case famJS:
			if m := jsImportRe.FindStringSubmatch(line); m != nil {
				rel := resolve(m[2])
				for _, n := range jsClauseNames(m[1]) {
					name(n, rel)
				}
				addAll(rel)
			} else if m := jsExportFromRe.FindStringSubmatch(line); m != nil {
				rel := resolve(m[2])
				for _, n := range jsClauseNames(m[1]) {
					name(n, rel)
				}
				addAll(rel)
			} else if m := jsRequireRe.FindStringSubmatch(line); m != nil {
				rel := resolve(m[2])
				for _, n := range jsClauseNames(m[1]) {
					name(n, rel)
				}
				addAll(rel)
			} else if m := jsSideEffectRe.FindStringSubmatch(line); m != nil {
				addAll(resolve(m[1]))
			}
		case famPy:
			if m := pyFromRe.FindStringSubmatch(line); m != nil {
				rel := resolve(pyModuleSpec(m[1]))
				for _, part := range strings.Split(m[2], ",") {
					n := strings.TrimSpace(part)
					if _, alias, ok := strings.Cut(n, " as "); ok {
						n = strings.TrimSpace(alias)
					}
					name(n, rel)
				}
				addAll(rel)
			} else if m := pyImportRe.FindStringSubmatch(line); m != nil {
				rel := resolve(pyModuleSpec(m[1]))
				n := m[2]
				if n == "" {
					n = m[1][strings.LastIndexByte(m[1], '.')+1:]
				}
				name(n, rel)
			}
		case famGo:
			switch {
			case goImportStartRe.MatchString(line):
				inGoBlock = true
			case inGoBlock && strings.HasPrefix(strings.TrimSpace(line), ")"):
				inGoBlock = false
			case inGoBlock:
				if m := goImportLineRe.FindStringSubmatch(line); m != nil {
					goImport(name, strings.TrimSpace(strings.TrimSuffix(line, `"`+m[1]+`"`)), m[1], resolve)
				}
			default:
				if m := importPathRe.FindStringSubmatch(line); m != nil && strings.HasPrefix(strings.TrimSpace(line), "import") {
					goImport(name, "", m[1], resolve)
				}
			}
		case famJava:
			if m := javaImportRe.FindStringSubmatch(line); m != nil {
				segs := strings.Split(m[2], ".")
				if m[3] != "" || m[1] != "" {
					// import a.b.*; 和 import static a.b.C.m; 都只知道类所在的包或类
					if m[1] != "" && len(segs) > 1 {
						rel := javaFile(l, segs[:len(segs)-1])
						name(segs[len(segs)-1], rel)
					}
					continue
				}
				name(segs[len(segs)-1], javaFile(l, segs))
			} else if m := dartImportRe.FindStringSubmatch(line); m != nil {
				rel := resolve(dartSpec(m[1]))
				if m[2] != "" {
					name(m[2], rel)
				}
				addAll(rel)
			}
		case famC:
			if m := cIncludeRe.FindStringSubmatch(line); m != nil {
				addAll(resolve(m[1]))
			}
		case famRuby:
			if m := rubyRequireRe.FindStringSubmatch(line); m != nil {
				addAll(resolve(m[1]))
			}
		}
	}
	return named, all
}

// goImport 记一条 Go 的 import：别名，或路径最后一段（去掉 /v2 这样的版本后缀）。
func goImport(name func(n, rel string), alias, spec string, resolve func(string) string) {
	rel := resolve(spec)
	if rel == "" {
		return
	}
	if alias == "" || alias == "_" || alias == "." {
		alias = path.Base(spec)
		if strings.HasPrefix(alias, "v") && strings.Trim(alias[1:], "0123456789") == "" {
			alias = path.Base(path.Dir(spec))
		}
	}
	name(alias, rel)
}

// jsClauseNames 从 import 子句里取出本地名字：`X`、`* as ns`、`{a, b as c}`、`X, {a}`、`{a: b}`（require 的解构）。
func jsClauseNames(clause string) []string {
	var out []string
	clause = strings.TrimSpace(clause)
	if i := strings.IndexByte(clause, '{'); i >= 0 {
		if head := strings.TrimSpace(strings.TrimSuffix(strings.TrimSpace(clause[:i]), ",")); head != "" {
			out = append(out, nsName(head))
		}
		inner := clause[i+1:]
		if j := strings.IndexByte(inner, '}'); j >= 0 {
			inner = inner[:j]
		}
		for _, part := range strings.Split(inner, ",") {
			n := strings.TrimSpace(part)
			if _, alias, ok := strings.Cut(n, " as "); ok {
				n = strings.TrimSpace(alias)
			} else if _, alias, ok := strings.Cut(n, ":"); ok {
				n = strings.TrimSpace(alias)
			}
			n = strings.TrimPrefix(n, "type ")
			if n != "" {
				out = append(out, n)
			}
		}
		return out
	}
	for _, part := range strings.Split(clause, ",") {
		if n := nsName(strings.TrimSpace(part)); n != "" {
			out = append(out, n)
		}
	}
	return out
}

// nsName 取 `* as ns` 里的 ns，别的原样。
func nsName(s string) string {
	if a, b, ok := strings.Cut(s, " as "); ok && strings.TrimSpace(a) == "*" {
		return strings.TrimSpace(b)
	}
	if s == "*" {
		return ""
	}
	return s
}

// pyModuleSpec 把 Python 的模块名换成路径：`.mod` → `./mod`，`..pkg.mod` → `../pkg/mod`，`pkg.mod` → `pkg/mod`。
func pyModuleSpec(mod string) string {
	dots := 0
	for dots < len(mod) && mod[dots] == '.' {
		dots++
	}
	rest := strings.ReplaceAll(mod[dots:], ".", "/")
	switch dots {
	case 0:
		return rest
	case 1:
		return "./" + rest
	}
	return strings.Repeat("../", dots-1) + rest
}

// dartSpec 把 `package:app/x.dart` 换成 lib 下的路径（不管包名，当前项目的 lib 就在根目录下），别的原样。
func dartSpec(spec string) string {
	if rest, ok := strings.CutPrefix(spec, "package:"); ok {
		if i := strings.IndexByte(rest, '/'); i >= 0 {
			return "/lib/" + rest[i+1:]
		}
		return ""
	}
	if strings.HasPrefix(spec, "dart:") {
		return ""
	}
	return spec
}

// javaFile 在清单里找 Java、Kotlin 的类文件：路径以 a/b/C.java（或 .kt、.scala）结尾。
func javaFile(l *List, segs []string) string {
	if len(segs) == 0 {
		return ""
	}
	suffix := "/" + strings.Join(segs, "/")
	for _, rel := range l.Files {
		slash := filepath.ToSlash(rel)
		ext := path.Ext(slash)
		if (ext == ".java" || ext == ".kt" || ext == ".scala") && strings.HasSuffix(strings.TrimSuffix(slash, ext), suffix) {
			return slash
		}
	}
	return ""
}
