package app

import (
	"bytes"
	"context"
	"encoding/json"
	"image"
	"image/color"
	"image/gif"
	"image/png"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/egoist/mygo"
	"github.com/egoist/mygo/ui"
	"posse/internal/files"
	"posse/internal/history"
	"posse/internal/media"
	"posse/internal/rpc"
	"posse/locales"
)

func remoteMediaGIF(t *testing.T) []byte {
	t.Helper()
	palette := color.Palette{color.RGBA{240, 100, 60, 255}, color.RGBA{40, 80, 160, 255}}
	first := image.NewPaletted(image.Rect(0, 0, 24, 16), palette)
	second := image.NewPaletted(first.Rect, palette)
	for i := range second.Pix {
		second.Pix[i] = 1
	}
	var b bytes.Buffer
	if err := gif.EncodeAll(&b, &gif.GIF{Image: []*image.Paletted{first, second}, Delay: []int{10, 10}}); err != nil {
		t.Fatal(err)
	}
	return b.Bytes()
}

type remoteMediaFixture struct {
	a     *app
	f     *hostControlFixture
	cur   *terminalInfo
	item  media.Item
	path  string
	data  []byte
	stamp files.Stamp
	calls atomic.Int32
}

func newRemoteMediaFixture(t *testing.T) *remoteMediaFixture {
	t.Helper()
	a, f, local, _ := remoteFileFixture(t, "target")
	path := filepath.Join(filepath.Dir(local), "image.gif")
	if err := os.WriteFile(path, []byte("local poison"), 0600); err != nil {
		t.Fatal(err)
	}
	x := &remoteMediaFixture{a: a, f: f, path: path, data: remoteMediaGIF(t)}
	x.stamp = files.Stamp{Size: int64(len(x.data)), Mode: 0600, Ino: 42}
	x.item = media.Item{Key: "target-image", Path: path, Label: "animation.gif"}
	x.cur = &terminalInfo{ID: "t", Agent: "codex", AgentState: &agentState{SessionID: "session", TranscriptPath: "/target/session.jsonl"}}
	a.home = t.TempDir()
	a.workspaces = []workspace{{ID: "w", Path: filepath.Dir(path), Terminals: []terminalInfo{*x.cur}}}
	a.indexWorkspaces()
	a.selected = "w"
	a.activeTerm = map[string]string{"w": "t"}
	a.files = map[string]*filesView{}
	f.mu.Lock()
	f.desktopCall = x.handle
	f.mu.Unlock()
	t.Cleanup(func() {
		a.closeImages()
		// 同步写入较新的序号，避免旧保存协程在临时目录清理后重新创建文件。
		if err := saveUIState(a.home, a.state); err != nil {
			t.Error(err)
		}
	})
	return x
}

func (x *remoteMediaFixture) handle(_ context.Context, p rpc.DesktopCall) (any, error) {
	x.calls.Add(1)
	if p.Sequence != 0 {
		return nil, rpc.Errorf(rpc.CodeBadRequest, "media read became a write")
	}
	switch p.Method {
	case "media.list":
		return rpc.MediaListResult{Made: []media.Item{x.item}, MadeAll: 1, Found: true}, nil
	case "media.path":
		return rpc.MediaPathResult{Path: x.path}, nil
	case "files.resourceRange", "files.resourceVersion":
		var args rpc.FileResourceRangeParams
		if err := json.Unmarshal(p.Params, &args); err != nil {
			return nil, err
		}
		if args.Root != "/" || args.Path != strings.TrimPrefix(x.path, "/") {
			return nil, rpc.Errorf(rpc.CodeBadRequest, "wrong target resource")
		}
		if p.Method == "files.resourceVersion" {
			return imageResourceVersion(x.path, x.data, x.stamp), nil
		}
		return rpc.FileRangeResult{Data: x.data, Stamp: x.stamp, EOF: true}, nil
	case "files.stat":
		var args rpc.FilePathsParams
		if err := json.Unmarshal(p.Params, &args); err != nil {
			return nil, err
		}
		out := make([]rpc.FileStatResult, len(args.Paths))
		for i, path := range args.Paths {
			out[i] = rpc.FileStatResult{Path: path, Stamp: x.stamp}
		}
		return out, nil
	}
	return nil, rpc.Errorf(rpc.CodeUnknown, "unexpected method %s", p.Method)
}

func TestRemoteImagesReadBigAndCopy(t *testing.T) {
	x := newRemoteMediaFixture(t)
	runImagesPoll(t, x.a, x.cur)
	if len(x.a.images.made) != 1 || x.a.images.made[0].thumb == nil || !x.a.images.found {
		t.Fatal("target thumbnail missing")
	}
	if x.a.host.snapshot().Controlled {
		t.Fatal("media viewing acquired control")
	}
	it := x.a.images.made[0]
	x.a.loadBig(it)
	pumpSaves(t, []*app{x.a}, func() bool { return !x.a.images.bigBusy })
	if x.a.images.bigAnim == nil || x.a.images.bigKey != it.Key {
		t.Fatal("target GIF did not animate")
	}
	old := writeMediaClipboard
	defer func() { writeMediaClipboard = old }()
	var copied []byte
	writeMediaClipboard = func(data []byte) { copied = bytes.Clone(data) }
	x.a.copyImage(appL(), it)
	pumpSaves(t, []*app{x.a}, func() bool { return copied != nil })
	cfg, err := png.DecodeConfig(bytes.NewReader(copied))
	if err != nil || cfg.Width != 24 || cfg.Height != 16 {
		t.Fatalf("clipboard PNG: %+v %v", cfg, err)
	}
	if local, _ := os.ReadFile(x.path); string(local) != "local poison" {
		t.Fatal("target read altered local path")
	}
}

func TestRemoteImagesFailureRecovers(t *testing.T) {
	for _, stage := range []string{"media.list", "files.resourceRange"} {
		t.Run(stage, func(t *testing.T) {
			x := newRemoteMediaFixture(t)
			var fail atomic.Bool
			fail.Store(true)
			x.f.mu.Lock()
			x.f.desktopCall = func(ctx context.Context, p rpc.DesktopCall) (any, error) {
				if fail.Load() && p.Method == stage {
					return nil, rpc.Errorf(rpc.CodePermissionDenied, "denied")
				}
				return x.handle(ctx, p)
			}
			x.f.mu.Unlock()
			runImagesPoll(t, x.a, x.cur)
			if stage == "media.list" && x.a.images.target.err == nil {
				t.Fatal("list failure was hidden")
			}
			if stage != "media.list" && !x.a.images.failed[x.item.Key] {
				t.Fatal("thumbnail failure was hidden")
			}
			fail.Store(false)
			x.a.images.poll.at = time.Time{}
			runImagesPoll(t, x.a, x.cur)
			if len(x.a.images.made) != 1 || x.a.images.made[0].thumb == nil || x.a.images.failed[x.item.Key] || x.a.images.target.err != nil {
				t.Fatal("failed media never recovered")
			}
		})
	}
}

func TestRemoteImagesDiscardOldRequests(t *testing.T) {
	for _, mode := range []string{"close", "session", "identity"} {
		t.Run(mode, func(t *testing.T) {
			x := newRemoteMediaFixture(t)
			entered, release := make(chan struct{}), make(chan struct{})
			var once atomic.Bool
			x.f.mu.Lock()
			x.f.desktopCall = func(ctx context.Context, p rpc.DesktopCall) (any, error) {
				if p.Method == "media.list" && once.CompareAndSwap(false, true) {
					close(entered)
					<-release
					return nil, rpc.Errorf(rpc.CodePermissionDenied, "stale failure")
				}
				return x.handle(ctx, p)
			}
			x.f.mu.Unlock()
			newTester(func(c *ui.Context) { x.a.pollImages(c, x.cur) }, 100, 100).Frame()
			waitConnectionEvent(t, entered)
			old := x.a.images.target
			switch mode {
			case "close":
				x.a.closeImages()
			case "session":
				x.cur.AgentState.SessionID = "new-session"
				runImagesPoll(t, x.a, x.cur)
			case "identity":
				takeRequestTestControl(t, x.a)
				runImagesPoll(t, x.a, x.cur)
			}
			if old.ctx.Err() == nil {
				t.Fatal("obsolete media session was not cancelled")
			}
			close(release)
			if mode == "close" {
				awaitHostUpdate(t, x.a)
			} else {
				x.a.runPending()
			}
			if x.a.err != "" {
				t.Fatalf("stale error applied: %s", x.a.err)
			}
			if mode == "close" && len(x.a.images.made) != 0 {
				t.Fatal("closed panel got stale images")
			}
			if mode != "close" && (x.a.images.target == old || len(x.a.images.made) != 1) {
				t.Fatal("old request replaced current panel")
			}
		})
	}
}

func TestRemoteImagesOpenEmbeddedDocument(t *testing.T) {
	x := newRemoteMediaFixture(t)
	x.item.Path = ""
	x.item.Src = "/target/session.jsonl"
	x.item.Tool = &history.ToolImage{LineHash: [32]byte{1}}
	runImagesPoll(t, x.a, x.cur)
	x.a.openImage(x.a.images.made[0])
	pumpSaves(t, []*app{x.a}, func() bool { v := x.a.files["w"]; return v != nil && len(v.docs) > 0 })
	v := x.a.files["w"]
	if v.docs[0].path != x.path || !v.remote {
		t.Fatalf("embedded image opened wrong document: %+v", v.docs[0])
	}
	if local, _ := os.ReadFile(x.path); string(local) != "local poison" {
		t.Fatal("embedded image touched local target")
	}
}

func TestRemoteImagesMenuKeepsDelayedAction(t *testing.T) {
	for _, copyImage := range []bool{true, false} {
		t.Run(map[bool]string{true: "copy-image", false: "open-copy"}[copyImage], func(t *testing.T) {
			x := newRemoteMediaFixture(t)
			runImagesPoll(t, x.a, x.cur)
			entered, release := make(chan struct{}), make(chan struct{})
			released := false
			defer func() {
				if !released {
					close(release)
				}
			}()
			x.f.mu.Lock()
			x.f.desktopCall = func(ctx context.Context, p rpc.DesktopCall) (any, error) {
				if p.Method == "media.path" {
					close(entered)
					<-release
				}
				return x.handle(ctx, p)
			}
			x.f.mu.Unlock()
			oldWrite, oldSave, oldOpen := writeMediaClipboard, saveDialog, openDownloadedCopy
			defer func() { writeMediaClipboard, saveDialog, openDownloadedCopy = oldWrite, oldSave, oldOpen }()
			finished := false
			destination := filepath.Join(t.TempDir(), "copy.gif")
			writeMediaClipboard = func([]byte) { finished = true }
			saveDialog = func(mygo.SaveDialogOptions) (string, error) { return destination, nil }
			openDownloadedCopy = func(path string) error {
				if path != destination {
					t.Errorf("opened target path %q", path)
				}
				finished = true
				return nil
			}
			it := x.a.images.made[0]
			x.a.state.RightPanel = panelImages
			x.a.images.bigKey = it.Key
			tt := newTester(func(c *ui.Context) {
				x.a.mainBody(c, x.a.workspace(x.a.selected), x.cur)
			}, 700, 500)
			settleMenu(tt)
			m := locales.In("zh-Hans")
			if err := tt.RightClick(m.ImagesImageLabel(it.Label)); err != nil {
				t.Fatal(err)
			}
			settleMenu(tt)
			label := m.FilesOpenCopy()
			if copyImage {
				label = m.ImagesCopyImage()
			}
			if err := tt.Click(label); err != nil {
				t.Fatal(err)
			}
			tt.Frame()
			waitConnectionEvent(t, entered)
			pass(tt, 250*time.Millisecond)
			tt.Frame()
			if x.a.state.RightPanel != panelImages {
				t.Fatal("mainBody treated menu choice as a panel outside press")
			}
			if !x.a.images.target.current() {
				t.Fatal("menu choice cancelled delayed media action")
			}
			close(release)
			released = true
			pumpSaves(t, []*app{x.a}, func() bool { return finished })
		})
	}
}

func TestRemoteImagesDownloadCopies(t *testing.T) {
	for _, mode := range []string{"video", "image-copy", "stale-dialog", "stale-download"} {
		t.Run(mode, func(t *testing.T) {
			x := newRemoteMediaFixture(t)
			x.item.Video = mode != "image-copy"
			runImagesPoll(t, x.a, x.cur)
			destination := filepath.Join(t.TempDir(), "copy.gif")
			oldSave, oldOpen := saveDialog, openDownloadedCopy
			defer func() { saveDialog, openDownloadedCopy = oldSave, oldOpen }()
			var opened []string
			openDownloadedCopy = func(path string) error { opened = append(opened, path); return nil }
			answer := make(chan string, 1)
			saveDialog = func(mygo.SaveDialogOptions) (string, error) { return <-answer, nil }
			entered, release := make(chan struct{}), make(chan struct{})
			if mode == "stale-download" {
				x.f.mu.Lock()
				x.f.desktopCall = func(ctx context.Context, p rpc.DesktopCall) (any, error) {
					if p.Method == "files.resourceRange" {
						close(entered)
						<-release
					}
					return x.handle(ctx, p)
				}
				x.f.mu.Unlock()
			}
			if mode == "image-copy" {
				x.a.openTargetImage(x.a.images.made[0], true)
			} else {
				x.a.openImage(x.a.images.made[0])
			}
			pumpSaves(t, []*app{x.a}, func() bool { return x.a.dialogOpen })
			if mode == "stale-dialog" {
				x.a.closeImages()
			}
			answer <- destination
			pumpSaves(t, []*app{x.a}, func() bool { return !x.a.dialogOpen })
			if mode == "stale-download" {
				waitConnectionEvent(t, entered)
				x.a.closeImages()
				close(release)
			}
			v := x.a.files["w"]
			pumpSaves(t, []*app{x.a}, func() bool { return v.transfer == nil })
			if strings.HasPrefix(mode, "stale") {
				if len(opened) != 0 {
					t.Fatal("old media session opened a local app")
				}
			} else {
				if len(opened) != 1 || opened[0] != destination || opened[0] == x.path {
					t.Fatalf("opened paths = %v", opened)
				}
				if data, err := os.ReadFile(destination); err != nil || !bytes.Equal(data, x.data) {
					t.Fatalf("downloaded copy: %v", err)
				}
			}
			if local, _ := os.ReadFile(x.path); string(local) != "local poison" {
				t.Fatal("target path used as local copy destination")
			}
		})
	}
}
