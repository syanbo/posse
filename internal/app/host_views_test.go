package app

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"sync"
	"testing"

	"github.com/egoist/mygo/plugins/terminal"
	"github.com/egoist/mygo/ui/editor"

	"posse/internal/client"
	"posse/internal/files"
)

func hostViewsFixture(t *testing.T) (*hostViews, *app, *app) {
	t.Helper()
	home := t.TempDir()
	a := newHostApp(&client.Client{Home: home}, home)
	v := newHostViews(a)
	b := v.forHost(newHostSession("remote", "Remote"), &client.Client{Home: "/remote/home", Remote: true})
	t.Cleanup(func() {
		// 同步写入较新的序号，排队的旧保存不会在临时目录清理后重新创建文件。
		for _, x := range []*app{a, b} {
			if err := saveUIState(x.home, x.state); err != nil {
				t.Error(err)
			}
		}
	})
	return v, a, b
}

func hostDirtyDoc(path, text string) *fileDoc {
	b := editor.NewBuffer("base")
	b.Set(text)
	return &fileDoc{path: path, kind: docText, ed: editor.State{Buffer: b}, base: files.Stamp{Missing: true}}
}

func TestHostViewsKeepDocumentsAndLayout(t *testing.T) {
	v, a, b := hostViewsFixture(t)
	da, db := hostDirtyDoc("/same/path.txt", "host a"), hostDirtyDoc("/same/path.txt", "host b")
	for i, x := range []*app{a, b} {
		d := []*fileDoc{da, db}[i]
		x.selected = "same-w"
		x.activeTerm["same-w"] = "same-t"
		x.state.TermOrder = map[string][]string{"same-w": {"same-t", "other-t"}}
		x.state.LastWorkspace = "same-w"
		x.state.Panes = map[string]layoutState{"same-w": {Root: paneNode{Split: splitH, Kids: []paneNode{
			{ID: "a", Tabs: []string{"same-t"}, Active: "same-t"},
			{ID: "b", Tabs: []string{d.path}, Active: d.path},
		}}, Focus: "b"}}
		x.files = map[string]*filesView{"same-w": {wsID: "same-w", docs: []*fileDoc{d}, cur: d}}
	}
	layout := a.state.Panes["same-w"].clone()
	before := a.request()
	before.update(func() { a.selected = "stale" })
	if !v.activate("remote") || a.displayed() != b {
		t.Fatal("remote view not activated")
	}
	if before.current() || a.files["same-w"].cur != da || b.files["same-w"].cur != db {
		t.Fatal("state owner changed")
	}
	if !v.activate(localHostID) {
		t.Fatal("local view not restored")
	}
	a.runPending()
	if a.selected != "same-w" || before.current() {
		t.Fatal("switching back revived an old callback")
	}
	if !reflect.DeepEqual(layout, a.state.Panes["same-w"]) || a.activeTerm["same-w"] != "same-t" {
		t.Fatal("layout lost")
	}
	if da.ed.Buffer.String() != "host a" || db.ed.Buffer.String() != "host b" || !da.isModified() || !db.isModified() {
		t.Fatal("dirty content lost")
	}
	if docs := a.allDirtyDocs(); !slices.Equal(docs, []*fileDoc{da, db}) {
		t.Fatalf("dirty docs = %v", docs)
	}
	if a.documentOwner(da) != a || a.documentOwner(db) != b {
		t.Fatal("same path merged document ownership")
	}
	waitState(t, a.home, func(s uiState) bool { return s.Selected == "same-w" })
	waitState(t, b.home, func(s uiState) bool { return s.Selected == "same-w" })
}

type hostViewConn struct {
	closed chan struct{}
	once   sync.Once
}

func (c *hostViewConn) Read([]byte) (int, error)    { <-c.closed; return 0, io.EOF }
func (c *hostViewConn) Write(p []byte) (int, error) { return len(p), nil }
func (c *hostViewConn) Close() error                { c.once.Do(func() { close(c.closed) }); return nil }

func TestHostViewsCloseOnlyRenderingConnection(t *testing.T) {
	v, a, b := hostViewsFixture(t)
	conn := &hostViewConn{closed: make(chan struct{})}
	term, err := terminal.New(terminal.Options{Conn: conn, ReadOnly: true})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { term.Close() })
	view := &termView{id: "same-t", term: term}
	a.panes[0] = view
	a.activeTerm["w"], b.activeTerm["w"] = "same-t", "same-t"
	v.activate("remote")
	select {
	case <-conn.closed:
	default:
		t.Fatal("rendering connection left open")
	}
	if a.live(view) || b.live(view) || a.panes[0] != nil || b.panes[0] != nil {
		t.Fatal("renderer reused across hosts")
	}
	if a.activeTerm["w"] != "same-t" || b.activeTerm["w"] != "same-t" {
		t.Fatal("closing renderer removed tab selection")
	}
}

func TestHostViewsPreferencesAndStorage(t *testing.T) {
	v, a, b := hostViewsFixture(t)
	a.state.Theme, a.state.FontSize = "dark", 17
	a.state.Keys = map[string][]string{"closeTab": {"Super+k"}}
	a.state.NavigationOrder = []string{"workspaces", "discussions"}
	a.state.DiscRead = map[string]int64{"room": 8}
	a.state.PinnedWorkspaces = []string{"local-w"}
	b.state.PinnedWorkspaces = []string{"remote-w"}
	v.activate("remote")
	if b.fontSize != 17 || b.state.Theme != "dark" || b.state.Keys["closeTab"][0] != "Super+k" {
		t.Fatal("window preferences did not follow view")
	}
	if b.state.DiscRead != nil || !slices.Equal(b.state.PinnedWorkspaces, []string{"remote-w"}) {
		t.Fatal("local resources copied to remote")
	}
	b.state.FontSize = 19
	b.state.Keys["closeTab"][0] = "Super+j"
	if a.state.Keys["closeTab"][0] != "Super+k" {
		t.Fatal("key binding slices aliased")
	}
	b.saveState()
	if a.state.FontSize != 19 || a.state.Keys["closeTab"][0] != "Super+j" {
		t.Fatal("window preferences not persisted locally")
	}
	v.activate(localHostID)
	if a.fontSize != 19 || a.state.DiscRead["room"] != 8 || a.state.PinnedWorkspaces[0] != "local-w" {
		t.Fatal("local state not restored")
	}
	waitState(t, a.home, func(s uiState) bool { return s.FontSize == 19 })
	waitState(t, b.home, func(s uiState) bool { return s.FontSize == 19 })
	if b.home == b.c.Home || b.home == a.home {
		t.Fatal("remote state uses target home")
	}
	for _, id := range []string{"../outside", "A", "a", "/absolute"} {
		p := hostUIHome(a.home, id)
		if !stringsInsideHostUI(p, a.home) {
			t.Fatalf("unsafe UI path: %s", p)
		}
	}
	if hostUIHome(a.home, "A") == hostUIHome(a.home, "a") || hostUIHome(a.home, localHostID) != a.home {
		t.Fatal("state identity or local compatibility changed")
	}
}

func stringsInsideHostUI(path, home string) bool {
	return filepath.Dir(path) == filepath.Join(home, "host-ui")
}

func TestHostViewsUnsavedUsesOriginalOwners(t *testing.T) {
	v, a, b := hostViewsFixture(t)
	path := filepath.Join(t.TempDir(), "same.txt")
	if err := os.WriteFile(path, []byte("disk"), 0o600); err != nil {
		t.Fatal(err)
	}
	da, db := hostDirtyDoc(path, "local edit"), hostDirtyDoc(path, "remote edit")
	da.base = files.StatFile(path)
	a.files = map[string]*filesView{"w": {docs: []*fileDoc{da}}}
	b.files = map[string]*filesView{"w": {docs: []*fileDoc{db}}}
	v.activate("remote")
	b.askUnsaved(b.allDirtyDocs(), func() { t.Fatal("must not finish closing") })
	u := b.unsaved
	if u.requests[da].app != a || u.requests[db].app != b {
		t.Fatal("save-all rebound documents to current host")
	}
	if waitSave(t, []*app{a, b}, func(done func(bool)) { b.saveUnsaved(appL(), u, done) }) {
		t.Fatal("disconnected documents reported saved")
	}
	text, err := os.ReadFile(path)
	if err != nil || string(text) != "local edit" || da.isModified() || !db.isModified() {
		t.Fatal("save-all wrote a different host's local path")
	}
	v.activate(localHostID)
	if waitSave(t, []*app{a, b}, func(done func(bool)) { a.saveUnsaved(appL(), u, done) }) {
		t.Fatal("switching back revived old save confirmation")
	}
	waitState(t, b.home, func(s uiState) bool { return s.Selected == "" })
}

func TestHostViewsSaveHiddenLocalDocument(t *testing.T) {
	v, a, b := hostViewsFixture(t)
	path := filepath.Join(t.TempDir(), "local.txt")
	d := hostDirtyDoc(path, "local edit")
	a.files = map[string]*filesView{"w": {docs: []*fileDoc{d}}}
	v.activate("remote")
	b.askUnsaved(b.allDirtyDocs(), func() {})
	if !waitSave(t, []*app{a, b}, func(done func(bool)) { b.saveUnsaved(appL(), b.unsaved, done) }) {
		t.Fatalf("hidden local save failed: %s", b.err)
	}
	if text, err := os.ReadFile(path); err != nil || string(text) != "local edit" || d.isModified() {
		t.Fatal("local document not saved through its owner")
	}
}

func TestHostViewsHiddenConflictConfirmation(t *testing.T) {
	for _, switchBack := range []bool{false, true} {
		t.Run(fmt.Sprint(switchBack), func(t *testing.T) {
			v, a, b := hostViewsFixture(t)
			path := filepath.Join(t.TempDir(), "local.txt")
			if err := os.WriteFile(path, []byte("base"), 0o600); err != nil {
				t.Fatal(err)
			}
			d := hostDirtyDoc(path, "edit")
			d.base = files.StatFile(path)
			a.files = map[string]*filesView{"w": {docs: []*fileDoc{d}}}
			if err := os.WriteFile(path, []byte("external"), 0o600); err != nil {
				t.Fatal(err)
			}
			v.activate("remote")
			b.askUnsaved(b.allDirtyDocs(), func() {})
			finished := false
			b.saveUnsaved(appL(), b.unsaved, func(bool) { finished = true })
			pumpSaves(t, []*app{a, b}, func() bool { return b.confirming })
			if finished || a.confirming {
				t.Fatal("conflict confirmation hidden with its owner")
			}
			confirm := b.confirmDo
			want := "edit"
			if switchBack {
				v.activate(localHostID)
				want = "external"
			}
			confirm()
			pumpSaves(t, []*app{a, b}, func() bool { return d.save == nil })
			if text, err := os.ReadFile(path); err != nil || string(text) != want {
				t.Fatalf("confirmed content=%q err=%v", text, err)
			}
		})
	}
}

func TestHostViewsWindowStateRouting(t *testing.T) {
	v, a, b := hostViewsFixture(t)
	a.locale = "en"
	a.seenFor, a.viewing = "same-t", "same-t"
	a.workspaces = []workspace{{ID: "w", Terminals: []terminalInfo{{ID: "same-t", Agent: "claude", AgentState: &agentState{Status: "waiting"}}}}}
	var badges []int
	a.setBadge = func(n int) { badges = append(badges, n) }
	a.updateBadge()
	v.activate("remote")
	if a.seenFor != "" {
		t.Fatal("cancelled seen request remained cached")
	}
	b.rememberLocale("zh-Hans")
	b.selected = "remote-workspace"
	b.openSettings()
	if v.active.Load() != a || a.selected != settingsBoardID || b.selected != "remote-workspace" {
		t.Fatal("settings changed a hidden view")
	}
	if a.locale != "zh-Hans" || a.seenFor != "" {
		t.Fatal("window locale or seen cache not restored")
	}
	v.activate("remote")
	if b.locale != "zh-Hans" {
		t.Fatal("old local locale replaced window choice")
	}
	b.openAbout()
	if v.active.Load() != a || a.settingsTab != "about" || a.selected != settingsBoardID {
		t.Fatal("about page not routed locally")
	}
	if !slices.Equal(badges, []int{1, 0, 1, 0, 1}) {
		t.Fatalf("system badge updates = %v", badges)
	}
	b.updateBadge()
	if len(badges) != 5 {
		t.Fatal("hidden view changed global badge")
	}
}

func TestHostViewsRenderSwitch(t *testing.T) {
	for _, dark := range []bool{false, true} {
		t.Run(fmt.Sprint(dark), func(t *testing.T) {
			a, b := previewApp(t), previewApp(t)
			offlineClient(t, a)
			offlineClient(t, b)
			a.home, b.home = "", ""
			a.selected, b.selected = "w9", "w9"
			a.projects[0].Name, b.projects[0].Name = "Local machine", "Other machine"
			v := newHostViews(a)
			if !v.add("other", b) {
				t.Fatal("add view")
			}
			a.files = map[string]*filesView{"same": {docs: []*fileDoc{hostDirtyDoc("/same/file", "a")}}}
			b.files = map[string]*filesView{"same": {docs: []*fileDoc{hostDirtyDoc("/same/file", "b")}}}
			tt := newTester(v.build, 1200, 760)
			tt.SetDark(dark)
			settle(tt)
			if !tt.HasText("Local machine") || tt.HasText("Other machine") {
				t.Fatal("wrong initial host")
			}
			v.activate("other")
			settle(tt)
			if !tt.HasText("Other machine") || tt.HasText("Local machine") {
				t.Fatal("old host UI survived switch")
			}
			if a.dirtyCount.Load() != 2 {
				t.Fatalf("window dirty count = %d", a.dirtyCount.Load())
			}
			if os.Getenv("POSSE_PREVIEW_DIR") != "" {
				savePNG(t, tt, fmt.Sprintf("host-view-switch-%t.png", dark))
			}
			v.activate(localHostID)
			settle(tt)
			if !tt.HasText("Local machine") || tt.HasText("Other machine") || a.selected != "w9" {
				t.Fatal("original view did not return")
			}
		})
	}
}

func TestHostViewsLegacyUIStateLoadsLocally(t *testing.T) {
	home := t.TempDir()
	original := uiState{Selected: "w", LastWorkspace: "w", TermOrder: map[string][]string{"w": {"t"}}, PinnedWorkspaces: []string{"w"}}
	if err := saveUIState(home, original); err != nil {
		t.Fatal(err)
	}
	a := newHostApp(&client.Client{Home: home}, home)
	v := newHostViews(a)
	b := v.forHost(newHostSession("other", "Other"), &client.Client{Remote: true})
	if a.selected != "w" || !slices.Equal(a.state.TermOrder["w"], []string{"t"}) {
		t.Fatal("legacy local state lost")
	}
	if b.selected != "" || len(b.state.TermOrder) != 0 || len(b.state.PinnedWorkspaces) != 0 {
		t.Fatal("legacy local resources assigned to remote")
	}
	if v.forHost(b.host, b.c) != b {
		t.Fatal("reopening host replaced document owner")
	}
}
