package app

import (
	"slices"
	"sync"
	"time"

	"github.com/egoist/mygo/ui"

	"posse/internal/mermaid/diagram"
)

// Mermaid 图在后台协程里画，按源码和样式缓存；画好之前照代码块显示，画好了请求一帧换成图。
var diagrams struct {
	sync.Mutex
	m       map[diagramKey]*diagramEntry
	pending int
}

type diagramKey struct {
	src string
	st  diagram.Style
}

type diagramEntry struct {
	d    *diagram.Diagram // nil 表示还在画
	used time.Time
}

// 缓存超过 diagramCacheMax 张时，丢掉 diagramStale 以上没用到的图，最久没用的先丢。
// 刚用过的不丢：一篇文档的图比上限多时，同一帧里要画的图互相挤掉，每帧都要整批重画。
const (
	diagramCacheMax = 64
	diagramStale    = 10 * time.Second
)

// mermaidSource 记用户切到源码的图（按源码），只在界面线程上用。
var mermaidSource = map[string]bool{}

// diagramOf 返回 src 画好的图，还没画好是 nil（这时开始画）。
func diagramOf(c *ui.Context, src string) *diagram.Diagram {
	k := diagramKey{src, mermaidStyle(c.Theme())}
	now := time.Now()
	diagrams.Lock()
	defer diagrams.Unlock()
	if e, ok := diagrams.m[k]; ok {
		e.used = now
		return e.d
	}
	if diagrams.m == nil {
		diagrams.m = map[diagramKey]*diagramEntry{}
	}
	diagrams.m[k] = &diagramEntry{used: now}
	if over := len(diagrams.m) - diagramCacheMax; over > 0 {
		var stale []diagramKey
		for sk, e := range diagrams.m {
			if now.Sub(e.used) > diagramStale {
				stale = append(stale, sk)
			}
		}
		slices.SortFunc(stale, func(a, b diagramKey) int { return diagrams.m[a].used.Compare(diagrams.m[b].used) })
		for _, sk := range stale[:min(over, len(stale))] {
			delete(diagrams.m, sk)
		}
	}
	diagrams.pending++
	svc := c.Services()
	go func() {
		d := diagram.Render(k.src, k.st)
		diagrams.Lock()
		if e, ok := diagrams.m[k]; ok {
			e.d = d
		}
		diagrams.pending--
		diagrams.Unlock()
		svc.Invalidate()
	}()
	return nil
}

// diagramView 把图按原始大小居中画出来，比栏宽就等比缩小。
func diagramView(c *ui.Context, d *diagram.Diagram) {
	w, h := d.Size()
	ui.Column(c).AlignItems(ui.Center).Padding(8, 12, 12, 12).Children(func() {
		ui.Box(c).WidthPercent(100).MaxWidth(w).AspectRatio(w / h).Draw(d.Draw).Label(L(c).MarkdownMermaidLabel()).Unselectable()
	})
}
