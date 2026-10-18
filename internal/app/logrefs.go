package app

import (
	"slices"
	"strconv"
	"strings"

	"github.com/egoist/mygo/ui"

	"hive/internal/gitx"
)

// Git 日志左边的分支树，照 WebStorm：HEAD（当前分支）、本地、远程（按远端分组）、标签。
// 点一项只看它的提交，再点一次回到全部分支。

func (a *app) logRefs(c *ui.Context, v *filesView, d *fileDoc) {
	ld := d.log
	g := &v.git
	if g.branches.list == nil && !g.branches.loading {
		a.loadBranches(v)
	}
	if ld.refsOpen == nil {
		ld.refsOpen = map[string]bool{"local": true}
	}
	ui.Box(c).Height(40).Shrink(0).Padding(0, 8).Center().Children(func() {
		fieldInput(c, &ld.refQuery, "筛选分支和标签")
	})
	query := strings.ToLower(strings.TrimSpace(ld.refQuery))
	match := func(name string) bool { return query == "" || strings.Contains(strings.ToLower(name), query) }
	// 筛选时各组都展开，只列出对得上的
	open := func(key string) bool { return query != "" || ld.refsOpen[key] }
	pick := func(ref string) {
		if ld.filter.Branch == ref {
			ref = ""
		}
		ld.filter.Branch = ref
		a.loadLog(v, d, false)
	}
	var local, remote []gitx.Branch
	for _, b := range g.branches.list {
		if !match(b.Name) {
			continue
		}
		if b.Remote {
			remote = append(remote, b)
		} else {
			local = append(local, b)
		}
	}
	var tags []string
	for _, tag := range g.branches.tags {
		if match(tag) {
			tags = append(tags, tag)
		}
	}
	ui.Scroll(c).Grow(1).Children(func() {
		ui.Column(c).Padding(0, 6, 12, 6).Gap(1).Children(func() {
			if query == "" {
				label := "HEAD（当前分支）"
				if cur, ok := currentBranch(g.branches.list); ok {
					label = "HEAD（" + cur.Name + "）"
				}
				if a.refRow(c, "HEAD", label, label, 0, ld.filter.Branch == "HEAD", nil) {
					pick("HEAD")
				}
			}
			if a.refGroup(c, ld, "local", "本地", len(local), open("local")) {
				for _, b := range local {
					if a.refRow(c, "l:"+b.Name, b.Name, "本地分支 "+b.Name, 1, ld.filter.Branch == branchRef(b), refMark(b)) {
						pick(branchRef(b))
					}
				}
			}
			if a.refGroup(c, ld, "remote", "远程", len(remote), open("remote")) {
				// 按远端分组：origin/main 放在 origin 下面
				var remotes []string
				for _, b := range remote {
					r, _, _ := strings.Cut(b.Name, "/")
					if !slices.Contains(remotes, r) {
						remotes = append(remotes, r)
					}
				}
				for _, r := range remotes {
					if !a.refGroupAt(c, ld, "remote:"+r, r, -1, open("remote:"+r), 1) {
						continue
					}
					for _, b := range remote {
						if rest, ok := strings.CutPrefix(b.Name, r+"/"); ok {
							if a.refRow(c, "r:"+b.Name, rest, "远程分支 "+b.Name, 2, ld.filter.Branch == branchRef(b), nil) {
								pick(branchRef(b))
							}
						}
					}
				}
			}
			if a.refGroup(c, ld, "tags", "标签", len(tags), open("tags")) {
				for _, tag := range tags {
					if a.refRow(c, "t:"+tag, tag, "标签 "+tag, 1, ld.filter.Branch == "refs/tags/"+tag, nil) {
						pick("refs/tags/" + tag)
					}
				}
			}
		})
	})
}

// branchRef 是日志按分支筛选用的完整引用名：分支和标签同名时 git 会取标签。
func branchRef(b gitx.Branch) string {
	if b.Remote {
		return "refs/remotes/" + b.Name
	}
	return "refs/heads/" + b.Name
}

// refShort 是筛选栏上显示的名字。
func refShort(ref string) string {
	for _, p := range []string{"refs/heads/", "refs/remotes/", "refs/tags/"} {
		if s, ok := strings.CutPrefix(ref, p); ok {
			return s
		}
	}
	return ref
}

// refMark 是本地分支名后面的小字：当前分支。
func refMark(b gitx.Branch) func(c *ui.Context) {
	if !b.Current {
		return nil
	}
	return func(c *ui.Context) {
		ui.Text(c, "当前").FontSize(10.5).TextColor(c.Theme().TextMuted).Shrink(0)
	}
}

func (a *app) refGroup(c *ui.Context, ld *logDoc, key, label string, n int, open bool) bool {
	return a.refGroupAt(c, ld, key, label, n, open, 0)
}

// refGroupAt 是可以展开的一组；n 是组里有几项，-1 不显示。返回是否展开。
func (a *app) refGroupAt(c *ui.Context, ld *logDoc, key, label string, n int, open bool, indent int) bool {
	t := c.Theme()
	row := ui.Row(c.Key("g:"+key)).Height(26).Padding(0, 8, 0, 4+float32(indent)*14).Gap(4).Radius(6).AlignItems(ui.Center).
		Role(ui.RoleButton).Label(label)
	selectedBg(c, row, false)
	row.Children(func() {
		chevron := iconChevron
		if open {
			chevron = iconDown
		}
		ui.Icon(c, chevron).FontSize(12).TextColor(t.TextMuted).Shrink(0)
		ui.Text(c, label).FontSize(12.5).SingleLine().Shrink(1)
		if n >= 0 {
			ui.Text(c, strconv.Itoa(n)).FontSize(11).FontFeatures("tnum").TextColor(t.TextMuted).Shrink(0)
		}
	})
	if row.Clicked() {
		ld.refsOpen[key] = !ld.refsOpen[key]
	}
	return open
}

// refRow 是一个分支或标签，返回这一帧是否点了它；name 是读屏念的名字，带上种类免得和别处同名。
func (a *app) refRow(c *ui.Context, key, label, name string, indent int, selected bool, trailing func(c *ui.Context)) bool {
	row := ui.Row(c.Key(key)).Height(26).Padding(0, 8, 0, 20+float32(indent)*14).Gap(6).Radius(6).AlignItems(ui.Center).
		Role(ui.RoleButton).Label(name).Tooltip(label)
	selectedBg(c, row, selected)
	row.Children(func() {
		ui.Text(c, label).FontSize(12.5).Font(monoFont).SingleLine().Shrink(1).Grow(1)
		if trailing != nil {
			trailing(c)
		}
	})
	return row.Clicked()
}
