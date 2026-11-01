package app

import (
	"time"

	"github.com/egoist/mygo/ui"
)

// 右侧面板：标签栏右边的按钮打开，同一时间只开一个，开着哪个记在 ui.json。
const (
	panelUsage   = "usage"
	panelHistory = "history" // 旧版 ui.json 里历史是浮层面板，读进来时改成文件栏里的"历史"
	panelImages  = "images"
	panelCheat   = "cheat"
	panelFiles   = "files" // 旧版 ui.json 里文件栏也是浮层面板，读进来时改成 FilesPanel
)

// panelPoll 是右侧面板的定时刷新：面板开着时每隔一段时间在后台读一次，同一时间只读一次。
type panelPoll struct {
	busy bool      // 后台正在读
	at   time.Time // 上次读完的时间，零值表示还没读过
}

// due 在画面板时调用，报告现在要不要开始读；要读时记为正在读，读完在界面线程调用 done。
// 不到点时约好到点的那一帧：界面没有变化时不会自己重建。
func (p *panelPoll) due(c *ui.Context, every time.Duration) bool {
	if p.busy {
		return false
	}
	if wait := every - time.Since(p.at); wait > 0 {
		c.After(wait)
		return false
	}
	p.busy = true
	return true
}

func (p *panelPoll) done() { p.busy, p.at = false, time.Now() }

// panelWidth 是右侧面板的宽度，各面板一样宽。
const panelWidth = 280

// rightPanel 是右侧面板的外框：浮在终端右侧的卡片，有标题和关闭按钮，body 自己决定滚动。
func (a *app) rightPanel(c *ui.Context, title string, body func()) ui.Element {
	return floating(c, ui.Column(c).Absolute().Top(8).Right(8).Bottom(8).Width(panelWidth)).Children(func() {
		ui.Row(c).Padding(16, 12, 12, 16).Gap(8).AlignItems(ui.Center).Children(func() {
			cardTitle(c, title).Grow(1)
			if iconButtonSized(c, iconClose, "关闭", compactBox(), smallIcon()).Clicked() {
				a.state.RightPanel = ""
				a.saveState()
			}
		})
		body()
	})
}

// panelButton 是终端右上角打开某个右侧面板的按钮（不带悬停提示，见 sessionButtons），开着时垫一层强调色淡底。
func (a *app) panelButton(c *ui.Context, name string, icon *ui.SVG, label string) {
	b := quietIconButton(c.Key("panel-"+name), icon, label, buttonBox(), iconSize)
	if b.Hovered() {
		a.overPanelButton = true
	}
	if a.state.RightPanel == name {
		b.Background(colors(c.Theme()).tint)
	}
	if b.Clicked() {
		if a.state.RightPanel == name {
			a.state.RightPanel = ""
		} else {
			a.state.RightPanel = name
			a.branchPop = nil // 和分支弹窗互斥，见 branchButton
		}
		a.saveState()
	}
}
