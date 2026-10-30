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

// ensurePanes 让两栏接入的终端与 ids 一致：换掉的断开（释放界面侧内存），接入新的；
// 同一个终端换到另一栏时沿用原来的视图，不重新接入。
func (a *app) ensurePanes(ids [2]string) {
	old := a.panes
	var next [2]*termView
	for i, id := range ids {
		for j, v := range old {
			if id != "" && v != nil && v.id == id {
				next[i], old[j] = v, nil
				break
			}
		}
	}
	changed := false
	for _, v := range old {
		if v != nil {
			v.term.Close()
			changed = true
		}
	}
	for i, id := range ids {
		if id != "" && next[i] == nil {
			next[i] = a.attachView(id)
			changed = true
		}
	}
	a.panes = next
	if changed {
		// 悬停预览只对原来那个终端里的链接有用，换终端就放掉；没接上的查找栏也不再带
		a.linkCard, a.hoverImage, a.previews = nil, "", nil
		a.carryFind = nil
	}
}
