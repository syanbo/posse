//go:build !js

package mygo

// Posse: Update.Install downloads and replaces the app in one step. Posse
// downloads in the background but replaces the app only when the user
// restarts it: on macOS a process whose bundle was replaced loses the
// privacy permissions it had not used yet (Desktop, Documents…), and Posse's
// daemon runs from the same bundle.

import (
	"context"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"runtime"
	"sync"

	"github.com/egoist/mygo/internal/update"
)

// Configured reports whether this build checks for updates at all: a
// production build with updates in mygo.json. Enabled also needs the app to
// be able to write where it is installed.
func (u *UpdaterModule) Configured() bool {
	return packageUpdateFeed != "" && packageUpdateKey != "" && !IsDev()
}

// Staged is an update downloaded next to the app, which Apply puts in its
// place.
type Staged struct {
	// Version and Notes of the update.
	Version string
	Notes   string

	mu       sync.Mutex
	work     string
	unpacked string
	target   string
}

// Stage downloads the update, checks that it is signed with the app's key
// and unpacks it next to the app, which keeps running unchanged. Like
// Install, it downloads the delta for the running version when there is
// one. Discard the result when it is not applied.
func (up *Update) Stage(ctx context.Context, progress func(downloaded, total int64)) (*Staged, error) {
	target, err := installTarget()
	if err != nil {
		return nil, err
	}
	work, err := updateWorkDir(target)
	if err != nil {
		return nil, fmt.Errorf("mygo: staging the update: %w", err)
	}
	get := httpGet
	if up.source != nil {
		get = up.source.Open
	}
	unpacked, err := stageUpdate(ctx, get, up.manifest, up.key, up.current, target, work, progress)
	if err != nil {
		os.RemoveAll(work)
		return nil, fmt.Errorf("mygo: staging the update: %w", err)
	}
	os.Remove(filepath.Join(work, "update.delta"))
	return &Staged{Version: up.Version, Notes: up.Notes, work: work, unpacked: unpacked, target: target}, nil
}

// Apply replaces the app with the staged update. The running app keeps its
// open files; the new version runs after a relaunch.
func (s *Staged) Apply() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.work == "" {
		return fmt.Errorf("mygo: the update of %s was applied or discarded", s.Version)
	}
	err := swapUpdate(s.unpacked, s.target)
	os.RemoveAll(s.work)
	s.work = ""
	if err != nil {
		return fmt.Errorf("mygo: installing the update: %w", err)
	}
	if runtime.GOOS == "linux" {
		if err := update.RefreshDesktopEntry(s.target, filepath.Base(startExe)); err != nil {
			log.Printf("mygo: registering the desktop entry of the update: %v", err)
		}
	}
	return nil
}

// Discard removes the staged update.
func (s *Staged) Discard() {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.work != "" {
		os.RemoveAll(s.work)
		s.work = ""
	}
}

// CleanStaged removes updates staged next to the app by earlier runs, and
// what an interrupted swap left behind. Call it before staging, while no
// other instance of the app stages one.
func (u *UpdaterModule) CleanStaged() {
	target, err := installTarget()
	if err != nil {
		return
	}
	base := "." + filepath.Base(target)
	for _, pattern := range []string{base + ".update-*", base + ".old-*"} {
		olds, _ := filepath.Glob(filepath.Join(filepath.Dir(target), pattern))
		for _, p := range olds {
			_ = os.RemoveAll(p)
		}
	}
}
