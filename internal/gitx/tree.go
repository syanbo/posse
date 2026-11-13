package gitx

import (
	"maps"
	"path/filepath"
	"strings"
)

// StatusLetter 优先使用工作区状态；暂存后又修改的文件仍显示为修改。
func StatusLetter(e Entry) byte {
	switch {
	case e.Untracked:
		return '?'
	case e.Conflict:
		return 'U'
	case e.Y != '.':
		return e.Y
	}
	return e.X
}

// TreeMarks 给文件树标记状态，删除等状态也使父目录显示修改，仓库根目录不标记。
// shownPath 将真实路径换成工作区中的写法；没有软链映射时可传 nil。
func TreeMarks(root string, entries []Entry, shownPath func(string) string) map[string]byte {
	marks := map[string]byte{}
	for _, e := range entries {
		mark := StatusLetter(e)
		switch mark {
		case 'A', '?', 'U':
		default:
			mark = 'M'
		}
		abs := filepath.Join(root, filepath.FromSlash(e.Path))
		shown := func(p string) string {
			if shownPath != nil {
				return shownPath(p)
			}
			return p
		}
		marks[shown(abs)] = mark
		// 先沿仓库路径找父目录，再换显示路径；软链别名的长度可能与真实路径不同。
		for dir := filepath.Dir(abs); len(dir) > len(root); dir = filepath.Dir(dir) {
			p := shown(dir)
			if _, ok := marks[p]; ok {
				break
			}
			marks[p] = 'M'
		}
	}
	return marks
}

// RepoTreeMarks 让同一个仓库经不同软链展开时使用相同的文件状态。
func RepoTreeMarks(repo Repo, entries []Entry, shownPath func(string) string) map[string]byte {
	marks := TreeMarks(repo.Root, entries, shownPath)
	for _, alias := range repo.Aliases {
		maps.Copy(marks, TreeMarks(alias, entries, shownPath))
	}
	return marks
}

// MergeRepoTreeMarks 同时标记脏仓库在工作区中的各个入口。
func MergeRepoTreeMarks(dst map[string]byte, workspace string, repo Repo, marks map[string]byte) {
	MergeTreeMarks(dst, workspace, repo.Root, marks)
	for _, alias := range repo.Aliases {
		MergeTreeMarks(dst, workspace, alias, marks)
	}
}

// MergeTreeMarks 合并各仓库的文件标记，并让工作区中收起的脏仓库和上级目录显示修改。
func MergeTreeMarks(dst map[string]byte, workspace, root string, marks map[string]byte) {
	for p, m := range marks {
		// 外层仓库的目录标记不能盖住嵌套仓库里文件的标记。
		if _, ok := dst[p]; !ok || m != 'M' {
			dst[p] = m
		}
	}
	if len(marks) > 0 {
		for dir := root; len(dir) > len(workspace) && strings.HasPrefix(dir, workspace+string(filepath.Separator)); dir = filepath.Dir(dir) {
			if _, ok := dst[dir]; !ok {
				dst[dir] = 'M'
			}
		}
	}
}
