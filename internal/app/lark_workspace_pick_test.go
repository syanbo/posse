package app

import (
	"context"
	"encoding/json"
	"errors"
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/egoist/mygo"

	"posse/internal/client"
	"posse/internal/lark"
	"posse/internal/rpc"
)

func TestLarkWorkspacePickHelpers(t *testing.T) {
	base := t.TempDir()
	dirA := filepath.Join(base, "Notes")
	dirB := filepath.Join(base, "Notes ")
	if err := os.MkdirAll(dirA, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(dirB, 0o755); err != nil {
		t.Fatal(err)
	}
	linkA := filepath.Join(base, "NotesLink")
	if err := os.Symlink(dirA, linkA); err != nil {
		t.Fatal(err)
	}

	ws := []workspace{
		{ID: "w_folder", ProjectID: "p1", Name: "extra", Path: dirA, Kind: "folder", Label: "p1/extra"},
		{ID: "w_main", ProjectID: "p1", Name: "main", Path: dirA, Kind: "main", Label: "p1/main"},
	}
	// 同路径优先选 main；软链接也能按真实目录身份命中
	if got, ok := findWorkspaceByDir(ws, linkA); !ok || got.ID != "w_main" {
		t.Fatalf("findWorkspaceByDir(linkA) = (%+v, %v), want w_main", got, ok)
	}
	// 带尾随空格的相邻目录不能误匹配到无空格目录
	if got, ok := findWorkspaceByDir(ws, dirB); ok {
		t.Fatalf("findWorkspaceByDir(dirB) unexpectedly matched %+v", got)
	}

	if got, ok := findWorkspaceForAddedProject(ws, "p1", "/nonexistent"); !ok || got.ID != "w_main" {
		t.Fatalf("findWorkspaceForAddedProject = (%+v, %v), want w_main", got, ok)
	}

	// 错误分类：路径文本里含 "already added" 的 BadRequest 不能误判为已登记
	badReq := rpc.Errorf(rpc.CodeBadRequest, "/tmp/already added is not a directory")
	if isProjectAlreadyAddedErr(badReq) {
		t.Fatal("BadRequest with 'already added' in path must not match isProjectAlreadyAddedErr")
	}
	conflictAdded := rpc.Errorf(rpc.CodeConflict, "project %s already added as %q", dirA, "Notes")
	if !isProjectAlreadyAddedErr(conflictAdded) {
		t.Fatal("expected conflict already-added error to match")
	}
	if !isProjectNameErr(rpc.Errorf(rpc.CodeConflict, "project name %q already used", "docs")) {
		t.Fatal("expected project name conflict to match isProjectNameErr")
	}
	if !isProjectNameErr(rpc.Errorf(rpc.CodeBadRequest, "name %q must not contain spaces", "My Notes")) {
		t.Fatal("expected space name error to match isProjectNameErr")
	}

	if s := sanitizeProjectName("  My Project: v1  "); s != "My-Project-v1" {
		t.Fatalf("sanitizeProjectName = %q, want My-Project-v1", s)
	}
	if s := sanitizeProjectName(".."); s != "folder" {
		t.Fatalf("sanitizeProjectName(..) = %q, want folder", s)
	}

	tried := map[string]bool{"docs": true}
	if n := nextLarkProjectName("/tmp/work/docs", tried); n != "work-docs" {
		t.Fatalf("nextLarkProjectName = %q, want work-docs", n)
	}
	tried["work-docs"] = true
	if n := nextLarkProjectName("/tmp/work/docs", tried); n != "docs-2" {
		t.Fatalf("nextLarkProjectName = %q, want docs-2", n)
	}
}

func TestLarkPickWorkspaceFolderExistingAndNew(t *testing.T) {
	base := t.TempDir()
	existingDir := filepath.Join(base, "Notes")
	trailingSpaceDir := filepath.Join(base, "Notes ")
	conflictDir := filepath.Join(base, "team", "docs")
	for _, d := range []string{existingDir, trailingSpaceDir, conflictDir} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}

	var (
		mu         sync.Mutex
		addCalls   int
		addedPaths []string
		addedNames []string
		projects   = []project{{ID: "p_exist", Name: "Notes", Path: existingDir, Kind: "folder"}}
		workspaces = []workspace{{ID: "w_exist", ProjectID: "p_exist", Name: "main", Path: existingDir, Kind: "main", Project: "Notes", Label: "Notes/main"}}
		takenNames = map[string]bool{"notes": true, "docs": true}
		nextID     = 1
	)

	sockDir, err := os.MkdirTemp("", "posse-lark-*")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(sockDir) })
	sock := filepath.Join(sockDir, "s.sock")
	ln, err := net.Listen("unix", sock)
	if err != nil {
		t.Fatal(err)
	}
	srv := rpc.NewServer()
	srv.Handle("project.list", func(context.Context, json.RawMessage) (any, error) {
		mu.Lock()
		defer mu.Unlock()
		return append([]project(nil), projects...), nil
	})
	srv.Handle("daemon.status", func(context.Context, json.RawMessage) (any, error) {
		return daemonStatus{}, nil
	})
	srv.Handle("workspace.list", func(context.Context, json.RawMessage) (any, error) {
		mu.Lock()
		defer mu.Unlock()
		return append([]workspace(nil), workspaces...), nil
	})
	// 故意让 task.list 失败，验证即使整轮 refresh 失败，自动选中也不受影响
	srv.Handle("task.list", func(context.Context, json.RawMessage) (any, error) {
		return nil, errors.New("task list temporarily unavailable")
	})
	srv.Handle("project.add", func(_ context.Context, raw json.RawMessage) (any, error) {
		var p struct {
			Path string `json:"path"`
			Name string `json:"name"`
		}
		if err := rpc.Decode(raw, &p); err != nil {
			return nil, err
		}
		mu.Lock()
		defer mu.Unlock()
		addCalls++
		name := p.Name
		if name == "" {
			name = filepath.Base(p.Path)
		}
		if strings.Contains(name, " ") {
			return nil, rpc.Errorf(rpc.CodeBadRequest, "name %q must not contain spaces", name)
		}
		if takenNames[strings.ToLower(name)] {
			return nil, rpc.Errorf(rpc.CodeConflict, "project name %q already used", name)
		}
		takenNames[strings.ToLower(name)] = true
		addedPaths = append(addedPaths, p.Path)
		addedNames = append(addedNames, name)
		pid := "p_new_" + name
		wid := "w_new_" + name
		nextID++
		proj := project{ID: pid, Name: name, Path: p.Path, Kind: "folder"}
		ws := workspace{ID: wid, ProjectID: pid, Name: "main", Path: p.Path, Kind: "main", Project: name, Label: name + "/main"}
		projects = append(projects, proj)
		workspaces = append(workspaces, ws)
		return map[string]any{"id": proj.ID, "name": proj.Name, "path": proj.Path, "workspaces": 1}, nil
	})
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(func() {
		cancel()
		_ = ln.Close()
	})
	go srv.Serve(ctx, ln)

	calls, answer := fakeOpenDialog(t)
	a := previewApp(t)
	a.c = &client.Client{Socket: sock}
	a.projects = append([]project(nil), projects...)
	a.workspaces = append([]workspace(nil), workspaces...)
	a.openLarkChatEditor("bot1", lark.ChatOverride{ChatID: "oc_1"}, true)
	tt := newPreviewTester(a, false)

	// 1. 选已有工作区目录：直接选中，不调 project.add
	a.pickLarkWorkspaceFolder(L(nil), larkPickChat)
	until(t, tt, "first dialog shown", func() bool { return calls.Load() == 1 })
	answer <- []string{existingDir}
	until(t, tt, "existing workspace selected", func() bool {
		return !a.dialogOpen && !a.lark.chatWSPicking && a.lark.chatDraft.WorkspaceID == "w_exist"
	})
	mu.Lock()
	gotAddCalls := addCalls
	mu.Unlock()
	if gotAddCalls != 0 {
		t.Fatalf("expected 0 project.add calls for existing workspace, got %d", gotAddCalls)
	}

	// 2. 选带尾随空格的相邻目录 "Notes "：原样传给 project.add，且因 "Notes" 已被占用自动避让重名
	a.pickLarkWorkspaceFolder(L(nil), larkPickChat)
	until(t, tt, "second dialog shown", func() bool { return calls.Load() == 2 })
	answer <- []string{trailingSpaceDir}
	until(t, tt, "trailing-space workspace selected", func() bool {
		return !a.dialogOpen && !a.lark.chatWSPicking && strings.HasPrefix(a.lark.chatDraft.WorkspaceID, "w_new_")
	})
	mu.Lock()
	if len(addedPaths) != 1 || addedPaths[0] != trailingSpaceDir {
		mu.Unlock()
		t.Fatalf("addedPaths = %q, want exact [%q]", addedPaths, trailingSpaceDir)
	}
	mu.Unlock()
	if a.lark.chatWSErr != "" {
		t.Fatalf("unexpected chatWSErr: %q", a.lark.chatWSErr)
	}

	// 3. 选本地快照未知但 daemon 已占用的重名目录 "team/docs"：首次冲突后自动用 "team-docs" 注册成功
	a.pickLarkWorkspaceFolder(L(nil), larkPickChat)
	until(t, tt, "third dialog shown", func() bool { return calls.Load() == 3 })
	answer <- []string{conflictDir}
	until(t, tt, "conflict dir resolved to team-docs", func() bool {
		return !a.dialogOpen && !a.lark.chatWSPicking && a.lark.chatDraft.WorkspaceID == "w_new_team-docs"
	})
}

func TestLarkPickWorkspaceStaleAndDialogError(t *testing.T) {
	newDir := filepath.Join(t.TempDir(), "fresh")
	if err := os.MkdirAll(newDir, 0o755); err != nil {
		t.Fatal(err)
	}

	blockAdd := make(chan struct{})
	var addStarted atomic.Bool

	sockDir, err := os.MkdirTemp("", "posse-lark-stale-*")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(sockDir) })
	sock := filepath.Join(sockDir, "s.sock")
	ln, err := net.Listen("unix", sock)
	if err != nil {
		t.Fatal(err)
	}
	srv := rpc.NewServer()
	srv.Handle("project.add", func(context.Context, json.RawMessage) (any, error) {
		addStarted.Store(true)
		<-blockAdd
		return map[string]any{"id": "p_late", "name": "fresh", "path": newDir, "workspaces": 1}, nil
	})
	srv.Handle("workspace.list", func(context.Context, json.RawMessage) (any, error) {
		return []workspace{{ID: "w_late", ProjectID: "p_late", Name: "main", Path: newDir, Kind: "main", Label: "fresh/main"}}, nil
	})
	srv.Handle("project.list", func(context.Context, json.RawMessage) (any, error) { return []project{}, nil })
	srv.Handle("daemon.status", func(context.Context, json.RawMessage) (any, error) { return daemonStatus{}, nil })
	srv.Handle("task.list", func(context.Context, json.RawMessage) (any, error) { return []taskInfo{}, nil })

	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(func() {
		cancel()
		_ = ln.Close()
	})
	go srv.Serve(ctx, ln)

	calls, answer := fakeOpenDialog(t)
	a := previewApp(t)
	a.c = &client.Client{Socket: sock}
	a.openLarkBotEditor(lark.BotConfig{ID: "b1", Name: "Bot1", WorkspaceID: "w_old"})
	tt := newPreviewTester(a, false)

	// 发起选择，在 project.add 阻塞期间切换到另一个机器人草稿
	a.pickLarkWorkspaceFolder(L(nil), larkPickBot)
	until(t, tt, "dialog shown", func() bool { return calls.Load() == 1 })
	answer <- []string{newDir}
	until(t, tt, "project.add started", func() bool { return addStarted.Load() })

	a.openLarkBotEditor(lark.BotConfig{ID: "b2", Name: "Bot2", WorkspaceID: "w_b2"})
	close(blockAdd)

	// 等待迟到的 workspace.list 落到 a.workspaces，确认没有覆盖 Bot2 的草稿
	until(t, tt, "late workspace appended", func() bool {
		for _, w := range a.workspaces {
			if w.ID == "w_late" {
				return true
			}
		}
		return false
	})
	if a.lark.draft.WorkspaceID != "w_b2" {
		t.Fatalf("late pick overwrote new draft: got %q, want w_b2", a.lark.draft.WorkspaceID)
	}

	// openDialog 报错时写进表单错误并解除 picking 状态
	oldDialog := openDialog
	openDialog = func(mygo.OpenDialogOptions) ([]string, error) {
		return nil, errors.New("permission denied")
	}
	t.Cleanup(func() { openDialog = oldDialog })
	a.pickLarkWorkspaceFolder(L(nil), larkPickBot)
	until(t, tt, "dialog error surfaced in form", func() bool {
		return !a.dialogOpen && !a.lark.botWSPicking && a.lark.botWSErr != ""
	})
}
