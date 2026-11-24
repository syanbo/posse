package main

import (
	"slices"
	"strings"
	"testing"
)

func TestProductionTagsRespectsGOFLAGSBoundaries(t *testing.T) {
	t.Setenv("MYGO_INSPECTOR", "")

	// 引号包裹的 ldflags 值里包含 -tags=ts_omit_netstack，绝不能被当成真正的构建标签
	t.Setenv("GOFLAGS", `'-ldflags=-X "main.sample=two -tags=ts_omit_netstack spaces" -w' -tags=ts_omit_logtail,foo`)
	args := productionTags(false)
	if len(args) != 2 || args[0] != "-tags" {
		t.Fatalf("unexpected productionTags: %v", args)
	}
	got := strings.Split(args[1], ",")
	if slices.Contains(got, "ts_omit_netstack") {
		t.Fatalf("productionTags misparsed -tags= inside quoted -ldflags: %v", got)
	}
	for _, want := range []string{"ts_omit_logtail", "foo", inspectorTag} {
		if !slices.Contains(got, want) {
			t.Fatalf("productionTags missing %q: %v", want, got)
		}
	}

	// 旧式单引号标签、带逗号的空格标签与含单引号/空格标签都保留真实边界，不把 bar,baz 拆成额外标签
	t.Setenv("GOFLAGS", `-tags='legacy' '-tags=prod foo bar,baz'`)
	args2 := productionTags(false)
	if len(args2) != 2 || args2[0] != "-tags" {
		t.Fatalf("unexpected productionTags for legacy quotes: %v", args2)
	}
	got2 := splitGOFLAGS(args2[1])
	want2 := []string{"legacy", "prod", "foo", "bar,baz", inspectorTag}
	if !slices.Equal(got2, want2) {
		t.Fatalf("productionTags legacy round-trip = %v, want %v (raw=%q)", got2, want2, args2[1])
	}

	// 含单引号和空格的异常标签经 quoted.Join 保留边界，不吞掉后续 mygo_noinspector / ts_omit_logtail
	t.Setenv("GOFLAGS", `-tags=foo'bar "-tags='hello world'" -tags=ts_omit_logtail`)
	args3 := productionTags(false)
	if len(args3) != 2 || args3[0] != "-tags" {
		t.Fatalf("unexpected productionTags for quote/space tags: %v", args3)
	}
	got3 := splitGOFLAGS(args3[1])
	want3 := []string{"foo'bar", "hello world", "ts_omit_logtail", inspectorTag}
	if !slices.Equal(got3, want3) {
		t.Fatalf("productionTags quote/space round-trip = %v, want %v (raw=%q)", got3, want3, args3[1])
	}
}
