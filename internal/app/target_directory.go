package app

import (
	"context"
	"errors"
	"os"
	"path"
	"slices"
	"strings"
	"time"

	"github.com/egoist/mygo/ui"

	"posse/internal/errtext"
	"posse/internal/files"
	"posse/internal/rpc"
	"posse/locales"
)

type targetDirectory struct {
	request           appRequest
	title, verb, host string
	input, path       string
	entries           []files.Entry
	fallbacks         []string
	loaded, loading   bool
	err               error
	seq               uint64
	browseSeq         uint64
	createSeq         uint64
	cancel            context.CancelFunc
	browseTimer       *time.Timer
	done              func(string)
	list              ui.ListState

	newOpen  bool
	newName  string
	newErr   string
	creating bool
}

// chooseTargetDirectory 可从后台调用；打开和后续状态变更都排到窗口线程。
func (r appRequest) chooseTargetDirectory(title, verb, start string, done func(string)) {
	r.chooseTargetDirectoryWithFallbacks(title, verb, start, nil, done)
}

func (r appRequest) chooseTargetDirectoryWithFallbacks(title, verb, start string, fallbacks []string, done func(string)) {
	r.update(func() {
		if r.client == nil || !r.client.Remote || r.client.Desktop == nil || !r.mayWrite() {
			return
		}
		r.app.closeTargetDirectory()
		if !path.IsAbs(start) {
			start = "/"
		}
		host := r.snapshot.HostID
		if name := r.app.hostName(appL()); name != "" {
			host = name
		} else if r.host != nil && r.host.label != "" {
			host = r.host.label
		}
		d := &targetDirectory{
			request:   r,
			title:     title,
			verb:      verb,
			host:      host,
			fallbacks: slices.Clone(fallbacks),
			done:      done,
		}
		r.app.targetDir = d
		d.loadWithInput(start, "", true)
	})
}

func (a *app) closeTargetDirectory() {
	if d := a.targetDir; d != nil {
		d.seq++
		d.stopBrowseTimer()
		if d.cancel != nil {
			d.cancel()
			d.cancel = nil
		}
		d.done = nil
		a.targetDir = nil
	}
}

func (d *targetDirectory) stopBrowseTimer() {
	d.browseSeq++
	if d.browseTimer != nil {
		d.browseTimer.Stop()
		d.browseTimer = nil
	}
}

func (d *targetDirectory) current() bool {
	return d.request.current() && d.request.app.targetDir == d
}

func (d *targetDirectory) inputMatchesLoadedPath() bool {
	return d.input == d.path || (d.path != "/" && d.input == d.path+"/")
}

func (d *targetDirectory) canSelect() bool {
	return d.current() && d.request.snapshot.Controlled && d.loaded && !d.loading && !d.creating && d.err == nil && d.inputMatchesLoadedPath()
}

func (d *targetDirectory) canCreateDir() bool {
	return d.current() && d.request.snapshot.Controlled && d.loaded && !d.loading && !d.creating && d.err == nil && d.browseTimer == nil && d.inputMatchesLoadedPath()
}

func (d *targetDirectory) selectDirectory() {
	if !d.canSelect() || !d.request.mayWrite() {
		return
	}
	r, done, selected := d.request, d.done, d.path
	r.app.closeTargetDirectory()
	if done != nil {
		go func() {
			if r.current() && r.mayWrite() {
				done(selected)
			}
		}()
	}
}

func (d *targetDirectory) load(next string) {
	d.loadWithInput(next, "", false)
}

func (d *targetDirectory) loadWithInput(next, displayInput string, keepFallbacks bool) {
	if !d.current() || !d.request.mayWrite() || d.creating {
		return
	}
	d.stopBrowseTimer()
	if d.cancel != nil {
		d.cancel()
	}
	if !keepFallbacks {
		d.fallbacks = nil
	}
	d.seq++
	seq := d.seq
	d.path = next
	if displayInput != "" {
		d.input = displayInput
	} else {
		d.input = next
	}
	d.loaded, d.loading, d.err = false, true, nil
	d.entries = nil
	d.list = ui.ListState{}
	d.cancel = nil
	d.newOpen, d.newName, d.newErr = false, "", ""
	if !path.IsAbs(next) || strings.ContainsRune(next, '\x00') {
		d.loading = false
		d.err = errors.New("file path must be absolute and contain no NUL")
		return
	}
	ctx, cancel := context.WithTimeout(d.request.app.windowContext(), 30*time.Second)
	d.cancel = cancel
	r := d.request
	go func() {
		defer cancel()
		results, err := r.readDirectories(ctx, []string{next})
		var entries []files.Entry
		if err == nil {
			if len(results) != 1 || results[0].Path != next {
				err = errors.New("invalid directory listing response")
			} else {
				item := results[0]
				err = filePathError(next, item.Error)
				if err == nil && item.Stamp.Missing {
					err = &os.PathError{Op: "read", Path: next, Err: os.ErrNotExist}
				}
				if err == nil && !item.Stamp.Mode.IsDir() {
					err = errors.New("invalid directory listing response")
				}
				for _, entry := range item.Entries {
					if entry.Dir && targetDirectoryName(entry.Name) {
						entries = append(entries, entry)
					}
				}
			}
		}
		r.update(func() {
			if !d.current() || seq != d.seq {
				return
			}
			d.cancel = nil
			if err != nil && len(d.fallbacks) > 0 && isFallbackDirectoryError(err) {
				fb := d.fallbacks[0]
				d.fallbacks = d.fallbacks[1:]
				d.loadWithInput(fb, "", true)
				return
			}
			d.fallbacks = nil
			d.loading, d.loaded, d.err = false, err == nil, err
			if err == nil {
				d.entries = entries
			}
		})
	}()
}

func isFallbackDirectoryError(err error) bool {
	return errors.Is(err, os.ErrNotExist) || errors.Is(err, os.ErrPermission)
}

func targetDirectoryName(name string) bool {
	return name != "" && name != "." && name != ".." && !strings.ContainsAny(name, "/\x00")
}

func (d *targetDirectory) enter(name string) {
	if !d.loaded || d.loading || d.creating || d.err != nil || !targetDirectoryName(name) {
		return
	}
	for _, entry := range d.entries {
		if entry.Name == name {
			// Join 会提前消掉 link/..；这里把路径原样交给目标文件系统解析。
			d.load(strings.TrimRight(d.path, "/") + "/" + name)
			return
		}
	}
}

func targetDirectoryParent(value string) string {
	value = strings.TrimRight(value, "/")
	if i := strings.LastIndexByte(value, '/'); i > 0 {
		return value[:i]
	}
	return "/"
}

// filterQuery 返回当前输入框在 d.path 目录下对应的子文件夹过滤词。
func (d *targetDirectory) filterQuery() string {
	in := d.input
	if in == "" || in == d.path {
		return ""
	}
	if !path.IsAbs(in) {
		return strings.TrimSpace(in)
	}
	if strings.HasSuffix(in, "/") {
		return ""
	}
	base := strings.TrimRight(d.path, "/")
	prefix := base + "/"
	if base == "" {
		prefix = "/"
	}
	if strings.HasPrefix(in, prefix) {
		rest := in[len(prefix):]
		if !strings.ContainsRune(rest, '/') {
			return rest
		}
	}
	return ""
}

// visibleEntries 按输入过滤词筛选并排序当前目录子文件夹：前缀匹配优先，其次子串，最后模糊匹配。
func (d *targetDirectory) visibleEntries() []files.Entry {
	q := strings.ToLower(strings.TrimSpace(d.filterQuery()))
	if q == "" {
		return d.entries
	}
	var prefixMatch, containsMatch, fuzzyMatch []files.Entry
	for _, e := range d.entries {
		low := strings.ToLower(e.Name)
		switch {
		case strings.HasPrefix(low, q):
			prefixMatch = append(prefixMatch, e)
		case strings.Contains(low, q):
			containsMatch = append(containsMatch, e)
		case fuzzySubsequence(low, q):
			fuzzyMatch = append(fuzzyMatch, e)
		}
	}
	out := make([]files.Entry, 0, len(prefixMatch)+len(containsMatch)+len(fuzzyMatch))
	out = append(out, prefixMatch...)
	out = append(out, containsMatch...)
	out = append(out, fuzzyMatch...)
	return out
}

func fuzzySubsequence(haystack, needle string) bool {
	if needle == "" {
		return true
	}
	nr := []rune(needle)
	i := 0
	for _, r := range haystack {
		if r == nr[i] {
			i++
			if i == len(nr) {
				return true
			}
		}
	}
	return false
}

const targetDirectoryBrowseDebounce = 150 * time.Millisecond

// onInputChanged 在用户输入或修改路径时实时筛选当前目录，并在路径切换到其他父目录时防抖加载对应目录：
// 1. 仍在当前目录内输入（如 /Users/pur/Desktop/fl）或输入纯关键词（如 fl）时，0ms 内存实时过滤；
// 2. 路径以 / 结尾且指向其他目录，或编辑后父目录变化（如退格到 /Users/pur/Doc）时，150ms 防抖自动加载父目录并联动筛选；
// 3. 连续键入完整路径后直接回车，submitInput 会取消防抖并只发一次目标请求。
func (d *targetDirectory) onInputChanged() {
	if d.creating {
		return
	}
	d.fallbacks = nil
	d.list = ui.ListState{}
	d.stopBrowseTimer()
	in := d.input
	if !path.IsAbs(in) || strings.ContainsRune(in, '\x00') {
		return
	}
	curNorm := strings.TrimRight(d.path, "/")
	if curNorm == "" {
		curNorm = "/"
	}
	var targetDir string
	if strings.HasSuffix(in, "/") {
		targetDir = strings.TrimRight(in, "/")
		if targetDir == "" {
			targetDir = "/"
		}
	} else {
		if in == d.path {
			return
		}
		targetDir = targetDirectoryParent(in)
	}
	if targetDir == curNorm {
		return
	}
	seq := d.seq
	bseq := d.browseSeq
	wantInput := in
	r := d.request
	d.browseTimer = time.AfterFunc(targetDirectoryBrowseDebounce, func() {
		r.update(func() {
			if !d.current() || seq != d.seq || bseq != d.browseSeq || d.input != wantInput || d.creating {
				return
			}
			d.browseTimer = nil
			d.loadWithInput(targetDir, wantInput, false)
		})
	})
}

func (d *targetDirectory) submitInput() {
	if d.creating {
		return
	}
	d.stopBrowseTimer()
	// 明确输入了绝对路径时优先按原路径打开，绝不替换成模糊匹配的另一个目录；
	// 仅在输入纯关键字（非绝对路径）时，回车进入精确或唯一匹配的子目录。
	if path.IsAbs(d.input) {
		target := d.input
		if target != "/" {
			target = strings.TrimRight(target, "/")
		}
		d.load(target)
		return
	}
	if q := d.filterQuery(); q != "" {
		if vis := d.visibleEntries(); len(vis) > 0 {
			if strings.EqualFold(vis[0].Name, q) || len(vis) == 1 {
				d.enter(vis[0].Name)
			}
		}
	}
}

func (d *targetDirectory) createFolder(m locales.Messages) {
	if !d.canCreateDir() || !d.request.mayWrite() {
		return
	}
	name := strings.TrimSpace(d.newName)
	if !targetDirectoryName(name) {
		d.newErr = m.TargetDirectoryInvalidName()
		return
	}
	fullPath := strings.TrimRight(d.path, "/") + "/" + name
	d.creating = true
	d.newErr = ""
	d.createSeq++
	cseq := d.createSeq
	seq := d.seq
	r := d.request
	go func() {
		ctx, cancel := context.WithTimeout(r.app.windowContext(), 15*time.Second)
		defer cancel()
		err := r.invoke(ctx, "files.mkdir", rpc.FileMkdirParams{Path: fullPath}, nil)
		r.update(func() {
			if !d.current() || cseq != d.createSeq {
				return
			}
			d.creating = false
			if seq != d.seq {
				return
			}
			if err != nil {
				d.newErr = errtext.Text(err)
				return
			}
			d.newOpen, d.newName, d.newErr = false, "", ""
			d.load(fullPath)
		})
	}()
}

func (a *app) targetDirectoryDialog(c *ui.Context) {
	d := a.targetDir
	if d == nil {
		return
	}
	if !d.current() {
		a.closeTargetDirectory()
		return
	}
	open := true
	ui.Modal(c.Key(d), &open, func() {
		m, t, p := L(c), c.Theme(), colors(c.Theme())
		ui.Column(c).Width(targetDirectoryWidth()).Gap(12).Children(func() {
			titleText(c, d.title, fsTitle)
			ui.Row(c).Gap(8).AlignItems(ui.Center).Children(func() {
				ui.Text(c, m.TargetDirectoryHost(d.host)).FontSize(fsSmall).TextColor(t.TextMuted).SingleLine().Shrink(0)
				if d.path != "" && d.input != d.path {
					ui.Text(c, d.path).FontSize(fsSmall).Font(monoFont).TextColor(t.TextMuted).SingleLine().Ellipsis("…").Shrink(1).MinWidth(0)
				}
			})
			ui.Row(c).Gap(8).AlignItems(ui.Center).Children(func() {
				if iconButton(c, iconArrowUp, m.TargetDirectoryParent()).Disabled(d.path == "/" || d.creating).Clicked() {
					d.load(targetDirectoryParent(d.path))
				}
				ui.Column(c).Grow(1).MinWidth(0).Children(func() {
					inp := fieldInput(c.Key("target-dir-path"), &d.input, m.TargetDirectoryPathPlaceholder()).Label(m.TargetDirectoryPath()).Disabled(d.creating)
					if !d.newOpen {
						inp.AutoFocus()
					}
					if inp.Changed() {
						d.onInputChanged()
					}
					if inp.Submitted() {
						d.submitInput()
					}
				})
			})
			ui.Column(c).Height(targetDirectoryHeight()).Children(func() {
				entries := d.visibleEntries()
				switch {
				case d.loading:
					ui.Text(c, m.CommonLoading()).FontSize(fsBody).TextColor(t.TextMuted).Padding(8)
				case d.err != nil:
					ui.Text(c, errtext.Text(d.err)).FontSize(fsBody).TextColor(t.Danger).Wrap().Padding(8)
				case len(d.entries) == 0:
					ui.Text(c, m.TargetDirectoryEmpty()).FontSize(fsBody).TextColor(t.TextMuted).Padding(8)
				case len(entries) == 0:
					ui.Text(c, m.TargetDirectoryNoMatch()).FontSize(fsBody).TextColor(t.TextMuted).Padding(8)
				default:
					var entered string
					d.list.Key = func(i int) any { return entries[i].Name }
					list := ui.List(c.Key(d.seq), &d.list, len(entries), func(i int) {
						entry := entries[i]
						row := ui.Row(c.Key(entry.Name)).Height(pinnedRowHeight()).Padding(0, 8).Gap(8).Radius(rRow).AlignItems(ui.Center).Role(ui.RoleButton).Label(entry.Name)
						selectedBg(c, row, false)
						row.Children(func() {
							if entry.Link != "" {
								ui.Icon(c, iconFolderLink).FontSize(iconSize).TextColor(t.Accent)
							} else {
								dirGlyph(c, entry.Name, iconSize)
							}
							ui.Text(c, entry.Name).FontSize(fsBody).SingleLine().Grow(1).Shrink(1)
							ui.Icon(c, iconChevron).FontSize(fsSmall).TextColor(t.TextMuted)
						})
						if row.Clicked() {
							entered = entry.Name
						}
					}).Grow(1)
					fadeEdges(list, &d.list, p.raised)
					if entered != "" {
						d.enter(entered)
					}
				}
			})
			if d.newOpen {
				ui.Column(c.Key("target-dir-new-box")).Gap(4).Children(func() {
					ui.Row(c).Gap(8).AlignItems(ui.Center).Children(func() {
						ui.Column(c).Grow(1).MinWidth(0).Children(func() {
							ni := fieldInput(c.Key("target-dir-new-name"), &d.newName, m.TargetDirectoryNewFolderPlaceholder()).Label(m.TargetDirectoryNewFolder()).AutoFocus()
							if ni.Changed() {
								d.newErr = ""
							}
							if ni.Submitted() {
								d.createFolder(m)
							}
						})
						if confirmButton(c, pressable(accentButton(c.Key("target-dir-create"), m.TargetDirectoryCreate()).Disabled(d.creating))).Clicked() {
							d.createFolder(m)
						}
						if confirmButton(c, pressable(textButton(c.Key("target-dir-new-cancel"), m.CommonCancel()).Disabled(d.creating))).Clicked() {
							d.newOpen, d.newName, d.newErr = false, "", ""
						}
					})
					if d.newErr != "" {
						ui.Text(c, d.newErr).FontSize(fsSmall).TextColor(t.Danger).Wrap()
					}
				})
			}
			ui.Row(c).Gap(8).Justify(ui.End).AlignItems(ui.Center).Children(func() {
				if confirmButton(c, pressable(textButton(c.Key("target-dir-new-folder"), m.TargetDirectoryNewFolder()).Disabled(!d.canCreateDir()))).Clicked() {
					d.newOpen = !d.newOpen
					d.newErr = ""
				}
				if confirmButton(c, pressable(textButton(c, m.CommonCancel()))).Clicked() {
					open = false
				}
				if confirmButton(c, pressable(accentButton(c, d.verb))).Disabled(!d.canSelect()).Clicked() {
					d.selectDirectory()
				}
			})
		})
	})
	if !open {
		a.closeTargetDirectory()
	}
}
