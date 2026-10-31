package app

import (
	"slices"
	"testing"
)

func TestRunInsertAt(t *testing.T) {
	ranks := map[string]int{"a": 1, "b": 3, "c": 3}
	rank := func(id string) int { return ranks[id] }
	// a 从待处理降成空闲、还留在最前：新的工作中会话接在 b 后面
	order := []string{"a", "b"}
	order = slices.Insert(order, runInsertAt(order, 3, rank), "c")
	if want := []string{"a", "b", "c"}; !slices.Equal(order, want) {
		t.Fatalf("got %v, want %v", order, want)
	}
	// 比谁都高时排最前
	if at := runInsertAt(order, 5, rank); at != 0 {
		t.Fatalf("got %d, want 0", at)
	}
}

func TestSessionsInTree(t *testing.T) {
	a := &app{coordOf: map[string]string{"w": "x"}}
	got := a.sessionsInTree([]terminalInfo{{ID: "x"}, {ID: "y"}, {ID: "w"}})
	var ids []string
	for _, s := range got {
		ids = append(ids, s.ID)
	}
	if want := []string{"x", "w", "y"}; !slices.Equal(ids, want) {
		t.Fatalf("got %v, want %v", ids, want)
	}
}
