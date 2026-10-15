package files

import (
	"bytes"
	"context"
	"io/fs"
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
}

// ListFiles 列出 root 下的文件：git 仓库里用 git ls-files 列出已跟踪和未忽略的未跟踪文件，
// 不用自己解析 .gitignore；不是 git 仓库时遍历目录并跳过 skipDirs。
func ListFiles(ctx context.Context, root string) (*List, error) {
	root = filepath.Clean(root)
	l := &List{Root: root}
	cmd := exec.CommandContext(ctx, "git", "-C", root, "ls-files", "-co", "--exclude-standard", "-z")
	if out, err := cmd.Output(); err == nil {
		l.Git = true
		for name := range bytes.SplitSeq(out, []byte{0}) {
			if len(name) == 0 {
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
	if dir {
		return !l.dirs[rel]
	}
	_, found := slices.BinarySearch(l.Files, rel)
	return !found
}
