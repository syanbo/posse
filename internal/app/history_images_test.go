package app

import (
	"bytes"
	"context"
	"image"
	"image/color"
	"image/png"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/egoist/mygo/ui"

	"posse/internal/history"
	"posse/internal/rpc"
)

func historyImagePNG(t *testing.T, w, h int) []byte {
	t.Helper()
	im := image.NewRGBA(image.Rect(0, 0, w, h))
	for y := range h {
		for x := range w {
			im.Set(x, y, color.RGBA{R: 32, G: 128, B: 200, A: 255})
		}
	}
	var b bytes.Buffer
	if err := png.Encode(&b, im); err != nil {
		t.Fatal(err)
	}
	return b.Bytes()
}

func waitHistoryImage(t *testing.T, l *mdImages, src string) mdImgEntry {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		l.mu.Lock()
		e := l.m[src]
		var out mdImgEntry
		if e != nil {
			out = *e
		}
		l.mu.Unlock()
		if out.done {
			return out
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatal("history image did not finish")
	return mdImgEntry{}
}

func TestHistoryMarkdownImagesRemote(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "image.png"), historyImagePNG(t, 9, 8), 0600); err != nil {
		t.Fatal(err)
	}
	sess := history.Session{ID: "same-id", Agent: "codex", Path: filepath.Join(root, "session.jsonl"), Cwd: root}
	var previous *mdImages
	for _, size := range []int{3, 4} {
		a, f := requestTestApp(t, "host")
		calls := installPreviewResources(t, a, f, root, map[string][]byte{"./image.png": historyImagePNG(t, size, 2)})
		f.mu.Lock()
		resourceCall := f.desktopCall
		f.desktopCall = func(ctx context.Context, p rpc.DesktopCall) (any, error) {
			if p.Method == "history.show" {
				if p.Sequence != 0 {
					t.Error("history read used write sequence")
				}
				return historyDetail{Session: sess, Messages: []history.Message{{Role: "assistant", Text: "![target](image.png)", Seq: 1}}}, nil
			}
			return resourceCall(ctx, p)
		}
		f.mu.Unlock()
		a.selectSession(sess, -1)
		awaitHostUpdate(t, a)
		d := a.history.detail
		if d == nil {
			t.Fatal("missing detail")
		}
		tt := newTester(func(c *ui.Context) { a.messageView(c, sess, d.Messages[0], nil, false) }, 600, 300)
		settle(tt)
		if d.imgs == nil || d.imgs == previous {
			t.Fatal("missing or reused host image loader")
		}
		e := waitHistoryImage(t, d.imgs, "image.png")
		if e.err != nil || e.src == nil || e.w != float32(size) || e.h != 2 || calls.Load() != 1 {
			t.Fatalf("target image: %+v, reads=%d", e, calls.Load())
		}
		previous = d.imgs
		for _, src := range []string{"../outside.png", "file:///outside.png", "~/image.png"} {
			if _, _, err := d.imgs.read(src); err == nil {
				t.Fatalf("accepted %s", src)
			}
		}
		if calls.Load() != 1 {
			t.Fatal("unsafe history URL reached resource RPC")
		}
		a.releaseHistory()
		if previous.ctx.Err() == nil {
			t.Fatal("release retained history loader")
		}
	}
}

func TestHistoryMarkdownImagesLocal(t *testing.T) {
	root := t.TempDir()
	if err := os.Mkdir(filepath.Join(root, "images"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "images/x.png"), historyImagePNG(t, 5, 3), 0600); err != nil {
		t.Fatal(err)
	}
	for _, cwd := range []string{root, "relative", ""} {
		a := &app{}
		s := history.Session{ID: "local", Path: filepath.Join(root, "session.jsonl"), Cwd: cwd}
		a.history.detail = &historyDetail{Session: s}
		l := a.historyImages(s)
		if l == nil || l.root != root || l.dir != root {
			t.Fatal("wrong local session root")
		}
		for _, src := range []string{"images/x.png", "/images/x.png"} {
			im, w, h, err := l.load(src)
			if err != nil || im == nil || w != 5 || h != 3 {
				t.Fatalf("%s: %v %v %v", src, w, h, err)
			}
		}
		l.close()
	}
}

func TestHistoryImagesOnlyAssistant(t *testing.T) {
	a := &app{}
	s := history.Session{ID: "local", Cwd: t.TempDir()}
	a.history.detail = &historyDetail{Session: s}
	tt := newTester(func(c *ui.Context) {
		a.messageView(c, s, history.Message{Role: "tool", Text: "![tool](image.png)"}, nil, false)
	}, 600, 300)
	settle(tt)
	if a.history.detail.imgs != nil {
		t.Fatal("tool text parsed as image")
	}
}

func TestHistoryImagesCancelOnDetailChange(t *testing.T) {
	for _, mode := range []string{"session", "same-session", "host", "identity", "released"} {
		t.Run(mode, func(t *testing.T) {
			a, f := requestTestApp(t, "remote")
			s := history.Session{ID: "same", Path: "/repo/session.jsonl", Cwd: "/repo"}
			d := &historyDetail{Session: s, request: a.request()}
			a.history.detail = d
			l := a.historyImages(s)
			defer l.close()
			entered, release := make(chan struct{}), make(chan struct{})
			var once sync.Once
			defer once.Do(func() { close(release) })
			data := historyImagePNG(t, 3, 2)
			f.mu.Lock()
			f.desktopCall = func(ctx context.Context, p rpc.DesktopCall) (any, error) {
				if p.Sequence != 0 {
					t.Error("image read used write sequence")
				}
				if p.Method == "history.show" {
					return historyDetail{Session: s}, nil
				}
				if p.Method == "files.resourceVersion" {
					return imageResourceVersion("/repo/image.png", data, previewResourceStamp(data)), nil
				}
				close(entered)
				select {
				case <-release:
				case <-ctx.Done():
					return nil, ctx.Err()
				}
				return rpc.FileRangeResult{Data: data, Stamp: previewResourceStamp(data), EOF: true}, nil
			}
			f.mu.Unlock()
			done := make(chan error, 1)
			go func() { _, _, _, err := l.load("image.png"); done <- err }()
			waitConnectionEvent(t, entered)
			switch mode {
			case "session":
				next := s
				next.Path = "/repo/other.jsonl"
				a.selectSession(next, -1)
			case "same-session":
				a.selectSession(s, -1)
			case "host":
				a.viewGeneration.Add(1)
				a.closeTargetPreviews()
			case "identity":
				takeRequestTestControl(t, a)
				if a.historyImages(s) != nil {
					t.Fatal("obsolete detail reused new identity")
				}
			case "released":
				a.releaseHistory()
			}
			once.Do(func() { close(release) })
			select {
			case err := <-done:
				if err == nil {
					t.Fatal("late history image decoded")
				}
			case <-time.After(5 * time.Second):
				t.Fatal("history image not cancelled")
			}
			if l.ctx.Err() == nil {
				t.Fatal("obsolete loader still open")
			}
			if mode == "same-session" || mode == "session" {
				awaitHostUpdate(t, a)
				a.history.detail.closeImages()
			}
		})
	}
}

func TestHistoryImagesLateDecodeDiscarded(t *testing.T) {
	l := newMDImages(t.TempDir(), "")
	defer l.close()
	entered, release, returned := make(chan struct{}), make(chan struct{}), make(chan struct{})
	data := historyImagePNG(t, 3, 2)
	l.readLocal = func(string) ([]byte, string, error) {
		close(entered)
		<-release
		defer close(returned)
		return data, "image.png", nil
	}
	tt := newTester(func(c *ui.Context) { l.get(c, "image.png") }, 600, 300)
	settle(tt)
	waitConnectionEvent(t, entered)
	l.close()
	close(release)
	waitConnectionEvent(t, returned)
	// 即使读取方忽略取消、继续交回可解码的字节，后台也不得发布旧图片。
	time.Sleep(50 * time.Millisecond)
	l.mu.Lock()
	e := *l.m["image.png"]
	l.mu.Unlock()
	if e.done || e.src != nil {
		t.Fatal("closed loader retained late decode")
	}
}

func TestHistoryMarkdownParserIsolation(t *testing.T) {
	body := `Before <b>literal</b> ![image](x.png) after`
	bs := parseHistoryMarkdown(body)
	found := false
	for _, b := range bs {
		for _, in := range b.inlines {
			if in.img != nil {
				found = true
			}
		}
	}
	if !found {
		t.Fatal("history Markdown did not preserve image node")
	}
	for _, b := range parseMarkdown(body) {
		for _, in := range b.inlines {
			if in.img != nil {
				t.Fatal("generic Markdown image behavior changed")
			}
		}
	}
	for _, b := range parseHistoryMarkdown(`<img src="x.png">`) {
		for _, in := range b.inlines {
			if in.img != nil {
				t.Fatal("history enabled raw HTML images")
			}
		}
	}
}

func TestPreviewHistoryMarkdownImages(t *testing.T) {
	if os.Getenv("POSSE_PREVIEW_DIR") == "" {
		t.Skip("preview directory required")
	}
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "image.png"), historyImagePNG(t, 420, 140), 0600); err != nil {
		t.Fatal(err)
	}
	for _, dark := range []bool{false, true} {
		a := previewApp(t)
		previewHistory(a, "")
		previewMarkdown(a)
		d := a.history.detail
		d.Session.Cwd = root
		d.Messages = []history.Message{{Role: "assistant", Text: "The preview reads the image from this session.\n\n![Session image](image.png)\n\nThe source remains available.", Seq: 1}}
		tt := newPreviewTester(a, dark)
		settle(tt)
		if d.imgs == nil {
			t.Fatal("history preview did not request image")
		}
		e := waitHistoryImage(t, d.imgs, "image.png")
		if e.err != nil {
			t.Fatal(e.err)
		}
		suffix := map[bool]string{false: "light", true: "dark"}[dark]
		savePNG(t, tt, "history-markdown-images-"+suffix+".png")
		d.closeImages()
	}
}
