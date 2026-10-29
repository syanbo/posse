package files

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
)

// Entry 是目录里的一项。
type Entry struct {
	Name string
	Dir  bool
	Link string // 符号链接指向的路径（照链接里写的），不是链接时为空
}

// hidden 是树里不显示的名字：git 的内部目录和访达的元数据。
func hidden(name string) bool { return name == ".git" || name == ".DS_Store" }

// ReadDir 读一个目录：目录在前，名字不分大小写排序。指向目录的符号链接算目录。
// 返回目录自己的磁盘戳，增删改名其中的项会改变它，轮询时据此判断要不要重读。
func ReadDir(dir string) ([]Entry, Stamp, error) {
	stamp := StatFile(dir)
	des, err := os.ReadDir(dir)
	if err != nil {
		return nil, stamp, err
	}
	out := make([]Entry, 0, len(des))
	for _, de := range des {
		if hidden(de.Name()) {
			continue
		}
		isDir, link := de.IsDir(), ""
		if de.Type()&os.ModeSymlink != 0 {
			p := filepath.Join(dir, de.Name())
			if fi, err := os.Stat(p); err == nil {
				isDir = fi.IsDir()
			}
			if link, _ = os.Readlink(p); link == "" {
				link = "（读不出指向）"
			}
		}
		out = append(out, Entry{Name: de.Name(), Dir: isDir, Link: link})
	}
	SortEntries(out)
	return out, stamp, nil
}

// SortEntries 按目录在前、名字不分大小写排序。
func SortEntries(es []Entry) {
	slices.SortFunc(es, func(a, b Entry) int {
		if a.Dir != b.Dir {
			if a.Dir {
				return -1
			}
			return 1
		}
		if c := strings.Compare(strings.ToLower(a.Name), strings.ToLower(b.Name)); c != 0 {
			return c
		}
		return strings.Compare(a.Name, b.Name)
	})
}

// Row 是树展开后看得见的一行。
type Row struct {
	Path  string
	Name  string
	Dir   bool
	Link  string // 同 Entry.Link
	Depth int
}

type dirState struct {
	entries []Entry
	stamp   Stamp
}

// Tree 是工作区目录的树：展开时才读目录（读由调用方在后台做，读完 Set），只记展开的目录。
// 只在界面线程使用。
type Tree struct {
	Root     string
	dirs     map[string]*dirState // 读过的目录
	expanded map[string]bool
	rows     []Row
	stale    bool
}

func NewTree(root string) *Tree {
	return &Tree{Root: filepath.Clean(root), dirs: map[string]*dirState{}, expanded: map[string]bool{}, stale: true}
}

// Set 放入读好的目录内容；目录读不了（被删掉）时 err 非空，把它和下面的展开状态都去掉。
func (t *Tree) Set(dir string, entries []Entry, stamp Stamp, err error) {
	if err != nil {
		t.forget(dir)
		return
	}
	t.dirs[dir] = &dirState{entries: entries, stamp: stamp}
	t.stale = true
}

func (t *Tree) forget(dir string) {
	for d := range t.dirs {
		if d == dir || within(d, dir) {
			delete(t.dirs, d)
		}
	}
	for d := range t.expanded {
		if d == dir || within(d, dir) {
			delete(t.expanded, d)
		}
	}
	t.stale = true
}

// within 报告 p 是不是在目录 dir 下面（不含 dir 本身）。
func within(p, dir string) bool {
	return strings.HasPrefix(p, dir+string(filepath.Separator))
}

// Loaded 报告目录读过没有。
func (t *Tree) Loaded(dir string) bool { return t.dirs[dir] != nil }

// Expanded 报告目录是否展开；根目录总是展开。
func (t *Tree) Expanded(dir string) bool { return dir == t.Root || t.expanded[dir] }

// SetExpanded 展开或收起目录，返回需要读的目录（展开了还没读过的）。
func (t *Tree) SetExpanded(dir string, open bool) []string {
	if dir == t.Root {
		return t.missing()
	}
	if open {
		t.expanded[dir] = true
	} else {
		delete(t.expanded, dir)
	}
	t.stale = true
	return t.missing()
}

// CollapseAll 收起所有目录，读过的内容留着，再展开时不用重读。
func (t *Tree) CollapseAll() {
	clear(t.expanded)
	t.stale = true
}

// Reveal 展开 path 的各级上级目录，返回需要读的目录。
func (t *Tree) Reveal(path string) []string {
	for d := filepath.Dir(path); within(d, t.Root); d = filepath.Dir(d) {
		t.expanded[d] = true
	}
	t.stale = true
	return t.missing()
}

// missing 是看得见、还没读过的目录：根目录，和上级都展开了的展开目录。
func (t *Tree) missing() []string {
	var out []string
	for _, d := range t.Visible() {
		if t.dirs[d] == nil {
			out = append(out, d)
		}
	}
	return out
}

// Visible 是看得见的展开目录（含根目录）：它们的内容在树上显示，要跟着磁盘变化。
func (t *Tree) Visible() []string {
	out := []string{t.Root}
	for d := range t.expanded {
		if t.shown(d) {
			out = append(out, d)
		}
	}
	slices.Sort(out[1:])
	return out
}

// shown 报告目录的各级上级是否都展开（目录本身在树上看得见）。
func (t *Tree) shown(dir string) bool {
	if !within(dir, t.Root) {
		return false
	}
	for p := filepath.Dir(dir); p != t.Root; p = filepath.Dir(p) {
		if !t.expanded[p] {
			return false
		}
	}
	return true
}

// Expansions 是展开的目录，存进界面状态，下次打开时恢复。
func (t *Tree) Expansions() []string {
	out := make([]string, 0, len(t.expanded))
	for d := range t.expanded {
		out = append(out, d)
	}
	slices.Sort(out)
	return out
}

// Stamp 是读目录时它的磁盘戳，没读过为零值。
func (t *Tree) Stamp(dir string) (Stamp, bool) {
	d := t.dirs[dir]
	if d == nil {
		return Stamp{}, false
	}
	return d.stamp, true
}

// Rows 是树上看得见的行，按显示顺序；只在树变了之后重算。
func (t *Tree) Rows() []Row {
	if !t.stale {
		return t.rows
	}
	t.rows = t.rows[:0]
	t.walk(t.Root, 0)
	t.stale = false
	return t.rows
}

func (t *Tree) walk(dir string, depth int) {
	d := t.dirs[dir]
	if d == nil {
		return
	}
	for _, e := range d.entries {
		p := filepath.Join(dir, e.Name)
		t.rows = append(t.rows, Row{Path: p, Name: e.Name, Dir: e.Dir, Link: e.Link, Depth: depth})
		if e.Dir && t.expanded[p] {
			t.walk(p, depth+1)
		}
	}
}
