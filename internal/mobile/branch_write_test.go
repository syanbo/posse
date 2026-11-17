package mobile

import (
	"context"
	"os"
	"os/exec"
	"testing"

	"posse/internal/gitx"
	"posse/internal/term"
)

func TestGitCheckoutCoordinatedState(t *testing.T) {
	dir := t.TempDir()
	git := func(args ...string) {
		t.Helper()
		cmd := exec.Command("git", args...)
		cmd.Dir = dir
		cmd.Env = append(os.Environ(), "GIT_TRACE2_EVENT=0", "GIT_AUTHOR_NAME=Test", "GIT_AUTHOR_EMAIL=test@example.invalid", "GIT_COMMITTER_NAME=Test", "GIT_COMMITTER_EMAIL=test@example.invalid")
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v %s", args, err, out)
		}
	}
	git("init", "-q", "-b", "main")
	git("commit", "-q", "--allow-empty", "-m", "initial")
	git("branch", "side")
	h := &fakeHost{terms: map[string]*term.Terminal{}, dir: dir}
	srv, err := New(t.TempDir(), h, -1)
	if err != nil {
		t.Fatal(err)
	}
	srv.port = 0
	defer srv.Close()
	info, err := srv.Pair(false, "127.0.0.1")
	if err != nil {
		t.Fatal(err)
	}
	phone := dialPhone(t, srv, info)
	result(t, phone.next(phone.call("runtime.clientCapabilities.update", map[string]any{"client": "posse-flutter", "version": 1, "capabilities": []string{"posse.git.v1", "posse.git.v2", "posse.git.v3"}})))
	for _, mode := range []string{"already-switched", "working", "denied", "moved"} {
		t.Run(mode, func(t *testing.T) {
			git("switch", "-q", "main")
			released := make(chan struct{})
			h.mu.Lock()
			h.working, h.dir = "", dir
			h.gitWrite = func(ctx context.Context, target GitWriteTarget) (context.Context, func(), error) {
				if target.WorkspaceID != "w1" || target.WorkspacePath != dir || target.Root == "" {
					return nil, nil, errorf("bad_params", "wrong target")
				}
				switch mode {
				case "already-switched":
					cmd := exec.CommandContext(ctx, "git", "switch", "-q", "side")
					cmd.Dir = dir
					cmd.Env = append(os.Environ(), "GIT_TRACE2_EVENT=0")
					if err := cmd.Run(); err != nil {
						return nil, nil, err
					}
				case "working":
					h.mu.Lock()
					h.working = "codex"
					h.mu.Unlock()
				case "denied":
					return nil, nil, errorf("busy", "workspace writes are blocked during handoff")
				case "moved":
					h.mu.Lock()
					h.dir = dir + "-moved"
					h.mu.Unlock()
				}
				return ctx, func() { close(released) }, nil
			}
			h.mu.Unlock()
			res := phone.next(phone.call("git.checkout", map[string]any{"worktree": "id:p1::w1", "repo": ".", "name": "side"}))
			if mode == "denied" || mode == "moved" {
				if res["ok"] != false {
					t.Fatalf("invalid write accepted: %v", res)
				}
			} else {
				out := result(t, res)
				if out["switched"] != false || mode == "working" && out["confirm"] != "codex" {
					t.Fatalf("stale state used: %v", out)
				}
			}
			h.mu.Lock()
			n := len(h.gitRecs)
			h.mu.Unlock()
			if n != 0 {
				t.Fatal("unexpected checkout or Git record")
			}
			want := "main"
			if mode == "already-switched" {
				want = "side"
			}
			if head, _ := gitx.HeadRef(dir); head != want {
				t.Fatalf("head = %s, want %s", head, want)
			}
			if mode != "denied" {
				<-released
			}
		})
	}
}
