package daemon

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"posse/internal/media"
	"posse/internal/rpc"
)

func TestDesktopMediaRewriteRecovers(t *testing.T) {
	d, _ := newTestDaemon(t)
	socket := desktopServer(t, d)
	viewer := rpc.DesktopIdentity{InstanceID: d.instanceID, ClientID: "viewer"}
	source, original := mediaTranscript(t, 1)
	body, err := os.ReadFile(source.TranscriptPath)
	if err != nil {
		t.Fatal(err)
	}
	body = append(body, []byte(`{"type":"assistant","message":{"content":"`+strings.Repeat("tail", 100)+`"}}`+"\n")...)
	if err := os.WriteFile(source.TranscriptPath, body, 0o600); err != nil {
		t.Fatal(err)
	}
	var before, after rpc.MediaListResult
	if err := mediaTestCall(t.Context(), socket, viewer, "media.list", source, &before); err != nil || len(before.Made) != 1 {
		t.Fatalf("before = %+v %v", before, err)
	}
	oldScanner := d.media.scanners[source].scanner
	params := rpc.MediaPathParams{Item: before.Made[0]}
	var oldPath, newPath rpc.MediaPathResult
	if err := mediaTestCall(t.Context(), socket, viewer, "media.path", params, &oldPath); err != nil {
		t.Fatal(err)
	}
	changed := bytes.Clone(original)
	changed[len(changed)-1] ^= 1
	updated := bytes.Replace(body, []byte(base64.StdEncoding.EncodeToString(original)), []byte(base64.StdEncoding.EncodeToString(changed)), 1)
	if len(updated) != len(body) || !bytes.Equal(updated[len(updated)-64:], body[len(body)-64:]) {
		t.Fatal("fixture changed size or tail")
	}
	if err := os.WriteFile(source.TranscriptPath, updated, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := mediaTestCall(t.Context(), socket, viewer, "media.path", params, nil); err == nil {
		t.Fatal("old image reference accepted")
	}
	if err := mediaTestCall(t.Context(), socket, viewer, "media.list", source, &after); err != nil || len(after.Made) != 1 {
		t.Fatalf("after = %+v %v", after, err)
	}
	if after.Made[0].Key == before.Made[0].Key || d.media.scanners[source].scanner == oldScanner {
		t.Fatal("stale scanner survived image identity mismatch")
	}
	if err := mediaTestCall(t.Context(), socket, viewer, "media.path", rpc.MediaPathParams{Item: after.Made[0]}, &newPath); err != nil {
		t.Fatal(err)
	}
	if got, err := os.ReadFile(newPath.Path); err != nil || !bytes.Equal(got, changed) || newPath == oldPath {
		t.Fatalf("new image = %x %+v %v", got, newPath, err)
	}
}

func TestDesktopMediaDiskIdentity(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	dir := filepath.Join(home, ".codex", "generated_images", "test-session")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	imagePath, videoPath := filepath.Join(dir, "image.png"), filepath.Join(home, "video.mp4")
	for _, path := range []string{imagePath, videoPath} {
		if err := os.WriteFile(path, []byte("same size"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	link := filepath.Join(dir, "linked.png")
	if err := os.Symlink(imagePath, link); err != nil {
		t.Fatal(err)
	}
	encoded, _ := json.Marshal(map[string]any{"type": "response_item", "payload": map[string]any{"type": "function_call_output", "output": videoPath}})
	transcript := filepath.Join(home, "session.jsonl")
	if err := os.WriteFile(transcript, append(encoded, '\n'), 0o600); err != nil {
		t.Fatal(err)
	}
	d, _ := newTestDaemon(t)
	socket := desktopServer(t, d)
	viewer := rpc.DesktopIdentity{InstanceID: d.instanceID, ClientID: "viewer"}
	source := rpc.MediaSource{Agent: "codex", SessionID: "test-session", TranscriptPath: transcript}
	list := func() map[string]media.Item {
		t.Helper()
		var result rpc.MediaListResult
		if err := mediaTestCall(t.Context(), socket, viewer, "media.list", source, &result); err != nil {
			t.Fatal(err)
		}
		items := make(map[string]media.Item)
		for _, it := range result.Made {
			items[it.Path] = it
		}
		if len(items) != 3 || !items[videoPath].Video {
			t.Fatalf("items = %+v", items)
		}
		return items
	}
	before := list()
	for _, path := range []string{imagePath, videoPath} {
		fi, err := os.Stat(path)
		if err != nil {
			t.Fatal(err)
		}
		replacement := path + ".new"
		if err := os.WriteFile(replacement, []byte("different"), 0o600); err != nil {
			t.Fatal(err)
		}
		if err := os.Chtimes(replacement, fi.ModTime(), fi.ModTime()); err != nil {
			t.Fatal(err)
		}
		if err := os.Rename(replacement, path); err != nil {
			t.Fatal(err)
		}
		changed, err := os.Stat(path)
		if err != nil || os.SameFile(fi, changed) || fi.Size() != changed.Size() || !fi.ModTime().Equal(changed.ModTime()) {
			t.Fatalf("replacement fixture: %v", err)
		}
	}
	after := list()
	for _, path := range []string{imagePath, videoPath, link} {
		if before[path].Key == after[path].Key {
			t.Fatalf("replacement identity unchanged: %s", path)
		}
	}
	other := filepath.Join(home, "other.png")
	fi, _ := os.Stat(imagePath)
	if err := os.WriteFile(other, []byte("different"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(other, fi.ModTime(), fi.ModTime()); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(link); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(other, link); err != nil {
		t.Fatal(err)
	}
	if list()[link].Key == after[link].Key {
		t.Fatal("symlink target identity unchanged")
	}
}
