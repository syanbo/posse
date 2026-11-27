package app

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"sync/atomic"
	"testing"
	"time"

	"github.com/egoist/mygo/ui"
	"posse/internal/client"
	"posse/internal/files"
	"posse/internal/remote"
	"posse/internal/rpc"
	"posse/internal/skills"
	"posse/locales"
)

func TestRemoteStage5CapabilitiesAndHostShortcut(t *testing.T) {
	k, a, b, _, _, _ := connectionFixture(t)
	var seenRequired []string
	oldDial := k.dial
	k.dial = func(ctx context.Context, h remote.Host, opts remote.Options) (hostTransport, error) {
		seenRequired = slices.Clone(opts.Required)
		return oldDial(ctx, h, opts)
	}
	a.selectRemoteHost(remoteTestHost("remote"))
	awaitHostUpdate(t, b)
	if !slices.Equal(seenRequired, rpc.RemoteCapabilities) {
		t.Fatalf("connectRemote required = %v, want %v", seenRequired, rpc.RemoteCapabilities)
	}

	tt := newTester(func(c *ui.Context) { a.displayed().build(c) }, 1200, 760)
	tt.Frame()
	tt.Key(ui.Ctrl|ui.Alt, ui.KeyH)
	tt.Frame()
	if !tt.HasText(locales.In("zh-Hans").HostRelease()) {
		t.Fatalf("switchHost shortcut did not open host menu: %v", tt.Texts())
	}
}

func TestRemoteStage5SidebarDropAndWorkspaceRemove(t *testing.T) {
	a, f := requestTestApp(t, "remote")
	takeRequestTestControl(t, a)
	localDir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(localDir, ".git"), 0o755); err != nil {
		t.Fatal(err)
	}
	a.projects = []project{{ID: "p1", Name: "proj", Path: "/remote/proj", Kind: "git"}}
	a.workspaces = []workspace{
		{ID: "w_main", ProjectID: "p1", Project: "proj", Name: "main", Path: "/remote/proj", Kind: "main"},
		{ID: "w_wt", ProjectID: "p1", Project: "proj", Name: "feat", Path: localDir, Kind: "worktree", Terminals: []terminalInfo{{ID: "t1", Running: true}}},
	}
	a.selected = "w_main"
	a.loaded = true
	a.sectionsOpen = map[string]*bool{}
	a.indexWorkspaces()

	var addCalls, rmCalls atomic.Int32
	var dirty bool
	f.mu.Lock()
	f.desktopCall = func(_ context.Context, p rpc.DesktopCall) (any, error) {
		switch p.Method {
		case "project.add":
			addCalls.Add(1)
			return nil, nil
		case "workspace.rm":
			rmCalls.Add(1)
			var q map[string]any
			json.Unmarshal(p.Params, &q)
			if q["closeTerminals"] != true || q["force"] != false {
				t.Errorf("unexpected workspace.rm params: %+v", q)
			}
			if dirty {
				return nil, rpc.Errorf(rpc.CodeConflict, "worktree has uncommitted changes; pass force to discard:\n M a.go")
			}
			return map[string]any{"removed": "w_wt"}, nil
		case "project.list", "workspace.list", "task.list":
			return []any{}, nil
		}
		return map[string]any{}, nil
	}
	f.mu.Unlock()

	tt := newTester(a.build, 1200, 760)
	tt.Frame()
	tt.DropFiles(140, 200, localDir)
	tt.Frame()
	if addCalls.Load() != 0 || a.err != locales.In("zh-Hans").SidebarDropRemoteUnsupported() {
		t.Fatalf("remote project drop: addCalls=%d err=%q", addCalls.Load(), a.err)
	}

	a.err = ""
	dirty = true
	a.askRemoveWorkspace(appL(), a.workspaces[1])
	if !a.confirming {
		t.Fatal("missing workspace remove confirmation")
	}
	a.confirming = false
	a.confirmDo()
	a.runPending()
	if rmCalls.Load() != 1 || a.err != appL().TracesRemoveDirty(displayName(&a.workspaces[1])) {
		t.Fatalf("dirty worktree remove: rmCalls=%d err=%q", rmCalls.Load(), a.err)
	}

	a.err = ""
	dirty = false
	a.askRemoveWorkspace(appL(), a.workspaces[1])
	a.confirming = false
	a.confirmDo()
	a.runPending()
	if rmCalls.Load() != 2 || a.err != "" {
		t.Fatalf("clean worktree remove: rmCalls=%d err=%q", rmCalls.Load(), a.err)
	}
}

func TestRemoteStage5SkillsMemoryLinksAndControls(t *testing.T) {
	a, f := requestTestApp(t, "remote")
	decoyWs := t.TempDir()
	// 在本机同路径放一份假技能和假文件，确保远端绝不读取本机同名文件
	if err := os.MkdirAll(filepath.Join(decoyWs, ".claude", "skills", "local-decoy"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(decoyWs, ".claude", "skills", "local-decoy", "SKILL.md"), []byte("---\nname: local-decoy\ndescription: local poison\n---\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	localOnlyFile := filepath.Join(decoyWs, "local-only.txt")
	if err := os.WriteFile(localOnlyFile, []byte("local"), 0o600); err != nil {
		t.Fatal(err)
	}

	a.projects = []project{{ID: "p1", Name: "proj", Path: decoyWs, Kind: "git"}}
	a.workspaces = []workspace{{
		ID: "w1", ProjectID: "p1", Project: "proj", Name: "main", Path: decoyWs, Kind: "main",
		Terminals: []terminalInfo{{ID: "t1", Pid: os.Getpid(), Running: true, Dir: decoyWs}},
	}}
	a.selected = "w1"
	a.loaded = true
	a.sectionsOpen = map[string]*bool{}
	a.activeTerm = map[string]string{"w1": filesTabID}
	a.daemon.FootprintKB = 24 * 1024
	a.daemon.Running = 1
	a.daemon.Awake = &awakeStatus{Mode: "auto"}
	a.state.FilesPanelShared = true
	a.state.FilesPanelOn = true
	a.state.FilesPanelTab = tabSkills
	a.indexWorkspaces()

	skillText := "---\nname: remote-deploy\ndescription: Deploy on remote host\n---\n# Remote\n"
	skillPath := filepath.Join(decoyWs, ".agents", "skills", "remote-deploy", "SKILL.md")
	skillSum := files.Stamp{Ino: 7, Size: int64(len(skillText)), Mode: 0o644}
	var writeCalls atomic.Int32
	f.mu.Lock()
	f.desktopCall = func(_ context.Context, p rpc.DesktopCall) (any, error) {
		switch p.Method {
		case "files.readDirs":
			var q rpc.FilePathsParams
			json.Unmarshal(p.Params, &q)
			out := make([]rpc.FileDirectoryResult, len(q.Paths))
			for i, path := range q.Paths {
				if path == filepath.Join(decoyWs, ".agents", "skills") {
					out[i] = rpc.FileDirectoryResult{Path: path, Entries: []files.Entry{{Name: "remote-deploy", Dir: true}}}
				} else {
					out[i] = rpc.FileDirectoryResult{Path: path, Error: rpc.Errorf(rpc.CodeNotFound, "missing")}
				}
			}
			return out, nil
		case "files.stat":
			var q rpc.FilePathsParams
			json.Unmarshal(p.Params, &q)
			if len(q.Paths) == 1 && q.Paths[0] == skillPath {
				return []rpc.FileStatResult{{Path: skillPath, Stamp: skillSum}}, nil
			}
			return []rpc.FileStatResult{{Path: q.Paths[0], Stamp: files.Stamp{Missing: true}}}, nil
		case "files.readRange":
			return rpc.FileRangeResult{Data: []byte(skillText), Stamp: skillSum, EOF: true}, nil
		case "awake.set", "usage.refresh":
			writeCalls.Add(1)
			return map[string]any{}, nil
		case "project.list", "workspace.list", "task.list":
			return []any{}, nil
		}
		return map[string]any{}, nil
	}
	f.mu.Unlock()

	// 查看端：防休眠与用量刷新不发送
	a.setAwake("display")
	a.refreshPlan(appL())
	a.runPending()
	if writeCalls.Load() != 0 {
		t.Fatalf("viewer triggered write RPCs: %d", writeCalls.Load())
	}

	tt := newTester(a.build, 1200, 760)
	until(t, tt, "remote skills scanned", func() bool { return !a.skillsTab.poll.at.IsZero() })
	tt.Frame()
	if tt.HasText("local-decoy") || !tt.HasText("remote-deploy") {
		t.Fatalf("remote skills panel read wrong host: %v", tt.Texts())
	}

	// 远端内存浮窗不拿 os.Getpid() 查本机进程树
	a.loadMemory()
	if a.mem.terms != 0 || len(a.mem.rows) != 0 {
		t.Fatalf("remote loadMemory inspected local PIDs: terms=%d rows=%v", a.mem.terms, a.mem.rows)
	}

	// 远端链接卡片不读本机同名文件，也不展示访达或默认应用
	v := &termView{id: "t1", dir: decoyWs}
	if p, local := a.linkPath(v, "~/secret.txt"); !local || p != "" {
		t.Fatalf("remote tilde link resolved locally: %q", p)
	}
	p, local := a.linkPath(v, "sub/file.go:12")
	if !local || p != filepath.Join(decoyWs, "sub", "file.go") {
		t.Fatalf("remote relative link = %q, want remote path", p)
	}
	card := a.newLinkCard(localOnlyFile, 1)
	card.ws = &a.workspaces[0]
	a.showLinkCard(card)
	tt.Frame()
	if tt.HasText(locales.In("zh-Hans").CommonRevealInFinder()) || tt.HasText(locales.In("zh-Hans").CommonOpenInFinder()) {
		t.Fatalf("remote link card exposed Finder actions: %v", tt.Texts())
	}

	// 受控端允许 awake.set 与 usage.refresh
	a.linkCard = nil
	takeRequestTestControl(t, a)
	a.setAwake("display")
	a.refreshPlan(appL())
	pumpSaves(t, []*app{a}, func() bool { return writeCalls.Load() == 2 && !a.planBusy })
}

func TestPreviewRemoteStage5(t *testing.T) {
	if os.Getenv("POSSE_PREVIEW_DIR") == "" {
		t.Skip("set POSSE_PREVIEW_DIR to render previews")
	}
	for _, dark := range []bool{false, true} {
		suffix := "light"
		if dark {
			suffix = "dark"
		}
		t.Run("skills-and-memory-"+suffix, func(t *testing.T) {
			home := t.TempDir()
			local := newHostApp(&client.Client{Home: home}, home)
			local.selfKB = 42 * 1024
			views := newHostViews(local)
			a := views.forHost(newHostSession("office", "Posse"), &client.Client{Remote: true, Desktop: &rpc.DesktopIdentity{ClientID: "c", InstanceID: "i", Epoch: 1}})
			views.active.Store(a)
			views.target = a
			h := remoteTestHost("office")
			h.Name = "Office Mac"
			writeHostMenuConfig(t, local, h)
			a.loadHostMenu()
			a.c = nil
			a.host.current = hostSnapshot{
				HostID: "office", Connected: true, Controlled: true,
				Desktop: rpc.DesktopState{InstanceID: "i", Epoch: 1, Owner: a.host.controllerID, Label: "Posse"},
			}
			a.projects = []project{{ID: "p1", Name: "posse", Path: "/Users/office/posse", Kind: "git"}}
			a.workspaces = []workspace{{ID: "w1", ProjectID: "p1", Project: "posse", Name: "main", Path: "/Users/office/posse", Kind: "main"}}
			a.selected = "w1"
			a.loaded = true
			a.daemon.FootprintKB = 28 * 1024
			a.daemon.Running = 3
			a.daemon.Awake = &awakeStatus{Mode: "auto", Active: true}
			a.selfKB = 42 * 1024
			a.state.FilesPanelShared = true
			a.state.FilesPanelOn = true
			a.state.FilesPanelTab = tabSkills
			a.indexWorkspaces()
			v := a.filesFor(&a.workspaces[0])
			v.list = &files.List{}
			v.listAt = time.Now()
			v.poll.at = time.Now().Add(time.Hour)
			a.skillsTab = skillsPanelView{
				key:  "/Users/office/posse",
				real: "/Users/office/posse",
				cat: skills.Catalog{Skills: []skills.Skill{
					{Name: "deploy-prod", Description: "Deploy release artifacts on Office Mac", Dir: "/Users/office/posse/.agents/skills/deploy-prod", File: "/Users/office/posse/.agents/skills/deploy-prod/SKILL.md", Scope: skills.ScopeProject},
				}},
			}
			a.skillsTab.poll.done()
			a.mem.open = true

			tt := newTester(views.build, 1100, 680)
			tt.SetTitleBar(ui.TitleBar{Height: 40, Left: 70})
			tt.SetDark(dark)
			tt.SetScale(2)
			settleMenu(tt)
			savePNG(t, tt, "remote-stage5-"+suffix+".png")
		})
	}
}
