package daemon

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"posse/internal/agent"
)

// 聊天工作区里每开一个 agent 都在新的子文件夹里启动；关掉终端时没留下文件的文件夹删掉，留了的保留。
// 别的工作区照旧在工作区目录启动。
func TestChatFolders(t *testing.T) {
	defer func(v time.Duration) { resumeSettle = v }(resumeSettle)
	resumeSettle = 50 * time.Millisecond
	bin := t.TempDir()
	// 假的 grok：OUT 不为空时留下一个文件；agent 退出后终端回到 shell，等标签关掉
	os.WriteFile(filepath.Join(bin, "grok"), []byte("#!/bin/sh\n[ -n \"$OUT\" ] && echo hi > out.md\nexit 0\n"), 0o755)
	t.Setenv("SHELL", "/bin/sh")
	t.Setenv("PATH", bin+":/usr/bin:/bin")
	d, other := newTestDaemon(t)
	d.launcher = agent.Launcher{Exe: "/bin/true", Home: d.cfg.Home}
	chat := filepath.Join(d.cfg.Home, "chat")
	if err := agent.SetChatDir(d.cfg.Home, chat); err != nil {
		t.Fatal(err)
	}
	os.Mkdir(chat, 0o755)
	res := call(t, d.projectAdd, map[string]any{"path": chat}).(projectInfo)
	ws, err := d.store.Workspaces(res.ID)
	if err != nil || len(ws) != 1 {
		t.Fatalf("chat workspaces = %v, %v", ws, err)
	}
	w := ws[0]

	t.Setenv("OUT", "")
	empty := call(t, d.terminalCreate, map[string]any{"workspace": w.ID, "agent": "grok"}).(terminalInfo)
	t.Setenv("OUT", "1")
	kept := call(t, d.terminalCreate, map[string]any{"workspace": w.ID, "agent": "grok"}).(terminalInfo)
	plain := call(t, d.terminalCreate, map[string]any{"workspace": other.ID, "agent": "grok"}).(terminalInfo)
	for _, ti := range []terminalInfo{empty, kept} {
		if !agent.IsChatFolder(chat, ti.Dir) {
			t.Fatalf("chat agent started in %q, want a folder under %s", ti.Dir, chat)
		}
	}
	if empty.Dir == kept.Dir {
		t.Fatalf("two chats share %s", empty.Dir)
	}
	if plain.Dir != other.Path {
		t.Fatalf("agent in another workspace started in %q, want %q", plain.Dir, other.Path)
	}
	// 普通终端在聊天目录本身
	if sh := call(t, d.terminalCreate, map[string]any{"workspace": w.ID}).(terminalInfo); sh.Dir != chat {
		t.Fatalf("shell started in %q, want %q", sh.Dir, chat)
	}

	deadline := time.Now().Add(5 * time.Second)
	for _, err := os.Stat(filepath.Join(kept.Dir, "out.md")); err != nil; _, err = os.Stat(filepath.Join(kept.Dir, "out.md")) {
		if time.Now().After(deadline) {
			t.Fatal("fake agent never wrote its file")
		}
		time.Sleep(20 * time.Millisecond)
	}
	call(t, d.terminalClose, map[string]any{"terminal": empty.ID, "graceMs": 200})
	call(t, d.terminalClose, map[string]any{"terminal": kept.ID, "graceMs": 200})
	deadline = time.Now().Add(5 * time.Second)
	for {
		_, errEmpty := os.Stat(empty.Dir)
		_, errOut := os.Stat(filepath.Join(kept.Dir, "out.md"))
		if os.IsNotExist(errEmpty) && errOut == nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("after exit: empty folder err=%v, kept file err=%v", errEmpty, errOut)
		}
		time.Sleep(50 * time.Millisecond)
	}
	time.Sleep(300 * time.Millisecond)
	if _, err := os.Stat(kept.Dir); err != nil {
		t.Fatalf("folder with files removed: %v", err)
	}
}
