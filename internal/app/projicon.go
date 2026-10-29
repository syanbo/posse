package app

import (
	"bytes"
	"fmt"
	"image"
	"image/png"
	"log"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/egoist/mygo"
	"github.com/egoist/mygo/ui"

	"posse/internal/media"
	"posse/internal/projicon"
)

// projectIcon 是侧栏项目行自定义的图标，存在 $POSSE_HOME/project-icons（不放 ui.json：手机也要显示，由常驻进程读）。
type projectIcon = projicon.Icon

// projectIconPx 是导入图片缩到的最大边长：图标最大一档在 2 倍屏上也够清楚，手机拉一张也不费事。
const projectIconPx = 96

func projectIconDir(home string) string { return projicon.Dir(home) }

// projectGlyph 画项目行首的图标：自定义过就画自定义的，图片读不了时退回 def。
func (a *app) projectGlyph(c *ui.Context, pi projectIcon, def *ui.SVG) {
	t := c.Theme()
	if pi.Text != "" {
		size := smallIcon()
		// 一两个字放不进图标的方块时按比例缩小，不挤到旁边的名字上
		if w, _ := c.MeasureText(0, ui.Span{Text: pi.Text, Size: size}); w > iconSize {
			size *= iconSize / w
		}
		ui.Box(c).Size(iconSize, iconSize).Center().Shrink(0).Children(func() {
			ui.Text(c, pi.Text).FontSize(size).TextColor(t.Accent).SingleLine()
		})
		return
	}
	if pi.Image != "" {
		if img := a.projectIconImage(pi.Image); img != nil {
			ui.Image(c, img).Size(iconSize, iconSize).Fit(ui.Contain).Radius(rSmall).Shrink(0)
			return
		}
	}
	ui.Icon(c, def).FontSize(iconSize).TextColor(t.Accent).Shrink(0)
}

// projectIconImage 读出导入过的图标图片。文件名每次导入都不同，按文件名缓存，读不了的也记下，不每帧再读。
func (a *app) projectIconImage(name string) ui.ImageSource {
	if img, ok := a.projIconCache[name]; ok {
		return img
	}
	if a.projIconCache == nil {
		a.projIconCache = map[string]ui.ImageSource{}
	}
	var img ui.ImageSource
	if data, err := os.ReadFile(filepath.Join(projectIconDir(a.home), name)); err == nil {
		if bm, err := ui.DecodeBitmap(data); err == nil {
			img = bm
		}
	}
	a.projIconCache[name] = img
	return img
}

// importProjectIcon 把选的图片缩到 projectIconPx 存成 PNG，放进 $POSSE_HOME/project-icons：
// 原文件之后挪走、删掉都不影响图标。SVG 也画成 PNG，手机不用会画 SVG。返回存下的文件名。
func importProjectIcon(home, projectID, src string) (string, error) {
	data, err := os.ReadFile(src)
	if err != nil {
		return "", err
	}
	var img image.Image
	if strings.EqualFold(filepath.Ext(src), ".svg") {
		svg, err := ui.ParseSVG(data)
		if err != nil {
			return "", fmt.Errorf("读不了这个 SVG：%w", err)
		}
		img = rasterSVG(svg, projectIconPx)
	} else {
		cfg, _, err := image.DecodeConfig(bytes.NewReader(data))
		if err != nil {
			return "", fmt.Errorf("读不了这张图片：%w", err)
		}
		// 长边缩到 projectIconPx：media 按宽度缩，竖图换算成对应的宽度
		width := cfg.Width
		if long := max(cfg.Width, cfg.Height); long > projectIconPx {
			width = max(cfg.Width*projectIconPx/long, 1)
		}
		if img, _, err = media.ScaledImageBytes(data, width); err != nil {
			return "", fmt.Errorf("读不了这张图片：%w", err)
		}
	}
	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		return "", err
	}
	dir := projectIconDir(home)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", err
	}
	name := fmt.Sprintf("%s-%d.png", projectID, time.Now().UnixNano())
	if err := os.WriteFile(filepath.Join(dir, name), buf.Bytes(), 0o644); err != nil {
		return "", err
	}
	return name, nil
}

// rasterSVG 把 SVG 画成 px×px 带透明的图：离屏渲染总有不透明的底，分别垫黑底、白底各画一遍，
// 按两遍的差反推每个像素的透明度和颜色。
func rasterSVG(svg *ui.SVG, px int) *image.NRGBA {
	render := func(bg ui.Color) *image.RGBA {
		return ui.Render(func(c *ui.Context) {
			ui.Box(c).Size(float32(px), float32(px)).Background(bg).Children(func() {
				ui.Image(c, svg).Size(float32(px), float32(px)).Fit(ui.Contain)
			})
		}, px, px, 1)
	}
	black, white := render(ui.Color{A: 255}), render(ui.Color{R: 255, G: 255, B: 255, A: 255})
	out := image.NewNRGBA(image.Rect(0, 0, px, px))
	for i := 0; i+3 < len(out.Pix) && i+3 < len(black.Pix); i += 4 {
		k, w := black.Pix[i:i+3], white.Pix[i:i+3]
		// 透明度 = 1 - (白底 - 黑底)，三个通道取平均
		d := (int(w[0]) - int(k[0]) + int(w[1]) - int(k[1]) + int(w[2]) - int(k[2])) / 3
		a := min(max(255-d, 0), 255)
		if a == 0 {
			continue
		}
		for j := range 3 {
			out.Pix[i+j] = uint8(min(int(k[j])*255/a, 255))
		}
		out.Pix[i+3] = uint8(a)
	}
	return out
}

// iconEditor 是"更换图标"对话框的状态。text 和 image 分开记：写了字时用字，
// 字删空了又回到选过的图片，只有"恢复默认"才把两样都清掉。
type iconEditor struct {
	open    bool
	project project
	text    string
	image   string // 存着的或刚导入的图片
	picking bool
}

// draft 是对话框里现在的图标。
func (e *iconEditor) draft() projectIcon {
	if t := strings.TrimSpace(e.text); t != "" {
		return projectIcon{Text: t}
	}
	return projectIcon{Image: e.image}
}

// editProjectIcon 打开项目 p 的图标对话框。
func (a *app) editProjectIcon(p project) {
	cur := a.projIcons[p.ID]
	a.iconEdit = iconEditor{open: true, project: p, text: cur.Text, image: cur.Image}
}

// pickProjectIcon 让用户选一张图片导入成草稿；在后台调用，系统对话框会等用户选完。
func (a *app) pickProjectIcon(p project) {
	paths, err := mygo.Dialog.Open(mygo.OpenDialogOptions{Parent: a.win, Title: "选择 " + p.Name + " 的图标", ButtonLabel: "选择",
		DefaultPath: p.Path, Filters: []mygo.FileFilter{{Name: "图片", Extensions: []string{"png", "jpg", "jpeg", "gif", "webp", "bmp", "svg"}}}})
	var name string
	if err == nil && len(paths) > 0 {
		name, err = importProjectIcon(a.home, p.ID, paths[0])
	}
	a.win.Update(func() {
		e := &a.iconEdit
		e.picking = false
		switch {
		case err != nil:
			a.err = "导入图标失败：" + err.Error()
		case name != "" && e.open && e.project.ID == p.ID:
			a.dropIconFile(e.image, e.project.ID)
			e.image, e.text = name, ""
		case name != "":
			os.Remove(filepath.Join(projectIconDir(a.home), name))
		}
	})
}

// dropIconFile 删掉不再用的图标图片：对话框里换掉、没用上的，存着的那张不删。
func (a *app) dropIconFile(name, projectID string) {
	if name == "" || a.projIcons[projectID].Image == name {
		return
	}
	os.Remove(filepath.Join(projectIconDir(a.home), name))
	delete(a.projIconCache, name)
}

// setProjectIcon 存下项目的图标，pi 为空时恢复默认；换掉的图片一并删掉。
func (a *app) setProjectIcon(projectID string, pi projectIcon) {
	old := a.projIcons[projectID]
	if pi == (projectIcon{}) {
		delete(a.projIcons, projectID)
	} else {
		if a.projIcons == nil {
			a.projIcons = map[string]projectIcon{}
		}
		a.projIcons[projectID] = pi
	}
	if old.Image != "" && old.Image != pi.Image {
		os.Remove(filepath.Join(projectIconDir(a.home), old.Image))
		delete(a.projIconCache, old.Image)
	}
	a.saveProjectIcons()
}

// saveProjectIcons 写下图标。文件只有几百字节，在主线程上直接写：放进协程的话，
// 连着改两次时后写完的可能是旧的那份。
func (a *app) saveProjectIcons() {
	if a.home == "" {
		return
	}
	if err := projicon.Save(a.home, a.projIcons); err != nil {
		log.Printf("save project icons: %v", err)
	}
}

// pruneProjectIcons 在每次轮询拿到项目列表后调用：项目不在了（窗口、CLI、agent 移除的都算），
// 它的图标和图片一并删掉。
func (a *app) pruneProjectIcons(ps []project) {
	for id := range a.projIcons {
		if !slices.ContainsFunc(ps, func(p project) bool { return p.ID == id }) {
			a.setProjectIcon(id, projectIcon{})
		}
	}
}

// projectIconDialog 是右键项目行"更换图标…"打开的对话框：填文字或 emoji、选一张图片，或恢复默认的文件夹。
func (a *app) projectIconDialog(c *ui.Context) {
	e := &a.iconEdit
	if !e.open {
		return
	}
	t := c.Theme()
	cancel := func() {
		e.open = false
		a.dropIconFile(e.image, e.project.ID)
	}
	open := true
	ui.Modal(c, &open, func() {
		ui.Column(c).Width(360).Gap(12).Children(func() {
			titleText(c, "更换图标", fsTitle)
			// 照侧栏项目行的样子预览
			ui.Row(c).Height(sz(32)).Padding(0, navPad).Gap(8).Radius(rControl).Background(colors(t).fill).AlignItems(ui.Center).Children(func() {
				a.projectGlyph(c, e.draft(), iconFolder)
				ui.Text(c, e.project.Name).FontSize(fsBody).SingleLine()
			})
			submitted := fieldInput(c, &e.text, "一个 emoji 或一两个字").AutoFocus().Submitted()
			ui.Row(c).Gap(8).AlignItems(ui.Center).Children(func() {
				label := "选择图片…"
				if e.picking {
					label = "正在选择…"
				}
				if pressable(textButton(c, label)).Clicked() && !e.picking {
					e.picking = true
					go a.pickProjectIcon(e.project)
				}
				if e.draft() != (projectIcon{}) && pressable(textButton(c, "恢复默认")).Clicked() {
					a.dropIconFile(e.image, e.project.ID)
					e.image, e.text = "", ""
				}
			})
			ui.Text(c, "图片会复制一份保存，原文件可以删掉。").FontSize(fsSmall).TextColor(t.TextMuted)
			ui.Row(c).Gap(8).Justify(ui.End).Padding(8, 0, 0, 0).Children(func() {
				if pressable(textButton(c, "取消")).Clicked() {
					cancel()
				}
				if pressable(accentButton(c, "完成")).Clicked() {
					submitted = true
				}
			})
			if submitted {
				e.open = false
				pi := e.draft()
				// 写了字时没用上的新图片删掉；存着的那张由 setProjectIcon 删
				if pi.Image != e.image {
					a.dropIconFile(e.image, e.project.ID)
				}
				a.setProjectIcon(e.project.ID, pi)
			}
		})
	})
	if !open && e.open {
		cancel()
	}
}

func (a *app) projectByID(id string) *project {
	for i := range a.projects {
		if a.projects[i].ID == id {
			return &a.projects[i]
		}
	}
	return nil
}
