package daemon

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"posse/internal/rpc"
)

func TestWorkspaceDirectorySelection(t *testing.T) {
	for _, action := range []string{"add", "relocate"} {
		for _, relative := range []bool{false, true} {
			name := action + "/absolute"
			if relative {
				name = action + "/relative"
			}
			t.Run(name, func(t *testing.T) {
				d, _ := newTestDaemon(t)
				root, err := filepath.EvalSymlinks(t.TempDir())
				if err != nil {
					t.Fatal(err)
				}
				selected := filepath.Join(root, "actual", "project")
				for _, dir := range []string{filepath.Join(selected, "child"), filepath.Join(root, "alias"), filepath.Join(root, "original")} {
					if err := os.MkdirAll(dir, 0755); err != nil {
						t.Fatal(err)
					}
				}
				if err := os.Symlink(filepath.Join(selected, "child"), filepath.Join(root, "alias", "link")); err != nil {
					t.Fatal(err)
				}
				// 目录浏览和登记必须指向同一个对象，不能先把 link/.. 折叠成 alias。
				input := root + "/alias/link/.."
				listing := call(t, d.filesReadDirs, map[string]any{"paths": []string{input}}).([]rpc.FileDirectoryResult)
				if len(listing) != 1 || listing[0].Error != nil || len(listing[0].Entries) != 1 || listing[0].Entries[0].Name != "child" {
					t.Fatalf("selected directory listing: %+v", listing)
				}
				if relative {
					t.Chdir(root)
					input = "alias/link/.."
				}
				var project projectInfo
				if action == "add" {
					project = call(t, d.projectAdd, map[string]any{"path": input}).(projectInfo)
				} else {
					project = call(t, d.projectAdd, map[string]any{"path": filepath.Join(root, "original")}).(projectInfo)
					ws, err := d.store.Workspaces(project.ID)
					if err != nil || len(ws) != 1 {
						t.Fatalf("initial workspaces: %+v %v", ws, err)
					}
					call(t, d.workspaceRelocate, map[string]any{"workspace": ws[0].ID, "path": input})
				}
				stored, err := d.project(project.ID)
				if err != nil || !samePath(stored.Path, selected) {
					t.Fatalf("project differs from selected directory: %+v %v", stored, err)
				}
				ws, err := d.store.Workspaces(project.ID)
				if err != nil || len(ws) != 1 || !samePath(ws[0].Path, selected) {
					t.Fatalf("main workspace differs from selected directory: %+v %v", ws, err)
				}
			})
		}
	}
}

func TestProjectDirectoryPreservesLinkAndRejectsMissingParent(t *testing.T) {
	d, _ := newTestDaemon(t)
	root := t.TempDir()
	target, link := filepath.Join(root, "target"), filepath.Join(root, "link")
	if err := os.Mkdir(target, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, link); err != nil {
		t.Fatal(err)
	}
	project := call(t, d.projectAdd, map[string]any{"path": link}).(projectInfo)
	if project.Path != link || project.Name != "link" {
		t.Fatalf("ordinary symlink lost its display path: %+v", project)
	}
	raw, err := json.Marshal(projectAddParams{Path: root + "/missing/..", Name: "missing"})
	if err != nil {
		t.Fatal(err)
	}
	_, err = d.projectAdd(t.Context(), raw)
	desktopErrorCode(t, err, rpc.CodeBadRequest)
}
