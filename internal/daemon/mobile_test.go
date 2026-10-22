package daemon

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"hive/internal/history"
	"hive/internal/rpc"
)

// 手机端历史转给常驻进程的 history.* 接口：列表、搜索和原来一样；继续一个不存在的会话报 not_found。
func TestMobileHistory(t *testing.T) {
	d, w := newTestDaemon(t)
	roots := t.TempDir()
	d.cfg.HistoryRoots = &history.Roots{Home: roots}
	t.Cleanup(d.closeHistory)
	sid := "55555555-5555-4555-8555-555555555555"
	path := filepath.Join(roots, ".claude", "projects", "-proj", sid+".jsonl")
	os.MkdirAll(filepath.Dir(path), 0o755)
	os.WriteFile(path, []byte(`{"type":"user","cwd":"`+w.Path+`","sessionId":"`+sid+`","timestamp":"2026-10-01T10:00:00Z","message":{"role":"user","content":"手机上看历史"}}
`), 0o644)
	h := mobileHost{d}
	ctx := context.Background()
	d.refreshHistory(ctx, true, 0)
	list, err := h.History(ctx, "history.list", json.RawMessage(`{"limit":50}`))
	if err != nil {
		t.Fatal(err)
	}
	if ss := list.(historyListResult).Sessions; len(ss) != 1 || ss[0].ID != sid || ss[0].Path != path {
		t.Fatalf("list %+v", ss)
	}
	found, err := h.History(ctx, "history.search", json.RawMessage(`{"query":"历史"}`))
	if err != nil || len(found.(historySearchResult).Hits) != 1 {
		t.Fatalf("search %+v %v", found, err)
	}
	_, err = h.History(ctx, "history.resume", json.RawMessage(`{"session":"nope"}`))
	var re *rpc.Error
	if !errors.As(err, &re) || re.Code != rpc.CodeNotFound {
		t.Fatalf("resume unknown %v", err)
	}
}

// 桌面改的标签名记在常驻进程里，手机的标签用它；清空后恢复默认，数据库里也跟着改。
func TestTerminalRenameShowsOnMobile(t *testing.T) {
	d, w := newTestDaemon(t)
	s, err := d.startTerminal(w, []string{"/bin/cat"}, "", 80, 24)
	if err != nil {
		t.Fatal(err)
	}
	title := func() string {
		for _, mw := range (mobileHost{d}).State().Workspaces {
			for _, mt := range mw.Terminals {
				if mt.ID == s.ID {
					return mt.Title
				}
			}
		}
		return "<missing>"
	}
	info := call(t, d.terminalRename, map[string]any{"terminal": s.ID, "name": " 登录页重构 "}).(terminalInfo)
	if info.Name != "登录页重构" || title() != "登录页重构" {
		t.Fatalf("renamed: info %q, mobile %q", info.Name, title())
	}
	if got, _ := d.store.TerminalTitle(s.ID); got != "登录页重构" {
		t.Fatalf("stored %q", got)
	}
	call(t, d.terminalRename, map[string]any{"terminal": s.ID, "name": ""})
	if got := title(); got == "登录页重构" {
		t.Fatalf("cleared name still shown: %q", got)
	}
	if got, _ := d.store.TerminalTitle(s.ID); got != "" {
		t.Fatalf("stored after clear %q", got)
	}
}

// 手机上工作区的分支以 HEAD 为准：数据库里记的是建工作区时的分支，在项目目录里切了分支也要跟着变。
func TestMobileBranchFollowsHead(t *testing.T) {
	d, w := newTestDaemon(t)
	os.MkdirAll(filepath.Join(w.Path, ".git"), 0o755)
	os.WriteFile(filepath.Join(w.Path, ".git", "HEAD"), []byte("ref: refs/heads/develop_710\n"), 0o644)
	for _, mw := range (mobileHost{d}).State().Workspaces {
		if mw.ID == w.ID && mw.Branch != "develop_710" {
			t.Fatalf("branch = %q, want develop_710", mw.Branch)
		}
	}
}

// terminal send 的方向键跟着程序的光标键模式：开着应用模式（DECCKM）时发 ESC O A，ncurses 程序只认这种。
func TestSendKeysFollowCursorMode(t *testing.T) {
	d, w := newTestDaemon(t)
	script := `printf '\033[?1h'; stty -icanon -echo; printf 'ready\n'; head -c 3 | od -An -c`
	info := call(t, d.terminalCreate, map[string]any{"workspace": w.ID, "command": []string{"/bin/sh", "-c", script + "; sleep 5"}}).(terminalInfo)
	s, err := d.session(info.ID)
	if err != nil {
		t.Fatal(err)
	}
	waitScreen(t, s, "ready")
	call(t, d.terminalSend, map[string]any{"terminal": info.ID, "keys": []string{"up"}})
	waitScreen(t, s, "033   O   A")
}
