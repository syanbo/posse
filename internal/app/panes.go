package app

import "slices"

// focusView 是焦点栏接入的终端，没有时为 nil。
func (a *app) focusView() *termView { return a.panes[a.pane] }

// live 报告 v 还接入着：没被切走，也没因重新接入换成新的视图。回调和后台任务回来时用它判断还能不能动 v。
func (a *app) live(v *termView) bool { return v != nil && slices.Contains(a.panes[:], v) }

// showing 报告终端正接入在主区域的某一栏里。
func (a *app) showing(id string) bool {
	for _, v := range a.panes {
		if v != nil && v.id == id {
			return true
		}
	}
	return false
}

func (a *app) eachPane(fn func(v *termView)) {
	for _, v := range a.panes {
		if v != nil {
			fn(v)
		}
	}
}

// ensurePanes 让各栏接入的终端与 ids 一致：换掉的断开（释放界面侧内存），接入新的；
// 同一个终端换到另一栏时沿用原来的视图，不重新接入。
func (a *app) ensurePanes(ids [maxColumns]string) {
	old := a.panes
	var next [maxColumns]*termView
	for i, id := range ids {
		for j, v := range old {
			if id != "" && v != nil && v.id == id {
				next[i], old[j] = v, nil
				break
			}
		}
	}
	for _, v := range old {
		if v != nil {
			v.term.Close()
			a.dropPaneState(v.id)
		}
	}
	for i, id := range ids {
		if id != "" && next[i] == nil {
			if next[i] = a.attachView(id); next[i] != nil {
				delete(a.carryFind, id)
			}
		}
	}
	a.panes = next
}

// dropPaneState 放掉只对断开的那个终端有用的界面状态：它里面链接的卡片和图片预览。别的栏的不动。
func (a *app) dropPaneState(id string) {
	if a.linkCard != nil && (a.linkCard.term == id || a.linkCard.term == "") {
		a.linkCard = nil
	}
	if a.hoverTerm == id {
		a.hoverImage, a.hoverTerm, a.previews = "", "", nil
	}
}
