//go:build darwin

package desk

import (
	"io"
	"sync"
	"testing"
)

func TestLatestCaptureTargetWinsQueuedRebuilds(t *testing.T) {
	for _, lastWindow := range []bool{false, true} {
		t.Run(map[bool]string{false: "restore window then display", true: "display then window"}[lastWindow], func(t *testing.T) {
			e := resolutionTestEngine(800, 600, 1920, io.Discard)
			e.width, e.height, e.fullW, e.fullH = 800, 600, 800, 600
			e.picture.Store(pictureSetting(720, 1))
			e.locked.Store(true)
			e.lockWin = 55
			var wg sync.WaitGroup
			queued := func() {
				defer wg.Done()
				e.streamMu.Lock()
				defer e.streamMu.Unlock()
				if _, err := e.applyTargetLocked(); err != nil {
					t.Error(err)
				}
			}
			e.streamMu.Lock()
			e.target.Store(&captureTarget{window: !lastWindow, id: 1})
			wg.Add(1)
			go queued()
			id := uint32(1)
			if lastWindow {
				id = 77
			}
			final := &captureTarget{window: lastWindow, id: id}
			e.target.Store(final)
			wg.Add(1)
			go queued()
			e.streamMu.Unlock()
			wg.Wait()
			if e.appliedTarget != final {
				t.Fatal("older queued target overwrote the latest intent")
			}
			ds := e.displaysLocked(nil)
			if lastWindow {
				if ds.RestoreWindow != 77 {
					t.Fatalf("lost locked window: %+v", ds)
				}
			} else if ds.RestoreWindow != 0 {
				t.Fatalf("explicit display left a window to restore: %+v", ds)
			}
		})
	}
}

func TestResolutionRebuildNotifiesOnlyTargetChanges(t *testing.T) {
	e := &engine{}
	e.shown.Store(1)
	e.setWatched(watched{Window: Window{ID: 77}, pw: 800, ph: 600})
	var notifications []Displays
	notify := func() { notifications = append(notifications, e.displaysLocked(nil)) }
	// 模拟重建调用中的窗口失效回退，检查真正发给父进程的 gone 和窗口状态。
	err := e.rebuildWatching(func() error { e.setWatched(watched{}); e.gone.Store(true); return nil }, notify, false)
	if err != nil || len(notifications) != 1 || !notifications[0].Gone || notifications[0].Window != nil {
		t.Fatalf("missing fallback notification: %+v %v", notifications, err)
	}
	e.setWatched(watched{Window: Window{ID: 77}, pw: 800, ph: 600})
	notifications = nil
	e.rebuildWatching(func() error { e.setWatched(watched{Window: Window{ID: 77}, pw: 1200, ph: 900}); return nil }, notify, false)
	if len(notifications) != 0 {
		t.Fatal("ordinary resolution switch reset the target")
	}
	// 既有窗口轮询仍能通知尺寸变化。
	e.rebuildWatching(func() error { e.setWatched(watched{Window: Window{ID: 77}, pw: 1600, ph: 1200}); return nil }, notify, true)
	if len(notifications) != 1 || notifications[0].Gone {
		t.Fatalf("normal window resize: %+v", notifications)
	}
}
