package app

import (
	"runtime"
	"runtime/debug"
	"sync"
	"time"
)

// 比较几 MB 以上的文件时临时要几百 MB，用完 Go 只把一部分慢慢还给系统，窗口的 phys_footprint
// 停在高处；大 diff 关掉以后也一样。这里在这两个时候让 Go 把空闲的内存全部还回去。
//
// 大 diff 什么时候不再用到，有很多条路（关标签、diff 标签换文件、比较和回滚浮窗换文件或关掉、文件历史换提交），
// 不在每一处埋点，而是给每个大 diff 挂一个回收后的回调。关掉之后几乎不再分配内存，Go 要等两分钟的定时 GC
// 才会回收它；大 diff 还开着的时候每 bigDiffGC 主动 GC 一次，关掉后最多这么久就还。

const (
	// releaseDelay 内的几次请求合成一次：读完、比完、装上结果常常挨着发生。
	releaseDelay = time.Second
	// bigDiffGC 是还开着大 diff 时主动 GC 的间隔。
	bigDiffGC = 15 * time.Second
)

var mem struct {
	mu      sync.Mutex
	timer   *time.Timer
	big     int  // 还没被回收的大 diff
	watched bool // 定时 GC 的协程在跑
}

// releaseMemorySoon 稍后在后台让 Go 回收并把空闲内存全部还给系统。
func releaseMemorySoon() {
	mem.mu.Lock()
	defer mem.mu.Unlock()
	if mem.timer != nil {
		mem.timer.Reset(releaseDelay)
		return
	}
	mem.timer = time.AfterFunc(releaseDelay, func() {
		mem.mu.Lock()
		mem.timer = nil
		mem.mu.Unlock()
		debug.FreeOSMemory()
	})
}

// trackBigDiff 记下一个两边合起来超过 diffAheadSize 的 diff，它被回收后把内存还给系统。同一个只记一次。
func trackBigDiff(df *diffDoc) {
	if df.tracked {
		return
	}
	df.tracked = true
	mem.mu.Lock()
	mem.big++
	start := !mem.watched
	mem.watched = true
	mem.mu.Unlock()
	runtime.AddCleanup(df, func(struct{}) {
		mem.mu.Lock()
		mem.big--
		mem.mu.Unlock()
		releaseMemorySoon()
	}, struct{}{})
	if start {
		go watchBigDiffs()
	}
}

func watchBigDiffs() {
	for {
		time.Sleep(bigDiffGC)
		mem.mu.Lock()
		if mem.big <= 0 {
			mem.watched = false
			mem.mu.Unlock()
			return
		}
		mem.mu.Unlock()
		runtime.GC()
	}
}
