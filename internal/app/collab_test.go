package app

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/egoist/mygo/ui"

	"posse/internal/collaboration"
	"posse/internal/rpc"
)

func collabTester(t *testing.T, host bool) (*app, *fakeDaemon, func() *ui.Tester) {
	t.Helper()
	ok := func(ctx context.Context, raw json.RawMessage) (any, error) { return map[string]any{}, nil }
	a, tt, f := discTester(t, map[string]rpc.Handler{"discussion.decide": ok, "discussion.join": ok, "discussion.person.remove": ok,
		"discussion.member.resume": ok, "discussion.share": ok, "discussion.leave": ok})
	a.disc.d.members[1].Name = "Codex 审查"
	previewCollab(a, host)
	a.disc.quote = ""
	return a, f, func() *ui.Tester {
		for range 3 {
			tt.Frame()
		}
		return tt
	}
}

// 房主在右栏批准加入申请。
func TestCollabApproveRequest(t *testing.T) {
	a, f, frames := collabTester(t, true)
	a.disc.panel = true
	tt := frames()
	if err := tt.Click("批准"); err != nil {
		t.Fatalf("approve: %v; texts %v", err, tt.Texts())
	}
	p := waitCall(t, tt, f, "discussion.decide")
	if p["request"] != "rq_1" || p["approve"] != true {
		t.Fatalf("decide %+v", p)
	}
}

// 加入者：别人的 Agent 在接收者菜单里点不了；@ 人只是提醒，发出去带 mention。
func TestCollabReceiverMenu(t *testing.T) {
	a, f, frames := collabTester(t, false)
	tt := frames()
	if err := tt.Click("所有人"); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	if err := tt.Click("@Claude 方案 · Hana 的"); err == nil {
		tt.Frame()
	}
	if a.disc.target != "" {
		t.Fatalf("another person's agent became the target: %q", a.disc.target)
	}
	tt.Frame()
	if !strings.Contains(strings.Join(tt.Texts(), "\n"), "提醒 Hana") {
		if err := tt.Click("所有人"); err != nil {
			t.Fatal(err)
		}
		tt.Frame()
	}
	if err := tt.Click("提醒 Hana"); err != nil {
		t.Fatalf("mention item: %v; texts %v", err, tt.Texts())
	}
	tt.Frame()
	if a.disc.mention != strings.Repeat("a1", 32) || a.disc.target != "" {
		t.Fatalf("mention %q target %q", a.disc.mention, a.disc.target)
	}
	a.disc.text = "看一下"
	tt.Frame()
	if err := tt.Click("发送"); err != nil {
		t.Fatal(err)
	}
	p := waitCall(t, tt, f, "discussion.post")
	if p["mention"] != strings.Repeat("a1", 32) || p["target"] != "" {
		t.Fatalf("post %+v", p)
	}
}

// 别人的执行只显示，不给停止、确认、取消；自己的照常。
func TestCollabExecCardsByOwner(t *testing.T) {
	_, _, frames := collabTester(t, true)
	tt := frames()
	texts := "\n" + strings.Join(tt.Texts(), "\n") + "\n"
	if !strings.Contains(texts, "在 Mo 的电脑上") {
		t.Fatalf("owner label missing:\n%s", texts)
	}
	// x2（Mo 的，等回答）不给停止；x5（房主自己的，等确认）给「仍然继续」
	if strings.Contains(texts, "\n停止\n") {
		t.Fatalf("stop offered on another person's execution:\n%s", texts)
	}
	if !strings.Contains(texts, "仍然继续") {
		t.Fatalf("own held execution lost its actions:\n%s", texts)
	}
}

// 粘贴邀请：显示房主和指纹，发出加入申请。
func TestCollabJoinDialog(t *testing.T) {
	a, f, frames := collabTester(t, true)
	inv := collaboration.Invite{V: 1, Host: strings.Repeat("e5", 32), HostName: "Hana", Room: strings.Repeat("e5", 32) + "/d_9", Title: "接口评审",
		Addrs: []string{"192.168.1.8:6791"}, Secret: "s"}
	a.disc.join = joinForm{open: true, text: "收到：" + inv.Encode() + " 十分钟内有效"}
	tt := frames()
	texts := strings.Join(tt.Texts(), "\n")
	if !strings.Contains(texts, "「接口评审」，房主 Hana") || !strings.Contains(texts, "e5e5 e5e5 e5e5 e5e5") {
		t.Fatalf("invite preview:\n%s", texts)
	}
	if err := tt.Click("申请加入"); err != nil {
		t.Fatal(err)
	}
	p := waitCall(t, tt, f, "discussion.join")
	if !strings.Contains(p["invite"].(string), collaboration.InvitePrefix) {
		t.Fatalf("join %+v", p)
	}
}
