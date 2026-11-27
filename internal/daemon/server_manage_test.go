package daemon

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strings"
	"testing"

	"posse/internal/host"
	"posse/internal/procmem"
	"posse/internal/rpc"
	"posse/internal/spawn"
)

type fakeMemTerm struct {
	host.Terminal
	pid   int
	title string
}

func (f fakeMemTerm) Running() bool          { return true }
func (f fakeMemTerm) Pid() int               { return f.pid }
func (f fakeMemTerm) Title() string          { return f.title }
func (f fakeMemTerm) Agent() host.AgentState { return host.AgentState{} }

func TestDaemonMemoryReadOnlyNoArgvLeakAndStableTotal(t *testing.T) {
	d := &Daemon{
		instanceID: "one",
		terms: map[string]*session{
			"t_mem_1": {
				Terminal:    fakeMemTerm{pid: os.Getpid(), title: "shell"},
				ID:          "t_mem_1",
				WorkspaceID: "ws_1",
				name:        "main-shell",
			},
		},
	}
	t.Cleanup(d.desktop.shutdown)
	srv := rpc.NewServer()
	srv.Handle("daemon.memory", d.daemonMemory)

	st := desktopTestCall(t, d.desktopStatus, nil)
	viewer := rpc.DesktopIdentity{ClientID: "viewer", InstanceID: st.InstanceID, Epoch: st.Epoch}

	// 验证 SafeExeName / SafeName 只取内核可执行文件短名，不包含任何 argv 参数或内联脚本。
	if got := procmem.SafeExeName("/usr/local/bin/node"); got != "node" {
		t.Fatalf("SafeExeName = %q, want node", got)
	}
	vPlain, err := desktopRequestCall(d, srv, context.Background(), rpc.DesktopCall{
		Desktop: viewer,
		Method:  "daemon.memory",
	})
	if err != nil {
		t.Fatalf("daemon.memory (plain) failed: %v", err)
	}
	resPlain := vPlain.(rpc.DaemonMemoryResult)

	// 带垃圾 ID 与真实展开 ID 的请求：垃圾 ID 被过滤，只展开真实终端，且不改变响应结构与短名规则。
	params, _ := json.Marshal(rpc.DaemonMemoryParams{Expanded: []string{"t_mem_1", "nonexistent-garbage-id", ""}})
	vExp, err := desktopRequestCall(d, srv, context.Background(), rpc.DesktopCall{
		Desktop: viewer,
		Method:  "daemon.memory",
		Params:  params,
	})
	if err != nil {
		t.Fatalf("daemon.memory (expanded) failed: %v", err)
	}
	resExp := vExp.(rpc.DaemonMemoryResult)
	if len(resExp.Workspaces) != 1 || len(resExp.Workspaces[0].Terminals) != 1 {
		t.Fatalf("unexpected memory workspaces: %+v", resExp)
	}
	got := resExp.Workspaces[0].Terminals[0]
	if got.ID != "t_mem_1" || len(got.Procs) == 0 {
		t.Fatalf("expected expanded procs for t_mem_1, got %+v", got)
	}
	for _, p := range got.Procs {
		if len(p.Name) > daemonMemoryMaxNameBytes || strings.Contains(p.Name, "-test.") || strings.Contains(p.Name, " ") {
			t.Fatalf("proc name leaked argv or exceeded cap: %q", p.Name)
		}
	}
	if len(resPlain.Workspaces) != 1 {
		t.Fatalf("plain workspaces = %+v", resPlain)
	}
}

func TestDaemonMemoryBudgetUTF8AndWireCap(t *testing.T) {
	running := map[string]bool{"t_1": true}
	raw := []string{"garbage-1", "garbage-2", "t_1", "t_1"}
	ids, set, key := normalizeExpandedIDs(raw, running)
	if len(ids) != 1 || ids[0] != "t_1" || !set["t_1"] || key != "t_1" {
		t.Fatalf("running filter failed: ids=%v key=%q", ids, key)
	}
	budget := 0
	if _, trunc := procmem.TreeFootprintBounded(context.Background(), os.Getpid(), &budget); !trunc {
		t.Fatal("expected zero budget to report truncated=true")
	}
	if got := procmem.TruncateName("hello", 0); got != "" {
		t.Fatalf("TruncateName(0) = %q, want empty", got)
	}
	if got := procmem.TruncateName("ok\xff\xfe界", 6); strings.Contains(got, "\xff") || len(got) > 6 {
		t.Fatalf("TruncateName kept invalid UTF-8 or exceeded cap: %q", got)
	}
	if got := procmem.TruncateName("一二三四五六七八九十", 10); len(got) > 10 {
		t.Fatalf("TruncateName exceeded byte cap: %q (%d bytes)", got, len(got))
	}

	// 构造超大单工作区响应，验证整行 RPC 帧严格不超过 64 KiB。
	big := rpc.DaemonMemoryResult{
		TotalKB: 999,
		Workspaces: []rpc.DaemonMemoryWorkspace{{
			ID: "w1", Label: "huge", KB: 999,
			Terminals: make([]rpc.DaemonMemoryTerminal, 400),
		}},
	}
	for i := range big.Workspaces[0].Terminals {
		big.Workspaces[0].Terminals[i] = rpc.DaemonMemoryTerminal{
			ID:    fmt.Sprintf("t_%04d", i),
			Title: strings.Repeat("a", 64),
			KB:    int64(i),
			Procs: []rpc.DaemonMemoryProc{{Name: strings.Repeat("p", 64), KB: 1, Count: 1}},
		}
	}
	enforceDaemonMemorySizeBudget(&big, nil)
	if !big.Truncated || daemonMemoryWireSize(&big) > daemonMemoryMaxWireBytes {
		t.Fatalf("wire size budget exceeded: truncated=%v wire=%d", big.Truncated, daemonMemoryWireSize(&big))
	}
}

func TestQueryLaunchAgentDomainAndDisabledStates(t *testing.T) {
	home := t.TempDir()
	agentsDir := t.TempDir()
	plist := agentsDir + "/" + spawn.LaunchAgentLabel(home, t.TempDir()) + ".plist"
	if err := os.WriteFile(plist, []byte("<plist/>"), 0o644); err != nil {
		t.Fatal(err)
	}

	// 1. 活跃 Aqua 域但服务未加载：不得误报 aqua_inactive。
	st1, err := spawn.QueryLaunchAgent(context.Background(), spawn.LaunchAgentOptions{
		Home: home, UserHome: t.TempDir(), AgentsDir: agentsDir, Domain: "gui/501",
		RunLaunchctl: func(_ context.Context, args ...string) ([]byte, error) {
			if args[0] == "print" {
				return []byte("Could not find service \"com.dengbo.posse.server\" in domain for user gui: 501"), errors.New("exit status 113")
			}
			return []byte("disabled services = {\n}\n"), nil
		},
	})
	if err != nil || st1.Loaded || st1.Disabled || st1.NoteCode != "" {
		t.Fatalf("active domain missing service: st=%+v err=%v", st1, err)
	}

	// 2. 活跃 Aqua 域且 label 被显式禁用：Disabled=true。
	label := st1.Label
	st2, err := spawn.QueryLaunchAgent(context.Background(), spawn.LaunchAgentOptions{
		Home: home, UserHome: t.TempDir(), AgentsDir: agentsDir, Domain: "gui/501",
		RunLaunchctl: func(_ context.Context, args ...string) ([]byte, error) {
			if args[0] == "print" {
				return []byte("Could not find service"), errors.New("exit status 113")
			}
			return []byte(fmt.Sprintf("disabled services = {\n\t\"%s\" => true\n}\n", label)), nil
		},
	})
	if err != nil || !st2.Disabled || st2.NoteCode != "" {
		t.Fatalf("disabled override not detected: st=%+v err=%v", st2, err)
	}

	// 3. 真正无活跃 Aqua 域（print-disabled 也报 domain not found）：NoteCode=aqua_inactive。
	userHome := t.TempDir()
	realPlist := agentsDir + "/" + spawn.LaunchAgentLabel(home, userHome) + ".plist"
	if err := os.WriteFile(realPlist, []byte("<plist/>"), 0o644); err != nil {
		t.Fatal(err)
	}
	st3, err := spawn.QueryLaunchAgent(context.Background(), spawn.LaunchAgentOptions{
		Home: home, UserHome: userHome, AgentsDir: agentsDir, Domain: "gui/501",
		RunLaunchctl: func(_ context.Context, args ...string) ([]byte, error) {
			return []byte("Could not find domain for user gui: 501"), errors.New("exit status 113")
		},
	})
	if err != nil || !st3.Installed || st3.Loaded || st3.NoteCode != rpc.AutostartNoteAquaInactive {
		t.Fatalf("inactive Aqua domain not detected: st=%+v err=%v", st3, err)
	}
}

func TestServerAutostartStatusViaDesktopViewer(t *testing.T) {
	want := rpc.AutostartStatus{
		Supported:          true,
		Kind:               "systemd",
		Managed:            true,
		Installed:          true,
		Enabled:            true,
		Active:             true,
		Unit:               "posse-server.service",
		Path:               "/home/dev/.config/systemd/user/posse-server.service",
		TimerPath:          "/home/dev/.config/systemd/user/posse-server.timer",
		ExpectedExecutable: "/home/dev/.posse/server/current/posse",
		Linger:             "no",
		NoteCode:           rpc.AutostartNoteSystemdLingerDisabled,
	}
	d := &Daemon{
		instanceID: "one",
		autostart:  func(context.Context) (rpc.AutostartStatus, error) { return want, nil },
	}
	t.Cleanup(d.desktop.shutdown)
	srv := rpc.NewServer()
	srv.Handle("server.autostart.status", d.serverAutostartStatus)

	st := desktopTestCall(t, d.desktopStatus, nil)
	viewer := rpc.DesktopIdentity{ClientID: "viewer", InstanceID: st.InstanceID, Epoch: st.Epoch}
	v, err := desktopRequestCall(d, srv, context.Background(), rpc.DesktopCall{
		Desktop: viewer,
		Method:  "server.autostart.status",
	})
	if err != nil {
		t.Fatalf("server.autostart.status failed: %v", err)
	}
	got := v.(rpc.AutostartStatus)
	if got != want {
		t.Fatalf("autostart status = %+v, want %+v", got, want)
	}
}
