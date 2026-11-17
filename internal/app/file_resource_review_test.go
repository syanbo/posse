package app

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"image"
	"image/png"
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/egoist/mygo/ui/editor"
	"posse/internal/files"
	"posse/internal/rpc"
)

func imageResourceVersion(path string, data []byte, stamp files.Stamp) files.Version {
	sum := sha256.Sum256(data)
	return files.Version{Path: path, Stamp: stamp, Hash: hex.EncodeToString(sum[:])}
}

func TestDocumentReadInvalidationKeepsPolling(t *testing.T) {
	for _, mode := range []string{"save", "move"} {
		t.Run(mode, func(t *testing.T) {
			root := t.TempDir()
			path := filepath.Join(root, "file.txt")
			if err := os.WriteFile(path, []byte("before"), 0600); err != nil {
				t.Fatal(err)
			}
			version, err := files.ReadVersion(context.Background(), path)
			if err != nil {
				t.Fatal(err)
			}
			d := &fileDoc{path: path, kind: docText, base: version.Stamp, seen: version.Stamp, baseVersion: version, ed: editor.State{Buffer: editor.NewBuffer("before")}}
			v := &filesView{wsID: "w", root: root, tree: files.NewTree(root), docs: []*fileDoc{d}, cur: d}
			a := &app{home: t.TempDir(), files: map[string]*filesView{"w": v}}
			a.loadDoc(d)
			if mode == "save" {
				d.ed.Buffer.Set("saved")
				if !waitSave(t, []*app{a}, func(done func(bool)) { a.saveDoc(appL(), d, done) }) {
					t.Fatal("save failed")
				}
			} else {
				nextRoot := t.TempDir()
				next := &filesView{wsID: "w", root: nextRoot, tree: files.NewTree(nextRoot)}
				if err := os.Rename(path, filepath.Join(nextRoot, "file.txt")); err != nil {
					t.Fatal(err)
				}
				a.carryDocs(&workspace{ID: "w", Path: nextRoot}, v, next)
				v, a.files["w"] = next, next
			}
			if d.loadCancel != nil {
				t.Fatal("invalidated read still blocks polling")
			}
			if err := os.WriteFile(d.path, []byte("external change"), 0600); err != nil {
				t.Fatal(err)
			}
			a.pollFileChanges(v)
			pumpSaves(t, []*app{a}, func() bool { return d.ed.Buffer.String() == "external change" })
		})
	}
}

func TestTargetImageDNS(t *testing.T) {
	for _, ip := range []string{"127.0.0.1", "::1", "::ffff:127.0.0.1", "0.0.0.0", "::"} {
		t.Run(ip, func(t *testing.T) {
			lookups, dials := 0, 0
			dial := targetImageDialer(func(context.Context, string) ([]net.IPAddr, error) {
				lookups++
				return []net.IPAddr{{IP: net.ParseIP(ip)}}, nil
			}, func(context.Context, string, string) (net.Conn, error) {
				dials++
				return nil, errors.New("unexpected connection")
			})
			if _, err := dial(context.Background(), "tcp", "image.example:8080"); err == nil || !strings.Contains(err.Error(), "files.httpResource") || lookups != 1 || dials != 0 {
				t.Fatalf("loopback DNS accepted: lookups=%d dials=%d err=%v", lookups, dials, err)
			}
		})
	}
	lookups := 0
	var addresses []string
	var overallDeadline time.Time
	dial := targetImageDialer(func(ctx context.Context, _ string) ([]net.IPAddr, error) {
		overallDeadline, _ = ctx.Deadline()
		lookups++
		return []net.IPAddr{{IP: net.ParseIP("203.0.113.7")}, {IP: net.ParseIP("2001:db8::7")}}, nil
	}, func(ctx context.Context, _ string, address string) (net.Conn, error) {
		if len(addresses) == 0 {
			if deadline, ok := ctx.Deadline(); !ok || !deadline.Before(overallDeadline) {
				t.Error("first address can exhaust the entire deadline")
			}
		}
		addresses = append(addresses, address)
		return nil, errors.New("connection refused")
	})
	_, _ = dial(context.Background(), "tcp", "image.example:443")
	if lookups != 1 || len(addresses) != 2 || addresses[0] != "203.0.113.7:443" || addresses[1] != "[2001:db8::7]:443" {
		t.Fatalf("dial did not use checked addresses: lookups=%d addresses=%v", lookups, addresses)
	}
}

func TestTargetImageCloseCancelsRead(t *testing.T) {
	a, f, local, _ := remoteFileFixture(t, "target")
	path := filepath.Join(filepath.Dir(local), "image.png")
	d := &fileDoc{path: path, kind: docLoading, started: true}
	v := &filesView{root: filepath.Dir(path), wsID: "w", tree: files.NewTree(filepath.Dir(path)), docs: []*fileDoc{d}, cur: d}
	entered, cancelled := make(chan struct{}), make(chan struct{})
	f.mu.Lock()
	f.desktopCall = func(ctx context.Context, p rpc.DesktopCall) (any, error) {
		close(entered)
		<-ctx.Done()
		close(cancelled)
		return nil, ctx.Err()
	}
	f.mu.Unlock()
	a.loadDoc(d)
	select {
	case <-entered:
	case <-time.After(5 * time.Second):
		t.Fatal("image request did not start")
	}
	a.dropDoc(v, d)
	select {
	case <-cancelled:
	case <-time.After(5 * time.Second):
		t.Fatal("closed image kept its remote request")
	}
	waitFileUpdate(t, a)
	a.runPending()
	if len(v.docs) != 0 || d.img != nil || d.kind != docLoading || d.loadCancel != nil {
		t.Fatal("closed image accepted a late result")
	}
}

func TestTargetImageReloadRetries(t *testing.T) {
	a, f, local, _ := remoteFileFixture(t, "target")
	path := filepath.Join(filepath.Dir(local), "image.png")
	var encoded bytes.Buffer
	if err := png.Encode(&encoded, image.NewRGBA(image.Rect(0, 0, 3, 2))); err != nil {
		t.Fatal(err)
	}
	old := files.Stamp{Ino: 7, Mode: 0600}
	current := files.Stamp{Ino: 42, Size: int64(encoded.Len()), Mode: 0600}
	dirStamp := files.Stamp{Mode: os.ModeDir | 0700, Ino: 1}
	d := &fileDoc{path: path, kind: docImage, base: old, seen: old}
	v := &filesView{root: filepath.Dir(path), wsID: "w", tree: files.NewTree(filepath.Dir(path)), docs: []*fileDoc{d}, cur: d}
	v.tree.Set(v.root, nil, dirStamp, nil)
	a.files = map[string]*filesView{"w": v}
	var fail atomic.Bool
	fail.Store(true)
	var reads atomic.Int32
	f.mu.Lock()
	f.desktopCall = func(_ context.Context, p rpc.DesktopCall) (any, error) {
		switch p.Method {
		case "files.stat":
			var args rpc.FilePathsParams
			if err := json.Unmarshal(p.Params, &args); err != nil {
				return nil, err
			}
			var out []rpc.FileStatResult
			for _, p := range args.Paths {
				st := current
				if p == v.root {
					st = dirStamp
				}
				out = append(out, rpc.FileStatResult{Path: p, Stamp: st})
			}
			return out, nil
		case "files.resourceRange":
			reads.Add(1)
			if fail.Load() {
				return nil, rpc.Errorf(rpc.CodeInternal, "temporary read failure")
			}
			return rpc.FileRangeResult{Data: encoded.Bytes(), Stamp: current, EOF: true}, nil
		case "files.resourceVersion":
			return imageResourceVersion(path, encoded.Bytes(), current), nil
		}
		return nil, errors.New("unexpected method")
	}
	f.mu.Unlock()
	a.pollFileChanges(v)
	pumpSaves(t, []*app{a}, func() bool { return a.err != "" && d.loadCancel == nil })
	if !d.seen.Same(old) || d.kind != docImage || reads.Load() != 1 {
		t.Fatal("failed reload acknowledged unseen image version")
	}
	fail.Store(false)
	a.pollFileChanges(v)
	pumpSaves(t, []*app{a}, func() bool { return d.seen.Same(current) })
	if reads.Load() != 2 || d.imgInfo.w != 3 || d.imgInfo.h != 2 {
		t.Fatal("poll did not retry failed image version")
	}
}
