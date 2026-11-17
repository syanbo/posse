package app

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"image"
	"image/png"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/egoist/mygo/ui/editor"
	"posse/internal/files"
	"posse/internal/rpc"
)

func TestPreviewTargetImage(t *testing.T) {
	if os.Getenv("POSSE_PREVIEW_DIR") == "" {
		t.Skip("preview directory required")
	}
	for _, dark := range []bool{false, true} {
		a := previewApp(t)
		previewImageDoc(t, a, 0)
		h, _ := requestTestApp(t, "remote")
		a.host, a.viewID = h.host, "remote"
		for _, v := range a.files {
			v.remote = true
		}
		suffix := map[bool]string{false: "light", true: "dark"}[dark]
		savePNG(t, newPreviewTester(a, dark), "target-image-"+suffix+".png")
	}
}

func TestTargetMarkdownRejectsLocalhost(t *testing.T) {
	a, _, _, _ := remoteFileFixture(t, "target")
	l := a.docImages(&fileDoc{path: "/repo/readme.md"}, "/repo")
	defer l.close()
	for _, src := range []string{"http://localhost/image.png", "http://preview.localhost:8080/image.png", "https://LOCALHOST./image.png", "http://127.0.0.1/image.png", "http://[::1]/image.png", "http://0.0.0.0/image.png"} {
		if _, _, err := l.read(src); err == nil || !strings.Contains(err.Error(), "files.httpResource") {
			t.Errorf("remote document did not reject local host before fetching: %s, %v", src, err)
		}
		req, err := http.NewRequest(http.MethodGet, src, nil)
		if err != nil {
			t.Fatal(err)
		}
		if err := mdTargetHTTP.CheckRedirect(req, nil); err == nil {
			t.Errorf("remote image redirected to local host: %s", src)
		}
	}
}

func TestTargetMarkdownCloseCancelsRead(t *testing.T) {
	a, f, _, _ := remoteFileFixture(t, "target")
	l := a.docImages(&fileDoc{path: "/repo/readme.md"}, "/repo")
	defer l.close()
	entered := make(chan struct{})
	f.mu.Lock()
	f.desktopCall = func(ctx context.Context, p rpc.DesktopCall) (any, error) {
		close(entered)
		<-ctx.Done()
		return nil, ctx.Err()
	}
	f.mu.Unlock()
	done := make(chan error, 1)
	go func() {
		_, _, err := l.read("image.png")
		done <- err
	}()
	select {
	case <-entered:
	case <-time.After(5 * time.Second):
		t.Fatal("image request did not start")
	}
	l.close()
	select {
	case err := <-done:
		if err == nil {
			t.Fatal("closed document accepted image")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("closed document kept waiting for image")
	}
}

func TestTargetImagePaths(t *testing.T) {
	for _, tc := range []struct {
		src, want string
	}{
		{"image.png", "docs/image.png"},
		{"../assets/image.png", "docs/../assets/image.png"},
		{"/assets/image.png", "assets/image.png"},
		{"/repo/assets/image.png", "assets/image.png"},
		{"file:///repo/assets/a%20b.png?raw=true#part", "assets/a b.png"},
		{"file:/repo/assets/image.png", "assets/image.png"},
		{"file:../assets/image.png", "docs/../assets/image.png"},
		{"link/../image.png", "docs/link/../image.png"},
		{"/repo/link/../image.png", "link/../image.png"},
	} {
		got, err := targetImagePath("/repo/docs", "/repo", tc.src)
		if err != nil || got != tc.want {
			t.Errorf("%q = %q, %v; want %q", tc.src, got, err, tc.want)
		}
	}
	for _, src := range []string{"../../outside.png", "file:///outside.png", "file://another/repo/image.png", "~/image.png", "bad%ZZ.png", "bad%00.png", "/repo/../outside.png"} {
		if _, err := targetImagePath("/repo/docs", "/repo", src); err == nil {
			t.Errorf("accepted %q", src)
		}
	}
}

func TestTargetImageDocument(t *testing.T) {
	for _, mode := range []string{"success", "stale", "failure"} {
		t.Run(mode, func(t *testing.T) {
			a, f, local, _ := remoteFileFixture(t, "target")
			path := filepath.Join(filepath.Dir(local), "image.png")
			if err := os.WriteFile(path, []byte("local contents"), 0600); err != nil {
				t.Fatal(err)
			}
			var encoded bytes.Buffer
			if err := png.Encode(&encoded, image.NewRGBA(image.Rect(0, 0, 3, 2))); err != nil {
				t.Fatal(err)
			}
			st := files.Stamp{Ino: 42, Mode: 0600, Size: int64(encoded.Len())}
			entered, unblock := make(chan struct{}), make(chan struct{})
			var once sync.Once
			defer once.Do(func() { close(unblock) })
			f.mu.Lock()
			f.desktopCall = func(ctx context.Context, p rpc.DesktopCall) (any, error) {
				if p.Method == "files.resourceVersion" {
					return imageResourceVersion(path, encoded.Bytes(), st), nil
				}
				if p.Method != "files.resourceRange" {
					return nil, errors.New("unexpected method")
				}
				var args rpc.FileResourceRangeParams
				if err := json.Unmarshal(p.Params, &args); err != nil {
					return nil, err
				}
				if args.Root != "/" || args.Path != strings.TrimPrefix(path, "/") {
					t.Error("image lost its explicit target path")
				}
				if mode == "stale" {
					close(entered)
					select {
					case <-unblock:
					case <-ctx.Done():
						return nil, ctx.Err()
					}
				}
				if mode == "failure" {
					return nil, rpc.Errorf(rpc.CodePermissionDenied, "denied")
				}
				return rpc.FileRangeResult{Data: encoded.Bytes(), Stamp: st, EOF: true}, nil
			}
			f.mu.Unlock()
			d := &fileDoc{path: path, kind: docLoading, started: true}
			if mode == "failure" {
				d.kind, d.seen = docImage, files.Stamp{Ino: 7}
			}
			a.loadDoc(d)
			if mode == "stale" {
				select {
				case <-entered:
				case <-time.After(5 * time.Second):
					t.Fatal("image request did not start")
				}
				takeRequestTestControl(t, a)
				once.Do(func() { close(unblock) })
			}
			waitFileUpdate(t, a)
			a.runPending()
			switch mode {
			case "success":
				if d.kind != docImage || d.imgInfo.w != 3 || d.imgInfo.h != 2 || !d.base.Same(st) {
					t.Fatalf("target image not decoded: kind=%v info=%+v err=%s", d.kind, d.imgInfo, d.err)
				}
			case "stale":
				if d.kind != docLoading || d.img != nil {
					t.Fatal("stale image result replaced document")
				}
			case "failure":
				if d.kind != docImage || d.seen.Ino != 7 || a.err == "" {
					t.Fatal("failed reload discarded the previous image")
				}
			}
			if data, err := os.ReadFile(path); err != nil || string(data) != "local contents" {
				t.Fatal("target image read touched local file")
			}
		})
	}
}

func TestTargetMarkdownResource(t *testing.T) {
	a, f, _, _ := remoteFileFixture(t, "target")
	d := &fileDoc{path: "/repo/docs/readme.md", ed: editor.State{Buffer: editor.NewBuffer("before")}}
	l := a.docImages(d, "/repo")
	defer func() { d.mdImgs.close() }()
	f.mu.Lock()
	f.desktopCall = func(_ context.Context, p rpc.DesktopCall) (any, error) {
		var args rpc.FileResourceRangeParams
		if err := json.Unmarshal(p.Params, &args); err != nil {
			return nil, err
		}
		if (p.Method != "files.resourceRange" && p.Method != "files.resourceVersion") || args.Root != "/repo" || args.Path != "docs/../assets/image.png" {
			t.Errorf("wrong resource request: %s %+v", p.Method, args)
		}
		st := files.Stamp{Size: 6, Mode: 0600}
		if p.Method == "files.resourceVersion" {
			return imageResourceVersion("/repo/assets/image.png", []byte("target"), st), nil
		}
		return rpc.FileRangeResult{Data: []byte("target"), Stamp: st, EOF: true}, nil
	}
	f.mu.Unlock()
	data, name, err := l.read("../assets/image.png?raw=true#part")
	if err != nil || string(data) != "target" || name != "docs/../assets/image.png" {
		t.Fatalf("target markdown resource = %q %q %v", data, name, err)
	}
	takeRequestTestControl(t, a)
	if _, _, err := l.read("../assets/image.png"); !errors.Is(err, context.Canceled) {
		t.Fatalf("old image loader reused new identity: %v", err)
	}
	next := a.docImages(d, "/repo")
	if next == l {
		t.Fatal("new identity reused obsolete image cache")
	}
	select {
	case <-l.ctx.Done():
	default:
		t.Fatal("obsolete image loader was not cancelled")
	}
	d.ed.Buffer.Set("after")
	latest := a.docImages(d, "/repo")
	if latest == next {
		t.Fatal("changed document reused old resource cache")
	}
	select {
	case <-next.ctx.Done():
	default:
		t.Fatal("changed document did not cancel old resource reads")
	}
	latest.close()
	if _, _, err := latest.read("../assets/image.png"); err == nil {
		t.Fatal("closed document fetched image")
	}
}
