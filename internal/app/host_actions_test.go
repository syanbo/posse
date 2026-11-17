package app

import (
	"context"
	"encoding/json"
	"reflect"
	"sync/atomic"
	"testing"
	"time"

	"posse/internal/history"
	"posse/internal/rpc"
)

func TestHostActionsViewer(t *testing.T) {
	a, f := requestTestApp(t, "remote")
	var calls atomic.Int32
	f.mu.Lock()
	f.desktopCall = func(context.Context, rpc.DesktopCall) (any, error) { calls.Add(1); return nil, nil }
	f.mu.Unlock()
	term := terminalInfo{ID: "same-t", PhoneSized: true, Running: true}
	a.workspaces = []workspace{{ID: "same-w", Terminals: []terminalInfo{term}}}
	a.startRename(appL(), term)
	a.renameTerminal(term.ID, "new name")
	a.reclaimSize(appL(), term.ID)
	a.resumeSession(appL(), history.Session{ID: "same-session"})
	a.runPending()
	if calls.Load() != 0 || a.renaming != "" || len(a.state.TermNames) != 0 || a.renameQ != nil || !a.workspaces[0].Terminals[0].PhoneSized {
		t.Fatal("viewer changed terminal state or issued a write")
	}
}

func TestHostActionsRenameOldInputAndQueue(t *testing.T) {
	a, f := requestTestApp(t, "remote")
	takeRequestTestControl(t, a)
	var calls atomic.Int32
	f.mu.Lock()
	f.desktopCall = func(context.Context, rpc.DesktopCall) (any, error) { calls.Add(1); return nil, nil }
	f.mu.Unlock()
	term := terminalInfo{ID: "same-t", Title: "shell", Running: true}
	a.startRename(appL(), term)
	a.renameText = "old input"
	// 固定输入开始时的身份；完成输入时的控制权不能替它授权。
	takeRequestTestControl(t, a)
	a.finishRename(appL(), term)
	if a.renameQ != nil || calls.Load() != 0 {
		t.Fatal("old input borrowed new control")
	}
	a.renameOnce.Do(func() { a.renameQ = make(chan termNameRequest, 64) })
	a.renameTerminal(term.ID, "first")
	a.renameTerminal(term.ID, "second")
	takeRequestTestControl(t, a)
	close(a.renameQ)
	a.renameWorker(a.renameQ)
	a.runPending()
	if calls.Load() != 0 || len(a.state.TermNames) != 0 {
		t.Fatal("old queue wrote with new identity")
	}
}

func TestHostActionsRenameOrderAndNoReplay(t *testing.T) {
	for _, unknown := range []bool{false, true} {
		t.Run(map[bool]string{false: "ordered", true: "unknown"}[unknown], func(t *testing.T) {
			a, f := requestTestApp(t, "remote")
			takeRequestTestControl(t, a)
			identity := *a.request().client.Desktop
			var names []string
			f.mu.Lock()
			f.desktopCall = func(_ context.Context, p rpc.DesktopCall) (any, error) {
				if p.Method != "terminal.rename" {
					return nil, rpc.Errorf(rpc.CodeUnknown, "test does not serve polling")
				}
				if p.Desktop != identity {
					t.Error("rename used a different identity")
				}
				var body struct {
					Name string `json:"name"`
				}
				if err := json.Unmarshal(p.Params, &body); err != nil {
					return nil, err
				}
				f.mu.Lock()
				names = append(names, body.Name)
				f.mu.Unlock()
				if unknown {
					return nil, rpc.Errorf(rpc.CodeOutcomeUnknown, "desktop operation response was lost; inspect its status before trying again")
				}
				return nil, nil
			}
			f.mu.Unlock()
			a.renameOnce.Do(func() { a.renameQ = make(chan termNameRequest, 64) })
			a.renameTerminal("same-t", "first")
			a.renameTerminal("same-t", "second")
			close(a.renameQ)
			a.renameWorker(a.renameQ)
			a.runPending()
			f.mu.Lock()
			got := append([]string(nil), names...)
			f.mu.Unlock()
			if !reflect.DeepEqual(got, []string{"first", "second"}) {
				t.Fatalf("rename order = %v", got)
			}
			if unknown && a.err == "" {
				t.Fatal("unknown rename outcome was not reported")
			}
			a.state.TermNames = map[string]string{"same-t": "persisted old name"}
			fresh := terminalInfo{ID: "same-t", Name: "target name"}
			a.syncTermName(&fresh)
			if fresh.Name != "target name" {
				t.Fatal("pending local name masked remote name")
			}
			// 队列已关闭；若轮询重发旧名字，这里会 panic。
			a.syncTermName(&fresh)
		})
	}
}

func TestHostActionsReclaimAcknowledgement(t *testing.T) {
	for _, changed := range []bool{false, true} {
		t.Run(map[bool]string{false: "current", true: "changed"}[changed], func(t *testing.T) {
			a, f := requestTestApp(t, "remote")
			takeRequestTestControl(t, a)
			identity := *a.request().client.Desktop
			a.workspaces = []workspace{{ID: "same-w", Terminals: []terminalInfo{{ID: "same-t", PhoneSized: true}}}}
			entered, finish := make(chan struct{}), make(chan struct{})
			defer close(finish)
			f.mu.Lock()
			f.desktopCall = func(_ context.Context, p rpc.DesktopCall) (any, error) {
				if p.Method != "terminal.reclaimSize" || p.Desktop != identity {
					t.Error("reclaim target or identity changed")
				}
				close(entered)
				<-finish
				return nil, nil
			}
			f.mu.Unlock()
			a.reclaimSize(appL(), "same-t")
			waitConnectionEvent(t, entered)
			if !a.workspaces[0].Terminals[0].PhoneSized {
				t.Fatal("size changed before acknowledgement")
			}
			if changed {
				takeRequestTestControl(t, a)
			}
			finish <- struct{}{}
			awaitHostUpdate(t, a)
			if a.workspaces[0].Terminals[0].PhoneSized != changed {
				t.Fatal("acknowledgement changed the wrong identity")
			}
		})
	}
}

func TestHostActionsResumeChainAndLateResult(t *testing.T) {
	a, f := requestTestApp(t, "remote")
	takeRequestTestControl(t, a)
	identity := *a.request().client.Desktop
	var resumes atomic.Int32
	calls := make(chan rpc.DesktopCall, 4)
	finish := make(chan struct{})
	defer close(finish)
	f.mu.Lock()
	f.desktopCall = func(_ context.Context, p rpc.DesktopCall) (any, error) {
		switch p.Method {
		case "history.resume":
			calls <- p
			if resumes.Add(1) == 1 {
				return nil, rpc.Errorf(rpc.CodeNotFound, "no workspace contains this session directory")
			}
			<-finish
			return map[string]string{"id": "same-t", "workspaceId": "same-w"}, nil
		case "project.add":
			calls <- p
			return nil, nil
		default:
			return nil, rpc.Errorf(rpc.CodeUnknown, "test does not serve polling")
		}
	}
	f.mu.Unlock()
	a.resumeSession(appL(), history.Session{ID: "same-session", Cwd: "/target/path"})
	awaitHostUpdate(t, a)
	if a.confirmDo == nil {
		t.Fatal("missing confirmation")
	}
	a.confirmDo()
	for _, method := range []string{"history.resume", "project.add", "history.resume"} {
		select {
		case p := <-calls:
			if p.Method != method || p.Desktop != identity {
				t.Fatalf("resume chain changed target: %+v", p)
			}
			if method == "project.add" {
				var body struct {
					Path string `json:"path"`
				}
				if err := json.Unmarshal(p.Params, &body); err != nil || body.Path != "/target/path" {
					t.Fatal("project directory was not the target session directory")
				}
			}
		case <-time.After(3 * time.Second):
			t.Fatal("resume chain did not complete")
		}
	}
	// 第二次恢复已被接受；随后换身份，迟到结果不能跳转到同名工作区。
	a.host.detach()
	a.selected = "different-page"
	a.runPending()
	finish <- struct{}{}
	awaitHostUpdate(t, a)
	if a.selected != "different-page" {
		t.Fatal("late resume selected a stale workspace")
	}
}

func TestHostActionsResumeConfirmation(t *testing.T) {
	a, f := requestTestApp(t, "remote")
	takeRequestTestControl(t, a)
	var writes atomic.Int32
	f.mu.Lock()
	f.desktopCall = func(_ context.Context, p rpc.DesktopCall) (any, error) {
		writes.Add(1)
		if p.Method != "history.resume" {
			t.Error("old confirmation added a project")
		}
		return nil, rpc.Errorf(rpc.CodeNotFound, "no workspace contains this session directory")
	}
	f.mu.Unlock()
	a.resumeSession(appL(), history.Session{ID: "same-session", Cwd: "/target/path"})
	awaitHostUpdate(t, a)
	if a.confirmDo == nil {
		t.Fatal("missing add-project confirmation")
	}
	takeRequestTestControl(t, a)
	a.confirmDo()
	awaitHostUpdate(t, a)
	if writes.Load() != 1 {
		t.Fatal("old confirmation used new control")
	}
}
