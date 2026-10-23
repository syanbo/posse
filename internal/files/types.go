package files

import (
	"regexp"
	"strings"
)

// 一跳的类型推断：`picker.openAlbum()` 里 picker 是变量时，在当前文件里往上找它的声明，拿到类型名，
// 再去类型的文件里找方法。只认单行的常见写法，不做作用域分析：取光标上方最近的一处声明。

const (
	typeName = `([\pL_][\pL\pN_.]*)`
	identCh  = `[\pL\pN_$]`
)

// typePatternsFor 返回一类语言里变量 name 的声明写法，取到的分组是类型名。
func typePatternsFor(f family, name string) []*regexp.Regexp {
	n := regexp.QuoteMeta(name)
	mk := func(exprs ...string) []*regexp.Regexp {
		out := make([]*regexp.Regexp, 0, len(exprs))
		for _, e := range exprs {
			out = append(out, regexp.MustCompile(e))
		}
		return out
	}
	// 各语言都有的：`name: Type`、`name = new Type(`、`name = Type(`（构造或工厂）、`name as Type`
	common := []string{
		before + n + `\s*[?!]?\s*:\s*(?:readonly\s+)?` + typeName,
		before + n + `\s*(?::|=)?=\s*(?:await\s+)?new\s+` + typeName + `\s*[(<{]`,
		before + n + `\s*(?::|=)?=\s*(?:await\s+)?` + typeName + `\s*[({]`,
		before + n + `\s+as\s+` + typeName,
	}
	switch f {
	case famGo:
		return mk(append([]string{
			`\bvar\s+` + n + `\s+\*?` + typeName,
			before + n + `\s*,?\s*[\pL\pN_]*\s*:?=\s*&?` + typeName + `\s*\{`,             // x := Foo{、x := &Foo{
			before + n + `\s*(?:,\s*[\pL\pN_]+\s*)?:?=\s*(?:[\pL\pN_]+\.)?New` + typeName, // x := NewFoo(、x, err := pkg.NewFoo(
			`[(,]\s*` + n + `\s+\*?` + typeName,                                           // 参数、字段 x *Foo
		}, common...)...)
	case famJava:
		return mk(append([]string{
			before + typeName + `(?:<[^>]*>)?\s+` + n + `\s*[=;,)]`, // Foo x = 、Foo x;
			before + n + `\s*:\s*` + typeName,                       // Kotlin、Swift、Dart 的 x: Foo
			before + n + `\s*=\s*` + typeName + `\.(?:getInstance|instance|shared|create|of|from|new)\b`,
		}, common...)...)
	case famPy:
		return mk(append([]string{
			before + n + `\s*:\s*` + typeName,
			`\bisinstance\(\s*` + n + `\s*,\s*` + typeName,
		}, common...)...)
	case famC:
		return mk(append([]string{
			before + typeName + `(?:<[^>]*>)?\s*[\*&]*\s*` + n + `\s*[=;,)(\[]`, // Foo *x = 、Foo x;
		}, common...)...)
	}
	return mk(common...)
}

// TypeOf 在 text（当前文件）里从 line（从 0 起）往上找变量 name 的类型名，`new Foo(`、`x: Foo`、`Foo x =` 这类写法；
// 找不到为空。Go 的 `NewFoo(` 按惯例当 Foo。
func TypeOf(from, text, name string, line int) string {
	if name == "" || !startsLower(name) && familyOf(from) != famGo {
		// 大写开头的多半本来就是类型或类，不用推断
		return ""
	}
	ps := typePatternsFor(familyOf(from), name)
	lines := strings.Split(text, "\n")
	line = min(line, len(lines)-1)
	for i := line; i >= 0; i-- {
		ln := lines[i]
		if !strings.Contains(ln, name) || inComment(ln, len(ln)) && strings.HasPrefix(strings.TrimSpace(ln), "//") {
			continue
		}
		for _, re := range ps {
			m := re.FindStringSubmatch(ln)
			if m == nil {
				continue
			}
			t := typeSegment(m[1])
			if t == "" || t == name || isTypeKeyword(t) {
				continue
			}
			return t
		}
	}
	return ""
}

// typeSegment 从 `pkg.Foo`、`MediaPicker.create` 这样的写法里取类型名：最后一个大写开头的段，没有就是最后一段。
func typeSegment(s string) string {
	segs := strings.Split(s, ".")
	for i := len(segs) - 1; i >= 0; i-- {
		if seg := segs[i]; seg != "" && !startsLower(seg) {
			return seg
		}
	}
	return segs[len(segs)-1]
}

func startsLower(s string) bool {
	return s != "" && (s[0] >= 'a' && s[0] <= 'z' || s[0] == '_' || s[0] == '$')
}

// isTypeKeyword 过滤掉当成类型名抓到的关键字和基本类型。
func isTypeKeyword(t string) bool {
	switch strings.ToLower(t) {
	case "new", "await", "return", "const", "let", "var", "val", "final", "static", "public", "private", "protected",
		"string", "number", "boolean", "any", "void", "int", "float", "double", "bool", "byte", "char", "long", "short",
		"null", "undefined", "true", "false", "object", "function", "async", "if", "else", "for", "while", "switch", "case":
		return true
	}
	return false
}
