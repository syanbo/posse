package files

import (
	"context"
	"testing"
)

// 列文件失败时调用方拿到的清单是 nil：跳到定义、检索、找符号、限定目录都当什么都没有，不崩。
func TestNilListFindsNothing(t *testing.T) {
	ctx := context.Background()
	if defs, err := FindDefinitions(ctx, nil, nil, DefinitionQuery{Word: "open"}); defs != nil || err != nil {
		t.Fatalf("FindDefinitions: %v %v", defs, err)
	}
	called := false
	if trunc, err := Search(ctx, nil, Query{Text: "open"}, nil, func([]Hit) { called = true }); trunc || err != nil || called {
		t.Fatalf("Search: %v %v %v", trunc, err, called)
	}
	if syms, trunc, err := FindSymbols(ctx, nil, nil, SymbolQuery{Text: "open"}); syms != nil || trunc || err != nil {
		t.Fatalf("FindSymbols: %v %v %v", syms, trunc, err)
	}
	if l, err := ScopedList(ctx, nil, "sub"); l != nil || err != nil {
		t.Fatalf("ScopedList: %v %v", l, err)
	}
}
