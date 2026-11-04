package gitx

import (
	"context"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"
)

// Repo 是工作区里找到的一个仓库。
type Repo struct {
	// Root 是仓库根目录在工作区里的写法：经过软链时是软链的路径，不是 git 给的真实路径，
	// 文件标签里的路径都按工作区写法，两种写法混用会把同一个文件当成两个。
	Root string
	// Real 是解析过软链的根目录，同一个仓库经两条软链进来时按它去重。
	Real string
	// Aliases 是同一个仓库在工作区里的其他写法（另一条软链）。
	Aliases []string
}

// Contains 报告 abs 在这个仓库里（含根目录本身），返回所在的那种写法。
func (r Repo) Contains(abs string) (root string, ok bool) {
	for _, p := range append([]string{r.Root}, r.Aliases...) {
		if abs == p || strings.HasPrefix(abs, p+string(filepath.Separator)) {
			return p, true
		}
	}
	return "", false
}

// Discovery 是 Discover 的结果。
type Discovery struct {
	Repos []Repo
	// Partial 说明撞上了扫描上限（目录数、时间），可能没找全。
	Partial bool
}

// 扫描上限：普通目录（还没进入任何仓库的部分）最多走这么多个、这么久。仓库内部交给 git，不算在里面。
// 不限深度：不是仓库的源码目录（解压的第三方源码）常有十来层。
var (
	discoverMaxDirs = 20000
	discoverMaxTime = 5 * time.Second
)

// discoverSkip 是不往里找的依赖、构建产物和缓存目录；点目录另外一律跳过。
// 跟软链扫的话这些目录里全是第三方仓库（pub、npm 的 git 依赖、Flutter SDK）。
var discoverSkip = map[string]bool{
	"node_modules": true, "Pods": true, "build": true, "DerivedData": true,
	"venv": true, "__pycache__": true, "vendor": true, "target": true,
}

// Discover 找出工作区 dir 里的所有 git 仓库：dir 所在的仓库（dir 是仓库的子目录时），
// 以及 dir 下面的仓库，不限深度、跟随指向目录的软链。
//
// 规则：
//   - 普通目录往下走，碰到含 .git 的目录就记成仓库、不再往里走，仓库内部交给 git：
//     没被忽略的未跟踪目录里含 .git 的是嵌套仓库，ls-files 里模式 160000 的是子模块（含没有 .gitmodules 的），
//     仓库里指向目录的软链（git 当它是文件）另看目标是不是仓库。被外层忽略的嵌套仓库不算：
//     依赖缓存（.fvm/flutter_sdk、ios/.symlinks/plugins）都在这一类里。
//   - 跳过点目录和 discoverSkip，跳过指向 dir 自己或上级目录的软链（否则会扫遍整个家目录），
//     按真实路径去重，同一个目录不进第二次。
//   - 只用 git 的查询命令（不进操作记录），而且只对确认有 .git 的目录调 git，失败的命令也会被记下。
//
// multi 为假（项目关掉了"识别多个 Git 仓库"）时只认 dir 所在的那一个仓库。
func Discover(ctx context.Context, dir string, multi bool) Discovery {
	dir = filepath.Clean(dir)
	d := &discoverer{
		ctx:      ctx,
		top:      dir,
		seen:     map[string]bool{},
		byReal:   map[string]int{},
		deadline: time.Now().Add(discoverMaxTime),
	}
	d.topReal, _ = filepath.EvalSymlinks(dir)
	if d.topReal == "" {
		d.topReal = dir
	}
	// dir 本身在某个仓库里（是仓库根目录，或子目录工作区）：这个仓库也算，嵌套仓库只在 dir 范围里找
	// 按真实路径往上找：工作区是指向某个仓库子目录的软链时，字面路径的上级里没有 .git
	if root := enclosingRoot(d.topReal); root != "" {
		if top := TopLevel(ctx, root); top != "" {
			shown := ShownRoot(dir, top)
			// pathspec 按真实路径算：shown 可能是 git 给的真实根目录，不在 dir 的字面上级
			spec := ""
			if rel, err := filepath.Rel(top, d.topReal); err == nil && rel != "." {
				spec = rel
			}
			if d.add(shown) && multi {
				d.inside(shown, spec)
			}
			return d.result()
		}
	}
	if !multi {
		return d.result()
	}
	d.walk(dir)
	return d.result()
}

type discoverer struct {
	ctx      context.Context
	top      string
	topReal  string
	seen     map[string]bool // 走过的目录，按真实路径
	byReal   map[string]int  // 仓库的真实路径 → repos 下标
	repos    []Repo
	dirs     int
	deadline time.Time
	partial  bool
}

func (d *discoverer) result() Discovery {
	// 外层仓库经两条软链进来时，只顺着第一条找到了里面的仓库：给它们补上另一条的写法
	for changed := true; changed; {
		changed = false
		for i := range d.repos {
			outer := d.repos[i]
			for j := range d.repos {
				in := &d.repos[j]
				if i == j || !strings.HasPrefix(in.Root, outer.Root+string(filepath.Separator)) {
					continue
				}
				for _, a := range outer.Aliases {
					alias := a + in.Root[len(outer.Root):]
					if alias != in.Root && !slices.Contains(in.Aliases, alias) {
						in.Aliases = append(in.Aliases, alias)
						changed = true
					}
				}
			}
		}
	}
	slices.SortFunc(d.repos, func(a, b Repo) int { return strings.Compare(a.Root, b.Root) })
	return Discovery{Repos: d.repos, Partial: d.partial}
}

// add 记下一个仓库；同一个仓库已经记过（另一条软链）时只补一个写法，返回假。
func (d *discoverer) add(root string) bool {
	real, err := filepath.EvalSymlinks(root)
	if err != nil {
		real = root
	}
	if i, ok := d.byReal[real]; ok {
		if r := &d.repos[i]; r.Root != root && !slices.Contains(r.Aliases, root) {
			r.Aliases = append(r.Aliases, root)
		}
		return false
	}
	d.byReal[real] = len(d.repos)
	d.repos = append(d.repos, Repo{Root: root, Real: real})
	return true
}

// ancestorOfTop 报告真实路径 real 是工作区自己或它的上级目录：顺着这样的软链走会把工作区外面整个扫一遍。
func (d *discoverer) ancestorOfTop(real string) bool {
	return d.topReal == real || strings.HasPrefix(d.topReal, real+string(filepath.Separator)) || real == string(filepath.Separator)
}

// walk 走普通目录（还没进入任何仓库的部分）。
func (d *discoverer) walk(dir string) {
	if d.ctx.Err() != nil {
		return
	}
	real, err := filepath.EvalSymlinks(dir)
	if err != nil {
		return
	}
	// 另一条软链进来的同一个仓库：补上这个写法
	if _, ok := d.byReal[real]; ok {
		d.add(dir)
		return
	}
	if d.seen[real] {
		return
	}
	if dir != d.top && d.ancestorOfTop(real) {
		return
	}
	d.seen[real] = true
	d.dirs++
	if d.dirs > discoverMaxDirs || time.Now().After(d.deadline) {
		d.partial = true
		return
	}
	if dir != d.top && hasDotGit(dir) {
		if d.add(dir) {
			d.inside(dir, "")
		}
		return
	}
	if bareRepo(dir) {
		return
	}
	ents, err := os.ReadDir(dir)
	if err != nil {
		return
	}
	for _, e := range ents {
		if d.partial {
			return
		}
		name := e.Name()
		if strings.HasPrefix(name, ".") || discoverSkip[name] {
			continue
		}
		p := filepath.Join(dir, name)
		isDir := e.IsDir()
		if !isDir && e.Type()&os.ModeSymlink != 0 {
			fi, err := os.Stat(p)
			isDir = err == nil && fi.IsDir()
		}
		if isDir {
			d.walk(p)
		}
	}
}

// inside 在仓库 root 里找嵌套仓库、子模块和指向仓库的软链；spec 不为空时只看这个子目录（相对 root）。
func (d *discoverer) inside(root, spec string) {
	if d.ctx.Err() != nil {
		return
	}
	args := func(base ...string) []string {
		if spec != "" {
			base = append(base, "--", spec)
		}
		return base
	}
	var cands []string
	// 没被忽略的未跟踪目录：嵌套仓库在这里报成 "dir/"；软链在这里报成文件
	if out, err := runRaw(d.ctx, root, nil, nil, args("ls-files", "--others", "--directory", "--exclude-standard", "-z")...); err == nil {
		for _, p := range splitZ(out) {
			cands = append(cands, strings.TrimSuffix(p, "/"))
		}
	}
	// 子模块和 gitlink（160000）、已跟踪的软链（120000）
	if out, err := runRaw(d.ctx, root, nil, nil, args("ls-files", "--stage", "-z")...); err == nil {
		for _, line := range splitZ(out) {
			mode, rest, ok := strings.Cut(line, " ")
			if !ok || mode != "160000" && mode != "120000" {
				continue
			}
			if _, path, ok := strings.Cut(rest, "\t"); ok {
				cands = append(cands, path)
			}
		}
	}
	for _, rel := range cands {
		p := filepath.Join(root, filepath.FromSlash(rel))
		fi, err := os.Lstat(p)
		if err != nil {
			continue
		}
		if fi.Mode()&os.ModeSymlink != 0 {
			real, err := filepath.EvalSymlinks(p)
			if err != nil || d.ancestorOfTop(real) {
				continue
			}
			if st, err := os.Stat(p); err != nil || !st.IsDir() {
				continue
			}
		} else if !fi.IsDir() {
			continue
		} else if !hasDotGit(p) {
			// 未跟踪的普通目录：--directory 把它整个报成一行 dir/。不带 --directory 再列一次这个目录：
			// 照外层的忽略规则逐个列出文件，碰到嵌套仓库停下、报成 dir/x/
			d.untrackedRepos(root, rel)
			continue
		}
		if hasDotGit(p) && d.add(p) {
			d.inside(p, "")
		}
	}
}

// untrackedRepos 找仓库 root 里未跟踪的普通目录 rel 下面的嵌套仓库（被外层忽略的不算）。
func (d *discoverer) untrackedRepos(root, rel string) {
	out, err := runRaw(d.ctx, root, nil, nil, "ls-files", "--others", "--exclude-standard", "-z", "--", rel+"/")
	if err != nil {
		return
	}
	for _, f := range splitZ(out) {
		p := filepath.Join(root, filepath.FromSlash(strings.TrimSuffix(f, "/")))
		if !strings.HasSuffix(f, "/") {
			// 软链 git 报成文件：指向目录的照普通目录那样跟过去（和 walk 一样不跟指向工作区自己、上级的）
			fi, err := os.Lstat(p)
			if err != nil || fi.Mode()&os.ModeSymlink == 0 {
				continue
			}
			real, err := filepath.EvalSymlinks(p)
			if err != nil || d.ancestorOfTop(real) {
				continue
			}
			if st, err := os.Stat(p); err == nil && st.IsDir() {
				d.walk(p)
			}
			continue
		}
		if hasDotGit(p) && d.add(p) {
			d.inside(p, "")
		}
	}
}

func splitZ(b []byte) []string {
	var out []string
	for _, s := range strings.Split(string(b), "\x00") {
		if s != "" {
			out = append(out, s)
		}
	}
	return out
}

// hasDotGit 报告 dir 下有 .git（目录，或 worktree、子模块的 .git 文件）。
func hasDotGit(dir string) bool {
	_, err := os.Lstat(filepath.Join(dir, ".git"))
	return err == nil
}

// bareRepo 报告 dir 像是裸仓库：没有工作区，status 没有意义，里面 objects 的几百个目录也不用走。
func bareRepo(dir string) bool {
	for _, n := range []string{"HEAD", "objects", "refs"} {
		if _, err := os.Stat(filepath.Join(dir, n)); err != nil {
			return false
		}
	}
	return true
}

// enclosingRoot 往上找含 .git 的目录（dir 自己或上级），不调 git：对不是仓库的目录调 git 会失败，失败的命令要记进操作记录。
func enclosingRoot(dir string) string {
	for d := dir; ; {
		if hasDotGit(d) {
			return d
		}
		parent := filepath.Dir(d)
		if parent == d {
			return ""
		}
		d = parent
	}
}

// ShownRoot 把 git 给的仓库根目录（已解析符号链接，如 /private/var/…）换成工作区 ws 的写法。
// top 在 ws 外面又不是 ws 的上级时原样返回。
func ShownRoot(ws, top string) string {
	if top == "" {
		return ""
	}
	real, err := filepath.EvalSymlinks(ws)
	if err != nil {
		return top
	}
	up, err := filepath.Rel(real, top)
	if err != nil || (up != "." && up != ".." && !strings.HasPrefix(up, ".."+string(filepath.Separator))) {
		return top
	}
	shown := filepath.Join(ws, up)
	// ws 是指向仓库子目录的软链时，字面路径的上级不是仓库根目录：照 git 给的写
	if up != "." {
		if r, err := filepath.EvalSymlinks(shown); err != nil || r != top {
			return top
		}
	}
	return shown
}
