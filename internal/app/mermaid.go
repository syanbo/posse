package app

import (
	"sync"

	"github.com/egoist/mygo/ui"

	"posse/internal/mermaid"
)

// Mermaid 图在后台协程里画，按源码和样式缓存；画好之前照代码块显示，画好了请求一帧换成图。
var diagrams struct {
	sync.Mutex
	m       map[diagramKey]*mermaid.Diagram // 值为 nil 表示还在画
	order   []diagramKey
	pending int
}

type diagramKey struct {
	src string
	st  mermaid.Style
}

// diagramCacheMax 是缓存的图数，超过就丢最早的；换主题后旧样式的图很快被挤出去。
const diagramCacheMax = 64

// mermaidSource 记用户切到源码的图（按源码），只在界面线程上用。
var mermaidSource = map[string]bool{}

// diagramOf 返回 src 画好的图，还没画好是 nil（这时开始画）。
func diagramOf(c *ui.Context, src string) *mermaid.Diagram {
	k := diagramKey{src, mermaidStyle(c.Theme())}
	diagrams.Lock()
	defer diagrams.Unlock()
	if d, ok := diagrams.m[k]; ok {
		return d
	}
	if diagrams.m == nil {
		diagrams.m = map[diagramKey]*mermaid.Diagram{}
	}
	diagrams.m[k] = nil
	diagrams.order = append(diagrams.order, k)
	if len(diagrams.order) > diagramCacheMax {
		delete(diagrams.m, diagrams.order[0])
		diagrams.order = diagrams.order[1:]
	}
	diagrams.pending++
	svc := c.Services()
	go func() {
		d := mermaid.Render(k.src, k.st)
		diagrams.Lock()
		if _, ok := diagrams.m[k]; ok {
			diagrams.m[k] = d
		}
		diagrams.pending--
		diagrams.Unlock()
		svc.Invalidate()
	}()
	return nil
}

// diagramsPending 报告还有没有在画的图（离屏预览等它画完再截图）。
func diagramsPending() bool {
	diagrams.Lock()
	defer diagrams.Unlock()
	return diagrams.pending > 0
}

// diagramView 把图按原始大小居中画出来，比栏宽就等比缩小。
func diagramView(c *ui.Context, d *mermaid.Diagram) {
	w, h := d.Size()
	ui.Column(c).AlignItems(ui.Center).Padding(8, 12, 12, 12).Children(func() {
		ui.Box(c).WidthPercent(100).MaxWidth(w).AspectRatio(w / h).Draw(d.Draw).Label("Mermaid 图").Unselectable()
	})
}
