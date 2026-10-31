package agent

import (
	"strings"
	"testing"
)

func TestLaunchModel(t *testing.T) {
	l := Launcher{Exe: "/bin/posse", Home: t.TempDir()}
	cases := []struct {
		agent string
		m     Model
		want  string
	}{
		{"claude", Model{Name: "opus", Effort: "high"}, "--model 'opus' --effort 'high'"},
		{"codex", Model{Name: "gpt-5.5", Effort: "xhigh"}, `--model 'gpt-5.5' -c 'model_reasoning_effort="xhigh"'`},
		{"grok", Model{Effort: "low"}, "--reasoning-effort 'low'"},
		{"cursor", Model{Name: "claude-opus-4-8", Effort: "high"}, "--model 'claude-opus-4-8[effort=high]'"},
	}
	for _, c := range cases {
		r, err := l.Launch(c.agent, c.m, "do it")
		if err != nil {
			t.Fatal(err)
		}
		cmd := strings.Join(r.Argv, " ")
		if !strings.Contains(cmd, c.want) {
			t.Errorf("%s: %s lacks %s", c.agent, cmd, c.want)
		}
	}
	// 不指定时不加参数，沿用 agent 自己的默认设置
	r, _ := l.Launch("claude", Model{}, "")
	if cmd := strings.Join(r.Argv, " "); strings.Contains(cmd, "--model") || strings.Contains(cmd, "--effort") {
		t.Errorf("default launch has model args: %s", cmd)
	}
	for _, m := range []Model{{Effort: "high"}, {Name: "x[context=1m]", Effort: "high"}} {
		if _, err := l.Launch("cursor", m, ""); err == nil {
			t.Errorf("cursor %+v: want error", m)
		}
	}
}

// 派出去的 Cursor worker 不弹工作区信任框；用户自己开的照 Cursor 的默认，第一次进目录时问。
func TestCursorWorkerTrust(t *testing.T) {
	l := Launcher{Exe: "/bin/posse", Home: t.TempDir()}
	r, err := l.Launch("cursor", Model{Worker: true}, "do it")
	if err != nil {
		t.Fatal(err)
	}
	if cmd := strings.Join(r.Argv, " "); !strings.Contains(cmd, "--trust") {
		t.Errorf("worker 应带 --trust：%s", cmd)
	}
	r, _ = l.Launch("cursor", Model{}, "")
	if cmd := strings.Join(r.Argv, " "); strings.Contains(cmd, "--trust") {
		t.Errorf("用户自己开的不该带 --trust：%s", cmd)
	}
	r, _ = l.Launch("claude", Model{Worker: true}, "")
	if cmd := strings.Join(r.Argv, " "); strings.Contains(cmd, "--trust") {
		t.Errorf("只有 Cursor 加：%s", cmd)
	}
}
