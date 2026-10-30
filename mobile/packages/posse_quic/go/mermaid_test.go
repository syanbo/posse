package main

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestRenderMermaid(t *testing.T) {
	req, _ := json.Marshal(mermaidRequest{
		Source: "flowchart LR\nA[常驻进程] --> B[终端]",
		Style:  map[string]string{"bg": "#141413", "fg": "#ecebe8", "surface": "#222220", "border": "#5a5853"},
		Key:    "test", Base: 16,
		Advances: map[string]map[string]float32{"r": {"常": 16, "驻": 16, "进": 16, "程": 16, "终": 16, "端": 16}},
	})
	var out mermaidResult
	if err := json.Unmarshal(renderMermaid(req), &out); err != nil {
		t.Fatal(err)
	}
	if out.Err != "" || !strings.HasPrefix(out.SVG, "<svg") || out.W <= 0 || out.H <= 0 {
		t.Fatalf("result %+v", out)
	}
	var texts []string
	for _, l := range out.Labels {
		texts = append(texts, l.Text)
		if len(l.Color) != 9 || l.Size <= 0 {
			t.Errorf("label %+v", l)
		}
	}
	if s := strings.Join(texts, "|"); !strings.Contains(s, "常驻进程") || !strings.Contains(s, "终端") {
		t.Errorf("labels %q", s)
	}
	if strings.Contains(out.SVG, "<text") || strings.Contains(out.SVG, "var(") {
		t.Errorf("svg still has text or css variables")
	}

	if err := json.Unmarshal(renderMermaid([]byte(`{"source":"sequenceDiagram\nA->>B hi"}`)), &out); err != nil || !strings.Contains(out.Err, "line 2") {
		t.Errorf("err %q %v", out.Err, err)
	}
}

// 量到的字按表里的宽度，没量到的按全角、半角估，上下标缩小
func TestMermaidMeasurer(t *testing.T) {
	m := mermaidRequest{Base: 10, Advances: map[string]map[string]float32{"r": {"a": 5}, "b": {"a": 6}, "i": {"a": 4}, "bi": {"a": 7}}}.measurer()
	if w := m.Width("a", 20, 0); w != 10 {
		t.Errorf("regular %v", w)
	}
	if w := m.Width("a", 20, 1); w != 12 {
		t.Errorf("bold %v", w)
	}
	if w := m.Width("a", 10, 2); w != 4 {
		t.Errorf("italic %v", w)
	}
	if w := m.Width("a", 10, 3); w != 7 {
		t.Errorf("bold italic %v", w)
	}
	if w := m.Width("中x", 10, 0); w != 16 {
		t.Errorf("fallback %v", w)
	}
	if w := m.Width("a", 20, 8); w != 8 {
		t.Errorf("sub %v", w)
	}
}
