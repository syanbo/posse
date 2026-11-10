package files

import (
	"bytes"
	"context"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
)

// MaxFiles 是清单最多收多少个文件，超出的不进快速打开和检索。
const MaxFiles = 200000

// skipDirs 是不在 git 仓库里时遍历跳过的目录：依赖和构建产物，量大又很少要看。
var skipDirs = map[string]bool{
	".git": true, ".hg": true, ".svn": true, "node_modules": true, ".venv": true, "venv": true,
	"__pycache__": true, ".next": true, ".cache": true, "Pods": true, "DerivedData": true,
}

// List 是工作区里的文件清单，快速打开和全文检索都用它；在 git 仓库里时也用来判断哪些条目被忽略。
type List struct {
	Root      string
	Files     []string // 相对 Root 的路径，排好序
	Git       bool     // 清单来自 git ls-files，不在其中的就是被忽略的
	Truncated bool     // 超过 MaxFiles，后面的没收
	dirs      map[string]bool
	// repos 是工作区里有几个仓库时各仓库相对 Root 的路径（ListRepos）：Git 为真时只有它们里面的才按清单判断忽略，
	// 仓库外面的普通目录什么都不算忽略。为空时整个 Root 是一个仓库
	repos []string
	// inRepo 是工作区本身在某个仓库里（ListRepos）：嵌套仓库外面的文件也按那个仓库的忽略规则算
	inRepo bool
}

// ListFiles 列出 root 下的文件：git 仓库里用 git ls-files 列出已跟踪和未忽略的未跟踪文件，
// 不用自己解析 .gitignore；不是 git 仓库时遍历目录并跳过 skipDirs。
func ListFiles(ctx context.Context, root string) (*List, error) {
	root = filepath.Clean(root)
	l := &List{Root: root}
	cmd := exec.CommandContext(ctx, "git", "-C", root, "ls-files", "-co", "--exclude-standard", "-z")
	cmd.Env = append(os.Environ(), "GIT_TRACE2_EVENT=0")
	if out, err := cmd.Output(); err == nil {
		l.Git = true
		for name := range bytes.SplitSeq(out, []byte{0}) {
			// 嵌套的仓库报成 "nested/"，不是文件
			if len(name) == 0 || name[len(name)-1] == '/' {
				continue
			}
			if len(l.Files) == MaxFiles {
				l.Truncated = true
				break
			}
			l.Files = append(l.Files, filepath.FromSlash(string(name)))
		}
	} else if ctx.Err() != nil {
		return nil, ctx.Err()
	} else if err := l.walk(ctx); err != nil {
		return nil, err
	}
	slices.Sort(l.Files)
	l.Files = slices.Compact(l.Files)
	if l.Git {
		l.dirs = map[string]bool{}
		for _, f := range l.Files {
			for d := filepath.Dir(f); d != "." && !l.dirs[d]; d = filepath.Dir(d) {
				l.dirs[d] = true
			}
		}
	}
	return l, nil
}

// ListRepos 列出工作区 root 下的文件，repos 是工作区里找到的仓库（绝对路径，工作区里的写法）：
// 各仓库用 git ls-files 列（软链过去的仓库也在里面），仓库外面的普通目录遍历。
// 只有一个仓库、而且就是 root 或 root 的上级时和 ListFiles 一样。
func ListRepos(ctx context.Context, root string, repos []string) (*List, error) {
	root = filepath.Clean(root)
	var rels []string
	inRepo := false // 工作区本身在某个仓库里（是它的根目录或子目录）：整个工作区按这个仓库的忽略规则列
	for _, r := range repos {
		rel, err := filepath.Rel(root, filepath.Clean(r))
		if err != nil || rel == "." || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
			inRepo = true
			continue
		}
		rels = append(rels, rel)
	}
	if len(rels) == 0 {
		return ListFiles(ctx, root)
	}
	slices.Sort(rels)
	l := &List{Root: root, Git: true, repos: rels, inRepo: inRepo}
	isRepo := map[string]bool{}
	for _, r := range rels {
		isRepo[r] = true
	}
	if inRepo {
		// 外层仓库从工作区列起：ls-files 在子目录里只列这个子目录，路径相对它
		l.lsFiles(ctx, root, "", isRepo)
	}
	for _, rel := range rels {
		if l.Truncated {
			break
		}
		l.lsFiles(ctx, filepath.Join(root, rel), rel, isRepo)
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
	}
	if !l.Truncated && !inRepo {
		if err := l.walkOutside(ctx, isRepo); err != nil {
			return nil, err
		}
	}
	slices.Sort(l.Files)
	l.Files = slices.Compact(l.Files)
	l.dirs = map[string]bool{}
	for _, f := range l.Files {
		for d := filepath.Dir(f); d != "." && !l.dirs[d]; d = filepath.Dir(d) {
			l.dirs[d] = true
		}
	}
	return l, nil
}

// walkOutside 遍历仓库外面的普通目录，碰到仓库就不再往里走。
func (l *List) walkOutside(ctx context.Context, isRepo map[string]bool) error {
	return filepath.WalkDir(l.Root, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			if p == l.Root {
				return err
			}
			return nil
		}
		if ctx.Err() != nil {
			return ctx.Err()
		}
		rel, _ := filepath.Rel(l.Root, p)
		if d.IsDir() {
			if p != l.Root && (skipDirs[d.Name()] || isRepo[rel]) {
				return filepath.SkipDir
			}
			return nil
		}
		// 软链过去的仓库在这里是一个软链：它的文件已经列过了
		if isRepo[rel] || hidden(d.Name()) || !d.Type().IsRegular() && d.Type()&fs.ModeSymlink == 0 {
			return nil
		}
		if len(l.Files) == MaxFiles {
			l.Truncated = true
			return filepath.SkipAll
		}
		l.Files = append(l.Files, rel)
		return nil
	})
}

// ScopedList 返回限定在 dir（相对 Root）里检索时用的清单：清单里有 dir 下的文件就用它本身；一个都没有时
// （被 git 忽略的目录，或不是 git 仓库时遍历跳过的 node_modules 这类）现列一遍那个目录，用户点名要搜的照搜。
func ScopedList(ctx context.Context, l *List, dir string) (*List, error) {
	if dir == "" {
		return l, nil
	}
	prefix := dir + string(filepath.Separator)
	i, _ := slices.BinarySearch(l.Files, prefix)
	if i < len(l.Files) && strings.HasPrefix(l.Files[i], prefix) {
		return l, nil
	}
	return ListDir(ctx, l.Root, dir)
}

// ListAll 遍历 root 下的全部文件，被 git 忽略的也算（随处搜索的「包括非项目条目」）；依赖、构建产物目录（skipDirs）照旧跳过，
// 这些目录动辄几十万个文件，要搜里面的用 ListDir 点名。
func ListAll(ctx context.Context, root string) (*List, error) {
	l := &List{Root: filepath.Clean(root)}
	if err := l.walk(ctx); err != nil {
		return nil, err
	}
	slices.Sort(l.Files)
	return l, nil
}

// ListDir 遍历 root 下的 dir（相对 root）里的全部文件，路径照旧相对 root：用户限定在被 git 忽略的目录里检索时用，
// 这种目录不在 ListFiles 的清单里。是用户点名要搜的，依赖、构建产物目录也照搜，只跳过版本库目录。
func ListDir(ctx context.Context, root, dir string) (*List, error) {
	root = filepath.Clean(root)
	l := &List{Root: root}
	base := filepath.Join(root, dir)
	err := filepath.WalkDir(base, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			if p == base {
				return err
			}
			return nil
		}
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if d.IsDir() {
			if p != base && (d.Name() == ".git" || d.Name() == ".hg" || d.Name() == ".svn") {
				return filepath.SkipDir
			}
			return nil
		}
		if hidden(d.Name()) || !d.Type().IsRegular() && d.Type()&fs.ModeSymlink == 0 {
			return nil
		}
		if len(l.Files) == MaxFiles {
			l.Truncated = true
			return filepath.SkipAll
		}
		rel, _ := filepath.Rel(root, p)
		l.Files = append(l.Files, rel)
		return nil
	})
	if err != nil {
		return nil, err
	}
	slices.Sort(l.Files)
	return l, nil
}

func (l *List) walk(ctx context.Context) error {
	return filepath.WalkDir(l.Root, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			// 读不了的子目录跳过，根目录读不了才算失败
			if p == l.Root {
				return err
			}
			return nil
		}
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if d.IsDir() {
			if p != l.Root && skipDirs[d.Name()] {
				return filepath.SkipDir
			}
			return nil
		}
		if hidden(d.Name()) || !d.Type().IsRegular() && d.Type()&fs.ModeSymlink == 0 {
			return nil
		}
		if len(l.Files) == MaxFiles {
			l.Truncated = true
			return filepath.SkipAll
		}
		rel, _ := filepath.Rel(l.Root, p)
		l.Files = append(l.Files, rel)
		return nil
	})
}

// Ignored 报告 abs（Root 下的绝对路径）是否被 git 忽略：不在清单里的文件、清单里没有文件的目录。
// 不是 git 仓库时什么都不算忽略；清单被截断时没收的文件也会被当成忽略，干脆都不算。
func (l *List) Ignored(abs string, dir bool) bool {
	if l == nil || !l.Git || l.Truncated {
		return false
	}
	rel, err := filepath.Rel(l.Root, abs)
	if err != nil || rel == "." || strings.HasPrefix(rel, "..") {
		return false
	}
	if dir && l.dirs[rel] {
		return false
	}
	if l.repos != nil {
		// 仓库外面的普通目录、仓库根目录本身都不算忽略
		in := false
		for _, r := range l.repos {
			if rel == r {
				return false
			}
			if strings.HasPrefix(rel, r+string(filepath.Separator)) {
				in = true
			}
		}
		if !in && !l.inRepo {
			return false
		}
	}
	// 指向目录的符号链接在 git 里是一个文件，按文件找
	_, found := slices.BinarySearch(l.Files, rel)
	return !found
}

// lsFiles 把 dir 里 git ls-files 列出的文件加进清单，前面加上 prefix（dir 相对 Root 的路径）。
// 嵌套仓库（未跟踪的报成 "nested/"、子模块和指向仓库的软链报成 "nested"）跳过：它的文件由它自己列。
func (l *List) lsFiles(ctx context.Context, dir, prefix string, isRepo map[string]bool) {
	cmd := exec.CommandContext(ctx, "git", "-C", dir, "ls-files", "-co", "--exclude-standard", "-z")
	cmd.Env = append(os.Environ(), "GIT_TRACE2_EVENT=0")
	out, err := cmd.Output()
	if err != nil {
		return
	}
	for name := range bytes.SplitSeq(out, []byte{0}) {
		if len(name) == 0 || name[len(name)-1] == '/' {
			continue
		}
		rel := filepath.Join(prefix, filepath.FromSlash(string(name)))
		if isRepo[rel] {
			continue
		}
		if len(l.Files) == MaxFiles {
			l.Truncated = true
			return
		}
		l.Files = append(l.Files, rel)
	}
}
