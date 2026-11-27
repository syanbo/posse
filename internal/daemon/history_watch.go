package daemon

import (
	"context"
	"encoding/binary"
	"errors"
	"hash/fnv"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"time"

	"posse/internal/history"
)

// historyPageGrace 比窗口的一秒轮询长：漏一拍状态也不把目录监视停掉。
const historyPageGrace = 4 * time.Second

// historyWatchDebounce 是目录变化攒一攒再索引的时间，避免一次保存连着触发。
var historyWatchDebounce = 300 * time.Millisecond

const (
	historyWatchMaxEntries  = 25000
	historyWatchDefaultScan = time.Second
	historyWatchMaxBackoff  = 5 * time.Second
	historyWatchMaxDebounce = 2 * time.Second
)

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

// watchHistoryDirs 在历史页开着时按完成后的间隔扫描会话目录树摘要（支持取消、条目预算与大目录自适应退避），
// 不占用 kqueue/inotify 描述符也不依赖 mygo；持续写入时受最大防抖时长约束仍会定期索引，页面关上即停止。
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

	lastSig, ok := historyRootsSignature(ctx, d.historyWatchRoots())
	if !ok {
		return
	}

	debounce := historyWatchDebounce
	if debounce < 25*time.Millisecond {
		debounce = 25 * time.Millisecond
	}
	baseInterval := historyWatchDefaultScan
	if debounce < 300*time.Millisecond {
		baseInterval = max(debounce, 50*time.Millisecond)
	}
	maxDebounce := max(debounce*4, historyWatchMaxDebounce)
	if debounce < 300*time.Millisecond {
		maxDebounce = max(debounce*4, 400*time.Millisecond)
	}

	scanTimer := time.NewTimer(baseInterval)
	defer scanTimer.Stop()
	graceTick := time.NewTicker(time.Second)
	defer graceTick.Stop()

	var debounceTimer *time.Timer
	var debounceCh <-chan time.Time
	var dirtySince time.Time
	stopDebounce := func() {
		if debounceTimer != nil {
			if !debounceTimer.Stop() {
				select {
				case <-debounceTimer.C:
				default:
				}
			}
			debounceTimer = nil
		}
		debounceCh = nil
		dirtySince = time.Time{}
	}
	defer stopDebounce()

	for {
		select {
		case <-ctx.Done():
			return
		case <-scanTimer.C:
			started := time.Now()
			sig, ok := historyRootsSignature(ctx, d.historyWatchRoots())
			if !ok {
				return
			}
			if sig != lastSig {
				lastSig = sig
				now := time.Now()
				if dirtySince.IsZero() {
					dirtySince = now
				}
				if now.Sub(dirtySince) >= maxDebounce {
					stopDebounce()
					d.pokeHistory()
				} else if debounceTimer == nil {
					debounceTimer = time.NewTimer(debounce)
					debounceCh = debounceTimer.C
				} else {
					if !debounceTimer.Stop() {
						select {
						case <-debounceTimer.C:
						default:
						}
					}
					debounceTimer.Reset(debounce)
				}
			}
			elapsed := time.Since(started)
			nextWait := baseInterval
			if elapsed > 50*time.Millisecond {
				if adaptive := elapsed * 4; adaptive > nextWait {
					nextWait = min(adaptive, historyWatchMaxBackoff)
				}
			}
			scanTimer.Reset(nextWait)
		case <-debounceCh:
			stopDebounce()
			d.pokeHistory()
		case <-graceTick.C:
			h.mu.Lock()
			open := time.Now().Before(h.pageUntil)
			h.mu.Unlock()
			if !open {
				return
			}
		}
	}
}

var errWatchStop = errors.New("history watch stop")

func isHistoryWatchFile(name string) bool {
	ext := strings.ToLower(filepath.Ext(name))
	switch ext {
	case ".jsonl", ".json", ".db", ".sqlite", ".sqlite3", ".vscdb", ".pb", ".wal", ".shm":
		return true
	}
	return false
}

// historyRootsSignature 对会话目录树里相关条目做可取消、带上限的轻量指纹；
// 解析顶层根路径的软链接（支持数据目录软链），内部 WalkDir 不跟随任意子软链以防成环。
func historyRootsSignature(ctx context.Context, roots []string) (uint64, bool) {
	sum := fnv.New64a()
	var buf [16]byte
	seenEntries := 0
	for _, root := range roots {
		if ctx.Err() != nil {
			return 0, false
		}
		walkRoot := root
		if resolved, err := filepath.EvalSymlinks(root); err == nil {
			walkRoot = resolved
		}
		_, _ = sum.Write([]byte(root))
		_, _ = sum.Write([]byte{0})
		err := filepath.WalkDir(walkRoot, func(p string, d fs.DirEntry, err error) error {
			if ctx.Err() != nil {
				return errWatchStop
			}
			if err != nil {
				return nil
			}
			if seenEntries >= historyWatchMaxEntries {
				return errWatchStop
			}
			if !d.IsDir() && !isHistoryWatchFile(d.Name()) {
				return nil
			}
			seenEntries++
			info, ierr := d.Info()
			if ierr != nil {
				return nil
			}
			_, _ = sum.Write([]byte(p))
			var kind byte
			if d.IsDir() {
				kind = 1
			}
			_, _ = sum.Write([]byte{kind})
			binary.LittleEndian.PutUint64(buf[:8], uint64(info.Size()))
			binary.LittleEndian.PutUint64(buf[8:], uint64(info.ModTime().UnixNano()))
			_, _ = sum.Write(buf[:])
			return nil
		})
		if errors.Is(err, errWatchStop) && ctx.Err() != nil {
			return 0, false
		}
	}
	return sum.Sum64(), true
}

// pokeHistory 立刻做一轮增量索引。上一轮还在跑就记下来，做完再来一轮。
func (d *Daemon) pokeHistory() {
	d.refreshHistory(context.Background(), true, 0)
}
