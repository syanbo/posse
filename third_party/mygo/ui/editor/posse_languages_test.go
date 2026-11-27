package editor

import "testing"

func TestArkTSLanguage(t *testing.T) {
	for _, file := range []string{"entry/src/main/ets/pages/Index.ets", "types.d.ets", "APP.ETS"} {
		lang := LanguageFor(file)
		if lang == nil || lang.Name != "ArkTS" {
			t.Fatalf("LanguageFor(%q) = %v, want ArkTS", file, lang)
		}
	}
	for _, alias := range []string{"ets", "arkts", "ArkTS"} {
		lang := LanguageNamed(alias)
		if lang == nil || lang.Name != "ArkTS" {
			t.Fatalf("LanguageNamed(%q) = %v, want ArkTS", alias, lang)
		}
	}
	// TypeScript 不应把 struct 当成关键字，ArkTS 应该高亮 struct 与 @ 装饰器。
	ts := LanguageFor("demo.ts")
	ets := LanguageFor("Index.ets")
	lines := []string{
		"@Entry",
		"@Component",
		"struct Index {",
		"  @State msg: string = 'hi'",
		"}",
	}
	var state uint64
	var tokens [][]Token
	for _, line := range lines {
		var lineToks []Token
		lineToks, state = ets.Tokenizer.Tokenize(line, state, nil)
		tokens = append(tokens, lineToks)
	}
	hasKind := func(lineIdx int, text string, want TokenKind) bool {
		line := lines[lineIdx]
		start := 0
		for _, tok := range tokens[lineIdx] {
			if tok.Kind == want && line[start:tok.End] == text {
				return true
			}
			start = tok.End
		}
		return false
	}
	if !hasKind(0, "@Entry", TokenMeta) {
		t.Fatalf("@Entry not highlighted as meta: %+v", tokens[0])
	}
	if !hasKind(2, "struct", TokenKeyword) {
		t.Fatalf("struct not highlighted as keyword in ArkTS: %+v", tokens[2])
	}
	if !hasKind(2, "Index", TokenType) {
		t.Fatalf("Index not highlighted as type: %+v", tokens[2])
	}
	if !hasKind(3, "@State", TokenMeta) || !hasKind(3, "string", TokenType) || !hasKind(3, "'hi'", TokenString) {
		t.Fatalf("line 3 tokens = %+v", tokens[3])
	}
	tsLine := "const struct = 1"
	tsToks, _ := ts.Tokenizer.Tokenize(tsLine, 0, nil)
	start := 0
	for _, tok := range tsToks {
		if tok.Kind == TokenKeyword && tsLine[start:tok.End] == "struct" {
			t.Fatalf("TypeScript unexpectedly highlighted struct as keyword: %+v", tsToks)
		}
		start = tok.End
	}
}
