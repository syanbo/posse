package app

import (
	"slices"
	"testing"
	"time"
)

// 跨工作区连线的 Handle 按行复用：多画几帧数量不变，工作区没了跟着删掉。
func TestLinkHandlesReused(t *testing.T) {
	a := previewApp(t)
	a.workspaces = slices.Insert(a.workspaces, 1, workspace{ID: "w10", ProjectID: "p1", Name: "desk-password", Kind: "worktree", Project: "Apps",
		Terminals: []terminalInfo{{ID: "t12", Agent: "claude", Running: true, AgentState: state("working")}}})
	a.tasks = append(a.tasks, taskInfo{ID: "k8", Agent: "claude", WorkspaceID: "w10", Worker: "t12", Status: "running", Coordinator: "t1", CreatedAt: time.Now()})
	a.indexWorkspaces()
	a.indexTasks()
	tt := newTester(a.build, 1200, 760)
	tt.Frame()
	n := len(a.linkHandles)
	for _, k := range []string{"t1", "t12", "list:p1"} {
		if a.linkHandles[k] == nil {
			t.Fatalf("no handle for %s: %v", k, a.linkHandles)
		}
	}
	for range 5 {
		tt.Frame()
	}
	if len(a.linkHandles) != n {
		t.Fatalf("handles grew from %d to %d", n, len(a.linkHandles))
	}
	a.workspaces = slices.Delete(a.workspaces, 1, 2)
	a.indexWorkspaces()
	if a.linkHandles["t12"] != nil {
		t.Fatal("handle of a gone terminal kept")
	}
}
