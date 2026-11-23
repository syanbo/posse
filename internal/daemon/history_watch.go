package daemon

import (
	"context"
	"log"
	"os"
	"sync"
	"time"

	"github.com/egoist/mygo"

	"posse/internal/history"
)

// historyPageGrace 比窗口的一秒轮询长：漏一拍状态也不把目录监视停掉。
const historyPageGrace = 4 * time.Second

// historyWatchDebounce 是目录变化攒一攒再索引的时间，避免一次保存连着触发。
var historyWatchDebounce = 300 * time.Millisecond

type historyWatch struct {
	cancel context.CancelFunc
}

// noteHistoryPage 记下历史页还开着，并在第一次时开始监视会话目录。
func (d *Daemon) noteHistoryPage() {
	h := &d.history
	h.mu.Lock()
	h.pageUntil = time.Now().Add(historyPageGrace)
	start := h.watchStop == nil
	h.mu.Unlock()
	if start {
		go d.watchHistoryDirs()
	}
}

func (d *Daemon) historyGeneration() uint64 {
	d.history.mu.Lock()
	defer d.history.mu.Unlock()
	return d.history.gen
}

func (d *Daemon) historyWatchRoots() []string {
	d.history.mu.Lock()
	fn := d.history.watchRoots
	d.history.mu.Unlock()
	if fn != nil {
		return fn()
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return nil
	}
	return history.WatchDirs(home, d.zcodeDatabase())
}

// watchHistoryDirs 在历史页开着时盯会话目录。每个目录一个监视、不给每个文件建快照；
// 有变化就做一次增量索引，索引仍在子进程里。页面关上就停。
func (d *Daemon) watchHistoryDirs() {
	ctx, cancel := context.WithCancel(context.Background())
	stop := &historyWatch{cancel: cancel}
	h := &d.history
	h.mu.Lock()
	if h.watchStop != nil {
		h.mu.Unlock()
		cancel()
		return
	}
	h.watchStop = stop
	h.mu.Unlock()
	defer func() {
		cancel()
		h.mu.Lock()
		if h.watchStop == stop {
			h.watchStop = nil
		}
		h.mu.Unlock()
	}()

	events := make(chan struct{}, 1)
	var wg sync.WaitGroup
	for _, dir := range d.historyWatchRoots() {
		w, err := mygo.WatchFiles(ctx, dir, mygo.FileWatchOptions{
			Recursive: true,
			HintsOnly: true,
			Debounce:  historyWatchDebounce,
		})
		if err != nil {
			log.Printf("history watch %s: %v", dir, err)
			continue
		}
		wg.Add(1)
		go func(w *mygo.FileWatcher) {
			defer wg.Done()
			defer w.Close()
			for {
				if _, err := w.Next(ctx); err != nil {
					return
				}
				select {
				case events <- struct{}{}:
				default:
				}
			}
		}(w)
	}
	tick := time.NewTicker(time.Second)
	defer tick.Stop()
	for {
		select {
		case <-ctx.Done():
			wg.Wait()
			return
		case <-events:
			d.pokeHistory()
		case <-tick.C:
			h.mu.Lock()
			open := time.Now().Before(h.pageUntil)
			h.mu.Unlock()
			if !open {
				cancel()
				wg.Wait()
				return
			}
		}
	}
}

// pokeHistory 立刻做一轮增量索引。上一轮还在跑就记下来，做完再来一轮。
func (d *Daemon) pokeHistory() {
	d.refreshHistory(context.Background(), true, 0)
}
