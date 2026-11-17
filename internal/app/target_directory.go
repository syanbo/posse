package app

import (
	"context"
	"errors"
	"os"
	"path"
	"strings"
	"time"

	"github.com/egoist/mygo/ui"

	"posse/internal/errtext"
	"posse/internal/files"
)

type targetDirectory struct {
	request           appRequest
	title, verb, host string
	input, path       string
	entries           []files.Entry
	loaded, loading   bool
	err               error
	seq               uint64
	cancel            context.CancelFunc
	done              func(string)
	list              ui.ListState
}

// chooseTargetDirectory 可从后台调用；打开和后续状态变更都排到窗口线程。
func (r appRequest) chooseTargetDirectory(title, verb, start string, done func(string)) {
	r.update(func() {
		if r.client == nil || !r.client.Remote || r.client.Desktop == nil || !r.mayWrite() {
			return
		}
		r.app.closeTargetDirectory()
		if !path.IsAbs(start) {
			start = "/"
		}
		host := r.snapshot.HostID
		if r.host != nil && r.host.label != "" {
			host = r.host.label
		}
		d := &targetDirectory{request: r, title: title, verb: verb, host: host, done: done}
		r.app.targetDir = d
		d.load(start)
	})
}

func (a *app) closeTargetDirectory() {
	if d := a.targetDir; d != nil {
		d.seq++
		if d.cancel != nil {
			d.cancel()
			d.cancel = nil
		}
		d.done = nil
		a.targetDir = nil
	}
}

func (d *targetDirectory) current() bool {
	return d.request.current() && d.request.app.targetDir == d
}

func (d *targetDirectory) canSelect() bool {
	return d.current() && d.request.snapshot.Controlled && d.loaded && !d.loading && d.err == nil && d.input == d.path
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
	if !d.current() || !d.request.mayWrite() {
		return
	}
	if d.cancel != nil {
		d.cancel()
	}
	d.seq++
	seq := d.seq
	d.input, d.path = next, next
	d.loaded, d.loading, d.err = false, true, nil
	d.entries = nil
	d.list = ui.ListState{}
	d.cancel = nil
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
			d.loading, d.loaded, d.err = false, err == nil, err
			if err == nil {
				d.entries = entries
			}
		})
	}()
}

func targetDirectoryName(name string) bool {
	return name != "" && name != "." && name != ".." && !strings.ContainsAny(name, "/\x00")
}

func (d *targetDirectory) enter(name string) {
	if !d.loaded || d.loading || d.err != nil || !targetDirectoryName(name) {
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
		m, t := L(c), c.Theme()
		ui.Column(c).Width(targetDirectoryWidth()).Gap(12).Children(func() {
			titleText(c, d.title, fsTitle)
			ui.Text(c, m.TargetDirectoryHost(d.host)).FontSize(fsSmall).TextColor(t.TextMuted).Wrap()
			ui.Row(c).Gap(8).AlignItems(ui.Center).Children(func() {
				if iconButton(c, iconArrowUp, m.TargetDirectoryParent()).Disabled(d.path == "/").Clicked() {
					d.load(targetDirectoryParent(d.path))
				}
				ui.Column(c).Grow(1).MinWidth(0).Children(func() {
					if fieldInput(c, &d.input, "/").Label(m.TargetDirectoryPath()).AutoFocus().Submitted() {
						d.load(d.input)
					}
				})
			})
			ui.Column(c).Height(targetDirectoryHeight()).Children(func() {
				switch {
				case d.loading:
					ui.Text(c, m.CommonLoading()).FontSize(fsBody).TextColor(t.TextMuted).Padding(8)
				case d.err != nil:
					ui.Text(c, errtext.Text(d.err)).FontSize(fsBody).TextColor(t.Danger).Wrap().Padding(8)
				case len(d.entries) == 0:
					ui.Text(c, m.TargetDirectoryEmpty()).FontSize(fsBody).TextColor(t.TextMuted).Padding(8)
				default:
					entries := d.entries
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
					fadeEdges(list, &d.list, colors(t).raised)
					if entered != "" {
						d.enter(entered)
					}
				}
			})
			ui.Row(c).Gap(8).Justify(ui.End).Children(func() {
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
