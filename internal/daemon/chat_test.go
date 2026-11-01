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
	converted := call(t, d.terminalCreate, map[string]any{"workspace": w.ID, "agent": "grok"}).(terminalInfo)
	// 转为项目时写的路径和终端记的写法不同（/tmp 是 /private/tmp 的符号链接，重启后库里记的是解析过的）
	call(t, d.projectAdd, map[string]any{"path": "/private" + converted.Dir})
	shared := call(t, d.terminalCreate, map[string]any{"workspace": w.ID, "agent": "grok"}).(terminalInfo)
	// 聊天里派出的 worker 在派发方的文件夹里
	task := call(t, d.taskStart, map[string]any{"spec": "x", "command": []string{"/bin/sh", "-c", "exec sleep 30"}, "caller": shared.ID}).(taskInfo)
	worker := d.liveSession(task.Worker)
	if worker == nil || worker.Dir != shared.Dir {
		t.Fatalf("worker = %+v, want it in %s", worker, shared.Dir)
	}
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
	for _, id := range []string{empty.ID, kept.ID, converted.ID, shared.ID} {
		call(t, d.terminalClose, map[string]any{"terminal": id, "graceMs": 200})
	}
	// 转成项目的、还有 worker 在里面的不删
	for _, dir := range []string{converted.Dir, shared.Dir} {
		if _, err := os.Stat(dir); err != nil {
			t.Fatalf("%s removed: %v", dir, err)
		}
	}
	call(t, d.terminalClose, map[string]any{"terminal": worker.ID, "graceMs": 200})
	if _, err := os.Stat(shared.Dir); !os.IsNotExist(err) {
		t.Fatalf("shared folder after its worker closed: %v", err)
	}
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
