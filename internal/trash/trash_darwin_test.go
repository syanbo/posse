//go:build darwin

package trash_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"posse/internal/trash"
)

func TestMoveFoundation(t *testing.T) {
	for _, directory := range []bool{false, true} {
		name := "file"
		if directory {
			name = "directory"
		}
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			root := t.TempDir()
			source := filepath.Join(root, "posse-trash-"+filepath.Base(root)+"-"+name+" café")
			contentPath := source
			if directory {
				if err := os.Mkdir(source, 0700); err != nil {
					t.Fatal(err)
				}
				contentPath = filepath.Join(source, "payload.txt")
			}
			content := "trash test payload: " + source
			if err := os.WriteFile(contentPath, []byte(content), 0600); err != nil {
				t.Fatal(err)
			}
			destination, err := trash.Move(source)
			if err != nil {
				if got, readErr := os.ReadFile(contentPath); readErr != nil || string(got) != content {
					t.Errorf("failed move changed source: content %q, error %v", got, readErr)
				}
				t.Fatalf("Foundation move failed: %v", err)
			}
			if !filepath.IsAbs(destination) || destination == source {
				t.Fatalf("invalid destination %q", destination)
			}
			inTrash := false
			for _, part := range strings.Split(filepath.Dir(destination), string(filepath.Separator)) {
				if part == ".Trash" || part == ".Trashes" {
					inTrash = true
				}
			}
			if !inTrash {
				t.Fatalf("destination is not in a trash directory: %q", destination)
			}
			movedContent := destination
			if directory {
				movedContent = filepath.Join(destination, "payload.txt")
			}
			if got, err := os.ReadFile(movedContent); err != nil || string(got) != content {
				t.Fatalf("destination content mismatch; leaving %q untouched: content %q, error %v", destination, got, err)
			}
			// 只删除本次返回且内容已确认的对象；目录用 Remove，避免递归碰到其他内容。
			t.Cleanup(func() {
				if got, err := os.ReadFile(movedContent); err != nil || string(got) != content {
					t.Errorf("destination changed; leaving %q untouched", destination)
					return
				}
				if err := os.Remove(movedContent); err != nil {
					t.Errorf("remove test payload: %v", err)
					return
				}
				if directory {
					if err := os.Remove(destination); err != nil {
						t.Errorf("remove test directory: %v", err)
					}
				}
			})
			if _, err := os.Lstat(source); !os.IsNotExist(err) {
				t.Fatalf("source still exists or cannot be checked: %v", err)
			}
		})
	}
}

func TestMoveFoundationFailurePreservesSource(t *testing.T) {
	source := filepath.Join(t.TempDir(), "source.txt")
	const content = "source must survive Foundation errors"
	if err := os.WriteFile(source, []byte(content), 0600); err != nil {
		t.Fatal(err)
	}
	// 文件下不可能有子项，确保错误来自 Foundation，同时能核实已有内容未被改动。
	destination, err := trash.Move(filepath.Join(source, "missing"))
	if err == nil || destination != "" {
		t.Fatalf("Move = %q, %v; want empty destination and error", destination, err)
	}
	if got, err := os.ReadFile(source); err != nil || string(got) != content {
		t.Fatalf("source changed: content %q, error %v", got, err)
	}
}
