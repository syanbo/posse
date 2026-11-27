package daemon

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"golang.org/x/sys/unix"

	"posse/internal/spawn"
)

// 终端记着拉起它的副本，交接后新进程照样知道：回收副本时要留着它。
func TestHandoffCarriesRuntime(t *testing.T) {
	d, w := newTestDaemon(t)
	d.runtime = "/rt/old"
	s, err := d.startTerminal(w, []string{"/bin/sh", "-c", "sleep 30"}, "", 80, 24)
	if err != nil {
		t.Fatal(err)
	}
	if s.runtime != "/rt/old" {
		t.Fatalf("runtime %q", s.runtime)
	}
	rec, f, err := s.Pause()
	if err != nil {
		t.Fatal(err)
	}
	b, err := json.Marshal(handoffBatch{Version: handoffVersion, Terms: []handoffTerm{{Term: rec, Runtime: s.runtime}}})
	if err != nil {
		t.Fatal(err)
	}
	var batch handoffBatch
	if err := json.Unmarshal(b, &batch); err != nil {
		t.Fatal(err)
	}

	d2, _ := newTestDaemon(t)
	d2.runtime = "/rt/new"
	if err := d2.adopt(&incoming{batch: batch, ptys: []*os.File{f}}); err != nil {
		t.Fatal(err)
	}
	got := d2.terms[s.ID]
	if got == nil || got.runtime != "/rt/old" {
		t.Fatalf("adopted runtime: %+v", got)
	}
	got.Resume()
	// 旧进程那边已经交出去了，只留新进程的
	d.mu.Lock()
	delete(d.terms, s.ID)
	d.mu.Unlock()
	if dirs := d2.liveRuntimes(""); len(dirs) != 1 || dirs[0] != "/rt/old" {
		t.Fatalf("live runtimes %v", dirs)
	}
	if dirs := d2.liveRuntimes(s.ID); len(dirs) != 0 {
		t.Fatalf("exited terminal still counted: %v", dirs)
	}
}

// 旧版本交来的记录没有 runtime：表示原包或不知道，不参与回收。
func TestHandoffRecordWithoutRuntime(t *testing.T) {
	var ht handoffTerm
	if err := json.Unmarshal([]byte(`{"term":{"id":"t_1"},"agent":"claude","hooked":true}`), &ht); err != nil {
		t.Fatal(err)
	}
	if ht.Runtime != "" || ht.Term.ID != "t_1" {
		t.Fatalf("%+v", ht)
	}
	b, _ := json.Marshal(handoffTerm{Term: ht.Term})
	var raw map[string]any
	json.Unmarshal(b, &raw)
	if _, ok := raw["runtime"]; ok {
		t.Fatalf("empty runtime written: %s", b)
	}
}

func TestLiveRuntimesSkipsExited(t *testing.T) {
	d, w := newTestDaemon(t)
	d.runtime = "/rt/a"
	s, err := d.startTerminal(w, []string{"/bin/sh", "-c", "exit 0"}, "", 80, 24)
	if err != nil {
		t.Fatal(err)
	}
	select {
	case <-s.Exited():
	case <-time.After(5 * time.Second):
		t.Fatal("terminal did not exit")
	}
	if dirs := d.liveRuntimes(""); len(dirs) != 0 {
		t.Fatalf("exited terminal still holds %v", dirs)
	}
}

func TestLinkPosse(t *testing.T) {
	home := t.TempDir()
	for _, exe := range []string{"/a/Posse.app/Contents/MacOS/Posse", "/b/bin/posse", "/b/bin/posse"} {
		link, err := linkPosse(home, exe)
		if err != nil {
			t.Fatal(err)
		}
		if link != filepath.Join(home, "bin", "posse") {
			t.Fatalf("link %s", link)
		}
		if got, _ := os.Readlink(link); got != exe {
			t.Fatalf("points at %s, want %s", got, exe)
		}
	}
	if entries, _ := os.ReadDir(filepath.Join(home, "bin")); len(entries) != 1 {
		t.Fatalf("leftovers: %v", entries)
	}
}

func TestActivateAdoptedServerSelfHealing(t *testing.T) {
	home := t.TempDir()
	tag := "0.1.55-abcdef123456"
	verDir := filepath.Join(home, "server", "versions", tag)
	if err := os.MkdirAll(verDir, 0o700); err != nil {
		t.Fatal(err)
	}
	exe := filepath.Join(verDir, "posse")
	if err := os.WriteFile(exe, []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(verDir, spawn.ServerManagedMarker), []byte(tag+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	// 1. 完整版（full）绝不触碰 server/current。
	dFull := &Daemon{cfg: Config{Home: home}, runtime: verDir}
	_ = dFull.activateAdoptedServerEdition(context.Background(), exe, "full")
	if _, err := os.Lstat(filepath.Join(home, "server", "current")); !os.IsNotExist(err) {
		t.Fatalf("full edition unexpectedly touched server/current: err=%v", err)
	}

	// 2. 非托管无头（runtime 为空）或外部非 server/versions 目录绝不触碰 server/current。
	dUnmanaged := &Daemon{cfg: Config{Home: home}, runtime: ""}
	_ = dUnmanaged.activateAdoptedServerEdition(context.Background(), exe, "headless")
	if _, err := os.Lstat(filepath.Join(home, "server", "current")); !os.IsNotExist(err) {
		t.Fatalf("unmanaged daemon unexpectedly touched server/current: err=%v", err)
	}
	fakeCopyDir := filepath.Join(t.TempDir(), "Posse.app", "Contents", "MacOS")
	if err := os.MkdirAll(fakeCopyDir, 0o700); err != nil {
		t.Fatal(err)
	}
	fakeCopyExe := filepath.Join(fakeCopyDir, "posse")
	if err := os.WriteFile(fakeCopyExe, []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	if tag := spawn.ServerVersionTag(fakeCopyExe, home); tag != "" {
		t.Fatalf("ServerVersionTag matched non-server path: %q", tag)
	}

	// 3. 持有 versions/.lock 且 ctx 取消时，ActivateServerVersionContext 立刻响应取消而非无限阻塞。
	lockFile, err := os.OpenFile(filepath.Join(home, "server", "versions", ".lock"), os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	if err := unix.Flock(int(lockFile.Fd()), unix.LOCK_EX); err != nil {
		lockFile.Close()
		t.Fatal(err)
	}
	cctx, ccancel := context.WithTimeout(context.Background(), 80*time.Millisecond)
	dLocked := &Daemon{cfg: Config{Home: home}, runtime: verDir}
	lockErr := dLocked.activateAdoptedServerEdition(cctx, exe, "headless")
	ccancel()
	_ = unix.Flock(int(lockFile.Fd()), unix.LOCK_UN)
	lockFile.Close()
	if lockErr == nil {
		t.Fatal("expected deadline error while versions/.lock was held")
	}

	// 4. 受管无头节点 adopt 成功后自愈 server/current -> versions/<tag>，且可安全幂等重复执行。
	dManaged := &Daemon{cfg: Config{Home: home}, runtime: verDir}
	if err := dManaged.activateAdoptedServerEdition(context.Background(), exe, "headless"); err != nil {
		t.Fatalf("activateAdoptedServerEdition failed: %v", err)
	}
	if err := dManaged.activateAdoptedServerEdition(context.Background(), exe, "headless"); err != nil {
		t.Fatalf("second idempotent activateAdoptedServerEdition failed: %v", err)
	}
	target, err := os.Readlink(filepath.Join(home, "server", "current"))
	if err != nil {
		t.Fatalf("server/current not activated: %v", err)
	}
	if target != filepath.Join("versions", tag) {
		t.Fatalf("server/current -> %q, want %q", target, filepath.Join("versions", tag))
	}
}
