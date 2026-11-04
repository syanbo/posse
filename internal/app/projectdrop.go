package app

import (
	"os"
	"path/filepath"
	"time"

	"github.com/egoist/mygo/ui"

	"posse/locales"
)

// projectDrop 让侧栏的项目列表接住从访达拖来的文件夹：拖到上面时画一圈强调色的框，
// 松手后问一句再加成项目。已经是项目的文件夹不再问，直接切到它的主工作区；拖来的文件不算。
func (a *app) projectDrop(c *ui.Context, nav ui.Element) {
	if nav.FileDragOver() {
		accent := c.Theme().Accent
		nav.DrawOver(func(p *ui.Painter, r ui.Rect) {
			p.Stroke(ui.Rect{X: r.X + 1, Y: r.Y + 1, W: r.W - 2, H: r.H - 2}, accent, 8, 2)
		})
	}
	paths := nav.DroppedFiles()
	if len(paths) == 0 {
		return
	}
	l := L(c)
	var dirs []string
	var known *project
	for _, p := range paths {
		if fi, err := os.Stat(p); err != nil || !fi.IsDir() {
			continue
		}
		if pr := a.projectAt(p); pr != nil {
			known = pr
			continue
		}
		dirs = append(dirs, p)
	}
	switch {
	case len(dirs) == 0 && known != nil:
		a.showProject(known.ID)
	case len(dirs) == 0:
		a.err = l.SidebarDropNotFolder()
	case len(dirs) == 1:
		a.confirm(l.SidebarDropProjectConfirm(shortPath(dirs[0])), l.SidebarDropProjectBody(), l.SidebarDropProjectAdd(), func() { a.addProjects(l, dirs) })
	default:
		a.confirm(l.SidebarDropProjectsConfirm(len(dirs)), l.SidebarDropProjectBody(), l.SidebarDropProjectAdd(), func() { a.addProjects(l, dirs) })
	}
}

// addProjects 把几个文件夹依次加成项目，加好后切到第一个的主工作区；某个失败了报错，其余照加。
func (a *app) addProjects(l locales.Messages, dirs []string) {
	var first string
	for _, dir := range dirs {
		var p struct {
			ID string `json:"id"`
		}
		if a.call("project.add", map[string]any{"path": dir}, &p, 10*time.Second) == nil && first == "" {
			first = p.ID
		}
	}
	a.refresh()
	if first != "" {
		a.update(func() { a.showProject(first) })
	}
}

// projectAt 是路径就是这个文件夹的项目；访达给的路径和常驻进程记的写法可能不同（符号链接），两边都解析后比。
func (a *app) projectAt(dir string) *project {
	real := func(p string) string {
		if r, err := filepath.EvalSymlinks(p); err == nil {
			return r
		}
		return filepath.Clean(p)
	}
	want := real(dir)
	for i := range a.projects {
		if real(a.projects[i].Path) == want {
			return &a.projects[i]
		}
	}
	return nil
}

// showProject 切到项目的主工作区，侧栏滚到它那一行。
func (a *app) showProject(id string) {
	// byProject 里主工作区排在最前
	ws := a.byProject[id]
	if len(ws) == 0 {
		return
	}
	a.selected = ws[0].ID
	a.revealSel = true
}
