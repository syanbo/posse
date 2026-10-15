package cli

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestRetryParams(t *testing.T) {
	old := taskInfo{ID: "k_1", Title: "fix login", Spec: "do it", Agent: "codex", WorkspaceID: "w_1", Status: "failed"}
	p, err := retryParams(old, "", "")
	if err != nil {
		t.Fatal(err)
	}
	if p["agent"] != "codex" || p["spec"] != "do it" || p["workspace"] != "w_1" || p["title"] != "fix login (retry)" {
		t.Fatalf("params %v", p)
	}
	// 再重试一次不会叠加后缀
	old.Title = "fix login (retry)"
	if p, _ := retryParams(old, "", ""); p["title"] != "fix login (retry)" {
		t.Fatalf("title %v", p["title"])
	}
	// 可以换一份说明
	f := filepath.Join(t.TempDir(), "spec.md")
	os.WriteFile(f, []byte("new spec"), 0o644)
	if p, _ := retryParams(old, "", f); p["spec"] != "new spec" {
		t.Fatalf("spec %v", p["spec"])
	}
	for _, status := range []string{"running", "done"} {
		old.Status = status
		if _, err := retryParams(old, "", ""); err == nil || !strings.Contains(err.Error(), "can be retried") {
			t.Errorf("%s task retried: %v", status, err)
		}
	}
	old.Status, old.Agent = "exited", ""
	if _, err := retryParams(old, "", ""); err == nil {
		t.Error("command task retried")
	}
}

// 正文是协调方当场要读的，保持短；命令表里有的子命令正文都要提到
func TestTaskGuide(t *testing.T) {
	if n := strings.Count(taskGuide, "\n"); n > 80 {
		t.Fatalf("guide is %d lines", n)
	}
	for _, sub := range []string{"start", "wait", "inbox", "reply", "send", "retry", "close", "list", "show"} {
		if !strings.Contains(taskGuide, "hive task "+sub) {
			t.Errorf("guide does not mention hive task %s", sub)
		}
	}
}
