package gitx

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
)

func pushReadRepo(t *testing.T, tracking bool) string {
	t.Helper()
	root := t.TempDir()
	initRepo(t, root)
	gitIn(t, root, "remote", "add", "origin", "https://example.invalid/repo")
	if tracking {
		oid := gitIn(t, root, "rev-parse", "HEAD")
		gitIn(t, root, "update-ref", "refs/remotes/origin/main", oid)
		gitIn(t, root, "config", "branch.main.remote", "origin")
		gitIn(t, root, "config", "branch.main.merge", "refs/heads/main")
	}
	gitIn(t, root, "commit", "--allow-empty", "-qm", "ahead")
	return root
}
func comparePushNative(t *testing.T, root, branch string) PushPreviewRead {
	t.Helper()
	old, err := PushPreview(t.Context(), root, branch)
	if err != nil {
		t.Fatal(err)
	}
	got, err := ReadPushPreviewChecked(historyContext(t), root, branch, 500)
	if err != nil {
		t.Fatal(err)
	}
	a, b := old, got.Plan
	a.Commits = nil
	b.Commits = nil
	if !reflect.DeepEqual(a, b) {
		t.Fatalf("old %+v target %+v", a, b)
	}
	if len(old.Commits) != len(got.Plan.Commits) {
		t.Fatalf("old commits %d target %d", len(old.Commits), len(got.Plan.Commits))
	}
	for i := range old.Commits {
		if old.Commits[i].Hash != got.Plan.Commits[i].Hash {
			t.Fatal("commits differ")
		}
	}
	return got
}
func TestPushReadTargets(t *testing.T) {
	root := pushReadRepo(t, true)
	comparePushNative(t, root, "main")
	gitIn(t, root, "remote", "add", "fork", "https://example.invalid/fork")
	gitIn(t, root, "config", "remote.pushDefault", "fork")
	got := comparePushNative(t, root, "main")
	if got.Plan.Remote != "fork" || got.Plan.SetUpstream || !got.TrackingMissing {
		t.Fatalf("fork %+v", got)
	}
	gitIn(t, root, "config", "branch.main.pushRemote", "origin")
	comparePushNative(t, root, "main")
	gitIn(t, root, "config", "--unset", "branch.main.pushRemote")
	gitIn(t, root, "config", "--unset", "remote.pushDefault")
	gitIn(t, root, "config", "branch.main.merge", "refs/heads/old-name")
	got = comparePushNative(t, root, "main")
	if !got.Plan.Gone || !got.Plan.SetUpstream || got.Plan.RemoteBranch != "old-name" {
		t.Fatalf("gone %+v", got)
	}
	gitIn(t, root, "config", "push.default", "current")
	comparePushNative(t, root, "main")
	gitIn(t, root, "config", "remote.origin.push", "refs/heads/main:refs/heads/destination")
	got = comparePushNative(t, root, "main")
	if got.Plan.RemoteBranch != "destination" {
		t.Fatalf("refspec %+v", got)
	}
	gitIn(t, root, "branch", "special'\"$;]雪")
	gitIn(t, root, "config", "--unset", "remote.origin.push")
	comparePushNative(t, root, "special'\"$;]雪")
}
func TestPushReadDefaultsAndMissing(t *testing.T) {
	noRemote := t.TempDir()
	initRepo(t, noRemote)
	if _, err := ReadPushPreviewChecked(historyContext(t), noRemote, "main", 500); err == nil || err.Error() != "this repository has no remote" {
		t.Fatalf("no remote %v", err)
	}
	for _, names := range [][]string{{"only"}, {"zeta", "alpha"}, {"zeta", "origin", "alpha"}} {
		t.Run(strings.Join(names, "-"), func(t *testing.T) {
			root := t.TempDir()
			initRepo(t, root)
			for _, name := range names {
				gitIn(t, root, "remote", "add", name, "https://example.invalid/repo")
			}
			got := comparePushNative(t, root, "main")
			if !got.TrackingMissing || !got.Plan.SetUpstream || got.TrackingOID != "" {
				t.Fatalf("new %+v", got)
			}
		})
	}
	root := pushReadRepo(t, true)
	gitIn(t, root, "update-ref", "-d", "refs/remotes/origin/main")
	got, err := ReadPushPreviewChecked(historyContext(t), root, "main", 500)
	if err != nil || !got.TrackingMissing || !got.Plan.Gone {
		t.Fatalf("missing %+v %v", got, err)
	}
	if err := os.MkdirAll(filepath.Join(root, ".git", "refs", "remotes", "origin"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, ".git", "refs", "remotes", "origin", "main"), []byte(strings.Repeat("a", 40)+"\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := ReadPushPreviewChecked(historyContext(t), root, "main", 500); err == nil {
		t.Fatal("bad object treated as new")
	}
}
func TestPushReadFormatsLimitsAndHooks(t *testing.T) {
	for _, format := range []string{"sha1", "sha256"} {
		t.Run(format, func(t *testing.T) {
			root := t.TempDir()
			gitIn(t, root, "init", "-b", "main", "--object-format="+format)
			gitIn(t, root, "config", "user.name", "Test")
			gitIn(t, root, "config", "user.email", "test@example.invalid")
			write(t, filepath.Join(root, "a"), "base")
			gitIn(t, root, "add", "a")
			gitIn(t, root, "commit", "-qm", "base")
			base := gitIn(t, root, "rev-parse", "HEAD")
			gitIn(t, root, "remote", "add", "origin", "https://example.invalid/repo")
			gitIn(t, root, "update-ref", "refs/remotes/origin/main", base)
			var input strings.Builder
			for i := 1; i <= 501; i++ {
				parent := base
				if i > 1 {
					parent = fmt.Sprintf(":%d", i-1)
				}
				fmt.Fprintf(&input, "commit refs/heads/main\nmark :%d\ncommitter Test <test@example.invalid> %d +0000\ndata 1\nx\nfrom %s\n\n", i, time.Now().Unix()+int64(i), parent)
			}
			if _, err := runRaw(t.Context(), root, []byte(input.String()), nil, "fast-import", "--quiet"); err != nil {
				t.Fatal(err)
			}
			marker := filepath.Join(t.TempDir(), "called")
			hooks := t.TempDir()
			if err := os.WriteFile(filepath.Join(hooks, "pre-push"), []byte("#!/bin/sh\ntouch '"+marker+"'\n"), 0700); err != nil {
				t.Fatal(err)
			}
			gitIn(t, root, "config", "core.hooksPath", hooks)
			gitIn(t, root, "config", "credential.helper", "!touch '"+marker+"'; exit 1")
			t.Setenv("GIT_SSH_COMMAND", "touch '"+marker+"'; exit 1")
			for _, branch := range []string{"main", "new"} {
				if branch == "new" {
					gitIn(t, root, "branch", branch)
				}
				got, err := ReadPushPreviewChecked(historyContext(t), root, branch, 500)
				if err != nil || !got.More || len(got.Plan.Commits) != 500 || len(got.BranchOID) != len(base) {
					t.Fatalf("%s commits=%d more=%v err=%v", branch, len(got.Plan.Commits), got.More, err)
				}
			}
			if _, err := os.Stat(marker); !os.IsNotExist(err) {
				t.Fatalf("hook/network helper invoked: %v", err)
			}
		})
	}
}

func TestPushReadConfigurationBoundaries(t *testing.T) {
	for name, configure := range map[string]func(*testing.T, string){
		"unknown-remote": func(t *testing.T, r string) { gitIn(t, r, "config", "remote.pushDefault", "unknown") },
		"empty-remote":   func(t *testing.T, r string) { gitIn(t, r, "config", "remote.pushDefault", "") },
		"multi-url": func(t *testing.T, r string) {
			gitIn(t, r, "config", "--add", "remote.origin.url", "https://secret:token@example.invalid/other")
		},
		"multi-pushurl": func(t *testing.T, r string) {
			gitIn(t, r, "config", "--add", "remote.origin.pushurl", "https://example.invalid/one")
			gitIn(t, r, "config", "--add", "remote.origin.pushurl", "https://secret:token@example.invalid/two")
		},
		"wildcard-push": func(t *testing.T, r string) { gitIn(t, r, "config", "remote.origin.push", "refs/heads/*:refs/heads/*") },
		"custom-fetch": func(t *testing.T, r string) {
			gitIn(t, r, "config", "remote.origin.fetch", "+refs/heads/*:refs/custom/*")
		},
		"unknown-default": func(t *testing.T, r string) {
			gitIn(t, r, "config", "push.default", "https://secret:token@example.invalid/repo")
		},
		"mirror": func(t *testing.T, r string) { gitIn(t, r, "config", "remote.origin.mirror", "true") },
	} {
		t.Run(name, func(t *testing.T) {
			root := pushReadRepo(t, true)
			configure(t, root)
			_, err := ReadPushPreviewChecked(historyContext(t), root, "main", 500)
			if err == nil || strings.Contains(err.Error(), "secret") || strings.Contains(err.Error(), "token") {
				t.Fatalf("error %v", err)
			}
		})
	}
	root := pushReadRepo(t, true)
	for _, branch := range []string{"--all", "HEAD", "main~1", "main..other", "missing"} {
		if _, err := ReadPushPreviewChecked(historyContext(t), root, branch, 500); err == nil {
			t.Fatalf("accepted %s", branch)
		}
	}
	gitIn(t, root, "config", "push.default", "https://secret:token@example.invalid/repo")
	if _, err := ReadPushPreviewChecked(WithReadLimit(t.Context(), 1), root, "main", 500); !errors.Is(err, ErrReadLimit) {
		t.Fatalf("budget %v", err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if _, err := ReadPushPreviewChecked(WithReadLimit(ctx, 4<<20), root, "main", 500); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancel %v", err)
	}
}
