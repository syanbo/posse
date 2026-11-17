package daemon

import (
	"os"
	"path/filepath"
	"testing"

	"posse/internal/media"
	"posse/internal/rpc"
)

func TestDesktopMediaParentAfterLink(t *testing.T) {
	root := t.TempDir()
	actual := filepath.Join(root, "other")
	if err := os.MkdirAll(filepath.Join(actual, "sub"), 0o700); err != nil {
		t.Fatal(err)
	}
	for path, text := range map[string]string{
		filepath.Join(actual, "image.png"): "target image",
		filepath.Join(root, "image.png"):   "wrong image",
	} {
		if err := os.WriteFile(path, []byte(text), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	link := filepath.Join(root, "link")
	if err := os.Symlink(filepath.Join(actual, "sub"), link); err != nil {
		t.Fatal(err)
	}
	d, _ := newTestDaemon(t)
	socket := desktopServer(t, d)
	viewer := rpc.DesktopIdentity{InstanceID: d.instanceID, ClientID: "viewer"}
	var out rpc.MediaPathResult
	if err := mediaTestCall(t.Context(), socket, viewer, "media.path", rpc.MediaPathParams{Item: media.Item{Path: link + "/../image.png"}}, &out); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(filepath.Clean(out.Path))
	if err != nil || string(data) != "target image" {
		t.Fatalf("resolved path %q: %q, %v", out.Path, data, err)
	}
	if err := mediaTestCall(t.Context(), socket, viewer, "media.path", rpc.MediaPathParams{Item: media.Item{Path: root + "/missing/../image.png"}}, nil); err == nil {
		t.Fatal("missing parent was cleaned before filesystem resolution")
	}
}
