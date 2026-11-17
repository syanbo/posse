package app

import (
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/egoist/mygo/ui/editor"

	"posse/internal/errtext"
	"posse/internal/gitx"
)

func TestRemoteRepoAutomaticGuards(t *testing.T) {
	root := t.TempDir()
	if err := os.Mkdir(filepath.Join(root, ".git"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, ".git", "HEAD"), []byte("ref: refs/heads/local-only\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	a := &app{viewID: "remote", err: "keep existing error"}
	v := &filesView{root: root, remote: true}
	newGitStates(v)
	g := &gitState{v: v, repo: root}
	v.repos.list = []*gitState{g}

	a.scanRepos(v)
	a.pollRepos(nil, v)
	a.refreshGit(v)
	a.refreshRepo(g)
	a.pollRepo(g, true)
	a.applyRepos(v, gitx.Discovery{}, false, time.Now(), nil)
	a.useRepo(v, g)
	a.gitChanged(g, nil)
	if a.rescanDue(nil, v) {
		t.Fatal("remote repository requested a local rescan")
	}
	if v.repos.scanning || v.repos.scanned || !v.repos.statAt.IsZero() || v.realRoot != "" || g.poll.busy || v.git != v.repos.none {
		t.Fatal("automatic refresh started local repository work")
	}
	if got := a.gitBranch(&workspace{ID: "remote-w", Path: root}); got != "" {
		t.Fatalf("remote branch read local HEAD: %q", got)
	}
	for _, cached := range []bool{false, true} {
		if !cached {
			v.repos.list = nil
		} else {
			v.repos.list = []*gitState{g}
		}
		if got := v.repoBranch(root); got != "" || !g.headAt.IsZero() {
			t.Fatalf("remote comparison read local HEAD: %q", got)
		}
	}
	if a.err != "keep existing error" {
		t.Fatalf("automatic refresh replaced error: %q", a.err)
	}
}

func TestRemoteFileActionsRejected(t *testing.T) {
	root := t.TempDir()
	for _, scanned := range []bool{false, true} {
		t.Run(fmt.Sprintf("repos-scanned-%t", scanned), func(t *testing.T) {
			a := &app{viewID: "remote"}
			v := &filesView{root: root, remote: true}
			v.repos.scanned = scanned
			run := false
			a.withRepos(v, func() { run = true })
			if run || v.repos.scanning || len(v.repos.waiting) != 0 {
				t.Fatal("remote repository action ran or queued local work")
			}
			assertRemoteFileUnsupported(t, a.err, "git.discover")
			a.err = ""
			a.rescanRepos(v)
			assertRemoteFileUnsupported(t, a.err, "git.discover")
			if v.repos.scanning {
				t.Fatal("manual refresh started a local scan")
			}
		})
	}
}

func TestRemoteHTMLPreviewUnavailableKeepsSource(t *testing.T) {
	a := &app{viewID: "remote"}
	d := &fileDoc{path: filepath.Join(t.TempDir(), "index.html"), kind: docText, preview: true}
	d.ed.Buffer = editor.NewBuffer("<h1>remote source</h1>")
	pg := a.newHTMLPage(&workspace{ID: "remote-w"}, d)
	if !pg.remote || pg.err != appL().WebUnavailable() {
		t.Fatal("missing remote preview state")
	}
	if pg.view != nil || d.ed.Buffer.String() != "<h1>remote source</h1>" || d.kind != docText {
		t.Fatal("remote HTML created a local WebView or lost source")
	}
}

func TestMDImagesLocalReadCallback(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "image.png")
	if err := os.WriteFile(path, []byte("local image"), 0o644); err != nil {
		t.Fatal(err)
	}
	l := newMDImages(root, root)
	defer l.close()
	denied := errors.New("remote image unavailable")
	var received string
	l.readLocal = func(src string) ([]byte, string, error) {
		received = src
		return nil, "", denied
	}
	for _, src := range []string{"image.png", path, "file://" + path, "file:" + path, "~/image.png", "image.png?raw=true#part"} {
		received = ""
		data, _, err := l.read(src)
		if !errors.Is(err, denied) || received != src || data != nil {
			t.Fatalf("callback did not replace file source %q: data=%q received=%q err=%v", src, data, received, err)
		}
	}
	l.readLocal = func(src string) ([]byte, string, error) {
		return []byte("remote image"), "remote.png", nil
	}
	if data, name, err := l.read(path); err != nil || string(data) != "remote image" || name != "remote.png" {
		t.Fatalf("callback result: %q %q %v", data, name, err)
	}
	l.readLocal = nil
	if data, name, err := l.read("image.png"); err != nil || string(data) != "local image" || name != path {
		t.Fatalf("default local read changed: %q %q %v", data, name, err)
	}
}

func TestMDImagesCallbackPreservesURLSources(t *testing.T) {
	l := newMDImages(t.TempDir(), "")
	defer l.close()
	l.readLocal = func(string) ([]byte, string, error) {
		t.Error("URL source reached local file callback")
		return nil, "", errors.New("unexpected local read")
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "image/png")
		fmt.Fprint(w, "http image")
	}))
	defer srv.Close()
	for src, want := range map[string]string{
		"data:image/png;base64,ZGF0YSBpbWFnZQ==": "data image",
		"data:image/png,data%20image":            "data image",
		srv.URL + "/image.png":                   "http image",
	} {
		if data, _, err := l.read(src); err != nil || string(data) != want {
			t.Errorf("URL read %q: %q %v", src, data, err)
		}
	}
}

func assertRemoteFileUnsupported(t *testing.T, got, method string) {
	t.Helper()
	want := errtext.Text(targetFileError(method))
	if got != want {
		t.Fatalf("unsupported error = %q, want %q", got, want)
	}
}
