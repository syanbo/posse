package daemon

import (
	"sync"
	"time"

	"posse/internal/host"
)

// windowFresh 内没收到窗口的轮询就当窗口关了：窗口每秒调一次 daemon.status
const windowFresh = 5 * time.Second

// windowColors 是窗口报来的终端配色（daemon.status 的 colors）。窗口可以不跟系统的深浅
// （设置里选了浅色或深色），只看窗口接着哪个终端判断不了它开没开、选了哪套，所以随轮询报。
// 配色变了换到所有终端上：没接入的终端内核也照它应答，手机照它画；
// 窗口开着时新终端按它起，程序启动时查到的底色和窗口一致，窗口关了按系统外观选。
type windowColors struct {
	mu     sync.Mutex
	colors *host.Colors
	at     time.Time
}

// report 记下窗口的配色，返回要不要换到所有终端上：配色变了，或者之前当窗口关了
// （那期间新建的终端按系统外观起）
func (w *windowColors) report(c host.Colors) bool {
	w.mu.Lock()
	defer w.mu.Unlock()
	stale := time.Since(w.at) > windowFresh
	w.at = time.Now()
	if w.colors != nil && *w.colors == c && !stale {
		return false
	}
	w.colors = &c
	return true
}

func (w *windowColors) current() *host.Colors {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.colors == nil || time.Since(w.at) > windowFresh {
		return nil
	}
	c := *w.colors
	return &c
}

// windowReported 处理窗口随 daemon.status 报来的配色。
func (d *Daemon) windowReported(c host.Colors) {
	if !d.window.report(c) {
		return
	}
	d.mu.Lock()
	terms := make([]*session, 0, len(d.terms))
	for _, s := range d.terms {
		terms = append(terms, s)
	}
	d.mu.Unlock()
	for _, s := range terms {
		s.SetColors(c)
	}
}
