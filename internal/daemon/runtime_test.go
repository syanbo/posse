package daemon

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"
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
